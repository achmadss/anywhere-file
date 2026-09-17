package main

// Prometheus series, built on github.com/prometheus/client_golang. Each Metrics holds
// its own registry, so tests stay isolated from each other and from the process. The
// series describe the new model (#89): remote requests, the tunnels they travel down and
// the checks that refuse them.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds every series. The collectors are safe for concurrent use by
// request handlers and background jobs.
type Metrics struct {
	reg *prometheus.Registry

	subscriptionTransition *prometheus.CounterVec
	remoteRequest          *prometheus.CounterVec
	remoteDuration         prometheus.Histogram
	remoteDenial           *prometheus.CounterVec
	tunnelConnect          prometheus.Counter
	tunnelDisconnect       *prometheus.CounterVec
	tunnelsLive            prometheus.Gauge
	jobLastRun             *prometheus.GaugeVec
}

// NewMetrics builds a registry with every series.
func NewMetrics() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	m.subscriptionTransition = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "rfm", Name: "subscription_transitions_total",
		Help: "Subscription state changes by previous and new status.",
	}, []string{"from_status", "to_status"})
	m.remoteRequest = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "rfm", Name: "remote_requests_total",
		Help: "Remote requests by response status, refusals included.",
	}, []string{"status"})
	m.remoteDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "rfm", Name: "remote_request_duration_seconds",
		Help: "Time from arrival to the last byte of the response.",
		// Wide buckets: this path serves both a directory listing and a file that takes
		// minutes, and one histogram covers them.
		Buckets: []float64{0.05, 0.25, 1, 5, 30, 120, 600},
	})
	m.tunnelConnect = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "rfm", Name: "tunnel_connects_total",
		Help: "Tunnels opened by an agent.",
	})
	m.tunnelDisconnect = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "rfm", Name: "tunnel_disconnects_total",
		Help: "Tunnels closed, by why they closed.",
	}, []string{"reason"})
	m.tunnelsLive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "rfm", Name: "tunnels_live",
		Help: "Devices with a live tunnel right now.",
	})
	m.remoteDenial = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "rfm", Name: "remote_denials_total",
		Help: "Remote requests refused, by the check that refused them.",
	}, []string{"reason"})
	m.jobLastRun = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "rfm", Name: "subscription_job_last_run_unixtime",
		Help: "Last successful run of a periodic job, as unix time.",
	}, []string{"job"})
	m.reg.MustRegister(m.subscriptionTransition, m.remoteRequest, m.remoteDuration,
		m.remoteDenial, m.tunnelConnect, m.tunnelDisconnect, m.tunnelsLive, m.jobLastRun)
	return m
}

// RecordSubscriptionTransition counts one subscription status change.
func (m *Metrics) RecordSubscriptionTransition(fromStatus, toStatus string) {
	m.subscriptionTransition.WithLabelValues(fromStatus, toStatus).Inc()
}

// RecordRemoteRequest counts one finished remote request and its duration. Refusals count
// here too, under the status they answered with.
func (m *Metrics) RecordRemoteRequest(status int, d time.Duration) {
	m.remoteRequest.WithLabelValues(strconv.Itoa(status)).Inc()
	m.remoteDuration.Observe(d.Seconds())
}

// RecordTunnelConnect counts one tunnel opening.
func (m *Metrics) RecordTunnelConnect() { m.tunnelConnect.Inc() }

// RecordTunnelDisconnect counts one tunnel closing, by reason.
func (m *Metrics) RecordTunnelDisconnect(reason string) {
	m.tunnelDisconnect.WithLabelValues(reason).Inc()
}

// SetLiveTunnels publishes how many devices are reachable right now.
func (m *Metrics) SetLiveTunnels(n int) { m.tunnelsLive.Set(float64(n)) }

// RecordRemoteDenial counts one refused remote request.
func (m *Metrics) RecordRemoteDenial(reason string) {
	m.remoteDenial.WithLabelValues(reason).Inc()
}

// RecordJobRun sets the last-run gauge for a periodic job.
func (m *Metrics) RecordJobRun(job string, at time.Time) {
	m.jobLastRun.WithLabelValues(job).Set(float64(at.Unix()))
}

// querer covers *pgxpool.Pool and pgx.Tx, so refreshes can run inside a
// feature's transaction or on their own.
type querer interface {
	execer
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// RecordJobHeartbeat stores a job run in job_heartbeats and sets the gauge,
// so the last-run series survives a restart.
func (m *Metrics) RecordJobHeartbeat(ctx context.Context, db execer, job string, at time.Time) error {
	if err := validateToken("job", job); err != nil {
		return err
	}
	_, err := db.Exec(ctx,
		`INSERT INTO job_heartbeats (name, last_run) VALUES ($1, $2)
		 ON CONFLICT (name) DO UPDATE SET last_run = EXCLUDED.last_run`,
		job, at.UTC())
	if err != nil {
		return fmt.Errorf("metrics: heartbeat: %w", err)
	}
	m.RecordJobRun(job, at)
	return nil
}

// RefreshJobGauges reloads every stored heartbeat into the gauges. Call it at
// startup before serving /metrics.
func (m *Metrics) RefreshJobGauges(ctx context.Context, db querer) error {
	rs, err := db.Query(ctx, `SELECT name, last_run FROM job_heartbeats`)
	if err != nil {
		return fmt.Errorf("metrics: refresh jobs: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name string
		var at time.Time
		if err := rs.Scan(&name, &at); err != nil {
			return fmt.Errorf("metrics: refresh jobs: %w", err)
		}
		m.RecordJobRun(name, at)
	}
	return rs.Err()
}

// Handler serves /metrics in Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
