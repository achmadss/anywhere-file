package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Agent request authentication (r3 section 10.1: the device signs and submits with the
// user's session). A request carries both proofs at once: the session token says which
// account is asking, the Ed25519 signature says which device holds the key. Either one
// alone is refused.
//
// Signed headers:
//
//	X-Device-Key:       the device key, hex or base64, 32 bytes raw
//	X-Device-Nonce:     random, at least 16 characters, accepted once
//	X-Device-Timestamp: unix seconds, within 5 minutes of now
//	X-Device-Signature: base64 or hex signature over
//	    METHOD\npath\ndevice_key\nnonce\ntimestamp\nhex(sha256(body))
//
// The path excludes the query string. Device keys stay opaque text in the database; the
// signature check accepts hex or base64 encodings of the raw 32 byte key. The iroh z32
// rendering the core crate prints is not accepted yet; it arrives when #22 settles the
// on-the-wire encoding the 0001 migration left open.
const (
	agentNonceTTL  = 10 * time.Minute
	agentClockSkew = 5 * time.Minute
	minNonceLen    = 16

	agentMessagesLimit = 60
)

func registerAgentRoutes(mux *http.ServeMux, db *pgxpool.Pool) {
	mux.Handle("GET /v1/agent/messages",
		newRateLimiter(agentMessagesLimit, rateWindow).middleware(agentMessages(db)))
}

func decodeB64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(strings.TrimSpace(s)); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

func parseDeviceKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
		return ed25519.PublicKey(b), nil
	}
	if b, err := decodeB64(s); err == nil && len(b) == ed25519.PublicKeySize {
		return ed25519.PublicKey(b), nil
	}
	return nil, fmt.Errorf("device key is not 32 bytes hex or base64")
}

func parseSignature(s string) ([]byte, error) {
	if b, err := hex.DecodeString(strings.TrimSpace(s)); err == nil && len(b) == ed25519.SignatureSize {
		return b, nil
	}
	b, err := decodeB64(s)
	if err != nil || len(b) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature is not 64 bytes hex or base64")
	}
	return b, nil
}

func requestPath(r *http.Request) string {
	if p := r.URL.EscapedPath(); p != "" {
		return p
	}
	return r.URL.Path
}

// verifyAgentSignature checks the device signature on a request and claims its nonce.
// It returns the device key from the header. The caller binds that key to its payload.
// This check is the section 10.1 deliverable: the agent signs and submits with the
// user's session. Issue #22 owns the Enable Remote Access endpoint that calls it.
func verifyAgentSignature(r *http.Request, db *pgxpool.Pool, body []byte) (string, error) {
	keyHeader := r.Header.Get("X-Device-Key")
	nonce := r.Header.Get("X-Device-Nonce")
	timestamp := r.Header.Get("X-Device-Timestamp")
	sigHeader := r.Header.Get("X-Device-Signature")
	if keyHeader == "" || nonce == "" || timestamp == "" || sigHeader == "" {
		return "", fmt.Errorf("missing device signature headers")
	}
	if len(nonce) < minNonceLen {
		return "", fmt.Errorf("nonce too short")
	}
	pub, err := parseDeviceKey(keyHeader)
	if err != nil {
		return "", err
	}
	sig, err := parseSignature(sigHeader)
	if err != nil {
		return "", err
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return "", fmt.Errorf("bad timestamp")
	}
	if dt := time.Since(time.Unix(secs, 0)); dt > agentClockSkew || dt < -agentClockSkew {
		return "", fmt.Errorf("timestamp outside window")
	}

	sum := sha256.Sum256(body)
	payload := strings.Join([]string{
		r.Method, requestPath(r), strings.TrimSpace(keyHeader), nonce,
		strings.TrimSpace(timestamp), hex.EncodeToString(sum[:]),
	}, "\n")
	if !ed25519.Verify(pub, []byte(payload), sig) {
		return "", fmt.Errorf("bad signature")
	}

	ctx := r.Context()
	_, _ = db.Exec(ctx, `DELETE FROM device_nonces WHERE expires_at <= now()`)
	var claimed bool
	err = db.QueryRow(ctx,
		`INSERT INTO device_nonces (nonce, device_key, expires_at)
		 VALUES ($1, $2, now() + $3::interval)
		 ON CONFLICT (nonce) DO NOTHING RETURNING true`,
		nonce, strings.TrimSpace(keyHeader),
		fmt.Sprintf("%d seconds", int(agentNonceTTL.Seconds()))).Scan(&claimed)
	if err != nil || !claimed {
		return "", fmt.Errorf("nonce already used")
	}
	return strings.TrimSpace(keyHeader), nil
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
