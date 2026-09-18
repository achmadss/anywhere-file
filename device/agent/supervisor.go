package main

// Running the applications (#92). An application entry may carry the command that provides
// it. The agent starts it, keeps it running and stops it on the way out, so a PC that
// reboots comes back serving what it served before with nothing typed into a window.
//
// The command runs as the registry gives it. The agent fills nothing in, so what is in the
// file is what runs. An entry with no command is an application something else starts and
// the agent only forwards to.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

// The wait between restarts, as variables so a test does not spend a minute proving it.
var (
	appBackoffMin = time.Second
	appBackoffMax = time.Minute
)

const (
	// appSettled is how long a child has to stay up before its next exit counts as the
	// first one. Without it an application that fails once a week ends up waiting a
	// minute for a restart it should get at once.
	appSettled = time.Minute
	// appStopGrace is how long a child gets to shut down after being asked, before it is
	// killed. A file being written is the reason to ask first.
	appStopGrace = 5 * time.Second
	// maxAppLogLine caps what is held while waiting for a newline, for a child that
	// writes a progress bar and never ends the line.
	maxAppLogLine = 8 << 10
)

// supervisor keeps the registry's applications running, and follows the registry when it
// changes. A directory shared from the settings page starts being served without the agent
// being restarted, and one removed stops.
type supervisor struct {
	ctx context.Context
	log *slog.Logger

	// One lock for the whole of set, which waits for a stopped child. Changes come from a
	// person pressing a button, so nothing here is on a hot path.
	mu      sync.Mutex
	running map[string]*child
	wg      sync.WaitGroup
}

// child is one application the agent started. done closes when its loop has returned, so
// the port it held is free before a replacement asks for one.
type child struct {
	spec   app
	cancel context.CancelFunc
	done   chan struct{}
}

func newSupervisor(ctx context.Context, log *slog.Logger) *supervisor {
	return &supervisor{ctx: ctx, log: log, running: map[string]*child{}}
}

// set brings what is running into line with apps. An entry with no command is started by
// something else, so the agent leaves it alone.
func (s *supervisor) set(apps []app) {
	want := map[string]app{}
	for _, a := range apps {
		if len(a.Command) > 0 {
			want[a.Name] = a
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var stopping []*child
	for name, c := range s.running {
		if a, ok := want[name]; ok && a.Address == c.spec.Address && slices.Equal(a.Command, c.spec.Command) {
			delete(want, name)
			continue
		}
		delete(s.running, name)
		stopping = append(stopping, c)
	}
	// Stopped before anything starts. A changed entry keeps its name, and two copies of an
	// application serving the same directory is the thing the registry exists to prevent.
	for _, c := range stopping {
		c.cancel()
	}
	for _, c := range stopping {
		<-c.done
	}

	for _, a := range want {
		ctx, cancel := context.WithCancel(s.ctx)
		c := &child{spec: a, cancel: cancel, done: make(chan struct{})}
		s.running[a.Name] = c
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer close(c.done)
			superviseApp(ctx, a, s.log)
		}()
	}
}

// wait returns once every application has exited, which is what the agent's context ending
// brings about.
func (s *supervisor) wait() { s.wg.Wait() }

// superviseApp runs one application for as long as ctx lives. An application that exits is
// an application that comes back: the agent has no way to tell a crash from a restart and
// no reason to treat them differently.
func superviseApp(ctx context.Context, a app, log *slog.Logger) {
	log = log.With("app", a.Name)
	wait := appBackoffMin
	for {
		started := time.Now()
		err := runApp(ctx, a, log)
		if ctx.Err() != nil {
			log.Info("application stopped")
			return
		}
		if time.Since(started) >= appSettled {
			wait = appBackoffMin
		}
		retry := jittered(wait)
		log.Warn("application exited", "err", err, "restart_in", retry.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		wait = min(wait*2, appBackoffMax)
	}
}

// runApp starts the child and returns when it has exited. Whatever it writes goes to the
// agent's log under the application's name, because a service has no window to write to
// and the agent's log is the one place an operator is told to look.
func runApp(ctx context.Context, a app, log *slog.Logger) error {
	cmd := exec.CommandContext(ctx, program(a.Command[0]), a.Command[1:]...)
	cmd.Stdout = &appLog{log: log}
	cmd.Stderr = &appLog{log: log}
	// Ask before killing. Windows has no signal to ask with, so there it is the kill.
	cmd.Cancel = func() error {
		if runtime.GOOS == "windows" {
			return cmd.Process.Kill()
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = appStopGrace
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Info("application started", "pid", cmd.Process.Pid, "address", a.Address)
	return cmd.Wait()
}

// appLog turns what a child writes into log lines. One instance per stream, so the two
// goroutines the child's pipes are copied by never touch the same buffer.
type appLog struct {
	log  *slog.Logger
	rest []byte
}

func (w *appLog) Write(p []byte) (int, error) {
	w.rest = append(w.rest, p...)
	for {
		i := bytes.IndexByte(w.rest, '\n')
		if i < 0 {
			break
		}
		w.line(w.rest[:i])
		w.rest = w.rest[i+1:]
	}
	if len(w.rest) > maxAppLogLine {
		w.line(w.rest)
		w.rest = nil
	}
	return len(p), nil
}

func (w *appLog) line(b []byte) {
	if line := string(bytes.TrimRight(b, "\r")); line != "" {
		w.log.Info(line)
	}
}

// program finds the program a command names. An application that ships with the agent sits
// next to the agent's own binary, and that directory is on nobody's PATH: a launchd job
// inside an app bundle, a systemd unit and a scheduled task each start with the system's
// PATH and nothing of the user's. So PATH is asked first, and this directory second.
func program(name string) string {
	if filepath.Base(name) != name {
		return name
	}
	if _, err := exec.LookPath(name); err == nil {
		return name
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	// LookPath again rather than os.Stat: on Windows it is what adds .exe, and everywhere
	// it is what refuses a file that cannot be run.
	beside := filepath.Join(filepath.Dir(exe), name)
	if found, err := exec.LookPath(beside); err == nil {
		return found
	}
	return name
}
