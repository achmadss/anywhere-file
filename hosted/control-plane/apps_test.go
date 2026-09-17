package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/jackc/pgx/v5/pgxpool"
)

// enrolKey enrols a device and keeps its key, so the test can sign later requests as
// that device.
func enrolKey(t *testing.T, h http.Handler, email, name string) ed25519.PrivateKey {
	t.Helper()
	priv := newDeviceKey(t)
	body := `{"name":"` + name + `","enrolment_token":"` + enrolToken(t, h, email) + `"}`
	if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusOK {
		t.Fatalf("enrol %s: status = %d (body %s)", name, rec.Code, rec.Body)
	}
	return priv
}

// syncApps sends a signed sync. sign is nil for a signed request; pass a no-op to send
// one without a signature.
func syncAppsReq(t *testing.T, h http.Handler, priv ed25519.PrivateKey, body string, sign func(*http.Request, []byte)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/devices/apps", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sign == nil {
		sign = func(r *http.Request, b []byte) { devicesig.Sign(r, priv, b) }
	}
	sign(req, []byte(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func appNames(t *testing.T, pool *pgxpool.Pool, deviceID string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT name FROM device_apps WHERE device_id = $1 ORDER BY name`, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func TestAppSyncReplacesTheList(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)

	rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"},{"name":"jellyfin","type":"http"}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first sync: status = %d (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Apps []string `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Apps) != 2 {
		t.Fatalf("first sync body = %s", rec.Body)
	}
	for _, name := range []string{"dufs", "jellyfin"} {
		if ok, err := deviceAppAllowed(t.Context(), pool, deviceID, name); err != nil || !ok {
			t.Errorf("%s allowed = %v (err %v), want true", name, ok, err)
		}
	}

	// Two applications down to one: the list the device sends is the whole truth.
	if rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("second sync: status = %d (body %s)", rec.Code, rec.Body)
	}
	if got := appNames(t, pool, deviceID); len(got) != 1 || got[0] != "dufs" {
		t.Errorf("apps = %v, want only dufs", got)
	}
	// Routing (#88) asks this before it looks for a tunnel.
	if ok, err := deviceAppAllowed(t.Context(), pool, deviceID, "jellyfin"); err != nil || ok {
		t.Errorf("removed app allowed = %v (err %v), want false", ok, err)
	}

	// And again with the same list, which must change nothing.
	if rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("repeat sync: status = %d (body %s)", rec.Code, rec.Body)
	}
	if got := appNames(t, pool, deviceID); len(got) != 1 || got[0] != "dufs" {
		t.Errorf("apps after repeat = %v, want only dufs", got)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1 AND device_id = $2`, ActionDeviceAppsSynced, deviceID); n != 3 {
		t.Errorf("audit rows = %d, want 3", n)
	}

	// An empty list is a device that offers nothing, which is a valid state.
	if rec := syncAppsReq(t, h, priv, `{"apps":[]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("empty sync: status = %d (body %s)", rec.Code, rec.Body)
	}
	if got := appNames(t, pool, deviceID); len(got) != 0 {
		t.Errorf("apps after empty sync = %v, want none", got)
	}
}

func TestAppSyncRefusals(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)
	if rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("setup sync: status = %d (body %s)", rec.Code, rec.Body)
	}

	// A name reaches a URL, so anything that could carry a path is refused.
	for name, body := range map[string]string{
		"uppercase":      `{"apps":[{"name":"Dufs","type":"http"}]}`,
		"path traversal": `{"apps":[{"name":"../etc","type":"http"}]}`,
		"slash":          `{"apps":[{"name":"media/files","type":"http"}]}`,
		"empty name":     `{"apps":[{"name":"","type":"http"}]}`,
		"long name":      `{"apps":[{"name":"` + strings.Repeat("a", 33) + `","type":"http"}]}`,
		"bad type":       `{"apps":[{"name":"dufs","type":"HTTP/1.1"}]}`,
		"duplicate":      `{"apps":[{"name":"dufs","type":"http"},{"name":"dufs","type":"ssh"}]}`,
	} {
		if rec := syncAppsReq(t, h, priv, body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", name, rec.Code, rec.Body)
		}
	}
	many := make([]string, maxApps+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"name":"app%d","type":"http"}`, i)
	}
	if rec := syncAppsReq(t, h, priv, `{"apps":[`+strings.Join(many, ",")+`]}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("more than %d applications: status = %d, want 400", maxApps, rec.Code)
	}
	if rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, func(*http.Request, []byte) {}); rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned sync: status = %d, want 401", rec.Code)
	}
	if rec := syncAppsReq(t, h, newDeviceKey(t), `{"apps":[]}`, nil); rec.Code != http.StatusNotFound {
		t.Errorf("sync from an unenrolled device: status = %d, want 404", rec.Code)
	}
	if got := appNames(t, pool, deviceID); len(got) != 1 || got[0] != "dufs" {
		t.Errorf("apps after refusals = %v, want the earlier list untouched", got)
	}

	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	if rec := doJSON(t, h, http.MethodPost, "/v1/devices/"+deviceID+"/disable", nil, bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("disable: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := syncAppsReq(t, h, priv, `{"apps":[]}`, nil); rec.Code != http.StatusForbidden {
		t.Errorf("sync from a disabled device: status = %d, want 403", rec.Code)
	}
	if got := appNames(t, pool, deviceID); len(got) != 1 {
		t.Errorf("apps after a disabled sync = %v, want the earlier list untouched", got)
	}
}
