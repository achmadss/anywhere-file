//go:build windows

package main

import (
	"crypto/ed25519"
	"testing"

	"github.com/danieljoos/wincred"
)

// docs/spikes/keystore.md found that the Rust keyring crate wrote credentials with
// enterprise persistence, which roams with the user profile. A device key that follows the
// user to a second PC would give two machines one identity. The Go path writes
// local-machine persistence, and this is where that is checked on a Windows runner rather
// than taken on trust.
func TestWindowsCredentialDoesNotRoam(t *testing.T) {
	useTestKeyringItem(t)
	if err := (keyringStore{}).save(make([]byte, ed25519.SeedSize)); err != nil {
		t.Fatal(err)
	}
	cred, err := wincred.GetGenericCredential(keyringService + ":" + keyringAccount)
	if err != nil {
		t.Fatalf("reading back the credential: %v", err)
	}
	if cred.Persist != wincred.PersistLocalMachine {
		t.Errorf("persistence = %d, want %d (local machine)", cred.Persist, wincred.PersistLocalMachine)
	}
}
