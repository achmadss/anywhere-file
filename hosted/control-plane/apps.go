package main

// Application registry sync (#84). The agent tells the server which logical applications
// its device exposes. The server stores a name and a type and nothing else: where an
// application listens stays in the agent's config and never crosses the wire, so a
// compromised server cannot learn a loopback address it was never given.
//
// A name reaches a URL, so it is checked here before it is stored: lowercase letters,
// digits and hyphens only, which leaves no room for a path, a query or an escape.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	appsSyncLimit = 30
	maxApps       = 32
	maxAppNameLen = 32
)

var appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var errAppsDeviceDisabled = errors.New("device disabled")

func registerAppRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger) {
	limiter := newRateLimiter(appsSyncLimit, rateWindow)
	mux.Handle("POST /v1/devices/apps", limiter.middleware(syncApps(db, log)))
}

// deviceAppAllowed reports whether the device currently offers that application. Remote
// request routing (#88) asks this before it looks for a tunnel, so a request for an
// application the device does not expose is refused without reaching the device at all.
func deviceAppAllowed(ctx context.Context, db querer, deviceID, name string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM device_apps WHERE device_id = $1 AND name = $2)`,
		deviceID, name).Scan(&ok)
	return ok, err
}

// validAppToken accepts a short lowercase word. Both the name and the type go through it.
func validAppToken(s string) bool {
	return len(s) <= maxAppNameLen && appNamePattern.MatchString(s)
}

// syncApps replaces the device's rows with the list it sends. It is the whole state, not
// a delta, so a device that stops offering an application drops it by sending the list
// without it. Sending the same list twice changes nothing.
func syncApps(db *pgxpool.Pool, log *slog.Logger) http.HandlerFunc {
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
			Apps []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"apps"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(in.Apps) > maxApps {
			writeAuthError(w, http.StatusBadRequest, "too many applications")
			return
		}
		names := make([]string, 0, len(in.Apps))
		types := make([]string, 0, len(in.Apps))
		seen := map[string]bool{}
		for _, app := range in.Apps {
			if !validAppToken(app.Name) || !validAppToken(app.Type) {
				writeAuthError(w, http.StatusBadRequest, "application name and type must be lowercase letters, digits and hyphens")
				return
			}
			if seen[app.Name] {
				writeAuthError(w, http.StatusBadRequest, "duplicate application name")
				return
			}
			seen[app.Name] = true
			names = append(names, app.Name)
			types = append(types, app.Type)
		}

		ctx := r.Context()
		var deviceID string
		err = inTx(ctx, db, func(tx pgx.Tx) error {
			var status string
			if err := tx.QueryRow(ctx,
				`SELECT device_id, status FROM devices WHERE public_key = $1`,
				deviceKey).Scan(&deviceID, &status); err != nil {
				return err
			}
			if status != "active" {
				return errAppsDeviceDisabled
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM device_apps WHERE device_id = $1 AND name <> ALL($2::text[])`,
				deviceID, names); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO device_apps (device_id, name, type)
				 SELECT $1, n, t FROM unnest($2::text[], $3::text[]) AS a(n, t)
				 ON CONFLICT (device_id, name) DO UPDATE SET type = EXCLUDED.type`,
				deviceID, names, types); err != nil {
				return err
			}
			return appendAudit(ctx, tx, deviceID, deviceID, ActionDeviceAppsSynced,
				AuditDetails{DeviceKey: deviceKey})
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "device not enrolled")
			return
		case errors.Is(err, errAppsDeviceDisabled):
			writeAuthError(w, http.StatusForbidden, "device disabled")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		log.Info("apps synced", "device", deviceID, "apps", len(names))
		writeJSON(w, http.StatusOK, map[string]any{"apps": names})
	}
}
