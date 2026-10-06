package main

// The agent's log file (#220). dufs's lines and every share's events go here, so on a PC
// used every day it grows without end unless it is rotated. The agent writes it itself on
// macOS and Windows, which is what lets one rotation cover both: launchd would hold the file
// open under its old name forever.

import (
	"fmt"
	"os"
	"sync"
)

const (
	logFileMax  = 5 << 20 // bytes in one file before it is moved aside
	logFileKeep = 3       // old files kept: agent.log.1 is the newest, .3 the oldest
)

// logFile is an append-only file that moves itself aside once it is full: the current
// file becomes .1, .1 becomes .2, and the oldest falls off the end.
type logFile struct {
	path string
	max  int64
	keep int

	mu   sync.Mutex
	f    *os.File
	size int64
}

func openLogFile(path string, max int64, keep int) (*logFile, error) {
	l := &logFile{path: path, max: max, keep: keep}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *logFile) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, info.Size()
	return nil
}

func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && l.size > 0 && l.size+int64(len(p)) > l.max {
		// Closed before the renames, because Windows will not rename an open file.
		_ = l.f.Close()
		l.f = nil
		// A rename that fails leaves the file where it is, and it is appended to again.
		// That happens on Windows when a second process has it open.
		for i := l.keep; i > 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", l.path, i-1), fmt.Sprintf("%s.%d", l.path, i))
		}
		_ = os.Rename(l.path, l.path+".1")
	}
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	return l.f.Close()
}
