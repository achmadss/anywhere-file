package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Auth limits. Per IP, per endpoint, per minute. Sign-in is the brute-force target, so it
// is the tightest; the rest follow the same shape.
const (
	signupLimit       = 10
	signinLimit       = 10
	verifyLimit       = 20
	resetReqLimit     = 5
	resetConfirmLimit = 10
	signoutLimit      = 30
	meLimit           = 60
	deleteLimit       = 10
	rateWindow        = time.Minute
)

// Token lifetimes.
const (
	sessionTTL = 30 * 24 * time.Hour
	verifyTTL  = 24 * time.Hour
	resetTTL   = time.Hour
)

// Password rules. 12 characters is the minimum; the hash is PBKDF2-HMAC-SHA256 from the
// standard library so this package adds no dependency for one function.
const (
	minPasswordLen = 12
	hashIterations = 210000
	hashSaltLen    = 32
	hashKeyLen     = 32
)

const sessionCookie = "rfm_session"

// account is the authenticated user. It carries no file state: an account is a cloud
// identity only (r3 section 3) and never appears in a trust list or a share.
type account struct {
	id            string
	email         string
	emailVerified bool
}

type ctxKey string

const ctxAccountKey ctxKey = "account"
const ctxSessionKey ctxKey = "session"

func accountFromContext(ctx context.Context) (account, string, bool) {
	a, ok := ctx.Value(ctxAccountKey).(account)
	if !ok {
		return account{}, "", false
	}
	s, _ := ctx.Value(ctxSessionKey).(string)
	return a, s, true
}

// registerAuthRoutes adds every account endpoint. Each auth route gets its own limiter:
// rate limiting covers every auth endpoint, with no shared bucket to dodge through.
func registerAuthRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger) {
	wrap := func(limit int, h http.HandlerFunc) http.Handler {
		return newRateLimiter(limit, rateWindow).middleware(h)
	}
	mux.Handle("POST /v1/auth/signup", wrap(signupLimit, signup(db, log)))
	mux.Handle("POST /v1/auth/signin", wrap(signinLimit, signin(db, log)))
	mux.Handle("POST /v1/auth/signout", wrap(signoutLimit, requireSession(db, signout(db))))
	mux.Handle("POST /v1/auth/verify", wrap(verifyLimit, verifyEmail(db)))
	mux.Handle("POST /v1/auth/password-reset/request", wrap(resetReqLimit, resetRequest(db, log)))
	mux.Handle("POST /v1/auth/password-reset/confirm", wrap(resetConfirmLimit, resetConfirm(db)))
	mux.Handle("GET /v1/me", wrap(meLimit, requireSession(db, me)))
	mux.Handle("DELETE /v1/account", wrap(deleteLimit, requireSession(db, deleteAccount(db, log))))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// isUniqueViolation reports PostgreSQL error 23505: the row is already there.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func writeAuthError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// decodeBody reads a small JSON body. The cap keeps a large upload from eating memory on
// an endpoint that only ever takes an email and a password.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validEmail(email string) bool {
	email = normalizeEmail(email)
	if email == "" || len(email) > 254 || strings.Contains(email, " ") {
		return false
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || !strings.Contains(domain, ".") {
		return false
	}
	return true
}

// pbkdf2Key is PBKDF2 with HMAC-SHA256 and one block, which covers a 32 byte key. The
// standard library has no password hash, so this file carries a small one rather than a
// new dependency for a single function.
func pbkdf2Key(password string, salt []byte, iterations int) []byte {
	mac := hmac.New(sha256.New, []byte(password))
	var block [4]byte
	block[3] = 1
	mac.Write(salt)
	mac.Write(block[:])
	sum := mac.Sum(nil)
	out := make([]byte, len(sum))
	copy(out, sum)
	prev := sum
	for range iterations - 1 {
		mac = hmac.New(sha256.New, []byte(password))
		mac.Write(prev)
		prev = mac.Sum(nil)
		for i := range out {
			out[i] ^= prev[i]
		}
	}
	return out[:hashKeyLen]
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, hashSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2Key(password, salt, hashIterations)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", hashIterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func checkPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	if parts[1] != fmt.Sprint(hashIterations) {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil || len(want) != hashKeyLen {
		return false
	}
	got := pbkdf2Key(password, salt, hashIterations)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// newRawToken mints a token for email links and sessions. The raw value goes out once;
// the table keeps only its hash, so a database read never yields a usable token.
func newRawToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// sessionTokenFromRequest accepts the cookie the dashboard sets and the bearer token the
// agent sends. One account, two clients: the browser holds the cookie, the agent holds
// the token and presents it beside its device signature (r3 section 10.1).
func sessionTokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		if token, ok := strings.CutPrefix(auth, "Bearer "); ok && token != "" {
			return token
		}
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((sessionTTL).Seconds()),
		Secure:   !insecureCookies(),
	})
}

