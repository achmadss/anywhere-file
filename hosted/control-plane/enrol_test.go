package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newDeviceKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func derivedDeviceID(priv ed25519.PrivateKey) string {
	sum := sha256.Sum256(priv.Public().(ed25519.PublicKey))
	return hex.EncodeToString(sum[:])
}

// enrolToken signs up (or signs in) the account and mints one enrolment token through the
// HTTP surface, as the client would.
func enrolToken(t *testing.T, h http.Handler, email string) string {
	t.Helper()
	signupReq(t, h, email, "correct horse battery")
	session := signinToken(t, h, email, "correct horse battery")
	rec := doJSON(t, h, http.MethodPost, "/v1/devices/enrolment-token", nil, bearer(session))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint token: status = %d (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("mint token: no token in %q", rec.Body)
	}
	return out.Token
}

// enrol sends a signed enrolment. sign is nil for an unsigned request; the default signs
// with a fresh nonce.
func enrol(t *testing.T, h http.Handler, priv ed25519.PrivateKey, body string, sign func(*http.Request, []byte)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/devices/enrol", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sign == nil {
		sign = func(r *http.Request, b []byte) { devicesig.Sign(r, priv, b) }
	}
	sign(req, []byte(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestEnrolCreatesDeviceAndAdminBinding(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)
	token := enrolToken(t, h, "owner@example.com")

	// The agent's body carries a device_id of its choosing. It must be ignored.
	body := `{"device_id":"attacker-chosen","public_key":"` + hex.EncodeToString(priv.Public().(ed25519.PublicKey)) +
		`","name":"pc1","enrolment_token":"` + token + `"}`
	rec := enrol(t, h, priv, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol: status = %d (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.DeviceID != derivedDeviceID(priv) {
		t.Errorf("device_id = %q, want the derived %q", out.DeviceID, derivedDeviceID(priv))
	}

	owner := accountIDByEmail(t, pool, "owner@example.com")
	if n := countRows(t, pool, `SELECT count(*) FROM devices WHERE device_id = $1 AND name = 'pc1' AND status = 'active'`, out.DeviceID); n != 1 {
		t.Errorf("devices rows = %d, want 1", n)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND user_id = $2::uuid AND role = 'admin' AND created_by = $2::uuid AND revoked_at IS NULL`,
		out.DeviceID, owner); n != 1 {
		t.Errorf("admin bindings = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1 AND device_id = $2 AND actor = $3`,
		ActionDeviceEnrolled, out.DeviceID, owner); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM enrolment_tokens WHERE used_at IS NOT NULL`); n != 1 {
		t.Errorf("used tokens = %d, want 1", n)
	}
}

func TestEnrolRefusals(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)
	owner := "owner@example.com"
	bodyWith := func(token string) string {
		return `{"name":"pc1","enrolment_token":"` + token + `"}`
	}
	fresh := func() string { return enrolToken(t, h, owner) }

	t.Run("unsigned", func(t *testing.T) {
		rec := enrol(t, h, priv, bodyWith(fresh()), func(*http.Request, []byte) {})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body)
		}
	})
	t.Run("bad signature", func(t *testing.T) {
		rec := enrol(t, h, priv, bodyWith(fresh()), func(r *http.Request, b []byte) {
			devicesig.Sign(r, priv, append([]byte(`{"name":"nas"}`), b[len(`{"name":"nas"}`):]...))
		})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body)
		}
	})
	t.Run("replayed nonce", func(t *testing.T) {
		nonce := "0123456789abcdef0123456789abcdef"
		sign := func(r *http.Request, b []byte) { devicesig.SignAt(r, priv, b, nonce, time.Now()) }
		if rec := enrol(t, h, priv, bodyWith(fresh()), sign); rec.Code != http.StatusOK {
			t.Fatalf("first use: status = %d (body %s)", rec.Code, rec.Body)
		}
		rec := enrol(t, h, priv, bodyWith(fresh()), sign)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "nonce") {
			t.Errorf("status = %d body %s, want 401 about the nonce", rec.Code, rec.Body)
		}
	})
	t.Run("expired token", func(t *testing.T) {
		raw := mintTokenRow(t, pool, "enrolment_tokens", accountIDByEmail(t, pool, owner), time.Now().Add(-time.Minute))
		rec := enrol(t, h, priv, bodyWith(raw), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body)
		}
	})
	t.Run("used token", func(t *testing.T) {
		token := fresh()
		if rec := enrol(t, h, priv, bodyWith(token), nil); rec.Code != http.StatusOK {
			t.Fatalf("first use: status = %d (body %s)", rec.Code, rec.Body)
		}
		rec := enrol(t, h, priv, bodyWith(token), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body)
		}
	})
	t.Run("unknown token", func(t *testing.T) {
		rec := enrol(t, h, priv, bodyWith("nope"), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body)
		}
	})
	t.Run("public_key that is not the signing key", func(t *testing.T) {
		other := newDeviceKey(t)
		body := `{"public_key":"` + hex.EncodeToString(other.Public().(ed25519.PublicKey)) + `","name":"pc1","enrolment_token":"` + fresh() + `"}`
		rec := enrol(t, h, priv, body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
		}
	})

	// Every refusal above left the device table alone except the two successful enrolments
	// of the same key, which share one row.
	if n := countRows(t, pool, `SELECT count(*) FROM devices`); n != 1 {
		t.Errorf("devices rows = %d, want 1", n)
	}
}

func TestReenrolIsIdempotent(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)

	for _, name := range []string{"pc1", "pc1-renamed"} {
		body := `{"name":"` + name + `","enrolment_token":"` + enrolToken(t, h, "owner@example.com") + `"}`
		if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusOK {
			t.Fatalf("enrol %s: status = %d (body %s)", name, rec.Code, rec.Body)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM devices WHERE name = 'pc1-renamed'`); n != 1 {
		t.Errorf("devices named pc1-renamed = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM devices`); n != 1 {
		t.Errorf("devices rows = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users`); n != 1 {
		t.Errorf("bindings = %d, want 1", n)
	}
}

func TestDisableDeviceIsAdminOnlyAndBlocksReenrol(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)
	body := `{"name":"pc1","enrolment_token":"` + enrolToken(t, h, "owner@example.com") + `"}`
	if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusOK {
		t.Fatalf("enrol: status = %d (body %s)", rec.Code, rec.Body)
	}
	id := derivedDeviceID(priv)
	path := "/v1/devices/" + id + "/disable"

	signupReq(t, h, "guest@example.com", "correct horse battery")
	guest := signinToken(t, h, "guest@example.com", "correct horse battery")
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO device_users (device_id, user_id, role) VALUES ($1, $2::uuid, 'guest')`,
		id, accountIDByEmail(t, pool, "guest@example.com")); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, h, http.MethodPost, path, nil, bearer(guest)); rec.Code != http.StatusNotFound {
		t.Errorf("guest disable: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, path, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous disable: status = %d, want 401", rec.Code)
	}

	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	if rec := doJSON(t, h, http.MethodPost, path, nil, bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("admin disable: status = %d (body %s)", rec.Code, rec.Body)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM devices WHERE device_id = $1 AND status = 'disabled'`, id); n != 1 {
		t.Errorf("disabled rows = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1 AND device_id = $2`, ActionDeviceDisabled, id); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}

	body = `{"name":"pc1","enrolment_token":"` + enrolToken(t, h, "owner@example.com") + `"}`
	if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusForbidden {
		t.Errorf("re-enrol of a disabled device: status = %d, want 403 (body %s)", rec.Code, rec.Body)
	}
}
