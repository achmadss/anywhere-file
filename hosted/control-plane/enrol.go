package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device enrolment (#83). A signed-in user mints a short-lived, single-use token in the
// client. The agent on the PC sends it with its public key and a name, signed with the
// device key. The server derives device_id from the key (the schema does it), stores the
// device, and binds the token's user to it as admin, all in one transaction.
//
// Browser enrolment (#139) puts the same thing behind an approval, so nobody has to carry a
// token to the PC. The agent asks for a code, the person approves it while signed in on the
// website, and the agent polls until it can collect a token and enrol the way it already
// does. The approval binds nothing on its own, and neither does the device key.
const (
	enrolTokenTTL   = 10 * time.Minute
	enrolTokenLimit = 10
	enrolLimit      = 20
	disableLimit    = 30

	// A code lives ten minutes and the agent asks every five seconds, which is 120 polls
	// for one enrolment. The limit is per address, and a household behind one address can
	// be setting up more than one PC.
	enrolStartTTL    = 10 * time.Minute
	enrolPollEvery   = 5 * time.Second
	enrolStartLimit  = 10
	enrolPollLimit   = 500
	enrolAnswerLimit = 20
	approvePageLimit = 20
	unenrolLimit     = 20
)

func registerDeviceRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger) {
	wrap := func(limit int, h http.HandlerFunc) http.Handler {
		return newRateLimiter(limit, rateWindow).middleware(h)
	}
	mux.Handle("POST /v1/devices/enrolment-token", wrap(enrolTokenLimit, requireSession(db, mintEnrolToken(db))))
	mux.Handle("POST /v1/devices/enrol", wrap(enrolLimit, enrolDevice(db, log)))
	mux.Handle("POST /v1/devices/{id}/disable", wrap(disableLimit, requireSession(db, disableDevice(db, log))))
	mux.Handle("POST /v1/devices/enrolment/start", wrap(enrolStartLimit, startEnrolment(db)))
	mux.Handle("POST /v1/devices/enrolment/poll", wrap(enrolPollLimit, pollEnrolment(db)))
	mux.Handle("POST /v1/devices/enrolment/answer", wrap(enrolAnswerLimit, requireSession(db, answerEnrolment(db, log))))
	mux.Handle("POST /v1/devices/unenrol", wrap(unenrolLimit, unenrolDevice(db, log)))
}

// signedBody reads the body and checks the device signature over it. The body is read
// before it is decoded, so a byte changed in transit fails the signature rather than
// being parsed.
func signedBody(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool) (body []byte, deviceKey string, ok bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid request body")
		return nil, "", false
	}
	deviceKey, err = verifyAgentSignature(r, db, body)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, err.Error())
		return nil, "", false
	}
	return body, deviceKey, true
}

// mintEnrolToken hands the client a token to type or paste into the agent. The raw
// value goes out once; only its hash is stored.
func mintEnrolToken(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		raw, hash, err := newRawToken()
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		var expiresAt time.Time
		err = db.QueryRow(r.Context(),
			`INSERT INTO enrolment_tokens (account_id, token_hash, expires_at)
			 VALUES ($1::uuid, $2, now() + $3::interval) RETURNING expires_at`,
			a.id, hash, fmt.Sprintf("%d seconds", int(enrolTokenTTL.Seconds()))).Scan(&expiresAt)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": raw, "expires_at": expiresAt.UTC()})
	}
}

var errDeviceDisabled = errors.New("device disabled")

