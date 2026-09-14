package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a real PostgreSQL. There is no in-memory stand-in that would prove
// anything here: the thing under test is a PostgreSQL constraint and PostgreSQL's own
// behaviour when two transactions race for it.
//
//	cd cloud && docker compose up -d
//	RFM_TEST_DATABASE_URL=postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable go test ./...
const testDatabaseURLEnv = "RFM_TEST_DATABASE_URL"

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// freshDB returns a pool on an empty, fully migrated database. The schema is dropped first,
// so a test never inherits rows from the one before it.
func freshDB(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	pool := connect(t, maxConns)

	ctx := t.Context()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := migrateUp(ctx, pool, discard); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return pool
}

func connect(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testDatabaseURLEnv)
	if url == "" {
		t.Skipf("%s is not set; start hosted/control-plane/docker-compose.yml and export it", testDatabaseURLEnv)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse %s: %v", testDatabaseURLEnv, err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

func tableNames(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return names
}

func TestMigrationsRunForwardAndBack(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	want := []string{
		"accounts", "agent_messages", "audit_events", "device_authorizations", "device_nonces",
		"devices", "email_verification_tokens", "job_heartbeats", "pairing_requests",
		"password_reset_tokens", "schema_migrations", "sessions", "subscriptions",
		"transfer_requests", "workspace_associations", "workspace_members",
	}
	got := tableNames(t, pool)
	if len(got) != len(want) {
		t.Fatalf("after up, tables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("after up, tables = %v, want %v", got, want)
		}
	}

	if err := migrateDown(ctx, pool, 0, discard); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	// schema_migrations survives a full down: it is the runner's own bookkeeping, not part
	// of any migration.
	if got := tableNames(t, pool); len(got) != 1 || got[0] != "schema_migrations" {
		t.Fatalf("after down, tables = %v, want only schema_migrations", got)
	}
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 0 {
		t.Fatalf("after down, %d migrations still recorded", applied)
	}

	// And forward again, which is the case a rollback in production is followed by.
	if err := migrateUp(ctx, pool, discard); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if got := tableNames(t, pool); len(got) != len(want) {
		t.Fatalf("after second up, tables = %v, want %v", got, want)
	}
}

// A skip looks like a pass in short output, so one test says out loud whether
// the schema suite ran or skipped. Read the SKIP line as a failure to provide
// a database, not as success.
func TestSchemaTestsReportPresence(t *testing.T) {
	if os.Getenv(testDatabaseURLEnv) == "" {
		t.Skipf("SKIP: %s is not set, schema tests did not run. Start hosted/control-plane/docker-compose.yml and export it.", testDatabaseURLEnv)
	}
	t.Logf("RUN: %s is set, schema tests run against a real PostgreSQL.", testDatabaseURLEnv)
}

func TestMigrateUpIsIdempotent(t *testing.T) {
	pool := freshDB(t, 4)
	if err := migrateUp(t.Context(), pool, discard); err != nil {
		t.Fatalf("second migrate up: %v", err)
	}
}

// The mirror comment is a requirement of #19, so it is asserted rather than trusted to
// survive the next person's edit.
func TestDeviceAuthorizationsIsDocumentedAsAMirror(t *testing.T) {
	pool := freshDB(t, 4)

	var comment string
	err := pool.QueryRow(t.Context(),
		`SELECT obj_description('device_authorizations'::regclass, 'pg_class')`).Scan(&comment)
	if err != nil {
		t.Fatalf("read comment: %v", err)
	}
	if !strings.Contains(comment, "NEVER AUTHORITATIVE") || !strings.Contains(comment, "trust list") {
		t.Fatalf("device_authorizations comment does not say it is a mirror: %q", comment)
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	for i := range 2 {
		if err := seed(ctx, pool, discard); err != nil {
			t.Fatalf("seed %d: %v", i+1, err)
		}
	}

	for table, want := range map[string]int{
		"accounts": 2, "subscriptions": 1, "workspace_associations": 1,
		"workspace_members": 2, "devices": 3, "device_authorizations": 3, "audit_events": 1,
	} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("after two seeds, %s has %d rows, want %d", table, n, want)
		}
	}
}
