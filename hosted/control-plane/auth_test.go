package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/jackc/pgx/v5/pgxpool"
)

// All tests here that touch the schema use freshDB, so they skip when
// RFM_TEST_DATABASE_URL is not set. The skip comes from connect and names the variable,
// so a skipped run reads as skipped, never as passed.

func doJSON(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = strings.NewReader(string(buf))
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func signupReq(t *testing.T, h http.Handler, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/auth/signup",
		map[string]string{"email": email, "password": password}, nil)
}

func signinReq(t *testing.T, h http.Handler, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/auth/signin",
		map[string]string{"email": email, "password": password}, nil)
}

func signinToken(t *testing.T, h http.Handler, email, password string) string {
	t.Helper()
	rec := signinReq(t, h, email, password)
	if rec.Code != http.StatusOK {
		t.Fatalf("signin %s: status = %d, want 200 (body %s)", email, rec.Code, rec.Body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("signin %s: no token in %q", email, rec.Body)
	}
	return out.Token
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func accountIDByEmail(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`SELECT id::text FROM accounts WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("find account %s: %v", email, err)
	}
	return id
}

// mintTokenRow writes a verification or reset token straight to its table and returns
// the raw value. Tests use it for setup; the HTTP confirm path is what is exercised.
func mintTokenRow(t *testing.T, pool *pgxpool.Pool, table, accountID string, expiresAt time.Time) string {
	t.Helper()
	raw, hash, err := newRawToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	_, err = pool.Exec(t.Context(),
		`INSERT INTO `+table+` (account_id, token_hash, expires_at) VALUES ($1::uuid, $2, $3)`,
		accountID, hash, expiresAt)
	if err != nil {
		t.Fatalf("store token: %v", err)
	}
	return raw
}

func TestSignupSigninMeRoundTrip(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "alice@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	rec := signinReq(t, h, "alice@example.test", "correct-horse-123")
	if rec.Code != http.StatusOK {
		t.Fatalf("signin: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var signedIn struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &signedIn); err != nil || signedIn.Token == "" {
		t.Fatalf("signin returned no token: %q", rec.Body)
	}
	if cookies := rec.Result().Cookies(); len(cookies) == 0 {
		t.Error("signin set no cookie, want the dashboard session cookie")
	}

	rec = doJSON(t, h, http.MethodGet, "/v1/me", nil, bearer(signedIn.Token))
	if rec.Code != http.StatusOK {
		t.Fatalf("me: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var me struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.Email != "alice@example.test" || me.EmailVerified {
		t.Errorf("me = %+v, want alice unverified", me)
	}
}

func TestSigninWrongPasswordMatchesUnknownAddress(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "bob@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}

	wrong := signinReq(t, h, "bob@example.test", "wrong-password-1")
	unknown := signinReq(t, h, "nobody@example.test", "wrong-password-1")
	if wrong.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status = %d, want 401", wrong.Code)
	}
	if unknown.Code != http.StatusUnauthorized {
		t.Errorf("unknown address: status = %d, want 401", unknown.Code)
	}
	if wrong.Body.String() != unknown.Body.String() {
		t.Errorf("wrong password body %q differs from unknown address body %q; signin must not tell them apart",
			wrong.Body, unknown.Body)
	}
}

func TestSignupEnumerationResistance(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	first := signupReq(t, h, "carol@example.test", "correct-horse-123")
	second := signupReq(t, h, "carol@example.test", "correct-horse-123")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 200, 200", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("second signup body %q differs from first %q; repeats must read the same",
			second.Body, first.Body)
	}
	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM accounts WHERE email = 'carol@example.test'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("carol has %d accounts, want 1", n)
	}
}

func TestPasswordResetEnumerationResistance(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "dave@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	known := doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/request",
		map[string]string{"email": "dave@example.test"}, nil)
	unknown := doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/request",
		map[string]string{"email": "ghost@example.test"}, nil)
	if known.Code != http.StatusOK || unknown.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 200, 200", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Errorf("known body %q differs from unknown %q; reset must not tell them apart",
			known.Body, unknown.Body)
	}
}

func TestVerifyExpiredTokenFails(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "erin@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	id := accountIDByEmail(t, pool, "erin@example.test")
	raw := mintTokenRow(t, pool, "email_verification_tokens", id, time.Now().Add(-time.Hour))

	rec := doJSON(t, h, http.MethodPost, "/v1/auth/verify",
		map[string]string{"token": raw}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expired link: status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

func TestVerifyTokenWorksOnce(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "fred@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	id := accountIDByEmail(t, pool, "fred@example.test")
	raw := mintTokenRow(t, pool, "email_verification_tokens", id, time.Now().Add(time.Hour))

	first := doJSON(t, h, http.MethodPost, "/v1/auth/verify",
		map[string]string{"token": raw}, nil)
	second := doJSON(t, h, http.MethodPost, "/v1/auth/verify",
		map[string]string{"token": raw}, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first use: status = %d, want 200 (body %s)", first.Code, first.Body)
	}
	if second.Code != http.StatusBadRequest {
		t.Errorf("second use: status = %d, want 400 (body %s)", second.Code, second.Body)
	}

	token := signinToken(t, h, "fred@example.test", "correct-horse-123")
	rec := doJSON(t, h, http.MethodGet, "/v1/me", nil, bearer(token))
	var me struct {
		EmailVerified bool `json:"email_verified"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || !me.EmailVerified {
		t.Errorf("after verify, me = %q, want email_verified true", rec.Body)
	}
}

func TestResetLinkWorksOnceAndRotatesPassword(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "gina@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	id := accountIDByEmail(t, pool, "gina@example.test")
	raw := mintTokenRow(t, pool, "password_reset_tokens", id, time.Now().Add(time.Hour))

	confirm := map[string]string{"token": raw, "new_password": "brand-new-horse-9"}
	if rec := doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/confirm", confirm, nil); rec.Code != http.StatusOK {
		t.Fatalf("first use: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/confirm", confirm, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("reused link: status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	if rec := signinReq(t, h, "gina@example.test", "correct-horse-123"); rec.Code != http.StatusUnauthorized {
		t.Errorf("old password: status = %d, want 401 (body %s)", rec.Code, rec.Body)
	}
	if rec := signinReq(t, h, "gina@example.test", "brand-new-horse-9"); rec.Code != http.StatusOK {
		t.Errorf("new password: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

func TestResetConfirmExpiredTokenFails(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "hank@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	id := accountIDByEmail(t, pool, "hank@example.test")
	raw := mintTokenRow(t, pool, "password_reset_tokens", id, time.Now().Add(-time.Hour))

	rec := doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/confirm",
		map[string]string{"token": raw, "new_password": "brand-new-horse-9"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expired link: status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

func TestSignoutRevokesSession(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "iris@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	token := signinToken(t, h, "iris@example.test", "correct-horse-123")

	if rec := doJSON(t, h, http.MethodPost, "/v1/auth/signout", nil, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("signout: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodGet, "/v1/me", nil, bearer(token)); rec.Code != http.StatusUnauthorized {
		t.Errorf("after signout, me: status = %d, want 401 (body %s)", rec.Code, rec.Body)
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	var last *httptest.ResponseRecorder
	for i := range signupLimit + 1 {
		last = signupReq(t, h, fmt.Sprintf("rate%d@example.test", i), "correct-horse-123")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Errorf("request %d: status = %d, want 429 (body %s)", signupLimit+1, last.Code, last.Body)
	}
}

// No database: the password hash must round-trip without one.
func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct-horse-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !checkPassword("correct-horse-123", hash) {
		t.Error("fresh hash does not verify")
	}
	if checkPassword("wrong-password-1", hash) {
		t.Error("wrong password verifies")
	}
	if checkPassword("correct-horse-123", "garbage") {
		t.Error("garbage hash verifies")
	}
}

// The signature format itself is tested in internal/devicesig. This covers the part that
// needs the database: a nonce is accepted once.
func TestVerifyAgentSignature(t *testing.T) {
	pool := freshDB(t, 4)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"name":"pc1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/devices/enrol", strings.NewReader(string(body)))
	devicesig.Sign(req, priv, body)

	got, err := verifyAgentSignature(req, pool, body)
	if err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if want := req.Header.Get(devicesig.HeaderKey); got != want {
		t.Errorf("device key = %q, want %q", got, want)
	}
	if _, err := verifyAgentSignature(req, pool, body); err == nil {
		t.Error("replayed nonce accepted, want rejected")
	}

	bare := httptest.NewRequest(http.MethodPost, "/v1/devices/enrol", strings.NewReader(string(body)))
	if _, err := verifyAgentSignature(bare, pool, body); err == nil {
		t.Error("unsigned request accepted, want rejected")
	}
}

// The session cookie carries a bearer token, so it must never travel over plain HTTP.
// Secure is the app's to set: a load balancer terminating TLS does not rewrite Set-Cookie.
func TestSessionCookieIsSecureAndHTTPOnly(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())

	if rec := signupReq(t, h, "carol@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	rec := signinReq(t, h, "carol@example.test", "correct-horse-123")
	if rec.Code != http.StatusOK {
		t.Fatalf("signin: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatalf("signin set no %s cookie", sessionCookie)
	}
	if !found.Secure {
		t.Error("session cookie is not Secure, so a plain HTTP request would send the token")
	}
	if !found.HttpOnly {
		t.Error("session cookie is not HttpOnly, so script can read the token")
	}
}
