package main

// Prometheus series for r3 section 18 (issue #27), built on
// github.com/prometheus/client_golang. Each Metrics holds its own registry, so
// tests stay isolated from each other and from the process. The operator
// console (issue #44) reads these series directly: recommended relay count,
// committed rate, headroom, and denials by reason are computed once here, so
// the console and the alert rules cannot disagree about them.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// authorizeLatencyBuckets bounds the relay authorization histogram. The
// endpoint sits on the connection path and must answer from cache, so the
// buckets cluster below half a second, which is also the alert budget.
var authorizeLatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// relayStat is the per-relay input behind the fleet series. Committed rate is
// what was sold to the workspaces placed on the relay, actual rate is measured
// throughput, capacity is measured serving ability. Headroom and the
// recommended relay count derive from these three, in one place.
type relayStat struct {
	committedBps float64
	actualBps    float64
	capacityBps  float64
}

// Metrics holds every series. The collectors are safe for concurrent use by
// request handlers and background jobs; the mutex guards only the relay input
// map behind the derived gauges.
type Metrics struct {
	mu         sync.Mutex
	reg        *prometheus.Registry
	relayStats map[string]relayStat

	associationsActive     prometheus.Gauge
	membersActive          prometheus.Gauge
	relayAuthorizations    prometheus.Gauge
	recommendedRelayCount  prometheus.Gauge
	overcommitRatio        prometheus.Gauge
	authorizeRequests      prometheus.Counter
	authorizeErrors        prometheus.Counter
	authorizeLatency       prometheus.Histogram
	pairingOutcomes        *prometheus.CounterVec
	transferOutcomes       *prometheus.CounterVec
	subscriptionTransition *prometheus.CounterVec
	relayBytes             *prometheus.CounterVec
	denials                *prometheus.CounterVec
	jobLastRun             *prometheus.GaugeVec
	relayCommitted         *prometheus.GaugeVec
	relayActual            *prometheus.GaugeVec
	relayHeadroom          *prometheus.GaugeVec
	relayCapacity          *prometheus.GaugeVec
}

// NewMetrics builds a registry with every series. Ratio is the operator
// overcommit setting (r3 section 11.3); a value at or below zero means unset,
// and the registry keeps the conservative default of 1, which allows no
// overcommit.
func NewMetrics(ratio float64) *Metrics {
	if ratio <= 0 {
		ratio = 1
	}
	m := &Metrics{reg: prometheus.NewRegistry(), relayStats: map[string]relayStat{}}
	gauge := func(name, help string) prometheus.Gauge {
		g := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "rfm", Name: name, Help: help})
		m.reg.MustRegister(g)
		return g
	}
	counter := func(name, help string) prometheus.Counter {
		c := prometheus.NewCounter(prometheus.CounterOpts{Namespace: "rfm", Name: name, Help: help})
		m.reg.MustRegister(c)
		return c
	}
	m.associationsActive = gauge("associations_active", "Workspaces with an active cloud association.")
	m.membersActive = gauge("members_active", "Accounts holding active membership in any workspace.")
	m.relayAuthorizations = gauge("relay_authorizations_active", "Device keys currently admitted to relays.")
	m.recommendedRelayCount = gauge("relays_recommended_count", "Relays the fleet needs at current committed rate and overcommit ratio.")
	m.overcommitRatio = gauge("relay_overcommit_ratio", "Operator overcommit setting used for the recommended count.")
	m.overcommitRatio.Set(ratio)
	m.authorizeRequests = counter("relay_authorize_requests_total", "Calls to the relay authorization endpoint.")
	m.authorizeErrors = counter("relay_authorize_errors_total", "Authorization calls that errored. The endpoint fails closed, so these are customers unable to relay.")
	m.authorizeLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "rfm", Name: "relay_authorize_latency_seconds",
		Help: "Authorization endpoint latency in seconds.", Buckets: authorizeLatencyBuckets,
	})
	m.reg.MustRegister(m.authorizeLatency)
	counterVec := func(name, help string, labels ...string) *prometheus.CounterVec {
		v := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "rfm", Name: name, Help: help}, labels)
		m.reg.MustRegister(v)
		return v
	}
	gaugeVec := func(name, help string, labels ...string) *prometheus.GaugeVec {
		v := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "rfm", Name: name, Help: help}, labels)
		m.reg.MustRegister(v)
		return v
	}
	m.pairingOutcomes = counterVec("pairing_requests_total", "Pairing requests by outcome.", "outcome")
	m.transferOutcomes = counterVec("transfer_requests_total", "Transfer requests by outcome.", "outcome")
	m.subscriptionTransition = counterVec("subscription_transitions_total", "Subscription state changes by previous and new status.", "from_status", "to_status")
	m.relayBytes = counterVec("relay_bytes_total", "Relayed bytes by workspace, for capacity planning. Counting is not billing.", "workspace")
	m.denials = counterVec("relay_denials_total", "Relay refusals by reason.", "reason")
	m.jobLastRun = gaugeVec("subscription_job_last_run_unixtime", "Last successful run of a periodic job, as unix time.", "job")
	m.relayCommitted = gaugeVec("relay_committed_bps", "Committed rate placed on each relay.", "relay")
	m.relayActual = gaugeVec("relay_actual_bps", "Measured throughput of each relay.", "relay")
	m.relayHeadroom = gaugeVec("relay_headroom_bps", "Capacity minus actual throughput per relay.", "relay")
	m.relayCapacity = gaugeVec("relay_capacity_bps", "Measured capacity of each relay.", "relay")
	return m
}

