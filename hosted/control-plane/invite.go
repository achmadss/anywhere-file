package main

// Invitations (#86). An admin mints a code for a device; whoever presents it gets a
// binding. The code is a bearer credential, so it is random, single use, short lived, and
// stored only as a hash. It is returned once, in the response body, and never written to
// a log or put in a URL.
//
// Consuming is one UPDATE guarded by used_at IS NULL. Only the request whose UPDATE
// returns a row creates a binding, in the same transaction, so two people presenting the
// same code at the same instant cannot both get in.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	inviteCreateLimit  = 20
	inviteRedeemLimit  = 30
	redeemAccountLimit = 10
	inviteTTL          = 24 * time.Hour
	maxInviteTTL       = 7 * 24 * time.Hour
)

var (
	errInviteInvalid = errors.New("invite invalid, expired or used")
	errInviteDevice  = errors.New("device disabled")
	errInviteCreator = errors.New("creator revoked")
	errAlreadyBound  = errors.New("already bound")
)

func registerInviteRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger) {
	wrap := func(limit int, h http.HandlerFunc) http.Handler {
		return newRateLimiter(limit, rateWindow).middleware(h)
	}
	// Redeem is guessing territory, so it is limited twice: by address, as every other
	// endpoint is, and by account, which an attacker cannot change by moving hosts.
	perAccount := newRateLimiter(redeemAccountLimit, rateWindow)
	mux.Handle("POST /v1/devices/{id}/invites", wrap(inviteCreateLimit, requireSession(db, createInvite(db, log))))
	mux.Handle("POST /v1/invites/redeem", wrap(inviteRedeemLimit, requireSession(db, redeemInviteHandler(db, log, perAccount))))
}

// createInvite is admin only. Anyone else gets the 404 an unknown device gets, so the
// endpoint does not tell a guest which devices exist.
func createInvite(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
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
		var in struct {
			Role      string `json:"role"`
			ExpiresIn string `json:"expires_in"`
		}
		if r.ContentLength > 0 && !decodeBody(w, r, &in) {
			return
		}
		if in.Role == "" {
			in.Role = "guest"
		}
		// Only an admin can create an invite at all, so neither role hands out more
		// than the creator holds.
		if in.Role != "guest" && in.Role != "admin" {
			writeAuthError(w, http.StatusBadRequest, "role must be guest or admin")
			return
		}
		ttl := inviteTTL
		if in.ExpiresIn != "" {
			parsed, err := time.ParseDuration(in.ExpiresIn)
			if err != nil || parsed <= 0 || parsed > maxInviteTTL {
				writeAuthError(w, http.StatusBadRequest, "expires_in must be a duration up to 168h")
				return
			}
			ttl = parsed
		}

		ctx := r.Context()
		raw, hash, err := newRawToken()
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		var expiresAt time.Time
		err = inTx(ctx, db, func(tx pgx.Tx) error {
			var status string
			if err := tx.QueryRow(ctx,
				`SELECT status FROM devices WHERE device_id = $1`, deviceID).Scan(&status); err != nil {
				return err
			}
			if admin, err := isActiveAdmin(ctx, tx, deviceID, a.id); err != nil || !admin {
				return pgx.ErrNoRows
			}
			if status != "active" {
				return errInviteDevice
			}
			if err := tx.QueryRow(ctx,
				`INSERT INTO invites (device_id, created_by, code_hash, role, expires_at)
				 VALUES ($1, $2::uuid, $3, $4, now() + $5::interval) RETURNING expires_at`,
				deviceID, a.id, hash, in.Role,
				fmt.Sprintf("%d seconds", int(ttl.Seconds()))).Scan(&expiresAt); err != nil {
				return err
			}
			return appendAudit(ctx, tx, deviceID, a.id, ActionInviteCreated,
				AuditDetails{AccountID: a.id, Role: in.Role})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		case errors.Is(err, errInviteDevice):
			writeAuthError(w, http.StatusForbidden, "device disabled")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		// The code goes out here and nowhere else. The log gets the device, never the code.
		log.Info("invite created", "device", deviceID, "by", a.id, "role", in.Role)
		writeJSON(w, http.StatusOK, map[string]any{
			"code": raw, "role": in.Role, "expires_at": expiresAt.UTC(),
		})
	}
}

// redeemInviteHandler turns a code into a binding. A revoked user who redeems a fresh
// code comes back on the same row with the role the code carries, so a revoked admin
// cannot climb back to admin through a guest invite.
func redeemInviteHandler(db *pgxpool.Pool, log *slog.Logger, perAccount *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !perAccount.allow(a.id) {
			w.Header().Set("Retry-After", fmt.Sprint(int(rateWindow.Seconds())))
			writeAuthError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Code == "" {
			writeAuthError(w, http.StatusNotFound, errInviteInvalid.Error())
			return
		}

		ctx := r.Context()
		var deviceID, role string
		err := inTx(ctx, db, func(tx pgx.Tx) error {
			var inviteID, creator, status string
			var creatorAdmin, alreadyBound bool
			err := tx.QueryRow(ctx,
				`SELECT i.id::text, i.created_by::text, d.status,
				        EXISTS (SELECT 1 FROM device_users
				                WHERE device_id = i.device_id AND user_id = i.created_by
				                  AND role = 'admin' AND revoked_at IS NULL),
				        EXISTS (SELECT 1 FROM device_users
				                WHERE device_id = i.device_id AND user_id = $2::uuid AND revoked_at IS NULL)
				 FROM invites i JOIN devices d ON d.device_id = i.device_id
				 WHERE i.code_hash = $1 AND i.used_at IS NULL AND i.expires_at > now()`,
				hashToken(in.Code), a.id).Scan(&inviteID, &creator, &status, &creatorAdmin, &alreadyBound)
			if err != nil {
				return err
			}
			if status != "active" {
				return errInviteDevice
			}
			if !creatorAdmin {
				return errInviteCreator
			}
			if alreadyBound {
				// Nothing to grant, and the code stays unused rather than being
				// burnt by someone who already had access.
				return errAlreadyBound
			}
			// The guard is this UPDATE. Two requests reach it with the same code, one
			// takes the row lock, and the other re-reads used_at after the winner
			// commits and finds it set.
			if err := tx.QueryRow(ctx,
				`UPDATE invites SET used_at = now(), used_by = $1::uuid
				 WHERE id = $2::uuid AND used_at IS NULL
				 RETURNING device_id, role`, a.id, inviteID).Scan(&deviceID, &role); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO device_users (device_id, user_id, role, created_by)
				 VALUES ($1, $2::uuid, $3, $4::uuid)
				 ON CONFLICT (device_id, user_id) DO UPDATE
				 SET role = EXCLUDED.role, created_by = EXCLUDED.created_by, revoked_at = NULL`,
				deviceID, a.id, role, creator); err != nil {
				return err
			}
			return appendAudit(ctx, tx, deviceID, a.id, ActionInviteRedeemed,
				AuditDetails{AccountID: a.id, Role: role})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, errInviteInvalid.Error())
			return
		case errors.Is(err, errInviteDevice):
			writeAuthError(w, http.StatusForbidden, "device disabled")
			return
		case errors.Is(err, errInviteCreator):
			writeAuthError(w, http.StatusForbidden, "the invite was made by someone who no longer administers this device")
			return
		case errors.Is(err, errAlreadyBound):
			writeAuthError(w, http.StatusConflict, "already has access to this device")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("invite redeemed", "device", deviceID, "account", a.id, "role", role)
		writeJSON(w, http.StatusOK, map[string]any{"device_id": deviceID, "role": role})
	}
}
