package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-key sliding window counter: each key keeps its hit times and a
// hit counts only inside the window behind now. Auth endpoints share nothing else, so a
// small in-memory limiter is enough (r3 section 10.6: no Redis in v1). Each route gets
// its own limiter, keyed by client IP. State lives on the handler, so tests get a fresh
// one per newHandler call.
//
// The limiter is per process by choice. With more than one cloud instance behind the
// balancer the real limit is the configured one times the instance count, and it resets
// on deploy. That matches r3 line 571: nothing in v1 needs cross-instance state.
type rateLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	limit     int
	window    time.Duration
	lastSweep time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

// allow records a hit for key and reports whether it is inside the limit.
//
// The sweep runs first so idle keys are gone before this one is counted. Without it a
// flood of distinct addresses would grow the map forever and turn the limiter into its
// own memory exhaustion path. What is left is bounded by the hits one window can hold.
func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)

	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// sweepLocked drops every key whose window is empty. It runs at most once per window,
// so a flood of distinct keys costs one scan per window, not one per hit.
func (l *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for key, times := range l.hits {
		alive := 0
		for _, t := range times {
			if now.Sub(t) < l.window {
				alive++
			}
		}
		if alive == 0 {
			delete(l.hits, key)
		}
	}
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			w.Header().Set("Retry-After", strconv.Itoa(int(l.window.Seconds())))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP prefers X-Forwarded-For, which is what a load balancer in front of this
// service sends. Locally it falls back to the connection address.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, _ := strings.Cut(fwd, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	host := strings.TrimSpace(r.RemoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
