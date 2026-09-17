package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// The HTTP surface of #86. The helpers below speak it the way a client would: a code is
// returned once by create and presented once to redeem.

func createInviteReq(t *testing.T, h http.Handler, session, deviceID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/devices/"+deviceID+"/invites", body, bearer(session))
}

func inviteCode(t *testing.T, h http.Handler, session, deviceID, role string) string {
	t.Helper()
	rec := createInviteReq(t, h, session, deviceID, map[string]string{"role": role})
	if rec.Code != http.StatusOK {
		t.Fatalf("create invite: status = %d (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Code string `json:"code"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Code == "" {
		t.Fatalf("create invite: no code in %q", rec.Body)
	}
	if out.Role != role {
		t.Fatalf("create invite: role = %q, want %q", out.Role, role)
	}
	return out.Code
}

func redeemReq(t *testing.T, h http.Handler, session, code string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/invites/redeem", map[string]string{"code": code}, bearer(session))
}

// plantInvite writes an invite straight to the table, which is how a test gets one that
// is already expired.
func plantInvite(t *testing.T, pool *pgxpool.Pool, deviceID, creator, code, role string, expiresAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO invites (device_id, created_by, code_hash, role, expires_at)
		 VALUES ($1, $2::uuid, $3, $4, $5)`,
		deviceID, creator, hashToken(code), role, expiresAt); err != nil {
		t.Fatalf("plant invite: %v", err)
	}
}

// Only an admin of the device can mint a code, so there is no path from an invite to a
// role the person who made it does not hold.
func TestInviteCreateIsAdminOnly(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	guest := bind(t, h, pool, pc1, "guest@example.com", "guest")
	signupReq(t, h, "stranger@example.com", "correct horse battery")
	stranger := signinToken(t, h, "stranger@example.com", "correct horse battery")

	for name, session := range map[string]string{"guest": guest, "stranger": stranger} {
		if rec := createInviteReq(t, h, session, pc1, map[string]string{"role": "guest"}); rec.Code != http.StatusNotFound {
			t.Errorf("%s creating an invite: status = %d, want 404", name, rec.Code)
		}
	}
	for name, body := range map[string]any{
		"unknown role":    map[string]string{"role": "owner"},
		"bad duration":    map[string]string{"expires_in": "soon"},
		"too long a life": map[string]string{"expires_in": "200h"},
	} {
		if rec := createInviteReq(t, h, owner, pc1, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if rec := createInviteReq(t, h, owner, "no-such-device", map[string]string{}); rec.Code != http.StatusNotFound {
		t.Errorf("invite for an unknown device: status = %d, want 404", rec.Code)
	}
	// An admin can hand out either role, and guest is what you get by default.
	inviteCode(t, h, owner, pc1, "admin")
	if rec := createInviteReq(t, h, owner, pc1, nil); rec.Code != http.StatusOK {
		t.Errorf("invite with no body: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM invites WHERE device_id = $1`, pc1); n != 2 {
		t.Errorf("invites stored = %d, want 2", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1`, ActionInviteCreated); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
}

func TestInviteRedeemBindsOnceAndCannotBeReused(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	code := inviteCode(t, h, owner, pc1, "guest")
	signupReq(t, h, "newcomer@example.com", "correct horse battery")
	newcomer := signinToken(t, h, "newcomer@example.com", "correct horse battery")
	signupReq(t, h, "late@example.com", "correct horse battery")
	late := signinToken(t, h, "late@example.com", "correct horse battery")

	rec := redeemReq(t, h, newcomer, code)
	var out struct {
		DeviceID string `json:"device_id"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("redeem: status = %d (body %s)", rec.Code, rec.Body)
	}
	if out.DeviceID != pc1 || out.Role != "guest" {
		t.Errorf("redeem = %+v, want pc1 as a guest", out)
	}
	newcomerID := accountIDByEmail(t, pool, "newcomer@example.com")
	if n := countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND user_id = $2::uuid AND role = 'guest' AND revoked_at IS NULL`,
		pc1, newcomerID); n != 1 {
		t.Errorf("active guest bindings = %d, want 1", n)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM invites WHERE device_id = $1 AND used_by = $2::uuid AND used_at IS NOT NULL`,
		pc1, newcomerID); n != 1 {
		t.Errorf("used invites = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1`, ActionInviteRedeemed); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}

	// The same code again, by the same person and by a different one.
	if rec := redeemReq(t, h, newcomer, code); rec.Code != http.StatusNotFound {
		t.Errorf("second redeem by the same account: status = %d, want 404", rec.Code)
	}
	if rec := redeemReq(t, h, late, code); rec.Code != http.StatusNotFound {
		t.Errorf("redeem of a used code: status = %d, want 404", rec.Code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1`, pc1); n != 2 {
		t.Errorf("bindings = %d, want 2 (the owner and the newcomer)", n)
	}
}

func TestInviteRedeemRefusals(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	ownerID := accountIDByEmail(t, pool, "owner@example.com")
	signupReq(t, h, "newcomer@example.com", "correct horse battery")
	newcomer := signinToken(t, h, "newcomer@example.com", "correct horse battery")

	plantInvite(t, pool, pc1, ownerID, "expired-code", "guest", time.Now().Add(-time.Minute))
	if rec := redeemReq(t, h, newcomer, "expired-code"); rec.Code != http.StatusNotFound {
		t.Errorf("expired code: status = %d, want 404", rec.Code)
	}
	if rec := redeemReq(t, h, newcomer, "never-issued"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown code: status = %d, want 404", rec.Code)
	}

	// A code whose creator lost their own admin binding is dead, even though the code
	// itself is still fresh.
	second := bind(t, h, pool, pc1, "second@example.com", "admin")
	secondID := accountIDByEmail(t, pool, "second@example.com")
	fromSecond := inviteCode(t, h, second, pc1, "guest")
	if code := revoke(t, h, owner, pc1, secondID); code != http.StatusOK {
		t.Fatalf("revoke second admin: status = %d, want 200", code)
	}
	if rec := redeemReq(t, h, newcomer, fromSecond); rec.Code != http.StatusForbidden {
		t.Errorf("code from a revoked admin: status = %d, want 403", rec.Code)
	}

	// Someone who already has access cannot burn a code on themselves.
	mine := inviteCode(t, h, owner, pc1, "guest")
	if rec := redeemReq(t, h, owner, mine); rec.Code != http.StatusConflict {
		t.Errorf("redeem by an already bound account: status = %d, want 409", rec.Code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM invites WHERE code_hash = $1 AND used_at IS NULL`, hashToken(mine)); n != 1 {
		t.Errorf("the refused code was consumed, want it left unused")
	}

	// And a disabled device takes its invites with it.
	if rec := doJSON(t, h, http.MethodPost, "/v1/devices/"+pc1+"/disable", nil, bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("disable: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := redeemReq(t, h, newcomer, mine); rec.Code != http.StatusForbidden {
		t.Errorf("redeem on a disabled device: status = %d, want 403", rec.Code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1 AND revoked_at IS NULL`, pc1); n != 1 {
		t.Errorf("active bindings = %d, want 1 (only the owner)", n)
	}
}

// Guessing codes is the attack an invite invites. The per-account limit stops it whatever
// address the attempts come from.
func TestInviteRedeemGuessingIsLimitedPerAccount(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	signupReq(t, h, "guesser@example.com", "correct horse battery")
	guesser := signinToken(t, h, "guesser@example.com", "correct horse battery")

	for i := range redeemAccountLimit {
		if rec := redeemReq(t, h, guesser, fmt.Sprintf("guess-%d", i)); rec.Code != http.StatusNotFound {
			t.Fatalf("guess %d: status = %d, want 404", i, rec.Code)
		}
	}
	if rec := redeemReq(t, h, guesser, "guess-again"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("guess past the limit: status = %d, want 429", rec.Code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1`, pc1); n != 1 {
		t.Errorf("bindings = %d, want 1 (only the owner)", n)
	}
}

// Two people present one code at the same instant, through the handler this time rather
// than straight at the table.
func TestConcurrentRedeemOverHTTPBindsOne(t *testing.T) {
	pool := freshDB(t, 8)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	code := inviteCode(t, h, owner, pc1, "guest")
	sessions := make([]string, 2)
	for i, email := range []string{"first@example.com", "second@example.com"} {
		signupReq(t, h, email, "correct horse battery")
		sessions[i] = signinToken(t, h, email, "correct horse battery")
	}

	var start, done sync.WaitGroup
	start.Add(1)
	codes := make([]int, len(sessions))
	for i, session := range sessions {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			codes[i] = redeemReq(t, h, session, code).Code
		}()
	}
	start.Done()
	done.Wait()

	wins := 0
	for _, c := range codes {
		if c == http.StatusOK {
			wins++
		}
	}
	bindings := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1`, pc1)
	if wins != 1 || bindings != 2 {
		t.Errorf("codes %v with %d bindings, want one win and the owner plus one guest", codes, bindings)
	}
}

// A revoked admin who redeems a guest code comes back as a guest. The code decides the
// role, so a revoked binding is not a way back to the role it used to hold.
func TestRedeemRestoresARevokedBindingAtTheInviteRole(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	second := bind(t, h, pool, pc1, "second@example.com", "admin")
	secondID := accountIDByEmail(t, pool, "second@example.com")
	if code := revoke(t, h, owner, pc1, secondID); code != http.StatusOK {
		t.Fatalf("revoke second admin: status = %d, want 200", code)
	}

	code := inviteCode(t, h, owner, pc1, "guest")
	if rec := redeemReq(t, h, second, code); rec.Code != http.StatusOK {
		t.Fatalf("redeem by a revoked admin: status = %d (body %s)", rec.Code, rec.Body)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND user_id = $2::uuid AND role = 'guest' AND revoked_at IS NULL`,
		pc1, secondID); n != 1 {
		t.Errorf("the revoked admin did not come back as one active guest")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1`, pc1); n != 2 {
		t.Errorf("bindings = %d, want 2 (the row is reused, not duplicated)", n)
	}
}
