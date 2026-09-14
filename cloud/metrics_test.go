package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// scrape serves one /metrics request the way Prometheus would make it.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	return rec.Body.String()
}

// exerciseAllSeries touches every method that feeds /metrics, so the scrape
// test below proves each series renders rather than trusting the renderer.
func exerciseAllSeries(m *Metrics) {
	m.RecordPairingOutcome("approved")
	m.RecordPairingOutcome("rejected")
	m.RecordTransferOutcome("initiated")
	m.RecordTransferOutcome("confirmed")
	m.RecordSubscriptionTransition("active", "grace")
	m.AddRelayBytes("ws-1", 1024)
	m.RecordDenial(DenyMemberRemoved)
	m.ObserveAuthorize(10*time.Millisecond, false)
	m.ObserveAuthorize(2*time.Second, true)
	_ = m.SetRelayStats("relay-1", 8_000_000, 5_000_000, 10_000_000)
	m.RecordJobRun("subscription-state", time.Unix(1_700_000_000, 0))
}

// samplePattern matches one exposition sample line: a metric name, optional
// labels, and a value. Timestamps are not rendered and not accepted.
var samplePattern = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{.*\})? ([^\s]+)$`)

var labelPattern = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"$`)

// parseExposition checks every line of a scrape the way Prometheus would:
// comments declare HELP and TYPE, samples carry a valid name, valid labels,
// and a parseable value. It returns the set of series seen.
func parseExposition(t *testing.T, body string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	typed := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			rest := strings.TrimPrefix(line, "# HELP ")
			name := strings.SplitN(rest, " ", 2)
			if len(name) != 2 || name[0] == "" || name[1] == "" {
				t.Errorf("bad HELP line: %q", line)
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.Split(strings.TrimPrefix(line, "# TYPE "), " ")
			if len(parts) != 2 {
				t.Errorf("bad TYPE line: %q", line)
				continue
			}
			if parts[1] != "counter" && parts[1] != "gauge" && parts[1] != "histogram" {
				t.Errorf("unknown type %q in line %q", parts[1], line)
			}
			typed[parts[0]] = parts[1]
			continue
		}
		if strings.HasPrefix(line, "#") {
			t.Errorf("unknown comment line: %q", line)
			continue
		}
		m := samplePattern.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("unparseable sample line: %q", line)
			continue
		}
		name, labels, value := m[1], m[2], m[3]
		if labels != "" {
			inner := strings.TrimSuffix(strings.TrimPrefix(labels, "{"), "}")
			if inner != "" {
				for _, pair := range splitLabels(inner) {
					if !labelPattern.MatchString(pair) {
						t.Errorf("bad label pair %q in line %q", pair, line)
					}
				}
			}
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil &&
			value != "+Inf" && value != "-Inf" && value != "Nan" {
			t.Errorf("bad value %q in line %q", value, line)
		}
		seen[name] = true
	}
	return seen
}

// splitLabels splits label pairs on commas outside quoted strings.
func splitLabels(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && inQuotes:
			cur.WriteRune(r)
			escaped = true
		case r == '"':
			cur.WriteRune(r)
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(parts, cur.String())
}