// SetOvercommitRatio changes the operator dial at runtime. The console writes
// it, the recommended count recomputes from it, and the alert rule reads the
// exported series, so all three always agree.
func (m *Metrics) SetOvercommitRatio(ratio float64) error {
	if ratio <= 0 {
		return fmt.Errorf("metrics: overcommit ratio must be above zero, got %v", ratio)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overcommitRatio.Set(ratio)
	m.recomputeRecommendedLocked()
	return nil
}

// ObserveAuthorize records one call to the relay authorization endpoint (#24).
// Failed true means the endpoint errored; a clean refusal is not an error and
// is recorded separately with RecordDenial.
func (m *Metrics) ObserveAuthorize(d time.Duration, failed bool) {
	m.authorizeRequests.Inc()
	if failed {
		m.authorizeErrors.Inc()
	}
	m.authorizeLatency.Observe(d.Seconds())
}

// RecordDenial counts one refusal by reason, using the Deny* constants.
func (m *Metrics) RecordDenial(reason string) {
	m.denials.WithLabelValues(reason).Inc()
}

// RecordPairingOutcome counts a pairing request leaving pending state. The
// outcome matches the pairing_requests status column, so a counter reconciles
// against the table.
func (m *Metrics) RecordPairingOutcome(outcome string) {
	m.pairingOutcomes.WithLabelValues(outcome).Inc()
}

// RecordTransferOutcome counts a transfer request reaching an outcome. The
// outcome matches the transfer_requests status column, so a counter reconciles
// against the table.
func (m *Metrics) RecordTransferOutcome(outcome string) {
	m.transferOutcomes.WithLabelValues(outcome).Inc()
}

// RecordSubscriptionTransition counts one subscription status change.
func (m *Metrics) RecordSubscriptionTransition(fromStatus, toStatus string) {
	m.subscriptionTransition.WithLabelValues(fromStatus, toStatus).Inc()
}

// AddRelayBytes counts relayed bytes for one workspace. Direct traffic never
// reaches a relay, so no path label is needed to attribute it.
func (m *Metrics) AddRelayBytes(workspaceID string, n float64) {
	m.relayBytes.WithLabelValues(workspaceID).Add(n)
}

// SetRelayStats records one relay's measured rates and recomputes the derived
// series: headroom per relay and the recommended relay count. Headroom is
// capacity minus actual. The recommended count packs total committed rate into
// relays of the largest seen capacity at the current overcommit ratio, and is
// at least 1 once any relay reports.
func (m *Metrics) SetRelayStats(relay string, committedBps, actualBps, capacityBps float64) error {
	if committedBps < 0 || actualBps < 0 || capacityBps < 0 {
		return fmt.Errorf("metrics: relay rates must not be negative")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.relayStats[relay] = relayStat{committedBps: committedBps, actualBps: actualBps, capacityBps: capacityBps}
	m.relayCommitted.WithLabelValues(relay).Set(committedBps)
	m.relayActual.WithLabelValues(relay).Set(actualBps)
	m.relayCapacity.WithLabelValues(relay).Set(capacityBps)
	m.relayHeadroom.WithLabelValues(relay).Set(capacityBps - actualBps)
	m.recomputeRecommendedLocked()
	return nil
}

func (m *Metrics) recomputeRecommendedLocked() {
	if len(m.relayStats) == 0 {
		m.recommendedRelayCount.Set(0)
		return
	}
	var total, maxCap float64
	for _, s := range m.relayStats {
		total += s.committedBps
		if s.capacityBps > maxCap {
			maxCap = s.capacityBps
		}
	}
	if maxCap <= 0 {
		m.recommendedRelayCount.Set(float64(len(m.relayStats)))
		return
	}
	// The gauge mirrors the operator dial, so read it back instead of keeping
	// a second copy that could drift.
	ratio := testutil.ToFloat64(m.overcommitRatio)
	if ratio <= 0 {
		ratio = 1
	}
	recommended := math.Ceil(total / (maxCap * ratio))
	if recommended < 1 {
		recommended = 1
	}
	m.recommendedRelayCount.Set(recommended)
}

// RecordJobRun sets the last-run gauge for a periodic job.
func (m *Metrics) RecordJobRun(job string, at time.Time) {
	m.jobLastRun.WithLabelValues(job).Set(float64(at.Unix()))
}

// RefreshDirectoryGauges recounts the directory tables into the gauges. Call
// it on a timer; counters need no refresh because they only move forward.
func (m *Metrics) RefreshDirectoryGauges(ctx context.Context, db querer) error {
	var associations, members, auths float64
	if err := queryCount(ctx, db, `SELECT count(*) FROM workspace_associations WHERE status = 'active'`, &associations); err != nil {
		return err
	}
	if err := queryCount(ctx, db, `SELECT count(*) FROM workspace_members WHERE status = 'active'`, &members); err != nil {
		return err
	}
	if err := queryCount(ctx, db, `SELECT count(*) FROM device_authorizations WHERE status = 'active'`, &auths); err != nil {
		return err
	}
	m.associationsActive.Set(associations)
	m.membersActive.Set(members)
	m.relayAuthorizations.Set(auths)
	return nil
}

// querer covers *pgxpool.Pool and pgx.Tx, so refreshes can run inside a
// feature's transaction or on their own.
type querer interface {
	execer
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryCount(ctx context.Context, db querer, sql string, out *float64) error {
	var n int64
	if err := db.QueryRow(ctx, sql).Scan(&n); err != nil {
		return fmt.Errorf("metrics: refresh: %w", err)
	}
	*out = float64(n)
	return nil
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
