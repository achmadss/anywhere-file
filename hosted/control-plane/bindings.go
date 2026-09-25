package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bindings, roles and revocation (#85). A binding is a device_users row. Revocation sets
// revoked_at and deletes nothing, so history stays and the routing check (#88) reads the
// column on every request. A guest sees their own devices and nothing else.
const (
	listDevicesLimit = 60
	listUsersLimit   = 60
	revokeLimit      = 30
)

var errLastAdmin = errors.New("last admin")

func registerBindingRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger, reg *tunnelRegistry) {
	wrap := func(limit int, h http.HandlerFunc) http.Handler {
		return newRateLimiter(limit, rateWindow).middleware(h)
	}
	mux.Handle("GET /v1/devices", wrap(listDevicesLimit, requireSession(db, listDevices(db, reg))))
	mux.Handle("GET /v1/devices/{id}/users", wrap(listUsersLimit, requireSession(db, listDeviceUsers(db))))
	mux.Handle("POST /v1/devices/{id}/users/{user}/revoke", wrap(revokeLimit, requireSession(db, revokeBinding(db, log))))
}

// isActiveAdmin reports whether the account holds an unrevoked admin binding on the device.
func isActiveAdmin(ctx context.Context, db querer, deviceID, accountID string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM device_users
		 WHERE device_id = $1 AND user_id = $2::uuid AND role = 'admin' AND revoked_at IS NULL)`,
		deviceID, accountID).Scan(&ok)
	return ok, err
}

// listDevices answers with every device the caller is bound to, revoked bindings included,
// so a client can show "access removed" rather than a device silently vanishing. The
// applications are the names routing checks (#88), and a revoked binding gets none.
func listDevices(db *pgxpool.Pool, reg *tunnelRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		rows, err := db.Query(r.Context(),
			`SELECT d.device_id, d.name, d.status, u.role, u.revoked_at,
			        CASE WHEN u.revoked_at IS NULL
			             THEN ARRAY(SELECT name FROM device_apps da WHERE da.device_id = d.device_id ORDER BY name)
			             ELSE '{}' END
			 FROM device_users u JOIN devices d ON d.device_id = u.device_id
			 WHERE u.user_id = $1::uuid ORDER BY d.name, d.device_id`, a.id)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		defer rows.Close()
		type device struct {
			DeviceID  string     `json:"device_id"`
			Name      string     `json:"name"`
			Status    string     `json:"status"`
			Role      string     `json:"role"`
			RevokedAt *time.Time `json:"revoked_at"`
			Online    bool       `json:"online"`
			Apps      []string   `json:"apps"`
		}
		out := []device{}
		for rows.Next() {
			var d device
			if err := rows.Scan(&d.DeviceID, &d.Name, &d.Status, &d.Role, &d.RevokedAt, &d.Apps); err != nil {
				writeAuthError(w, http.StatusInternalServerError, "try again later")
				return
			}
			d.Online = reg.online(d.DeviceID)
			out = append(out, d)
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": out})
	}
}

// listDeviceUsers is admin only. Anyone else gets the same 404 an unknown device gets.
func listDeviceUsers(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := r.Context()
		deviceID := r.PathValue("id")
		if admin, err := isActiveAdmin(ctx, db, deviceID, a.id); err != nil || !admin {
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		}
		rows, err := db.Query(ctx,
			`SELECT u.user_id::text, a.email, u.role, u.created_at, u.revoked_at
			 FROM device_users u JOIN accounts a ON a.id = u.user_id
			 WHERE u.device_id = $1 ORDER BY u.created_at`, deviceID)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		defer rows.Close()
		type user struct {
			UserID    string     `json:"user_id"`
			Email     string     `json:"email"`
			Role      string     `json:"role"`
			CreatedAt time.Time  `json:"created_at"`
			RevokedAt *time.Time `json:"revoked_at"`
		}
		out := []user{}
		for rows.Next() {
			var u user
			if err := rows.Scan(&u.UserID, &u.Email, &u.Role, &u.CreatedAt, &u.RevokedAt); err != nil {
				writeAuthError(w, http.StatusInternalServerError, "try again later")
				return
			}
			out = append(out, u)
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": out})
	}
}

// revokeBinding sets revoked_at on one binding. The device row is locked for the length of
// the transaction, so two admins revoking each other at the same instant run one after the
// other and the second sees the first's result. Without that lock both count two active
// admins, both proceed, and the device is left with none.
func revokeBinding(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := r.Context()
		deviceID, target := r.PathValue("id"), r.PathValue("user")
		if validateToken("device_id", deviceID) != nil || validateToken("user", target) != nil {
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		}
		var role string
		err := inTx(ctx, db, func(tx pgx.Tx) error {
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM devices WHERE device_id = $1 FOR UPDATE`, deviceID).Scan(&one); err != nil {
				return err
			}
			if admin, err := isActiveAdmin(ctx, tx, deviceID, a.id); err != nil || !admin {
				return pgx.ErrNoRows
			}
			err := tx.QueryRow(ctx,
				`UPDATE device_users SET revoked_at = now()
				 WHERE device_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL
				   AND (role <> 'admin' OR (SELECT count(*) FROM device_users
				        WHERE device_id = $1 AND role = 'admin' AND revoked_at IS NULL) > 1)
				 RETURNING role`, deviceID, target).Scan(&role)
			if errors.Is(err, pgx.ErrNoRows) {
				// Either there is no active binding, or it is the last admin's.
				var active bool
				if err := tx.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM device_users
					 WHERE device_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL)`,
					deviceID, target).Scan(&active); err != nil {
					return err
				}
				if active {
					return errLastAdmin
				}
				return pgx.ErrNoRows
			}
			if err != nil {
				return err
			}
			return appendAudit(ctx, tx, deviceID, a.id, ActionBindingRevoked,
				AuditDetails{AccountID: target, Role: role})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		case errors.Is(err, errLastAdmin):
			writeAuthError(w, http.StatusConflict, "a device keeps at least one admin")
			return
		case err != nil:
			// A target that is not a UUID lands here as a cast error; it is a 404 too.
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		}
		log.Info("binding revoked", "device", deviceID, "user", target, "by", a.id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
