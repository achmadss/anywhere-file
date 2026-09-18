package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The supervisor starts a real process, and the agent runs on Windows too, so there is no
// shell to borrow: the program it starts here is this test binary running the helper below.
func helperApp(t *testing.T, dir, mode string) app {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_TEST_APP", mode)
	t.Setenv("AGENT_TEST_APP_DIR", dir)
	return app{
		Name: "files", Type: "http", Address: "127.0.0.1:5000",
		Command: []string{exe, "-test.run=^TestApplicationHelper$"},
	}
}

// TestApplicationHelper is the application, not a test. It records that it was started and
// writes a line the way a server writes a banner. In `exit` it then stops, so there is
// something to restart; in `stay` it keeps writing, so there is something to stop. Run on
// its own it does nothing.
func TestApplicationHelper(t *testing.T) {
	dir := os.Getenv("AGENT_TEST_APP_DIR")
	if dir == "" {
		return
	}
	appendTo(t, dir, "starts")
	fmt.Println("the application wrote a line")
	if os.Getenv("AGENT_TEST_APP") == "stay" {
		for {
			appendTo(t, dir, "alive")
			time.Sleep(20 * time.Millisecond)
		}
	}
	os.Exit(0)
}

func appendTo(t *testing.T, dir, name string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// An application that exits comes back, and what it wrote is in the agent's log under its
// own name. A service has no window, so that log is the only place to read it.
func TestAnApplicationThatExitsIsStartedAgain(t *testing.T) {
	dir := t.TempDir()
	a := helperApp(t, dir, "exit")
	shortBackoff(t)
	log, read := logToBuffer()

	ctx, cancel := context.WithCancel(t.Context())
	wait := superviseApps(ctx, &state{Name: "pc1", Apps: []app{a}}, log)

	// Twice is the proof that nobody asked for the second one.
	waitFor(t, 30*time.Second, func() bool { return size(t, dir, "starts") >= 2 }, "the application did not start twice")
	cancel()
	wait()

	// Nothing comes back after the agent has stopped, or stopping the agent leaves an
	// application serving files on the machine.
	stopped := size(t, dir, "starts")
	time.Sleep(500 * time.Millisecond)
	if again := size(t, dir, "starts"); again != stopped {
		t.Errorf("%d starts after the agent stopped, want the %d it had", again, stopped)
	}

	logged := read()
	if !strings.Contains(logged, "the application wrote a line") {
		t.Errorf("what the application wrote is not in the agent's log:\n%s", logged)
	}
	if !strings.Contains(logged, `"app":"files"`) {
		t.Errorf("the log does not say which application wrote it:\n%s", logged)
	}
}

// An application that does not exit is stopped by the agent. One left behind would hold
// the port the next start needs, and would go on serving files with nothing supervising it.
func TestAnApplicationThatKeepsRunningIsStopped(t *testing.T) {
	dir := t.TempDir()
	a := helperApp(t, dir, "stay")
	shortBackoff(t)

	ctx, cancel := context.WithCancel(t.Context())
	wait := superviseApps(ctx, &state{Name: "pc1", Apps: []app{a}}, discard)
	waitFor(t, 30*time.Second, func() bool { return size(t, dir, "alive") > 0 }, "the application never started")

	cancel()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the agent is still waiting for an application it asked to stop")
	}

	quiet := size(t, dir, "alive")
	time.Sleep(500 * time.Millisecond)
	if again := size(t, dir, "alive"); again != quiet {
		t.Errorf("the application is still running after the agent stopped it")
	}
}

// An entry with no command is an application something else starts, and starting it here
// would be a second copy of it.
func TestAnApplicationWithNoCommandIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	a := helperApp(t, dir, "exit")
	a.Command = nil
	shortBackoff(t)

	ctx, cancel := context.WithCancel(t.Context())
	wait := superviseApps(ctx, &state{Name: "pc1", Apps: []app{a}}, discard)
	time.Sleep(200 * time.Millisecond)
	cancel()
	wait()
	if n := size(t, dir, "starts"); n != 0 {
		t.Errorf("the agent started it %d times, want none", n)
	}
}

func size(t *testing.T, dir, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return len(raw)
}

func shortBackoff(t *testing.T) {
	t.Helper()
	wasMin, wasMax := appBackoffMin, appBackoffMax
	appBackoffMin, appBackoffMax = 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { appBackoffMin, appBackoffMax = wasMin, wasMax })
}

func waitFor(t *testing.T, limit time.Duration, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(msg)
}

// logToBuffer collects the agent's log for the test to read. The children write from their
// own goroutines, so the buffer is locked.
func logToBuffer() (*slog.Logger, func() string) {
	var mu sync.Mutex
	var buf bytes.Buffer
	w := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	read := func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
	return slog.New(slog.NewJSONHandler(w, nil)), read
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The installers put dufs next to the agent, so a registry that says `dufs` has to find it
// there when PATH does not have it.
func TestAnApplicationBesideTheAgentIsFoundWithoutAPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const name = "anywhere-file-fake-app"
	path := filepath.Join(filepath.Dir(exe), name)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Skipf("the test binary's own directory is not writable: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	if got := program(name); got != path {
		t.Errorf("program(%q) = %q, want %q", name, got, path)
	}
	// Nothing of that name anywhere is left as it was, so the error the caller gets names
	// the program the registry asked for.
	const missing = "anywhere-file-no-such-app"
	if got := program(missing); got != missing {
		t.Errorf("program(%q) = %q, want it unchanged", missing, got)
	}
}
