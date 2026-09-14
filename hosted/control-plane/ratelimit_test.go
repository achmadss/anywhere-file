package main

import (
	"fmt"
	"testing"
	"time"
)

// No database: the limiter is pure memory.
func TestRateLimiterDropsIdleKeys(t *testing.T) {
	l := newRateLimiter(5, 50*time.Millisecond)

	const keys = 500
	for i := range keys {
		if !l.allow(fmt.Sprintf("10.1.%d.%d", i/256, i%256)) {
			t.Fatalf("key %d: first hit rejected, want allowed", i)
		}
	}
	l.mu.Lock()
	grown := len(l.hits)
	l.mu.Unlock()
	if grown != keys {
		t.Fatalf("map holds %d keys, want %d before the sweep", grown, keys)
	}

	// Let every window lapse, then send one more hit. The sweep on that hit must
	// drop all 500 idle keys, leaving only the new one.
	time.Sleep(60 * time.Millisecond)
	if !l.allow("10.2.0.1") {
		t.Fatal("hit after the window rejected, want allowed")
	}
	l.mu.Lock()
	kept := len(l.hits)
	l.mu.Unlock()
	if kept != 1 {
		t.Errorf("map holds %d keys after the sweep, want 1: idle keys are never dropped", kept)
	}
}

func TestRateLimiterRejectsOverLimit(t *testing.T) {
	l := newRateLimiter(3, time.Minute)
	for range 3 {
		if !l.allow("10.3.0.1") {
			t.Fatal("hit inside the limit rejected, want allowed")
		}
	}
	if l.allow("10.3.0.1") {
		t.Error("fourth hit inside the window allowed, want rejected")
	}
	if !l.allow("10.3.0.2") {
		t.Error("other key rejected, want per-key limits")
	}
}
