package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newHandler builds the HTTP surface. net/http's ServeMux routes by method and pattern since
// Go 1.22, which is all this service needs; no router dependency.
func newHandler(db *pgxpool.Pool, log *slog.Logger, m *Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(db))
	mux.Handle("GET /metrics", m.Handler())
	registerAuthRoutes(mux, db, log)
	registerDeviceRoutes(mux, db, log)
	registerBindingRoutes(mux, db, log)
	return logRequests(mux, log)
}

// health reports whether the process can still reach PostgreSQL. There is no separate
// liveness endpoint: a control plane that cannot read its own directory is not alive in any
// sense a load balancer should care about.
func health(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		body := map[string]string{"status": "ok"}
		code := http.StatusOK
		if err := db.Ping(ctx); err != nil {
			body = map[string]string{"status": "unavailable", "reason": "database"}
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func logRequests(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// serve runs the HTTP server until ctx is cancelled, then drains in-flight requests.
func serve(ctx context.Context, cfg config, db *pgxpool.Pool, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           newHandler(db, log, NewMetrics()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		var err error
		if cfg.tlsCert != "" {
			log.Info("listening", "addr", cfg.addr, "tls", true)
			err = srv.ListenAndServeTLS(cfg.tlsCert, cfg.tlsKey)
		} else {
			// TLS terminates at the load balancer in a real deployment, and locally there is
			// no certificate worth minting. Refusing to start without one would only push
			// developers towards a self-signed certificate they then teach curl to ignore.
			log.Warn("listening without TLS: set RFM_TLS_CERT and RFM_TLS_KEY, or terminate TLS in front", "addr", cfg.addr)
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errs <- err
		}
		close(errs)
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return <-errs
}