// enrolDevice is the agent's request. The signature is checked over the raw body before
// the body is decoded, so a byte changed in transit fails the signature rather than
// being parsed. A device_id in the body is not even decoded: the schema derives it.
func enrolDevice(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, deviceKey, ok := signedBody(w, r, db)
		if !ok {
			return
		}
		var in struct {
			PublicKey string `json:"public_key"`
			Name      string `json:"name"`
			Token     string `json:"enrolment_token"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Token == "" {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.PublicKey != "" && in.PublicKey != deviceKey {
			writeAuthError(w, http.StatusBadRequest, "public_key does not match the signature")
			return
		}
		if len(in.Name) > 64 {
			writeAuthError(w, http.StatusBadRequest, "name too long")
			return
		}

		ctx := r.Context()
		var accountID, deviceID string
		err := inTx(ctx, db, func(tx pgx.Tx) error {
			// Consuming the token is the atomic step: two agents racing with one token
			// both run this UPDATE, and only one sees a row come back.
			if err := tx.QueryRow(ctx,
				`UPDATE enrolment_tokens SET used_at = now()
				 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
				 RETURNING account_id::text`, hashToken(in.Token)).Scan(&accountID); err != nil {
				return err
			}
			var status string
			if err := tx.QueryRow(ctx,
				`INSERT INTO devices (public_key, name) VALUES ($1, $2)
				 ON CONFLICT (public_key) DO UPDATE SET name = EXCLUDED.name
				 RETURNING device_id, status`, deviceKey, in.Name).Scan(&deviceID, &status); err != nil {
				return err
			}
			if status != "active" {
				return errDeviceDisabled
			}
			// A user already bound to this device keeps their row and role.
			if _, err := tx.Exec(ctx,
				`INSERT INTO device_users (device_id, user_id, role, created_by)
				 VALUES ($1, $2::uuid, 'admin', $2::uuid)
				 ON CONFLICT (device_id, user_id) DO NOTHING`, deviceID, accountID); err != nil {
				return err
			}
			return appendAudit(ctx, tx, deviceID, accountID, ActionDeviceEnrolled,
				AuditDetails{AccountID: accountID, DeviceKey: deviceKey, Role: "admin"})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusUnauthorized, "enrolment token invalid, expired or used")
			return
		case errors.Is(err, errDeviceDisabled):
			writeAuthError(w, http.StatusForbidden, "device disabled")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("device enrolled", "device", deviceID, "account", accountID)
		writeJSON(w, http.StatusOK, map[string]any{"device_id": deviceID, "name": in.Name})
	}
}

// disableDevice is an admin's switch. The same UPDATE checks the caller holds an active
// admin binding, so a guest, a revoked admin or a stranger all get the same 404 and learn
// nothing about whether the device exists.
func disableDevice(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		deviceID := r.PathValue("id")
		if err := validateToken("device_id", deviceID); err != nil {
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		}
		ctx := r.Context()
		err := inTx(ctx, db, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`UPDATE devices SET status = 'disabled'
				 WHERE device_id = $1 AND EXISTS (
				   SELECT 1 FROM device_users
				   WHERE device_id = $1 AND user_id = $2::uuid AND role = 'admin' AND revoked_at IS NULL)`,
				deviceID, a.id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
			return appendAudit(ctx, tx, deviceID, a.id, ActionDeviceDisabled,
				AuditDetails{AccountID: a.id, FromStatus: "active", ToStatus: "disabled"})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("device disabled", "device", deviceID, "account", a.id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// userCodeAlphabet holds no vowel, so a random code cannot spell a word, and no character
// that reads as another one over the phone. Eight of them is about 34 bits. That is not
// enough on its own, which is why every route that takes a code is rate limited and a code
// lives ten minutes.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

func newUserCode() (string, error) {
	code := make([]byte, 0, 8)
	b := make([]byte, 1)
	for len(code) < 8 {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		// 240 is the largest multiple of the alphabet a byte holds. Taking the remainder
		// above it would favour the first characters.
		if b[0] >= 240 {
			continue
		}
		code = append(code, userCodeAlphabet[b[0]%byte(len(userCodeAlphabet))])
	}
	return string(code[:4]) + "-" + string(code[4:]), nil
}

// normalizeUserCode takes what a person typed. The dash is there to read by, and the case
// is not part of the code, so both are dropped before the hash is taken.
func normalizeUserCode(code string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(code)), "-", "")
}

// startEnrolment mints the code the person approves. The request is signed, so the row
// records the key that asked, and only that key can collect what the approval mints.
func startEnrolment(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, deviceKey, ok := signedBody(w, r, db)
		if !ok {
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(in.Name) > 64 {
			writeAuthError(w, http.StatusBadRequest, "name too long")
			return
		}
		// The name is whatever the PC calls itself, and it is shown to someone deciding
		// whether to let that PC in. A line break or an escape in it would let the PC
		// shape the page around the decision.
		if strings.ContainsFunc(in.Name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			writeAuthError(w, http.StatusBadRequest, "name has a control character in it")
			return
		}
		code, err := newUserCode()
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		var expiresAt time.Time
		err = db.QueryRow(r.Context(),
			`INSERT INTO device_enrolments (code_hash, device_key, name, expires_at)
			 VALUES ($1, $2, $3, now() + $4::interval) RETURNING expires_at`,
			hashToken(normalizeUserCode(code)), deviceKey, in.Name,
			fmt.Sprintf("%d seconds", int(enrolStartTTL.Seconds()))).Scan(&expiresAt)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user_code":        code,
			"approve_url":      linkBase(r) + "/approve?code=" + url.QueryEscape(code),
			"interval_seconds": int(enrolPollEvery.Seconds()),
			"expires_at":       expiresAt.UTC(),
		})
	}
}

// pollEnrolment is the agent asking what happened. Only a 200 saying pending means ask
// again: a refusal and an expiry are answers, and the agent stops on both.
func pollEnrolment(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, deviceKey, ok := signedBody(w, r, db)
		if !ok {
			return
		}
		var in struct {
			UserCode string `json:"user_code"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.UserCode == "" {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		codeHash := hashToken(normalizeUserCode(in.UserCode))

		ctx := r.Context()
		var status string
		// The device key is part of the lookup, so a code held by anyone else answers the
		// same as a code that was never minted.
		err := db.QueryRow(ctx,
			`SELECT status FROM device_enrolments
			 WHERE code_hash = $1 AND device_key = $2 AND used_at IS NULL AND expires_at > now()`,
			codeHash, deviceKey).Scan(&status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "no request is waiting: it expired or was already used")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		switch status {
		case "refused":
			writeAuthError(w, http.StatusForbidden, "the request was refused in the browser")
			return
		case "pending":
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "pending", "interval_seconds": int(enrolPollEvery.Seconds()),
			})
			return
		}

		// Approved. Consuming the row and minting the token are one step, so two polls
		// racing cannot both come away with a token.
		var raw string
		err = inTx(ctx, db, func(tx pgx.Tx) error {
			var accountID string
			if err := tx.QueryRow(ctx,
				`UPDATE device_enrolments SET used_at = now()
				 WHERE code_hash = $1 AND device_key = $2 AND status = 'approved'
				   AND used_at IS NULL AND expires_at > now()
				 RETURNING answered_by::text`, codeHash, deviceKey).Scan(&accountID); err != nil {
				return err
			}
			token, hash, err := newRawToken()
			if err != nil {
				return err
			}
			raw = token
			_, err = tx.Exec(ctx,
				`INSERT INTO enrolment_tokens (account_id, token_hash, expires_at)
				 VALUES ($1::uuid, $2, now() + $3::interval)`,
				accountID, hash, fmt.Sprintf("%d seconds", int(enrolTokenTTL.Seconds())))
			return err
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "no request is waiting: it expired or was already used")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "enrolment_token": raw})
	}
}

