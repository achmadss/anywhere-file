package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deleteAccount removes an account for real: no support ticket, one endpoint. Owned
// workspaces become local-only (their associations and memberships are deleted, so the
// workspace keeps working over LAN with no cloud state), memberships elsewhere are
// removed, and the account's sessions and tokens die with it. Files and device keys are
// untouched: devices rows keep their keys and their account binding simply clears.
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

		result, err := runAccountDeletion(ctx, db, a.id, a.email)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("account deleted", "account", a.id,
			"local_only", len(result.localOnly), "memberships_removed", len(result.membershipsRemoved))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                    true,
			"local_only_workspaces": result.localOnly,
			"removed_memberships":   result.membershipsRemoved,
		})
	}
}

type deletionResult struct {
	localOnly          []string
	membershipsRemoved []string
}

// runAccountDeletion performs the deletion inside one transaction. Agents learn about it
// through agent_messages rows, which their devices poll: one local-only notice per
// device in each owned workspace, one removal notice per bound device elsewhere.
func runAccountDeletion(ctx context.Context, db *pgxpool.Pool, accountID, email string) (deletionResult, error) {
	var result deletionResult
	result.localOnly = []string{}
	result.membershipsRemoved = []string{}

	err := inTx(ctx, db, func(tx pgx.Tx) error {
		owned, err := workspaceIDs(ctx, tx,
			`SELECT workspace_id FROM workspace_associations WHERE owner_account_id = $1::uuid`, accountID)
		if err != nil {
			return err
		}
		member, err := workspaceIDs(ctx, tx,
			`SELECT workspace_id FROM workspace_members WHERE account_id = $1::uuid`, accountID)
		if err != nil {
			return err
		}
		// Owned workspaces may also list the owner as a member; they go local-only
		// once, not twice.
		ownedSet := map[string]bool{}
		for _, id := range owned {
			ownedSet[id] = true
		}
		memberOnly := member[:0]
		for _, id := range member {
			if !ownedSet[id] {
				memberOnly = append(memberOnly, id)
			}
		}

		for _, workspaceID := range owned {
			devices, err := workspaceIDs(ctx, tx,
				`SELECT device_key FROM device_authorizations WHERE workspace_id = $1`, workspaceID)
			if err != nil {
				return err
			}
			for _, deviceKey := range devices {
				if _, err := tx.Exec(ctx,
					`INSERT INTO agent_messages (workspace_id, device_key, kind, body)
					 VALUES ($1, $2, 'workspace.local_only', $3)`,
					workspaceID, deviceKey,
					fmt.Sprintf("owner account %s deleted; workspace is local-only", email)); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO audit_events (workspace_id, actor, action)
				 VALUES ($1, $2, 'workspace.local_only')`, workspaceID, accountID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM workspace_associations WHERE workspace_id = $1`, workspaceID); err != nil {
				return err
			}
			result.localOnly = append(result.localOnly, workspaceID)
		}

		for _, workspaceID := range memberOnly {
			bound, err := workspaceIDs(ctx, tx,
				`SELECT d.device_key FROM devices d
				 JOIN device_authorizations da ON da.device_key = d.device_key
				 WHERE da.workspace_id = $1 AND d.account_id = $2::uuid`,
				workspaceID, accountID)
			if err != nil {
				return err
			}
			for _, deviceKey := range bound {
				if _, err := tx.Exec(ctx,
					`INSERT INTO agent_messages (workspace_id, device_key, kind, body)
					 VALUES ($1, $2, 'membership.removed', $3)`,
					workspaceID, deviceKey,
					fmt.Sprintf("account %s deleted; membership removed", email)); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO audit_events (workspace_id, actor, action)
				 VALUES ($1, $2, 'membership.removed')`, workspaceID, accountID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM workspace_members WHERE workspace_id = $1 AND account_id = $2::uuid`,
				workspaceID, accountID); err != nil {
				return err
			}
			result.membershipsRemoved = append(result.membershipsRemoved, workspaceID)
		}

		// Sessions and one-time tokens die here; devices rows stay with their keys,
		// their account binding clearing through the ON DELETE SET NULL rule.
		if _, err := tx.Exec(ctx,
			`DELETE FROM sessions WHERE account_id = $1::uuid`, accountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM accounts WHERE id = $1::uuid`, accountID); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func workspaceIDs(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
