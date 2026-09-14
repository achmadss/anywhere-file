package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Deleting an account (r3 section 14): owned workspaces become local-only, memberships
// are removed, files and device keys are untouched, and the agents are told through
// agent_messages. Schema setup uses freshDB, so this skips visibly without
// RFM_TEST_DATABASE_URL, like every other schema test here.

func TestDeleteAccountLeavesWorkspacesLocalAndTellsAgents(t *testing.T) {
	pool := freshDB(t, 8)
	ctx := t.Context()
	h := newHandler(pool, discard, NewMetrics(1))

	if rec := signupReq(t, h, "owner-del@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup owner: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := signupReq(t, h, "keeper-del@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup keeper: status = %d (body %s)", rec.Code, rec.Body)
	}
	ownerID := accountIDByEmail(t, pool, "owner-del@example.test")
	keeperID := accountIDByEmail(t, pool, "keeper-del@example.test")
	ownerToken := signinToken(t, h, "owner-del@example.test", "correct-horse-123")

	ownerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	memberPub, memberPriv, _ := ed25519.GenerateKey(rand.Reader)
	nasPub, _, _ := ed25519.GenerateKey(rand.Reader)
	ownerDev := hex.EncodeToString(ownerPub)
	memberDev := hex.EncodeToString(memberPub)
	nasDev := hex.EncodeToString(nasPub)

	// ws-owned-1 belongs to the deleting account and holds three devices. ws-kept-1
	// belongs to the keeper; the deleting account is only a member through one device.
	for _, q := range []string{
		`INSERT INTO workspace_associations (workspace_id, owner_account_id, status, trust_list_version)
		 VALUES ('ws-owned-1', '` + ownerID + `', 'active', 2)`,
		`INSERT INTO workspace_members (workspace_id, account_id, role, status) VALUES
		 ('ws-owned-1', '` + ownerID + `', 'owner', 'active'),
		 ('ws-owned-1', '` + keeperID + `', 'member', 'active')`,
		`INSERT INTO devices (device_key, account_id, display_name) VALUES
		 ('` + ownerDev + `', '` + ownerID + `', 'owner laptop'),
		 ('` + memberDev + `', '` + ownerID + `', 'owner desktop'),
		 ('` + nasDev + `', NULL, 'local NAS')`,
		`INSERT INTO device_authorizations (workspace_id, device_key, status) VALUES
		 ('ws-owned-1', '` + ownerDev + `', 'active'),
		 ('ws-owned-1', '` + memberDev + `', 'active'),
		 ('ws-owned-1', '` + nasDev + `', 'active')`,
		`INSERT INTO workspace_associations (workspace_id, owner_account_id, status, trust_list_version)
		 VALUES ('ws-kept-1', '` + keeperID + `', 'active', 1)`,
		`INSERT INTO workspace_members (workspace_id, account_id, role, status) VALUES
		 ('ws-kept-1', '` + keeperID + `', 'owner', 'active'),
		 ('ws-kept-1', '` + ownerID + `', 'member', 'active')`,
		`INSERT INTO device_authorizations (workspace_id, device_key, status) VALUES
		 ('ws-kept-1', '` + memberDev + `', 'active')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	rec := doJSON(t, h, http.MethodDelete, "/v1/account",
		map[string]string{"password": "correct-horse-123"}, bearer(ownerToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var deleted struct {
		LocalOnly []string `json:"local_only_workspaces"`
		Removed   []string `json:"removed_memberships"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if len(deleted.LocalOnly) != 1 || deleted.LocalOnly[0] != "ws-owned-1" {
		t.Errorf("local_only = %v, want [ws-owned-1]", deleted.LocalOnly)
	}
	if len(deleted.Removed) != 1 || deleted.Removed[0] != "ws-kept-1" {
		t.Errorf("removed = %v, want [ws-kept-1]", deleted.Removed)
	}

	// The owned workspace is local-only: no association, no members, no mirror rows.
	var n int
	for table, want := range map[string]int{
		`workspace_associations WHERE workspace_id = 'ws-owned-1'`: 0,
		`workspace_members WHERE workspace_id = 'ws-owned-1'`:      0,
		`device_authorizations WHERE workspace_id = 'ws-owned-1'`:  0,
		`workspace_associations WHERE workspace_id = 'ws-kept-1'`:  1,
		`workspace_members WHERE workspace_id = 'ws-kept-1'`:       1,
		`sessions WHERE account_id = '` + ownerID + `'`:            0,
		`accounts WHERE id = '` + ownerID + `'`:                    0,
	} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("after delete, %s has %d rows, want %d", table, n, want)
		}
	}

	// Files and device keys are untouched: every device row survives, and the bound
	// ones simply clear their account link.
	for _, key := range []string{ownerDev, memberDev, nasDev} {
		var accountID *string
		if err := pool.QueryRow(ctx,
			`SELECT account_id::text FROM devices WHERE device_key = $1`, key).Scan(&accountID); err != nil {
			t.Errorf("device %s gone: %v", key[:8], err)
		} else if accountID != nil {
			t.Errorf("device %s still bound to %s, want unbound", key[:8], *accountID)
		}
	}

	// The audit trail names both workspaces.
	for _, want := range [][2]string{
		{"ws-owned-1", "workspace.local_only"},
		{"ws-kept-1", "membership.removed"},
	} {
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = $2`,
			want[0], want[1]).Scan(&n); err != nil || n < 1 {
			t.Errorf("audit for %v: count = %d, err = %v, want at least 1", want, n, err)
		}
	}

	// The agents are told: the member device polls its inbox and finds both notices.
	// It authenticates as itself, with no session, since its account is gone.
	poll := func(deviceKey string, priv ed25519.PrivateKey) []map[string]any {
		t.Helper()
		path := "/v1/agent/messages?device_key=" + deviceKey
		headers := signAgentRequest(t, http.MethodGet, "/v1/agent/messages", deviceKey, priv, randomNonce(t), time.Now(), []byte{})
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range headers {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("messages for %s: status = %d (body %s)", deviceKey[:8], rec.Code, rec.Body)
		}
		var out struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode messages: %v", err)
		}
		return out.Messages
	}
	messages := poll(memberDev, memberPriv)
	kinds := map[string]bool{}
	for _, m := range messages {
		kinds[m["workspace_id"].(string)+"/"+m["kind"].(string)] = true
	}
	for _, want := range []string{"ws-owned-1/workspace.local_only", "ws-kept-1/membership.removed"} {
		if !kinds[want] {
			t.Errorf("device inbox has %v, want %q too", kinds, want)
		}
	}

	// The account is gone: its session no longer opens anything.
	if rec := doJSON(t, h, http.MethodGet, "/v1/me", nil, bearer(ownerToken)); rec.Code != http.StatusUnauthorized {
		t.Errorf("after delete, me: status = %d, want 401", rec.Code)
	}
	if rec := signinReq(t, h, "owner-del@example.test", "correct-horse-123"); rec.Code != http.StatusUnauthorized {
		t.Errorf("after delete, signin: status = %d, want 401", rec.Code)
	}
}

func TestDeleteAccountWrongPasswordKeepsAccount(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics(1))

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
