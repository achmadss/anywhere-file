package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// advisoryLockKey serialises migration runs across processes. Any constant works as long as
// every instance uses the same one.
const advisoryLockKey = 8291734

type migration struct {
	version string
	up      string
	down    string
}

// loadMigrations reads migrations/<version>.up.sql and the matching .down.sql, ordered by
// version. A missing down file is an error: a migration that cannot be reversed is not
// allowed in, because the acceptance criterion is that the schema runs both ways.
func loadMigrations() ([]migration, error) {
	names, err := fs.Glob(migrationFS, "migrations/*.up.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	out := make([]migration, 0, len(names))
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".up.sql")
		up, err := migrationFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		down, err := migrationFS.ReadFile("migrations/" + version + ".down.sql")
		if err != nil {
			return nil, fmt.Errorf("migration %s has no down file: %w", version, err)
		}
		out = append(out, migration{version: version, up: string(up), down: string(down)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations embedded")
	}
	return out, nil
}

func ensureMigrationTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`)
	return err
}

func appliedVersions(ctx context.Context, db *pgxpool.Pool) (map[string]bool, error) {
	rows, err := db.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// migrateUp applies every migration that is not yet recorded, each in its own transaction.
func migrateUp(ctx context.Context, db *pgxpool.Pool, log *slog.Logger) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	return withMigrationLock(ctx, db, func(ctx context.Context) error {
		if err := ensureMigrationTable(ctx, db); err != nil {
			return err
		}
		applied, err := appliedVersions(ctx, db)
		if err != nil {
			return err
		}
		for _, m := range migrations {
			if applied[m.version] {
				continue
			}
			err := inTx(ctx, db, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.up); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version)
				return err
			})
			if err != nil {
				return fmt.Errorf("apply %s: %w", m.version, err)
			}
			log.Info("migration applied", "version", m.version)
		}
		return nil
	})
}

// migrateDown reverses the last steps applied migrations, newest first. steps <= 0 reverses
// all of them.
func migrateDown(ctx context.Context, db *pgxpool.Pool, steps int, log *slog.Logger) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	return withMigrationLock(ctx, db, func(ctx context.Context) error {
		if err := ensureMigrationTable(ctx, db); err != nil {
			return err
		}
		applied, err := appliedVersions(ctx, db)
		if err != nil {
			return err
		}
		done := 0
		for i := len(migrations) - 1; i >= 0; i-- {
			m := migrations[i]
			if !applied[m.version] {
				continue
			}
			if steps > 0 && done == steps {
				break
			}
			err := inTx(ctx, db, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.down); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.version)
				return err
			})
			if err != nil {
				return fmt.Errorf("reverse %s: %w", m.version, err)
			}
			log.Info("migration reversed", "version", m.version)
			done++
		}
		return nil
	})
}

// withMigrationLock holds a session-level advisory lock on one connection for the duration
// of fn, so two instances starting at once do not both apply the same migration.
func withMigrationLock(ctx context.Context, db *pgxpool.Pool, fn func(context.Context) error) error {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return err
	}
	defer func() {
		// Best effort: releasing the connection without unlocking would leave the lock held
		// until the pooled session is closed.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}()

	return fn(ctx)
}

func inTx(ctx context.Context, db *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
