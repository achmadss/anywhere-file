package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func auditCounts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT action, count(*) FROM audit_events GROUP BY action`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var action string
		var n int
		if err := rows.Scan(&action, &n); err != nil {
			t.Fatal(err)
		}
		out[action] = n
	}
	return out
}

// One pass through every privileged action of the new model, then one look at the table.
// Each action must appear exactly once: a missing row is history nobody can reconstruct,
// and a doubled row is a path that writes twice.
func TestEveryPrivilegedActionAuditsOnce(t *testing.T) {
	t.Setenv(operatorTokenEnv, "operator-secret")
	pool := freshDB(t, 6)
	reg := newTunnelRegistry()
	reg.setPing(50*time.Millisecond, 100*time.Millisecond)
	m := NewMetrics()
	h := newHandlerWithTunnels(pool, discard, m, reg)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)
	if rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("sync apps: status = %d (body %s)", rec.Code, rec.Body)
	}
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	ownerID := accountIDByEmail(t, pool, "owner@example.com")

	agent := dialTunnel(t, srv, priv, echoApp())
	waitOnline(t, reg, deviceID, true)

	// A refused remote request, here an application the device does not offer.
	if rec := doJSON(t, h, http.MethodGet, "/d/"+deviceID+"/jellyfin/library", nil, bearer(owner)); rec.Code != http.StatusNotFound {
		t.Fatalf("denied remote request: status = %d (body %s)", rec.Code, rec.Body)
	}

	code := inviteCode(t, h, owner, deviceID, "guest")
	signupReq(t, h, "guest@example.com", "correct horse battery")
	guest := signinToken(t, h, "guest@example.com", "correct horse battery")
	if rec := redeemReq(t, h, guest, code); rec.Code != http.StatusOK {
		t.Fatalf("redeem: status = %d (body %s)", rec.Code, rec.Body)
	}
	guestID := accountIDByEmail(t, pool, "guest@example.com")
	if status := revoke(t, h, owner, deviceID, guestID); status != http.StatusOK {
		t.Fatalf("revoke: status = %d", status)
	}

	// The agent goes away, which the ping notices.
	_ = agent.conn.Close()
	waitOnline(t, reg, deviceID, false)

	if status := setStatus(t, h, ownerID, "suspended", operator("operator-secret")); status != http.StatusOK {
		t.Fatalf("suspend: status = %d", status)
	}
	if rec := doJSON(t, h, http.MethodPost, "/v1/devices/"+deviceID+"/disable", nil, bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("disable: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodDelete, "/v1/account",
		map[string]string{"password": "correct horse battery"}, bearer(guest)); rec.Code != http.StatusOK {
		t.Fatalf("delete account: status = %d (body %s)", rec.Code, rec.Body)
	}

	waitAudit(t, pool, ActionTunnelDisconnected, 1)
	got := auditCounts(t, pool)
	want := []string{
		ActionDeviceEnrolled, ActionDeviceAppsSynced, ActionTunnelConnected, ActionRemoteDenied,
		ActionInviteCreated, ActionInviteRedeemed, ActionBindingRevoked, ActionTunnelDisconnected,
		ActionSubscriptionChanged, ActionDeviceDisabled, ActionAccountDeleted,
	}
	for _, action := range want {
		if got[action] != 1 {
			t.Errorf("%s: %d audit rows, want 1", action, got[action])
		}
		delete(got, action)
	}
	if len(got) != 0 {
		t.Errorf("unexpected audit actions: %v", got)
	}
}

// The counters an operator reads during an incident: what the tunnels did and how the
// remote requests ended.
func TestRemoteAndTunnelSeriesReportWhatHappened(t *testing.T) {
	s := remoteScenario(t, echoApp())

	if rec := s.get(t, "/d/"+s.deviceID+"/dufs/files", s.cookies()); rec.Code != http.StatusOK {
		t.Fatalf("remote request: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := s.get(t, "/d/"+s.deviceID+"/jellyfin/library", s.cookies()); rec.Code != http.StatusNotFound {
		t.Fatalf("denied request: status = %d", rec.Code)
	}

	body := scrape(t, s.m)
	for _, want := range []string{
		`rfm_remote_requests_total{status="200"} 1`,
		`rfm_remote_requests_total{status="404"} 1`,
		`rfm_remote_denials_total{reason="no_app"} 1`,
		"rfm_tunnel_connects_total 1",
		"rfm_tunnels_live 1",
		"rfm_remote_request_duration_seconds_count 2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}

	// A tunnel that goes away is counted and leaves the gauge at zero, or an operator
	// reads "one device online" through an outage.
	s.reg.get(s.deviceID).close()
	waitOnline(t, s.reg, s.deviceID, false)
	body = scrape(t, s.m)
	for _, want := range []string{
		`rfm_tunnel_disconnects_total{reason="closed"} 1`,
		"rfm_tunnels_live 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("after the tunnel closed, metrics missing %q", want)
		}
	}
}
