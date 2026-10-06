package main

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
)

// handshakeStatus is what the tunnel endpoint answers a signed handshake with, without
// keeping the connection.
func handshakeStatus(t *testing.T, h http.Handler, priv ed25519.PrivateKey) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/tunnel", nil)
	devicesig.Sign(req, priv, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// An admin removes a PC they no longer have (#189). Everyone loses it, its tunnel closes,
// and when it dials again the server says it was removed. Signing in on the PC again
// brings it back for the admin and nobody else.
func TestAnAdminRemovesADeviceForEveryone(t *testing.T) {
	pool := freshDB(t, 4)
	h, reg, srv := tunnelHandler(t, pool)
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	pc1 := derivedDeviceID(priv)
	guest := bind(t, h, pool, pc1, "guest@example.com", "guest")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	remove := func(session string) int {
		return doJSON(t, h, http.MethodPost, "/v1/devices/"+pc1+"/remove", nil, bearer(session)).Code
	}

	peer := dialTunnel(t, srv, priv, http.NotFoundHandler())
	waitOnline(t, reg, pc1, true)

	if code := remove(guest); code != http.StatusNotFound {
		t.Fatalf("guest removes: status = %d, want 404", code)
	}
	if code := remove(owner); code != http.StatusOK {
		t.Fatalf("admin removes: status = %d, want 200", code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1 AND revoked_at IS NULL`, pc1); n != 0 {
		t.Errorf("live bindings after removal = %d, want 0", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1`, pc1); n != 2 {
		t.Errorf("binding rows after removal = %d, want both kept for history", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1 AND device_id = $2`, ActionDeviceRemoved, pc1); n != 1 {
		t.Errorf("removal audit rows = %d, want 1", n)
	}
	select {
	case <-peer.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel was still open after the removal")
	}
	waitOnline(t, reg, pc1, false)

	// The admin is no longer an admin of it, so a second removal is a stranger's.
	if code := remove(owner); code != http.StatusNotFound {
		t.Errorf("second removal: status = %d, want 404", code)
	}

	// The PC comes back online and is told.
	if code := handshakeStatus(t, h, priv); code != http.StatusGone {
		t.Errorf("handshake after removal: status = %d, want 410", code)
	}

	// Signed in again on the PC, it is the admin's once more, and the guest stays removed.
	body := `{"name":"pc1","enrolment_token":"` + enrolToken(t, h, "owner@example.com") + `"}`
	if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusOK {
		t.Fatalf("enrol again: status = %d (body %s)", rec.Code, rec.Body)
	}
	if n := activeAdmins(t, pool, pc1); n != 1 {
		t.Errorf("active admins after signing in again = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1 AND role = 'guest' AND revoked_at IS NULL`, pc1); n != 0 {
		t.Errorf("active guests after signing in again = %d, want 0", n)
	}
	dialTunnel(t, srv, priv, http.NotFoundHandler())
	waitOnline(t, reg, pc1, true)
}
