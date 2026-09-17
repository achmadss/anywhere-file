package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deleteAccount removes an account for real: no support ticket, one endpoint. The account's
// device bindings, invites, sessions and tokens go with it. Devices are left alone: a PC
// keeps its key and its other users, and only loses this one.
func deleteAccount(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, ok := accountFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var in struct {
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &in) || in.Password == "" {
			writeAuthError(w, http.StatusBadRequest, "password required")
			return
		}

		ctx := r.Context()
		var stored *string
		if err := db.QueryRow(ctx,
			`SELECT password_hash FROM accounts WHERE id = $1::uuid`, a.id).Scan(&stored); err != nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if stored == nil || !checkPassword(in.Password, *stored) {
			writeAuthError(w, http.StatusUnauthorized, "invalid password")
			return
		}

		bindings, err := runAccountDeletion(ctx, db, a.id)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("account deleted", "account", a.id, "bindings_removed", bindings)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bindings_removed": bindings})
	}
}

// runAccountDeletion deletes the account in one transaction and returns how many device
// bindings went with it. Bindings, invites, sessions and tokens are removed by the
// ON DELETE rules on their foreign keys; the audit row is written first so history
// records who was deleted.
func runAccountDeletion(ctx context.Context, db *pgxpool.Pool, accountID string) (int64, error) {
	var bindings int64
	err := inTx(ctx, db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM device_users WHERE user_id = $1::uuid`, accountID).Scan(&bindings); err != nil {
			return err
		}
		if err := appendAudit(ctx, tx, "", accountID, ActionAccountDeleted,
			AuditDetails{AccountID: accountID}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM accounts WHERE id = $1::uuid`, accountID)
		return err
	})
	return bindings, err
}
