package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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
	m.RecordSubscriptionTransition("active", "suspended")
	m.RecordRemoteRequest(http.StatusOK, 300*time.Millisecond)
	m.RecordRemoteDenial(denyNoBinding)
	m.RecordTunnelConnect()
	m.RecordTunnelDisconnect(reasonTimeout)
	m.SetLiveTunnels(1)
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
	m := NewMetrics()
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
		"rfm_subscription_transitions_total", "rfm_subscription_job_last_run_unixtime",
	} {
		if !seen[name] {
			t.Errorf("series %s missing from /metrics", name)
		}
	}

	for _, want := range []string{
		`rfm_subscription_transitions_total{from_status="active",to_status="suspended"} 1`,
		`rfm_subscription_job_last_run_unixtime{job="subscription-state"} 1.7e+09`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics has no line %q", want)
		}
	}
}

// Heartbeats persist so the stale-job alert survives a restart. Write twice to
// prove the upsert, then load into a fresh registry.
func TestJobHeartbeatSurvivesRestart(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	m := NewMetrics()
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

	fresh := NewMetrics()
	if err := fresh.RefreshJobGauges(ctx, pool); err != nil {
		t.Fatalf("refresh jobs: %v", err)
	}
	want := `rfm_subscription_job_last_run_unixtime{job="subscription-state"} 1.7000036e+09`
	if !strings.Contains(scrape(t, fresh), want) {
		t.Errorf("fresh registry has no line %q", want)
	}
}
