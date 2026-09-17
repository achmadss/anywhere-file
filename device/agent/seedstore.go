package main

// Where the 32-byte seed lives between runs, per the findings in docs/spikes/keystore.md.
// A keystore is used wherever the machine has one. A headless Linux box has no Secret
// Service and no other secret to wrap the key with, so the seed goes in a 0600 file and
// the documentation says plainly that filesystem permissions are the whole protection.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// keyringService names the keystore item. It is a variable so a test run can point
// somewhere else and never touch a real agent's key.
var keyringService = "dev.anywherefile.agent"

const (
	keyringAccount = "device-key-seed"
	seedFileName   = "device-key-seed"
)

// errNoSeed is the store saying it is reachable and holds nothing. It is the only answer
// that may lead to a new key being generated.
var errNoSeed = errors.New("no device key stored")

type seedStore interface {
	load() ([]byte, error)
	save(seed []byte) error
	String() string
}

// openSeedStore picks the store for this machine. RFM_AGENT_KEYSTORE overrides it, which
// is what a NAS or a container without a session bus sets when the guess is wrong.
func openSeedStore(cfg config) (seedStore, error) {
	switch cfg.keystore {
	case "keyring":
		return keyringStore{}, nil
	case "file":
		return fileStore{filepath.Join(cfg.dir, seedFileName)}, nil
	case "auto":
	default:
		return nil, fmt.Errorf("RFM_AGENT_KEYSTORE: unknown value %q, want auto, keyring or file", cfg.keystore)
	}
	// Only Linux has the headless case. macOS and Windows always have a store, and an
	// error from one there means locked, which is a wait and never a new key.
	if runtime.GOOS == "linux" && !hasSessionBus() {
		return fileStore{filepath.Join(cfg.dir, seedFileName)}, nil
	}
	return keyringStore{}, nil
}

// hasSessionBus reports whether a D-Bus session bus exists, which is what the Secret
// Service runs on. Without one there is no keystore to be locked, so waiting for it to
// unlock would wait forever.
func hasSessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid()))
	return err == nil
}

// keyringStore is the OS keystore: Keychain on macOS, Credential Manager on Windows, the
// Secret Service on a Linux desktop. The seed is stored hex encoded because the keystores
// hold text.
type keyringStore struct{}

func (keyringStore) String() string { return "os keystore" }

func (keyringStore) load() ([]byte, error) {
	value, err := keyring.Get(keyringService, keyringAccount)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return nil, errNoSeed
	case err != nil:
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(value))
}

func (keyringStore) save(seed []byte) error {
	return keyring.Set(keyringService, keyringAccount, hex.EncodeToString(seed))
}

// fileStore is the headless fallback: the raw seed, mode 0600, in a mode 0700 directory,
// which is what OpenSSH does with a passphrase-less private key. Wider permissions are
// refused rather than repaired, because a key that was readable is a key to replace.
type fileStore struct {
	path string
}

func (f fileStore) String() string { return "seed file " + f.path }

func (f fileStore) load() ([]byte, error) {
	seed, err := os.ReadFile(f.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errNoSeed
	case err != nil:
		return nil, err
	}
	if err := checkMode(filepath.Dir(f.path), 0o700); err != nil {
		return nil, err
	}
	if err := checkMode(f.path, 0o600); err != nil {
		return nil, err
	}
	return seed, nil
}

func (f fileStore) save(seed []byte) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := checkMode(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(f.path, seed, 0o600); err != nil {
		return err
	}
	// WriteFile leaves an existing file's mode alone, and the file may predate this code.
	return os.Chmod(f.path, 0o600)
}

// checkMode refuses permissions wider than want. Windows does not carry these bits and
// the file store is not its default, so there is nothing to check there.
func checkMode(path string, want fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if mode := info.Mode().Perm(); mode&^want != 0 {
		return fmt.Errorf("%s has mode %04o, want %04o or narrower", path, mode, want)
	}
	return nil
}
