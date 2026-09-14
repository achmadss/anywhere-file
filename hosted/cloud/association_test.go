package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// attempt records one goroutine's Enable Remote Access, including exactly when its INSERT
// was in flight. The timings are what let the test assert that the writes genuinely
// overlapped rather than queueing up behind each other in the Go code.
type attempt struct {
	account    string
	begunAt    time.Time
	finishedAt time.Time
	err        error
}

// TestConcurrentAssociationsLeaveExactlyOneRow is the acceptance criterion of #19, and the
// reason UNIQUE(workspace_id) is called load-bearing in r3 §10.1: "the first valid request
// wins" has to be a property of the database, because two Enable Remote Access requests for
// the same workspace can land on two instances of this service at the same instant and
// neither will see the other's row until one of them commits.
//
// Every attempt opens its own connection and waits on a barrier, so the INSERTs are in
// flight together. The test then proves they overlapped in wall-clock time before it trusts
// the "exactly one row" result: a sequential run would produce the same row count and prove
// nothing.
func TestConcurrentAssociationsLeaveExactlyOneRow(t *testing.T) {
	const racers = 8
	pool := freshDB(t, racers+2)
	ctx := t.Context()

	accounts := make([]string, racers)
	for i := range accounts {
		var id string
		err := pool.QueryRow(ctx,
			`INSERT INTO accounts (email) VALUES ($1) RETURNING id::text`,
			"racer"+string(rune('a'+i))+"@example.test").Scan(&id)
		if err != nil {
			t.Fatalf("create account %d: %v", i, err)
		}
		accounts[i] = id
	}

	// Warm one connection per racer first. Establishing a TCP connection and doing the
	// startup handshake inside the timed section would spread the attempts out and hide
	// the race behind connection setup.
	warm(t, pool, racers)

	start := make(chan struct{})
	results := make([]attempt, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = enableRemoteAccess(ctx, pool, "ws-contended", accounts[i])
		}()
	}
	close(start)
	wg.Wait()

	var winners, conflicts int
	var winner attempt
	for _, r := range results {
		switch {
		case r.err == nil:
			winners++
			winner = r
		case isUniqueViolation(r.err):
			conflicts++
		default:
			t.Errorf("account %s failed with something other than a unique violation: %v", r.account, r.err)
		}
	}

	overlaps := countOverlaps(results)
	t.Logf("%d attempts: %d committed, %d rejected 23505, %d overlapping pairs of in-flight INSERTs",
		racers, winners, conflicts, overlaps)
	for _, r := range results {
		outcome := "committed"
		if r.err != nil {
			outcome = r.err.Error()
		}
		t.Logf("  account %s in flight %6.2fms  %s",
			r.account[:8], float64(r.finishedAt.Sub(r.begunAt).Microseconds())/1000, outcome)
	}

	if overlaps == 0 {
		t.Fatalf("no two INSERTs were in flight at the same time, so nothing raced and the "+
			"result proves nothing (%d attempts)", racers)
	}
	if winners != 1 {
		t.Errorf("%d attempts committed, want exactly 1", winners)
	}
	if conflicts != racers-1 {
		t.Errorf("%d attempts were rejected with a unique violation, want %d", conflicts, racers-1)
	}

	var rows int
	var owner string
	err := pool.QueryRow(ctx,
		`SELECT count(*) OVER (), owner_account_id::text FROM workspace_associations WHERE workspace_id = 'ws-contended'`).
		Scan(&rows, &owner)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	if rows != 1 {
		t.Fatalf("workspace_associations has %d rows for ws-contended, want 1", rows)
	}
	if owner != winner.account {
		t.Errorf("surviving row is owned by %s, want the account whose INSERT committed, %s", owner, winner.account)
	}
}

// enableRemoteAccess is the write half of r3 §10.1, with none of the request validation that
// #21 will put in front of it. The point here is what happens at the storage layer.
func enableRemoteAccess(ctx context.Context, pool *pgxpool.Pool, workspaceID, accountID string) attempt {
	a := attempt{account: accountID, begunAt: time.Now()}
	a.err = inTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO workspace_associations (workspace_id, owner_account_id, status, trust_list_version)
			 VALUES ($1, $2, 'active', 1)`,
			workspaceID, accountID)
		return err
	})
	a.finishedAt = time.Now()
	return a
}

// warm opens n connections at once and holds them, so the pool has that many live sessions
// before the timed section starts.
func warm(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	conns := make([]*pgxpool.Conn, 0, n)
	for range n {
		c, err := pool.Acquire(t.Context())
		if err != nil {
			t.Fatalf("warm pool: %v", err)
		}
		if _, err := c.Exec(t.Context(), `SELECT 1`); err != nil {
			t.Fatalf("warm pool: %v", err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Release()
	}
}

// countOverlaps counts pairs of attempts whose in-flight windows intersect. One such pair is
// enough to know the writes actually collided inside PostgreSQL.
func countOverlaps(attempts []attempt) int {
	n := 0
	for i := range attempts {
		for j := i + 1; j < len(attempts); j++ {
			a, b := attempts[i], attempts[j]
			if a.begunAt.Before(b.finishedAt) && b.begunAt.Before(a.finishedAt) {
				n++
			}
		}
	}
	return n
}