// /metrics must scrape cleanly: status, content type, and a body Prometheus
// accepts, with every series the console and the alerts need.
func TestMetricsScrapeClean(t *testing.T) {
	m := NewMetrics(2)
	exerciseAllSeries(m)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type = %q, want text/plain with the exposition version", ct)
	}
	body := rec.Body.String()
	seen := parseExposition(t, body)

	for _, name := range []string{
		"rfm_associations_active", "rfm_members_active", "rfm_relay_authorizations_active",
		"rfm_pairing_requests_total", "rfm_transfer_requests_total",
		"rfm_subscription_transitions_total", "rfm_relay_bytes_total",
		"rfm_relay_denials_total", "rfm_relay_authorize_requests_total",
		"rfm_relay_authorize_errors_total", "rfm_relay_authorize_latency_seconds_bucket",
		"rfm_relay_committed_bps", "rfm_relay_actual_bps", "rfm_relay_headroom_bps",
		"rfm_relay_capacity_bps", "rfm_relays_recommended_count",
		"rfm_relay_overcommit_ratio", "rfm_subscription_job_last_run_unixtime",
	} {
		if !seen[name] {
			t.Errorf("series %s missing from /metrics", name)
		}
	}

	for _, want := range []string{
		`rfm_pairing_requests_total{outcome="approved"} 1`,
		`rfm_relay_denials_total{reason="member_removed"} 1`,
		`rfm_relay_headroom_bps{relay="relay-1"} 5e+06`,
		`rfm_relay_overcommit_ratio 2`,
		`rfm_relays_recommended_count 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics has no line %q", want)
		}
	}
}

// The recommended count packs committed rate into relays of the largest seen
// capacity at the current ratio. The console shows this number and the alert
// fires on it; both read the same gauge.
func TestRelayRecommendedCount(t *testing.T) {
	m := NewMetrics(1)
	if got := testutil.ToFloat64(m.recommendedRelayCount); got != 0 {
		t.Fatalf("empty fleet recommends %v, want 0", got)
	}

	_ = m.SetRelayStats("r1", 8_000_000, 0, 10_000_000)
	if got := testutil.ToFloat64(m.recommendedRelayCount); got != 1 {
		t.Errorf("8M committed on 10M capacity recommends %v, want 1", got)
	}
	_ = m.SetRelayStats("r2", 17_000_000, 0, 10_000_000)
	if got := testutil.ToFloat64(m.recommendedRelayCount); got != 3 {
		t.Errorf("25M committed on 10M capacity recommends %v, want 3", got)
	}
	if err := m.SetOvercommitRatio(2); err != nil {
		t.Fatalf("set ratio: %v", err)
	}
	if got := testutil.ToFloat64(m.recommendedRelayCount); got != 2 {
		t.Errorf("25M committed at ratio 2 recommends %v, want 2", got)
	}
	if err := m.SetOvercommitRatio(0); err == nil {
		t.Error("ratio 0 accepted, want rejection")
	}
	if err := m.SetRelayStats("r3", -1, 0, 10_000_000); err == nil {
		t.Error("negative rate accepted, want rejection")
	}

	// Unknown capacity keeps the current fleet size rather than guessing zero.
	m2 := NewMetrics(1)
	_ = m2.SetRelayStats("r1", 1_000_000, 0, 0)
	if got := testutil.ToFloat64(m2.recommendedRelayCount); got != 1 {
		t.Errorf("zero capacity recommends %v, want fleet size 1", got)
	}
}

// Directory gauges come from the tables, so the test seeds rows and checks
// the rendered values.
func TestDirectoryGaugesRefresh(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	if _, err := pool.Exec(ctx,
		`INSERT INTO accounts (id, email) VALUES
		 ('11111111-1111-1111-1111-111111111111', 'a@example.test'),
		 ('22222222-2222-2222-2222-222222222222', 'b@example.test')`); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace_associations (workspace_id, owner_account_id, status, trust_list_version)
		 VALUES ('ws-g', '11111111-1111-1111-1111-111111111111', 'active', 1)`); err != nil {
		t.Fatalf("seed association: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, account_id, role, status) VALUES
		 ('ws-g', '11111111-1111-1111-1111-111111111111', 'owner', 'active'),
		 ('ws-g', '22222222-2222-2222-2222-222222222222', 'member', 'active')`); err != nil {
		t.Fatalf("seed members: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO devices (device_key, account_id, display_name) VALUES
		 ('dev-1', '11111111-1111-1111-1111-111111111111', 'laptop'),
		 ('dev-2', NULL, 'nas')`); err != nil {
		t.Fatalf("seed devices: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_authorizations (workspace_id, device_key, status) VALUES
		 ('ws-g', 'dev-1', 'active'),
		 ('ws-g', 'dev-2', 'revoked')`); err != nil {
		t.Fatalf("seed authorizations: %v", err)
	}

	m := NewMetrics(1)
	if err := m.RefreshDirectoryGauges(ctx, pool); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	body := scrape(t, m)
	for _, want := range []string{
		"rfm_associations_active 1",
		"rfm_members_active 2",
		"rfm_relay_authorizations_active 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered metrics have no line %q", want)
		}
	}
}

// Heartbeats persist so the stale-job alert survives a restart. Write twice to
// prove the upsert, then load into a fresh registry.
func TestJobHeartbeatSurvivesRestart(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	m := NewMetrics(1)
	first := time.Unix(1_700_000_000, 0).UTC()
	second := first.Add(time.Hour)
	if err := m.RecordJobHeartbeat(ctx, pool, "subscription-state", first); err != nil {
		t.Fatalf("heartbeat 1: %v", err)
	}
	if err := m.RecordJobHeartbeat(ctx, pool, "subscription-state", second); err != nil {
		t.Fatalf("heartbeat 2: %v", err)
	}
	if err := m.RecordJobHeartbeat(ctx, pool, "bad/job", second); err == nil {
		t.Error("job name with a slash accepted, want rejection")
	}

	fresh := NewMetrics(1)
	if err := fresh.RefreshJobGauges(ctx, pool); err != nil {
		t.Fatalf("refresh jobs: %v", err)
	}
	want := `rfm_subscription_job_last_run_unixtime{job="subscription-state"} 1.7000036e+09`
	if !strings.Contains(scrape(t, fresh), want) {
		t.Errorf("fresh registry has no line %q", want)
	}
}
