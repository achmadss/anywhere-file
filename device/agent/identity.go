package main

// The device key (#90). One Ed25519 key per PC, generated here and never uploaded. The
// server derives device_id from the public key, so the key is the device: replacing it
// makes a new device and silently drops the PC out of every binding it had. That is why
// nothing below generates a key unless the store answered and said it was empty.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type deviceKey struct {
	priv ed25519.PrivateKey
}

func (k deviceKey) public() ed25519.PublicKey { return k.priv.Public().(ed25519.PublicKey) }

func (k deviceKey) publicHex() string { return hex.EncodeToString(k.public()) }

// deviceID is the server's identifier for this PC. The schema derives the same value from
// the public key it was given, so the agent knows its id before it ever enrols.
func (k deviceKey) deviceID() string {
	sum := sha256.Sum256(k.public())
	return hex.EncodeToString(sum[:])
}

// fingerprint is the same digest for a person to read and compare, in the shape OpenSSH
// prints. The client shows this next to a discovered device.
func (k deviceKey) fingerprint() string {
	sum := sha256.Sum256(k.public())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// loadOrCreateKey returns the PC's key. It generates one only when the store answered and
// held nothing. Every other failure is returned, because a store that is locked or
// unreachable will hold the real key again in a moment.
func loadOrCreateKey(store seedStore, log *slog.Logger) (deviceKey, error) {
	seed, err := store.load()
	switch {
	case err == nil:
		if len(seed) != ed25519.SeedSize {
			return deviceKey{}, fmt.Errorf("%s: device key is %d bytes, want %d", store, len(seed), ed25519.SeedSize)
		}
		return deviceKey{ed25519.NewKeyFromSeed(seed)}, nil
	case !errors.Is(err, errNoSeed):
		return deviceKey{}, fmt.Errorf("%s: %w: %w", store, errStoreLocked, err)
	}

	seed = make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return deviceKey{}, err
	}
	if err := store.save(seed); err != nil {
		return deviceKey{}, fmt.Errorf("%s: %w", store, err)
	}
	// Read it back before using it. A store that accepts a write and keeps nothing has to
	// fail on this first run, not at the next restart with a different identity.
	stored, err := store.load()
	if err != nil {
		return deviceKey{}, fmt.Errorf("%s: reading back the new key: %w", store, err)
	}
	if !bytes.Equal(stored, seed) {
		return deviceKey{}, fmt.Errorf("%s: stored key did not read back", store)
	}
	key := deviceKey{ed25519.NewKeyFromSeed(seed)}
	log.Info("device key generated", "device", key.deviceID(), "store", store.String())
	return key, nil
}

// errStoreLocked marks a store that answered with something other than "it is empty".
// The key is probably still there and readable in a moment, so the caller waits.
var errStoreLocked = errors.New("locked or unreachable")

// waitForDeviceKey is loadOrCreateKey with the wait a locked store needs. A user who has
// not logged in yet, or a keyring that has not been unlocked, is a normal state on a PC
// that just booted. The agent holds until the key is readable rather than starting
// without one.
func waitForDeviceKey(ctx context.Context, store seedStore, log *slog.Logger, retry time.Duration) (deviceKey, error) {
	for {
		key, err := loadOrCreateKey(store, log)
		if err == nil {
			return key, nil
		}
		// Anything else is a store that will answer the same way forever.
		if !errors.Is(err, errStoreLocked) || ctx.Err() != nil {
			return deviceKey{}, err
		}
		log.Warn("device key unavailable, waiting", "store", store.String(), "err", err, "retry", retry)
		select {
		case <-ctx.Done():
			return deviceKey{}, err
		case <-time.After(retry):
		}
	}
}