// insecureCookies drops the Secure flag from the session cookie. It exists for local
// development over plain HTTP and nothing else.
//
// Secure is on by default because a load balancer terminating TLS does not add the flag
// to a cookie this process emits; it would have to rewrite Set-Cookie, and it does not.
// Without it the browser sends the session token over any plain HTTP request to the same
// host. Read here rather than in config.go because the cookie helpers take no config.
var insecureCookies = sync.OnceValue(func() bool {
	return os.Getenv("RFM_INSECURE_COOKIES") != ""
})

// clearSessionCookie repeats the attributes the cookie was set with. A browser matches on
// name, path and the Secure flag when deciding what to replace, so an expiry that omits
// them can leave the original in place.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Secure:   !insecureCookies(),
	})
}

// createSession stores a new session and returns the raw token. Callers send it once and
// never store it.
func createSession(ctx context.Context, db *pgxpool.Pool, accountID, userAgent, ip string) (string, error) {
	raw, hash, err := newRawToken()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(ctx,
		`INSERT INTO sessions (account_id, token_hash, expires_at, user_agent, ip)
		 VALUES ($1, $2, now() + $3::interval, $4, $5)`,
		accountID, hash, fmt.Sprintf("%d seconds", int(sessionTTL.Seconds())), userAgent, ip)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// lookupSession resolves a raw token to its account. Expired and revoked sessions read as
// absent: the caller cannot tell them apart, and does not need to.
func lookupSession(ctx context.Context, db *pgxpool.Pool, raw string) (account, string, error) {
	var a account
	var sessionID string
	err := db.QueryRow(ctx,
		`SELECT a.id::text, a.email, a.email_verified_at IS NOT NULL, s.id::text
		 FROM sessions s JOIN accounts a ON a.id = s.account_id
		 WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`,
		hashToken(raw)).Scan(&a.id, &a.email, &a.emailVerified, &sessionID)
	return a, sessionID, err
}

// requireSession guards dashboard endpoints. It answers 401 with no detail: whether the
// token is unknown, expired, or revoked is not the caller's business.
func requireSession(db *pgxpool.Pool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := sessionTokenFromRequest(r)
		if raw == "" {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		a, sessionID, err := lookupSession(r.Context(), db, raw)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxAccountKey, a)
		ctx = context.WithValue(ctx, ctxSessionKey, sessionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func signup(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		email := normalizeEmail(in.Email)
		if !validEmail(email) {
			writeAuthError(w, http.StatusBadRequest, "invalid email")
			return
		}
		if len(in.Password) < minPasswordLen {
			writeAuthError(w, http.StatusBadRequest, "password too short")
			return
		}
		hash, err := hashPassword(in.Password)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}

		ctx := r.Context()
		var accountID string
		err = db.QueryRow(ctx,
			`INSERT INTO accounts (email, password_hash) VALUES ($1, $2) RETURNING id::text`,
			email, hash).Scan(&accountID)
		if err != nil {
			// The address is taken. Answer exactly as for a new account, so the
			// endpoint cannot be used to list who has signed up.
			if isUniqueViolation(err) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}

		raw, tokenHash, err := newRawToken()
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		_, err = db.Exec(ctx,
			`INSERT INTO email_verification_tokens (account_id, token_hash, expires_at)
			 VALUES ($1, $2, now() + $3::interval)`,
			accountID, tokenHash, fmt.Sprintf("%d seconds", int(verifyTTL.Seconds())))
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		// No mailer exists yet; the dashboard (#39) sends this link. Until then the
		// token is in the operator log, never in a response, so it cannot leak to
		// anyone who did not already hold the signup request.
		log.Info("verification link minted", "account", accountID, "token", raw)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func signin(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		email := normalizeEmail(in.Email)
		ctx := r.Context()

		var accountID string
		var stored *string
		err := db.QueryRow(ctx,
			`SELECT id::text, password_hash FROM accounts WHERE email = $1`, email).Scan(&accountID, &stored)
		if err != nil {
			// Unknown address. Burn the same work as a real check so timing does
			// not reveal whether the address exists, then answer as for a wrong
			// password.
			_, _ = hashPassword(in.Password)
			writeAuthError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		if stored == nil || !checkPassword(in.Password, *stored) {
			writeAuthError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}

		raw, err := createSession(ctx, db, accountID, r.UserAgent(), clientIP(r))
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("signed in", "account", accountID)
		setSessionCookie(w, raw)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": raw})
	}
}

func signout(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, sessionID, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		_, _ = db.Exec(r.Context(),
			`UPDATE sessions SET revoked_at = now() WHERE id = $1::uuid`, sessionID)
		clearSessionCookie(w)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func verifyEmail(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if !decodeBody(w, r, &in) || in.Token == "" {
			writeAuthError(w, http.StatusBadRequest, "invalid or expired link")
			return
		}
		ctx := r.Context()
		var tokenID, accountID string
		err := db.QueryRow(ctx,
			`SELECT id::text, account_id::text FROM email_verification_tokens
			 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`,
			hashToken(in.Token)).Scan(&tokenID, &accountID)
		if err != nil {
			// Used, expired, and unknown links share one answer: distinguishing
			// them would hand out which links are real.
			writeAuthError(w, http.StatusBadRequest, "invalid or expired link")
			return
		}
		err = inTx(ctx, db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx,
				`UPDATE email_verification_tokens SET used_at = now() WHERE id = $1::uuid`, tokenID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				`UPDATE accounts SET email_verified_at = now(), updated_at = now() WHERE id = $1::uuid`, accountID)
			return err
		})
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func resetRequest(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email string `json:"email"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		email := normalizeEmail(in.Email)
		if validEmail(email) {
			var accountID string
			if err := db.QueryRow(r.Context(),
				`SELECT id::text FROM accounts WHERE email = $1`, email).Scan(&accountID); err == nil {
				raw, tokenHash, err := newRawToken()
				if err == nil {
					_, err = db.Exec(r.Context(),
						`INSERT INTO password_reset_tokens (account_id, token_hash, expires_at)
						 VALUES ($1, $2, now() + $3::interval)`,
						accountID, tokenHash, fmt.Sprintf("%d seconds", int(resetTTL.Seconds())))
					if err == nil {
						// Same story as signup: no mailer yet, so the log carries
						// the link until the dashboard (#39) sends it.
						log.Info("password reset minted", "account", accountID, "token", raw)
					}
				}
			}
		}
		// Always the same answer, whether the address exists or not. Anything else
		// turns this endpoint into an account census.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func resetConfirm(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token       string `json:"token"`
			NewPassword string `json:"new_password"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Token == "" {
			writeAuthError(w, http.StatusBadRequest, "invalid or expired link")
			return
		}
		if len(in.NewPassword) < minPasswordLen {
			writeAuthError(w, http.StatusBadRequest, "password too short")
			return
		}
		ctx := r.Context()
		var tokenID, accountID string
		err := db.QueryRow(ctx,
			`SELECT id::text, account_id::text FROM password_reset_tokens
			 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`,
			hashToken(in.Token)).Scan(&tokenID, &accountID)
		if err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid or expired link")
			return
		}
		hash, err := hashPassword(in.NewPassword)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		err = inTx(ctx, db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx,
				`UPDATE password_reset_tokens SET used_at = now() WHERE id = $1::uuid`, tokenID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE accounts SET password_hash = $1, updated_at = now() WHERE id = $2::uuid`,
				hash, accountID); err != nil {
				return err
			}
			// A new password ends every session but the trust in the password
			// itself. Whoever reset it signs in again everywhere.
			_, err := tx.Exec(ctx,
				`UPDATE sessions SET revoked_at = now() WHERE account_id = $1::uuid AND revoked_at IS NULL`, accountID)
			return err
		})
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func me(w http.ResponseWriter, r *http.Request) {
	a, _, ok := accountFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": a.id, "email": a.email, "email_verified": a.emailVerified,
	})
}
