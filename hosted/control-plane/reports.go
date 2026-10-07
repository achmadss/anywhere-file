package main

// Error reports from PCs (ADR 0007, #209). The agent sends OpenTelemetry log records,
// signed with its device key. Which device and which account sent them is set here from
// the key that signed, and whatever the records say about that is dropped, so one PC
// cannot report as another. The records then go to the OpenTelemetry collector named by
// RFM_OTLP_ENDPOINT. With none set they are counted and dropped.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/achmadss/anywhere-file/internal/otlplog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const reportsLimit = 30

// stampedKeys are the attributes the server sets. A record that carries one loses it.
var stampedKeys = []string{"service.name", "device.id", "account.id"}

func registerReportRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger) {
	collector := strings.TrimRight(os.Getenv("RFM_OTLP_ENDPOINT"), "/")
	limiter := newRateLimiter(reportsLimit, rateWindow)
	mux.Handle("POST /v1/devices/reports", limiter.middleware(receiveReports(db, log, collector, &http.Client{Timeout: 10 * time.Second})))
}

func receiveReports(db *pgxpool.Pool, log *slog.Logger, collector string, hc *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, deviceKey, ok := signedBody(w, r, db)
		if !ok {
			return
		}
		var in otlplog.Request
		if err := json.Unmarshal(body, &in); err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		var deviceID, status string
		var accountID *string
		err := db.QueryRow(r.Context(),
			`SELECT device_id, status,
			   (SELECT user_id::text FROM device_users WHERE device_id = d.device_id
			      AND role = 'admin' AND revoked_at IS NULL ORDER BY created_at LIMIT 1)
			 FROM devices d WHERE public_key = $1`, deviceKey).Scan(&deviceID, &status, &accountID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "device not enrolled")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		case status != "active":
			writeAuthError(w, http.StatusForbidden, "device disabled")
			return
		case accountID == nil:
			// A PC on no account sends nothing (ADR 0007).
			writeAuthError(w, http.StatusGone, "device removed from its account")
			return
		}

		stamped := func(kv otlplog.KeyValue) bool { return slices.Contains(stampedKeys, kv.Key) }
		count := 0
		for i := range in.ResourceLogs {
			res := &in.ResourceLogs[i].Resource
			res.Attributes = append(slices.DeleteFunc(res.Attributes, stamped),
				otlplog.String("service.name", "agent"),
				otlplog.String("device.id", deviceID),
				otlplog.String("account.id", *accountID))
			for j := range in.ResourceLogs[i].ScopeLogs {
				recs := in.ResourceLogs[i].ScopeLogs[j].LogRecords
				for k := range recs {
					recs[k].Attributes = slices.DeleteFunc(recs[k].Attributes, stamped)
				}
				count += len(recs)
			}
		}
		if collector == "" {
			log.Info("error reports dropped, RFM_OTLP_ENDPOINT is not set", "device", deviceID, "records", count)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		out, err := json.Marshal(in)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, collector+"/v1/logs", bytes.NewReader(out))
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 300 {
				err = errors.New(resp.Status)
			}
		}
		if err != nil {
			// The agent keeps what it sent and tries again later.
			log.Warn("the collector did not take error reports", "device", deviceID, "records", count, "err", err)
			writeAuthError(w, http.StatusServiceUnavailable, "try again later")
			return
		}
		log.Info("error reports forwarded", "device", deviceID, "records", count)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
