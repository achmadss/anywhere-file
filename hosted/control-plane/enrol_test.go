package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// signedPost sends a body signed with a device key, the way the agent does.
func signedPost(t *testing.T, h http.Handler, priv ed25519.PrivateKey, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(buf)))
	req.Header.Set("Content-Type", "application/json")
	devicesig.Sign(req, priv, buf)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// startEnrolmentFor asks for a code as the PC would, and returns it.
func startEnrolmentFor(t *testing.T, h http.Handler, priv ed25519.PrivateKey, name string) string {
	t.Helper()
	rec := signedPost(t, h, priv, "/v1/devices/enrolment/start", map[string]string{"name": name})
	if rec.Code != http.StatusOK {
		t.Fatalf("start: status = %d (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		UserCode   string `json:"user_code"`
		ApproveURL string `json:"approve_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.UserCode == "" {
		t.Fatalf("start: no user code in %q", rec.Body)
	}
	if !strings.Contains(out.ApproveURL, "/approve?code="+url.QueryEscape(out.UserCode)) {
		t.Fatalf("start: approve url %q does not carry the code", out.ApproveURL)
	}
	return out.UserCode
}

// answer approves or refuses a code as the signed-in browser does.
func answer(t *testing.T, h http.Handler, session, code string, approve bool) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/devices/enrolment/answer",
		map[string]any{"code": code, "approve": approve}, bearer(session))
}

// poll asks what happened to a code, signed with the device key.
func poll(t *testing.T, h http.Handler, priv ed25519.PrivateKey, code string) *httptest.ResponseRecorder {
	t.Helper()
	return signedPost(t, h, priv, "/v1/devices/enrolment/poll", map[string]string{"user_code": code})
}

// TestApprovingInTheBrowserEnrolsThePC is #139's acceptance. Nobody carries a token to the
// PC: the PC asks, the person approves while signed in, and the PC collects what it needs.
func TestApprovingInTheBrowserEnrolsThePC(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)

	signupReq(t, h, "owner@example.com", "correct horse battery")
	session := signinToken(t, h, "owner@example.com", "correct horse battery")

	code := startEnrolmentFor(t, h, priv, "the study PC")

	// Before anyone answers, the PC is told to wait.
	rec := poll(t, h, priv, code)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pending"`) {
		t.Fatalf("poll before approval: status = %d (body %s)", rec.Code, rec.Body)
	}

	if rec := answer(t, h, session, code, true); rec.Code != http.StatusOK {
		t.Fatalf("approve: status = %d (body %s)", rec.Code, rec.Body)
	}

	rec = poll(t, h, priv, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll after approval: status = %d (body %s)", rec.Code, rec.Body)
	}
	var got struct {
		Status string `json:"status"`
		Token  string `json:"enrolment_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Token == "" {
		t.Fatalf("poll after approval: no token in %q", rec.Body)
	}
	if got.Status != "approved" {
		t.Fatalf("poll after approval: status = %q, want approved", got.Status)
	}

	body := `{"name":"the study PC","enrolment_token":"` + got.Token + `"}`
	if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusOK {
		t.Fatalf("enrol: status = %d (body %s)", rec.Code, rec.Body)
	}
	owner := accountIDByEmail(t, pool, "owner@example.com")
	if n := countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND user_id = $2::uuid
		   AND role = 'admin' AND revoked_at IS NULL`, derivedDeviceID(priv), owner); n != 1 {
		t.Fatalf("admin bindings for the owner = %d, want 1", n)
	}
}

// A code is half of the proof. Someone who reads it over a shoulder still holds nothing,
// because the poll that collects the token is signed by the key the code was minted for.
func TestACodeIsUselessWithoutTheDeviceKey(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv, thief := newDeviceKey(t), newDeviceKey(t)

	signupReq(t, h, "owner@example.com", "correct horse battery")
	session := signinToken(t, h, "owner@example.com", "correct horse battery")
	code := startEnrolmentFor(t, h, priv, "the study PC")
	if rec := answer(t, h, session, code, true); rec.Code != http.StatusOK {
		t.Fatalf("approve: status = %d (body %s)", rec.Code, rec.Body)
	}

	if rec := poll(t, h, thief, code); rec.Code != http.StatusNotFound {
		t.Fatalf("poll with another key: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	// And the real PC can still collect it, so the refusal above consumed nothing.
	if rec := poll(t, h, priv, code); rec.Code != http.StatusOK {
		t.Fatalf("poll with the right key: status = %d (body %s)", rec.Code, rec.Body)
	}
}

// An approval on its own binds nothing either: the enrolment token it mints is what the
// signed enrolment consumes, and it is spent once.
func TestAnApprovedCodeIsCollectedOnce(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)

	signupReq(t, h, "owner@example.com", "correct horse battery")
	session := signinToken(t, h, "owner@example.com", "correct horse battery")
	code := startEnrolmentFor(t, h, priv, "the study PC")
	if rec := answer(t, h, session, code, true); rec.Code != http.StatusOK {
		t.Fatalf("approve: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := poll(t, h, priv, code); rec.Code != http.StatusOK {
		t.Fatalf("first poll: status = %d (body %s)", rec.Code, rec.Body)
	}
	if rec := poll(t, h, priv, code); rec.Code != http.StatusNotFound {
		t.Fatalf("second poll: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM enrolment_tokens`); n != 1 {
		t.Fatalf("enrolment tokens minted = %d, want 1", n)
	}
}

// Refusing is an answer the PC hears, and it stops asking.
func TestRefusingLeavesThePCUnenrolled(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)

	signupReq(t, h, "owner@example.com", "correct horse battery")
	session := signinToken(t, h, "owner@example.com", "correct horse battery")
	code := startEnrolmentFor(t, h, priv, "the study PC")
	if rec := answer(t, h, session, code, false); rec.Code != http.StatusOK {
		t.Fatalf("refuse: status = %d (body %s)", rec.Code, rec.Body)
	}
	rec := poll(t, h, priv, code)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("poll after refusal: status = %d, want 403 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "refused") {
		t.Fatalf("poll after refusal: body = %s", rec.Body)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM devices`); n != 0 {
		t.Fatalf("devices = %d, want 0", n)
	}
	// A second answer cannot turn the refusal into an approval.
	if rec := answer(t, h, session, code, true); rec.Code != http.StatusNotFound {
		t.Fatalf("approve after refusal: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

func TestAnExpiredCodeIsRefused(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)

	signupReq(t, h, "owner@example.com", "correct horse battery")
	session := signinToken(t, h, "owner@example.com", "correct horse battery")
	code := startEnrolmentFor(t, h, priv, "the study PC")
	if _, err := pool.Exec(t.Context(),
		`UPDATE device_enrolments SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatalf("expire the code: %v", err)
	}
	if rec := answer(t, h, session, code, true); rec.Code != http.StatusNotFound {
		t.Fatalf("approve an expired code: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	if rec := poll(t, h, priv, code); rec.Code != http.StatusNotFound {
		t.Fatalf("poll an expired code: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

// Nobody can start an enrolment without a device key, which is what stops a stranger
// minting codes for a PC they do not hold.
func TestStartingAnEnrolmentNeedsASignature(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	rec := doJSON(t, h, http.MethodPost, "/v1/devices/enrolment/start", map[string]string{"name": "x"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned start: status = %d, want 401 (body %s)", rec.Code, rec.Body)
	}
}

// The name is shown to whoever is deciding, so the PC does not get to put a line break or
// an escape into it.
func TestAPCNameWithAControlCharacterIsRefused(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)
	for _, name := range []string{"study\nPC", "study\rPC", "study\x1b[31mPC"} {
		rec := signedPost(t, h, priv, "/v1/devices/enrolment/start", map[string]string{"name": name})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("start with name %q: status = %d, want 400 (body %s)", name, rec.Code, rec.Body)
		}
	}
	if rec := signedPost(t, h, priv, "/v1/devices/enrolment/start", map[string]string{"name": "the study PC"}); rec.Code != http.StatusOK {
		t.Fatalf("start with an ordinary name: status = %d (body %s)", rec.Code, rec.Body)
	}
}

// Signing out is the PC's own decision and its key is the proof. Afterwards the account
// reaches nothing, and the device row and its history stay.
func TestAPCCanSignItselfOut(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := newDeviceKey(t)
	token := enrolToken(t, h, "owner@example.com")
	if rec := enrol(t, h, priv, `{"name":"the study PC","enrolment_token":"`+token+`"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("enrol: status = %d (body %s)", rec.Code, rec.Body)
	}
	deviceID := derivedDeviceID(priv)

	// An unsigned request changes nothing.
	if rec := doJSON(t, h, http.MethodPost, "/v1/devices/unenrol", map[string]string{}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned unenrol: status = %d, want 401 (body %s)", rec.Code, rec.Body)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND revoked_at IS NULL`, deviceID); n != 1 {
		t.Fatalf("live bindings after an unsigned unenrol = %d, want 1", n)
	}

	if rec := signedPost(t, h, priv, "/v1/devices/unenrol", map[string]string{}); rec.Code != http.StatusOK {
		t.Fatalf("unenrol: status = %d (body %s)", rec.Code, rec.Body)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND revoked_at IS NULL`, deviceID); n != 0 {
		t.Fatalf("live bindings after signing out = %d, want 0", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM devices WHERE device_id = $1`, deviceID); n != 1 {
		t.Fatalf("device rows = %d, want the row to survive", n)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM audit_events WHERE device_id = $1 AND action = $2`,
		deviceID, ActionDeviceUnenrolled); n != 1 {
		t.Fatalf("unenrol audit rows = %d, want 1", n)
	}
	// Saying it twice is not an error: the PC belongs to no account either way.
	if rec := signedPost(t, h, priv, "/v1/devices/unenrol", map[string]string{}); rec.Code != http.StatusOK {
		t.Fatalf("second unenrol: status = %d (body %s)", rec.Code, rec.Body)
	}
}

func TestAUserCodeReadsBackWhateverTheCaseAndDashes(t *testing.T) {
	code, err := newUserCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 9 || code[4] != '-' {
		t.Fatalf("code = %q, want eight characters split by a dash", code)
	}
	want := normalizeUserCode(code)
	if len(want) != 8 {
		t.Fatalf("normalized %q to %q, want eight characters", code, want)
	}
	for _, typed := range []string{code, strings.ToLower(code), want, " " + strings.ToLower(want) + " "} {
		if got := normalizeUserCode(typed); got != want {
			t.Fatalf("normalizeUserCode(%q) = %q, want %q", typed, got, want)
		}
	}
	for _, r := range want {
		if !strings.ContainsRune(userCodeAlphabet, r) {
			t.Fatalf("code %q holds %q, which is not in the alphabet", code, r)
		}
	}
}
