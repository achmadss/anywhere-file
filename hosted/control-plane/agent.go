package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Agent request authentication. A request carries the device's Ed25519 signature in the
// headers that internal/devicesig defines. The signature says which device holds the key;
// the nonce is claimed here so a captured request cannot be replayed. Enrolment (#83) is
// the first route to use it.
const agentNonceTTL = 10 * time.Minute

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
