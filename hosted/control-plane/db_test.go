package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a real PostgreSQL. There is no in-memory stand-in that would prove
// anything here: the thing under test is a PostgreSQL constraint and PostgreSQL's own
// behaviour when two transactions race for it.
//
//	cd hosted/control-plane && docker compose up -d
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
		"accounts", "audit_events", "device_apps", "device_enrolments", "device_nonces", "device_users", "devices",
		"email_verification_tokens", "enrolment_tokens", "invites", "job_heartbeats", "password_reset_tokens",
		"schema_migrations", "sessions", "subscriptions",
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

// The rules #82 puts in the schema rather than in Go, each proven to bite once.
func TestSchemaConstraints(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	var deviceID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO devices (public_key) VALUES (repeat('ab', 32)) RETURNING device_id`).Scan(&deviceID); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	if deviceID == "" || deviceID == "abababababababababababababababababababababababababababababababab" {
		t.Errorf("device_id = %q, want a value derived from the key", deviceID)
	}
	var user string
	if err := pool.QueryRow(ctx,
		`INSERT INTO accounts (email) VALUES ('u@example.test') RETURNING id::text`).Scan(&user); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_users (device_id, user_id, role) VALUES ($1, $2::uuid, 'admin')`, deviceID, user); err != nil {
		t.Fatalf("insert binding: %v", err)
	}

	for name, sql := range map[string]string{
		"agent-supplied device_id":       `INSERT INTO devices (public_key, device_id) VALUES (repeat('cd', 32), 'chosen')`,
		"public key not lowercase hex":   `INSERT INTO devices (public_key) VALUES ('ABCD')`,
		"unknown device status":          `UPDATE devices SET status = 'lost' WHERE device_id = '` + deviceID + `'`,
		"unknown role":                   `INSERT INTO device_users (device_id, user_id, role) VALUES ('` + deviceID + `', '` + user + `'::uuid, 'owner')`,
		"second row per device and user": `INSERT INTO device_users (device_id, user_id, role) VALUES ('` + deviceID + `', '` + user + `'::uuid, 'guest')`,
		"invite used_by without used_at": `INSERT INTO invites (device_id, created_by, code_hash, role, expires_at, used_by)
			VALUES ('` + deviceID + `', '` + user + `'::uuid, 'h', 'guest', now(), '` + user + `'::uuid)`,
	} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Errorf("%s: accepted, want the schema to reject it", name)
		}
	}
}
