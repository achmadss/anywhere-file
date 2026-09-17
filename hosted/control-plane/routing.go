package main

// Remote request routing (#88). Every request a user makes to an application on their PC
// arrives here, and this is the only place the server decides who may reach what. The
// checks run in a fixed order and each one alone refuses. What comes back is the
// application's own response, streamed, so a large file never sits in this process.

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"github.com/jackc/pgx/v5/pgxpool"
)

// headerRequestID correlates one remote request across this log, the agent's log and the
// audit row #89 adds.
const headerRequestID = "X-Request-Id"

// Why a remote request was refused. The client only sees the status; the reason is for the
// operator, in the log and on the counter.
const (
	denyNoSession    = "no_session"
	denyBadTarget    = "bad_target"
	denyNoBinding    = "no_binding"
	denyNoApp        = "no_app"
	denySubscription = "subscription_inactive"
	denyOffline      = "device_offline"
)

func registerRoutingRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger, m *Metrics, reg *tunnelRegistry) {
	// No rate limiter: this path carries file transfers, and a per-request limit would
	// throttle a download rather than an attacker.
	h := remoteRequest(db, log, m, reg)
	mux.Handle("/d/{device}/{app}", h)
	mux.Handle("/d/{device}/{app}/{rest...}", h)
}

func remoteRequest(db *pgxpool.Pool, log *slog.Logger, m *Metrics, reg *tunnelRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		deviceID, app := r.PathValue("device"), r.PathValue("app")

		deny := func(status int, reason, message string) {
			m.RecordRemoteDenial(reason)
			log.Info("remote request denied",
				"request_id", requestID, "reason", reason, "status", status,
				"device", deviceID, "app", app)
			writeAuthError(w, status, message)
		}

		// The session is looked up here rather than through requireSession, so the first
		// check is counted and logged like the rest of them.
		ctx := r.Context()
		raw := sessionTokenFromRequest(r)
		if raw == "" {
			deny(http.StatusUnauthorized, denyNoSession, "unauthorized")
			return
		}
		a, _, err := lookupSession(ctx, db, raw)
		if err != nil {
			deny(http.StatusUnauthorized, denyNoSession, "unauthorized")
			return
		}
		if validateToken("device_id", deviceID) != nil || !validAppToken(app) {
			deny(http.StatusNotFound, denyBadTarget, "not found")
			return
		}

		// One round trip for the binding, the application and the device's owner. The
		// owner is the first admin the device ever had, revoked or not, because that is
		// the account the subscription belongs to.
		var bound, hasApp bool
		var owner *string
		err = db.QueryRow(ctx,
			`SELECT
			   EXISTS (SELECT 1 FROM device_users WHERE device_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL),
			   EXISTS (SELECT 1 FROM device_apps WHERE device_id = $1 AND name = $3),
			   (SELECT user_id::text FROM device_users WHERE device_id = $1 AND role = 'admin' ORDER BY created_at LIMIT 1)`,
			deviceID, a.id, app).Scan(&bound, &hasApp, &owner)
		if err != nil {
			deny(http.StatusNotFound, denyBadTarget, "not found")
			return
		}
		if !bound {
			deny(http.StatusNotFound, denyNoBinding, "not found")
			return
		}
		if !hasApp {
			deny(http.StatusNotFound, denyNoApp, "not found")
			return
		}
		// Enrolment always leaves an admin row, so a device with no owner is a device
		// nobody pays for.
		if owner == nil {
			deny(http.StatusPaymentRequired, denySubscription, "subscription inactive")
			return
		}
		switch active, err := subscriptionActive(ctx, db, *owner); {
		case err != nil:
			deny(http.StatusNotFound, denyBadTarget, "not found")
			return
		case !active:
			deny(http.StatusPaymentRequired, denySubscription, "subscription inactive")
			return
		}
		t := reg.get(deviceID)
		if t == nil {
			deny(http.StatusServiceUnavailable, denyOffline, "device offline")
			return
		}

		rest := r.PathValue("rest")
		path := "/" + app
		if rest != "" {
			path += "/" + rest
		}
		log.Info("remote request",
			"request_id", requestID, "device", deviceID, "app", app,
			"method", r.Method, "account", a.id)

		proxy := &httputil.ReverseProxy{
			// FlushInterval -1 writes each chunk straight through, so a download starts
			// arriving at once and nothing waits on a buffer filling.
			FlushInterval: -1,
			Transport:     t.cc,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = "http"
				pr.Out.URL.Host = deviceID
				pr.Out.URL.Path = path
				pr.Out.URL.RawPath = ""
				pr.Out.Host = ""
				// The application answers on the agent's gateway, which trusts whoever
				// reaches it. Our own credentials must not be part of what reaches it.
				pr.Out.Header.Del("Authorization")
				stripSessionCookie(pr.Out)
				pr.Out.Header.Set(headerRequestID, requestID)
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				// The tunnel died between the lookup above and the request going down it.
				log.Warn("remote request failed", "request_id", requestID, "device", deviceID, "err", err)
				deny(http.StatusServiceUnavailable, denyOffline, "device offline")
			},
			ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
		proxy.ServeHTTP(w, r)
	}
}

// stripSessionCookie removes our own cookie and leaves the application's. Copyparty and
// friends set cookies of their own on the same host, and dropping the whole header would
// sign the user out of the application on every request.
func stripSessionCookie(r *http.Request) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != sessionCookie {
			r.AddCookie(c)
		}
	}
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