// answerEnrolment is the browser's half. The session says which account the PC would join;
// the cookie is SameSite=Lax, so another origin cannot post this on the person's behalf.
func answerEnrolment(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var in struct {
			Code    string `json:"code"`
			Approve bool   `json:"approve"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		status, message := "refused", "The PC was refused. It stays signed out."
		if in.Approve {
			status, message = "approved", "Approved. The PC finishes signing itself in."
		}
		tag, err := db.Exec(r.Context(),
			`UPDATE device_enrolments SET status = $1, answered_by = $2::uuid
			 WHERE code_hash = $3 AND status = 'pending' AND used_at IS NULL AND expires_at > now()`,
			status, a.id, hashToken(normalizeUserCode(in.Code)))
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		if tag.RowsAffected() == 0 {
			writeAuthError(w, http.StatusNotFound, "that request expired or was already answered")
			return
		}
		log.Info("device enrolment answered", "account", a.id, "status", status)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message})
	}
}

// unenrolDevice is the PC taking itself off an account. The signature is the proof: the
// agent holds no session, and a PC should always be able to leave. The bindings are
// revoked rather than deleted, so the device row and the audit trail outlive the sign out.
// Nothing about the LAN changes; what stops is remote routing, which reads those bindings.
func unenrolDevice(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, deviceKey, ok := signedBody(w, r, db)
		if !ok {
			return
		}
		ctx := r.Context()
		var deviceID string
		err := inTx(ctx, db, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx,
				`SELECT device_id FROM devices WHERE public_key = $1`, deviceKey).Scan(&deviceID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE device_users SET revoked_at = now()
				 WHERE device_id = $1 AND revoked_at IS NULL`, deviceID); err != nil {
				return err
			}
			return appendAudit(ctx, tx, deviceID, deviceID, ActionDeviceUnenrolled,
				AuditDetails{DeviceKey: deviceKey})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The PC was never enrolled, or is already off. Either way it now belongs to
			// no account, which is what it asked for.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("device unenrolled", "device", deviceID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
