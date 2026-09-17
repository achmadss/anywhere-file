package main

// Subscription stub (#21, ADR 0005). Billing is not built. An account has a status an
// operator sets by hand, and remote request routing (#88) refuses when it reads
// suspended. Local access on the LAN never reads it.
//
// The whole stub is this file plus one call in signup, so a real provider can replace it
// without unpicking anything else.

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	operatorLimit    = 10
	operatorTokenEnv = "RFM_OPERATOR_TOKEN"
	operatorHeader   = "X-Operator-Token"
)

func registerSubscriptionRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger, m *Metrics) {
	limiter := newRateLimiter(operatorLimit, rateWindow)
	mux.Handle("POST /v1/admin/subscriptions/{account}", limiter.middleware(setSubscription(db, log, m)))
}

// seedSubscription gives a new account the status it starts on.
func seedSubscription(ctx context.Context, db execer, accountID string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO subscriptions (account_id, status) VALUES ($1::uuid, 'active')
		 ON CONFLICT (account_id) DO NOTHING`, accountID)
	return err
}

// subscriptionActive is the gate routing reads. An account with no row counts as active:
// the stub blocks only an account an operator suspended, never one that slipped through
// seeding.
func subscriptionActive(ctx context.Context, db querer, accountID string) (bool, error) {
	var status string
	err := db.QueryRow(ctx,
		`SELECT status FROM subscriptions WHERE account_id = $1::uuid`, accountID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return status == "active", nil
}

// operatorAuthorized checks the shared operator secret. With no secret configured the
// endpoint does not exist, so a deployment that forgets to set one cannot be flipped by
// anybody rather than by everybody.
func operatorAuthorized(r *http.Request) bool {
	want := os.Getenv(operatorTokenEnv)
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get(operatorHeader)), []byte(want)) == 1
}

// setSubscription flips one account's status. It exists to test the gate; there is no
// provider behind it and no webhook into it.
func setSubscription(db *pgxpool.Pool, log *slog.Logger, m *Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !operatorAuthorized(r) {
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		}
		accountID := r.PathValue("account")
		var in struct {
			Status string `json:"status"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Status != "active" && in.Status != "suspended" {
			writeAuthError(w, http.StatusBadRequest, "status must be active or suspended")
			return
		}

		ctx := r.Context()
		var from string
		err := inTx(ctx, db, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx,
				`SELECT status FROM subscriptions WHERE account_id = $1::uuid FOR UPDATE`, accountID).Scan(&from)
			if errors.Is(err, pgx.ErrNoRows) {
				var exists bool
				if err := tx.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1::uuid)`, accountID).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return pgx.ErrNoRows
				}
				from = "active"
			} else if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO subscriptions (account_id, status) VALUES ($1::uuid, $2)
				 ON CONFLICT (account_id) DO UPDATE SET status = EXCLUDED.status, updated_at = now()`,
				accountID, in.Status); err != nil {
				return err
			}
			return appendAudit(ctx, tx, "", accountID, ActionSubscriptionChanged,
				AuditDetails{AccountID: accountID, FromStatus: from, ToStatus: in.Status})
		})
		if err != nil {
			// An account id that is not a UUID fails the cast and lands here too.
			writeAuthError(w, http.StatusNotFound, "not found")
			return
		}
		m.RecordSubscriptionTransition(from, in.Status)
		log.Info("subscription set", "account", accountID, "from", from, "to", in.Status)
		writeJSON(w, http.StatusOK, map[string]any{"account_id": accountID, "status": in.Status})
	}
}
