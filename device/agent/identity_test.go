package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// agentDir is a directory the agent creates itself, with the 0700 it insists on. t.TempDir
// hands out a wider one, and the store is right to refuse that.
func agentDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "anywhere-file")
}

func testConfig(t *testing.T) config {
	t.Helper()
	return config{dir: agentDir(t), keystore: "auto", storeRetry: time.Millisecond}
}

// useTestKeyringItem points the keystore at a name no real agent uses, so running the
// tests on a developer's PC cannot read or delete that PC's device key.
func useTestKeyringItem(t *testing.T) {
	t.Helper()
	was := keyringService
	keyringService = "dev.anywherefile.agent-test." + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		_ = keyring.Delete(keyringService, keyringAccount)
		keyringService = was
	})
}

// The acceptance case: store, restart the process, load, same public key. The store is
// whichever one this machine actually has, so each CI runner tests its own: Keychain on
// macOS, Credential Manager on Windows, a seed file on a runner with no session bus.
func TestTheDefaultStoreHoldsTheKeyAcrossAReopen(t *testing.T) {
	useTestKeyringItem(t)
	cfg := testConfig(t)

	store, err := openSeedStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadOrCreateKey(store, discard)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// A second store value, opened the way a restarted agent opens it.
	reopened, err := openSeedStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateKey(reopened, discard)
	if err != nil {
		t.Fatalf("after reopening %s: %v", reopened, err)
	}
	if first.publicHex() != second.publicHex() {
		t.Errorf("public key = %s after a reopen, want %s", second.publicHex(), first.publicHex())
	}
}

// And the same thing through a real process restart, which is what the agent does when
// the PC reboots. The seed file is used on every OS so this runs everywhere.
func TestKeySurvivesAProcessRestart(t *testing.T) {
	dir := agentDir(t)
	first := runAgentKey(t, dir)
	second := runAgentKey(t, dir)
	if first["public key"] == "" {
		t.Fatalf("no public key printed: %v", first)
	}
	if first["public key"] != second["public key"] {
		t.Errorf("public key = %s after a restart, want %s", second["public key"], first["public key"])
	}
	// device_id is what the server stores, and the client compares the fingerprint.
	pub, err := hex.DecodeString(first["public key"])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key = %q, want 32 bytes hex", first["public key"])
	}
	sum := sha256.Sum256(pub)
	if want := hex.EncodeToString(sum[:]); first["device id"] != want {
		t.Errorf("device id = %s, want %s", first["device id"], want)
	}
	if !strings.HasPrefix(first["fingerprint"], "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256: prefix", first["fingerprint"])
	}
}

// A store that is locked or unreachable must never produce a new key: that would be a new
// device, and the PC would drop out of every binding it had.
func TestALockedStoreNeverGeneratesAKey(t *testing.T) {
	locked := &fakeStore{loadErr: errors.New("keyring is locked"), unlockAfter: 1 << 30}
	if _, err := loadOrCreateKey(locked, discard); err == nil {
		t.Fatal("loadOrCreateKey succeeded on a locked store, want an error")
	}
	if locked.saves != 0 {
		t.Errorf("saves = %d on a locked store, want 0", locked.saves)
	}

	// It waits instead, and takes the real key once the store answers.
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	locked.unlockAfter, locked.seed = locked.loads+3, seed
	key, err := waitForDeviceKey(t.Context(), locked, discard, time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDeviceKey: %v", err)
	}
	if key.publicHex() != hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)) {
		t.Error("waiting produced a key other than the one in the store")
	}
	if locked.saves != 0 {
		t.Errorf("saves = %d after waiting, want 0", locked.saves)
	}
}

// A store that accepts a write and then does not hand the same bytes back has to fail
// here, on the first run, rather than at the next restart under a different identity.
func TestFirstRunFailsWhenTheStoreForgets(t *testing.T) {
	for what, store := range map[string]*fakeStore{
		"kept nothing":        {alwaysEmpty: true},
		"kept something else": {mangle: true},
	} {
		if _, err := loadOrCreateKey(store, discard); err == nil {
			t.Errorf("loadOrCreateKey succeeded against a store that %s", what)
		}
	}
}

func TestSeedFileRefusesWidePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows files do not carry these permission bits")
	}
	dir := agentDir(t)
	store := fileStore{filepath.Join(dir, seedFileName)}
	if _, err := loadOrCreateKey(store, discard); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what string
		path string
		mode os.FileMode
	}{
		{"seed file", store.path, 0o644},
		{"directory", dir, 0o755},
	} {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(); err == nil {
			t.Errorf("a %04o %s was accepted, want a refusal", c.mode, c.what)
		}
		if err := os.Chmod(c.path, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.load(); err != nil {
		t.Errorf("load after restoring the permissions: %v", err)
	}
}

// The seed is the device. It must not reach a log at any level, including debug, and not
// through the errors the store returns either.
func TestTheSeedNeverReachesTheLog(t *testing.T) {
	dir := agentDir(t)
	_, logs := runAgent(t, dir, "debug")
	seed, err := os.ReadFile(filepath.Join(dir, seedFileName))
	if err != nil {
		t.Fatal(err)
	}
	for name, form := range map[string]string{
		"raw":    string(seed),
		"hex":    hex.EncodeToString(seed),
		"base64": base64.StdEncoding.EncodeToString(seed),
	} {
		if strings.Contains(logs, form) {
			t.Errorf("the %s seed appears in the log:\n%s", name, logs)
		}
	}
	if !strings.Contains(logs, "device key generated") {
		t.Errorf("nothing was logged at debug level, so this proved nothing:\n%s", logs)
	}
}

func TestUnknownKeystoreIsRefused(t *testing.T) {
	if _, err := openSeedStore(config{dir: agentDir(t), keystore: "vault"}); err == nil {
		t.Error("RFM_AGENT_KEYSTORE=vault was accepted, want a refusal")
	}
}

// fakeStore stands in for a keystore that is locked, then is not.
type fakeStore struct {
	seed        []byte
	loadErr     error // returned until unlockAfter loads have happened
	unlockAfter int
	loads       int
	saves       int
	alwaysEmpty bool // accepts a write and keeps nothing
	mangle      bool // accepts a write and keeps different bytes
}

func (f *fakeStore) String() string { return "fake store" }

func (f *fakeStore) load() ([]byte, error) {
	f.loads++
	if f.loadErr != nil && f.loads < f.unlockAfter {
		return nil, f.loadErr
	}
	if f.seed == nil {
		return nil, errNoSeed
	}
	return f.seed, nil
}

func (f *fakeStore) save(seed []byte) error {
	f.saves++
	switch {
	case f.alwaysEmpty:
	case f.mangle:
		f.seed = append([]byte(nil), seed...)
		f.seed[0]++
	default:
		f.seed = seed
	}
	return nil
}

// runAgentKey runs `agent key` in a real subprocess and returns what it printed.
func runAgentKey(t *testing.T, dir string) map[string]string {
	t.Helper()
	stdout, _ := runAgent(t, dir, "info")
	out := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if name, value, ok := strings.Cut(line, ":"); ok {
			out[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return out
}

// runAgent re-executes this test binary as the agent, which is a genuine restart: the
// process is gone and the key comes back from the store and nowhere else.
func runAgent(t *testing.T, dir, level string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestAgentSubprocess")
	cmd.Env = append(os.Environ(),
		"RFM_AGENT_SUBPROCESS=key",
		"RFM_AGENT_DIR="+dir,
		"RFM_AGENT_KEYSTORE=file",
		"RFM_AGENT_LOG_LEVEL="+level,
	)
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("agent key: %v\n%s", err, errs.String())
	}
	return out.String(), errs.String()
}

// TestAgentSubprocess is the agent, not a test. It runs only in the child above.
func TestAgentSubprocess(t *testing.T) {
	if os.Getenv("RFM_AGENT_SUBPROCESS") == "" {
		t.Skip("helper for runAgent")
	}
	if err := run(context.Background(), []string{os.Getenv("RFM_AGENT_SUBPROCESS")}, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
}
