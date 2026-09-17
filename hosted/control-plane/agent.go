package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Agent request authentication. A request carries the device's Ed25519 signature in the
// headers that internal/devicesig defines. The signature says which device holds the key;
// the nonce is claimed here so a captured request cannot be replayed.
const (
	agentNonceTTL = 10 * time.Minute

	agentMessagesLimit = 60
)

func registerAgentRoutes(mux *http.ServeMux, db *pgxpool.Pool) {
	mux.Handle("GET /v1/agent/messages",
		newRateLimiter(agentMessagesLimit, rateWindow).middleware(agentMessages(db)))
}

// verifyAgentSignature checks the device signature on a request and claims its nonce.
// It returns the device key from the header. The caller binds that key to its payload.
func verifyAgentSignature(r *http.Request, db *pgxpool.Pool, body []byte) (string, error) {
	deviceKey, nonce, err := devicesig.Verify(r, body)
	if err != nil {
		return "", err
	}
	ctx := r.Context()
	_, _ = db.Exec(ctx, `DELETE FROM device_nonces WHERE expires_at <= now()`)
	var claimed bool
	err = db.QueryRow(ctx,
		`INSERT INTO device_nonces (nonce, device_key, expires_at)
		 VALUES ($1, $2, now() + $3::interval)
		 ON CONFLICT (nonce) DO NOTHING RETURNING true`,
		nonce, deviceKey,
		fmt.Sprintf("%d seconds", int(agentNonceTTL.Seconds()))).Scan(&claimed)
	if err != nil || !claimed {
		return "", fmt.Errorf("nonce already used")
	}
	return deviceKey, nil
}

// agentMessages lets a device poll what the cloud needs it to know: after account
// deletion, which of its workspaces went local-only and which memberships ended. Devices
// prove key ownership with the same signature as signed requests; no session is needed,
// because a locally paired device may have no account at all.
func agentMessages(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceKey, err := verifyAgentSignature(r, db, []byte{})
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "invalid device signature")
			return
		}
		if q := strings.TrimSpace(r.URL.Query().Get("device_key")); q != "" && q != deviceKey {
			writeAuthError(w, http.StatusBadRequest, "device key mismatch")
			return
		}
		var since int64
		if s := strings.TrimSpace(r.URL.Query().Get("since")); s != "" {
			since, _ = strconv.ParseInt(s, 10, 64)
		}
		rows, err := db.Query(r.Context(),
			`SELECT id, workspace_id, kind, body, created_at
			 FROM agent_messages WHERE device_key = $1 AND id > $2
			 ORDER BY id ASC LIMIT 100`,
			deviceKey, since)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		defer rows.Close()
		type message struct {
			ID          int64  `json:"id"`
			WorkspaceID string `json:"workspace_id"`
			Kind        string `json:"kind"`
			Body        string `json:"body"`
			CreatedAt   string `json:"created_at"`
		}
		out := []message{}
		for rows.Next() {
			var m message
			var at time.Time
			if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.Kind, &m.Body, &at); err != nil {
				writeAuthError(w, http.StatusInternalServerError, "try again later")
				return
			}
			m.CreatedAt = at.UTC().Format(time.RFC3339)
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"messages": out})
	}
}
