package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthReportsOKWhenTheDatabaseAnswers(t *testing.T) {
	pool := freshDB(t, 2)

	rec := httptest.NewRecorder()
	newHandler(pool, discard, NewMetrics(1)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

// A control plane that cannot reach its directory must not report itself healthy, or a load
// balancer keeps sending it requests it can only fail.
func TestHealthReportsUnavailableWhenTheDatabaseIsGone(t *testing.T) {
	pool := connect(t, 2)
	pool.Close()

	rec := httptest.NewRecorder()
	newHandler(pool, discard, NewMetrics(1)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusServiceUnavailable, rec.Body)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	pool := connect(t, 2)

	rec := httptest.NewRecorder()
	newHandler(pool, discard, NewMetrics(1)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// No database needed: the pool is never used, and pgxpool.New does not dial until it is.
func TestServeStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	pool, err := pgxpool.New(ctx, "postgres://nobody@127.0.0.1:1/nothing")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	cfg := config{addr: "127.0.0.1:0", shutdownTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, pool, discard) }()

	// Give the listener a moment to bind, then ask it to stop.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after the context was cancelled")
	}
}
