package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device enrolment (#83). A signed-in user mints a short-lived, single-use token in the
// client. The agent on the PC sends it with its public key and a name, signed with the
// device key. The server derives device_id from the key (the schema does it), stores the
// device, and binds the token's user to it as admin, all in one transaction.
const (
	enrolTokenTTL   = 10 * time.Minute
	enrolTokenLimit = 10
	enrolLimit      = 20
	disableLimit    = 30
)

func registerDeviceRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger) {
	wrap := func(limit int, h http.HandlerFunc) http.Handler {
		return newRateLimiter(limit, rateWindow).middleware(h)
	}
	mux.Handle("POST /v1/devices/enrolment-token", wrap(enrolTokenLimit, requireSession(db, mintEnrolToken(db))))
	mux.Handle("POST /v1/devices/enrol", wrap(enrolLimit, enrolDevice(db, log)))
	mux.Handle("POST /v1/devices/{id}/disable", wrap(disableLimit, requireSession(db, disableDevice(db, log))))
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
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		deviceKey, err := verifyAgentSignature(r, db, body)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, err.Error())
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
		err = inTx(ctx, db, func(tx pgx.Tx) error {
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
