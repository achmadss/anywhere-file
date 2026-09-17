package main

import (
	"net/http"
	"testing"
)

// Deleting an account removes its bindings, invites and sessions and leaves devices
// alone: the PC keeps its key and its other users. Schema setup uses freshDB, so this
// skips visibly without RFM_TEST_DATABASE_URL, like every other schema test here.
func TestDeleteAccountRemovesBindingsAndKeepsDevices(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()
	h := newHandler(pool, discard, NewMetrics())

	for _, email := range []string{"leaver@example.test", "stayer@example.test"} {
		if rec := signupReq(t, h, email, "correct-horse-123"); rec.Code != http.StatusOK {
			t.Fatalf("signup %s: status = %d (body %s)", email, rec.Code, rec.Body)
		}
	}
	leaver := accountIDByEmail(t, pool, "leaver@example.test")
	stayer := accountIDByEmail(t, pool, "stayer@example.test")
	token := signinToken(t, h, "leaver@example.test", "correct-horse-123")

	// pc1 is the leaver's own PC with the stayer as a guest; pc2 is the stayer's PC with the
	// leaver as a guest and an open invite the leaver created.
	for _, q := range []string{
		`INSERT INTO devices (public_key, name) VALUES (repeat('01', 32), 'pc1'), (repeat('02', 32), 'pc2')`,
		`INSERT INTO device_users (device_id, user_id, role)
		 SELECT device_id, '` + leaver + `', 'admin' FROM devices WHERE name = 'pc1'`,
		`INSERT INTO device_users (device_id, user_id, role)
		 SELECT device_id, '` + stayer + `', 'guest' FROM devices WHERE name = 'pc1'`,
		`INSERT INTO device_users (device_id, user_id, role)
		 SELECT device_id, '` + stayer + `', 'admin' FROM devices WHERE name = 'pc2'`,
		`INSERT INTO device_users (device_id, user_id, role)
		 SELECT device_id, '` + leaver + `', 'guest' FROM devices WHERE name = 'pc2'`,
		`INSERT INTO invites (device_id, created_by, code_hash, role, expires_at)
		 SELECT device_id, '` + leaver + `', 'open-invite', 'guest', now() + interval '1 hour' FROM devices WHERE name = 'pc1'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	rec := doJSON(t, h, http.MethodDelete, "/v1/account",
		map[string]string{"password": "correct-horse-123"}, bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	var n int
	for table, want := range map[string]int{
		`accounts WHERE id = '` + leaver + `'`:          0,
		`sessions WHERE account_id = '` + leaver + `'`:  0,
		`device_users WHERE user_id = '` + leaver + `'`: 0,
		`device_users WHERE user_id = '` + stayer + `'`: 2,
		`invites`: 0,
		`devices`: 2,
		`audit_events WHERE action = 'account.deleted' AND actor = '` + leaver + `'`: 1,
	} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("after delete, %s has %d rows, want %d", table, n, want)
		}
	}

	// The account is gone: its session no longer opens anything.
	if rec := doJSON(t, h, http.MethodGet, "/v1/me", nil, bearer(token)); rec.Code != http.StatusUnauthorized {
		t.Errorf("after delete, me: status = %d, want 401", rec.Code)
	}
	if rec := signinReq(t, h, "leaver@example.test", "correct-horse-123"); rec.Code != http.StatusUnauthorized {
		t.Errorf("after delete, signin: status = %d, want 401", rec.Code)
	}
}

func TestDeleteAccountWrongPasswordKeepsAccount(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "stubborn@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	token := signinToken(t, h, "stubborn@example.test", "correct-horse-123")

	rec := doJSON(t, h, http.MethodDelete, "/v1/account",
		map[string]string{"password": "wrong-password-1"}, bearer(token))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status = %d, want 401 (body %s)", rec.Code, rec.Body)
	}
	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM accounts WHERE email = 'stubborn@example.test'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("account count = %d, err = %v, want 1", n, err)
	}
}
