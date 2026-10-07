package main

import (
	"context"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A taken gateway or settings port is waited for in the same process. Exiting instead has
// the service manager start the agent again every few seconds, for as long as the other
// program runs. The person is told once, and not on every try.
func TestATakenPortIsWaitedFor(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := held.Addr().String()
	wasRetry, wasTell := portRetry, tellPerson
	t.Cleanup(func() { portRetry, tellPerson = wasRetry, wasTell })
	portRetry = 20 * time.Millisecond
	var mu sync.Mutex
	var told []string
	tellPerson = func(msg string) { mu.Lock(); told = append(told, msg); mu.Unlock() }

	got := make(chan net.Listener, 1)
	go func() {
		ln, err := listenWhenFree(t.Context(), addr, addr, discard)
		if err != nil {
			t.Error(err)
		}
		got <- ln
	}()
	select {
	case <-got:
		t.Fatal("listened on a port another program holds")
	case <-time.After(300 * time.Millisecond):
	}
	_ = held.Close()
	select {
	case ln := <-got:
		defer ln.Close()
		if ln.Addr().String() != addr {
			t.Errorf("listening on %s, want %s", ln.Addr(), addr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting after the port was let go")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(told) != 1 {
		t.Errorf("the person was told %d times, want once: %q", len(told), told)
	}
}

// A share whose port another program has taken comes back on a new one, and the registry
// keeps the new one, with nothing done by the person.
func TestAShareWhosePortIsTakenMovesToANewOne(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	oldPort := strconv.Itoa(held.Addr().(*net.TCPAddr).Port)

	appDir := t.TempDir()
	a := helperApp(t, appDir, "stay")
	a.Address = held.Addr().String()
	a.Command = append(a.Command, "--", "--port", oldPort)
	shortBackoff(t)

	log, read := logToBuffer()
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(read())
		}
	})
	dir := agentDir(t)
	st := &state{Name: "pc1", Apps: []app{a}}
	if err := saveState(dir, st); err != nil {
		t.Fatal(err)
	}
	ag := newAgent(dir, testKey(t), st, log)
	ctx, cancel := context.WithCancel(t.Context())
	apps := newSupervisor(ctx, log, ag.moveApp)
	ag.onApps = apps.set
	apps.set([]app{a})
	defer func() { cancel(); apps.wait() }()

	waitFor(t, 30*time.Second, func() bool {
		st, err := loadState(dir)
		return err == nil && st.Apps[0].Address != a.Address
	}, "the registry still has the taken port")
	st, err = loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	moved := st.Apps[0]
	_, newPort, _ := net.SplitHostPort(moved.Address)
	if !slices.Contains(moved.Command, newPort) || slices.Contains(moved.Command, oldPort) {
		t.Errorf("command %q does not run on the new port %s", moved.Command, newPort)
	}
	waitFor(t, 30*time.Second, func() bool { return size(t, appDir, "alive") > 0 }, "the share did not start on its new port")
}

// One agent per PC (#222). A second user's agent finds the first one's on its port, and
// the person is told that, rather than which program it is, which another user's process
// hides from lsof and ss.
func TestAnotherUsersAgentOnThePortIsNamed(t *testing.T) {
	other := lanServer(t, testKey(t))
	addr := other.Listener.Addr().String()
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if agentAnswers(plain.Addr().String()) {
		t.Error("a program that is not an agent was taken for one")
	}

	wasTell := tellPerson
	t.Cleanup(func() { tellPerson = wasTell; toldOtherUser.Store(false) })
	told := make(chan string, 1)
	tellPerson = func(msg string) { told <- msg }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _, _ = listenWhenFree(ctx, addr, addr, discard) }()
	select {
	case msg := <-told:
		if msg != otherUser {
			t.Errorf("told %q, want %q", msg, otherUser)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the person was told nothing")
	}
}
