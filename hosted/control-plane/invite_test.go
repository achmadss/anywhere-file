package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// attempt records one goroutine's redeem, including exactly when its UPDATE was in
// flight. The timings are what let the test assert that the writes genuinely overlapped
// rather than queueing up behind each other in the Go code.
type attempt struct {
	account    string
	begunAt    time.Time
	finishedAt time.Time
	won        bool
	err        error
}

// TestConcurrentInviteRedeemsConsumeOnce is the acceptance criterion of #82: an invitation
// code is a bearer credential, and two accounts presenting it at the same instant must
// not both get a binding. The guard is the database, not Go: the UPDATE that consumes the
// invite takes the row lock, and the loser re-evaluates its WHERE clause after the winner
// commits and finds used_at set.
//
// Every attempt opens its own connection and waits on a barrier, so the UPDATEs are in
// flight together. The test then proves they overlapped in wall-clock time before it
// trusts the "exactly one binding" result: a sequential run would produce the same count
// and prove nothing.
func TestConcurrentInviteRedeemsConsumeOnce(t *testing.T) {
	const racers = 8
	pool := freshDB(t, racers+2)
	ctx := t.Context()

	var owner string
	if err := pool.QueryRow(ctx,
		`INSERT INTO accounts (email) VALUES ('owner@example.test') RETURNING id::text`).Scan(&owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var deviceID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO devices (public_key, name) VALUES (repeat('ab', 32), 'pc1') RETURNING device_id`).Scan(&deviceID); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO invites (device_id, created_by, code_hash, role, expires_at)
		 VALUES ($1, $2::uuid, 'contended-hash', 'guest', now() + interval '1 hour')`,
		deviceID, owner); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	accounts := make([]string, racers)
	for i := range accounts {
		err := pool.QueryRow(ctx,
			`INSERT INTO accounts (email) VALUES ($1) RETURNING id::text`,
			"racer"+string(rune('a'+i))+"@example.test").Scan(&accounts[i])
		if err != nil {
			t.Fatalf("create account %d: %v", i, err)
		}
	}

	// Warm one connection per racer first. Establishing a TCP connection inside the timed
	// section would spread the attempts out and hide the race behind connection setup.
	warm(t, pool, racers)

	start := make(chan struct{})
	results := make([]attempt, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = redeemInvite(ctx, pool, "contended-hash", accounts[i])
		}()
	}
	close(start)
	wg.Wait()

	var winners int
	var winner attempt
	for _, r := range results {
		if r.err != nil {
			t.Errorf("account %s failed: %v", r.account[:8], r.err)
		}
		if r.won {
			winners++
			winner = r
		}
	}
	overlaps := countOverlaps(results)
	t.Logf("%d attempts: %d consumed the invite, %d overlapping pairs of in-flight UPDATEs", racers, winners, overlaps)
	if overlaps == 0 {
		t.Fatalf("no two UPDATEs were in flight at the same time, so nothing raced and the result proves nothing")
	}
	if winners != 1 {
		t.Errorf("%d attempts consumed the invite, want exactly 1", winners)
	}

	var bindings int
	var boundTo, usedBy string
	err := pool.QueryRow(ctx,
		`SELECT count(*) OVER (), du.user_id::text, i.used_by::text
		 FROM device_users du JOIN invites i ON i.device_id = du.device_id
		 WHERE du.device_id = $1`, deviceID).Scan(&bindings, &boundTo, &usedBy)
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if bindings != 1 || boundTo != winner.account || usedBy != winner.account {
		t.Errorf("device has %d bindings for %s (invite used_by %s), want 1 for the winner %s",
			bindings, boundTo, usedBy, winner.account)
	}

	// The schema, not only the WHERE clause, keeps a used invite used: even an UPDATE
	// with no guard is refused.
	if _, err := pool.Exec(ctx,
		`UPDATE invites SET used_by = $1::uuid WHERE code_hash = 'contended-hash'`, accounts[0]); err == nil {
		t.Error("re-consuming a used invite succeeded, want the single-use trigger to reject it")
	}
}

// redeemInvite is the write half of docs/new-arch.md "Invitations": one UPDATE guarded by
// used_at IS NULL, and the binding only from the request whose UPDATE returned a row.
// Request validation is #86; the point here is what happens at the storage layer.
func redeemInvite(ctx context.Context, pool *pgxpool.Pool, codeHash, accountID string) attempt {
	a := attempt{account: accountID, begunAt: time.Now()}
	a.err = inTx(ctx, pool, func(tx pgx.Tx) error {
		var deviceID, role string
		err := tx.QueryRow(ctx,
			`UPDATE invites SET used_at = now(), used_by = $1::uuid
			 WHERE code_hash = $2 AND used_at IS NULL AND expires_at > now()
			 RETURNING device_id, role`, accountID, codeHash).Scan(&deviceID, &role)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		a.won = true
		_, err = tx.Exec(ctx,
			`INSERT INTO device_users (device_id, user_id, role) VALUES ($1, $2::uuid, $3)`,
			deviceID, accountID, role)
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
