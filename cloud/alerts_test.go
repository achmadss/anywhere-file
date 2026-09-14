package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ruleFile mirrors the Prometheus rule file shape: groups of alerts with an
// expression, a pending period, and labels. Parsing it here validates the YAML
// with a real parser instead of eyeballing indentation.
type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert       string            `yaml:"alert"`
			Expr        string            `yaml:"expr"`
			For         string            `yaml:"for"`
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	return filepath.Join(filepath.Dir(file), name)
}

func loadRules(t *testing.T) ruleFile {
	t.Helper()
	raw, err := os.ReadFile(repoFile(t, "rules.yml"))
	if err != nil {
		t.Fatalf("read rules.yml: %v", err)
	}
	var rf ruleFile
	if err := yaml.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("parse rules.yml: %v", err)
	}
	return rf
}

// Every alert the task requires must exist, with an expression, a pending
// period, a severity, and a human summary. A rule that fails to parse or
// misses any of those is a rule Prometheus would reject or an operator would
// ignore.
func TestAlertRulesCoverRequiredSignals(t *testing.T) {
	rf := loadRules(t)

	byName := map[string]bool{}
	for _, g := range rf.Groups {
		if g.Name == "" {
			t.Error("a rule group has no name")
		}
		for _, r := range g.Rules {
			byName[r.Alert] = true
			if r.Expr == "" {
				t.Errorf("%s has no expr", r.Alert)
			}
			if r.For == "" {
				t.Errorf("%s has no for period", r.Alert)
			}
			sev := r.Labels["severity"]
			if sev != "critical" && sev != "warning" {
				t.Errorf("%s has severity %q, want critical or warning", r.Alert, sev)
			}
			if r.Annotations["summary"] == "" || r.Annotations["description"] == "" {
				t.Errorf("%s needs summary and description annotations", r.Alert)
			}
		}
	}

	for _, want := range []string{
		"RelayAuthorizeFailing",
		"RelayDenialSpike",
		"RelayThroughputNearCapacity",
		"RelayCommittedOverRatio",
		"RelayUnreachable",
		"RelayMetricsMissing",
		"SubscriptionJobStale",
	} {
		if !byName[want] {
			t.Errorf("required alert %s missing from rules.yml", want)
		}
	}
}

// The rules may only reference series the control plane actually exports (plus
// the `up` series Prometheus itself adds). A rule on a series nothing emits
// never fires and hides the outage it names.
func TestAlertRulesReferenceExportedSeries(t *testing.T) {
	rf := loadRules(t)

	m := NewMetrics(1)
	exerciseAllSeries(m)
	exported := parseExposition(t, scrape(t, m))

	seriesRef := regexp.MustCompile(`rfm_[a-z_0-9]+`)
	seenRef := map[string]bool{}
	for _, g := range rf.Groups {
		for _, r := range g.Rules {
			for _, token := range seriesRef.FindAllString(r.Expr, -1) {
				seenRef[token] = true
			}
		}
	}
	for token := range seenRef {
		if !exported[token] {
			t.Errorf("rule references %s, which /metrics does not export", token)
		}
	}
	for _, token := range []string{
		"rfm_relay_authorize_errors_total", "rfm_relay_authorize_requests_total",
		"rfm_relay_authorize_latency_seconds_bucket", "rfm_relay_denials_total",
		"rfm_relay_actual_bps", "rfm_relay_capacity_bps", "rfm_relay_committed_bps",
		"rfm_relay_overcommit_ratio", "rfm_subscription_job_last_run_unixtime",
	} {
		if !seenRef[token] {
			t.Errorf("no rule references exported series %s", token)
		}
	}
}

// alertmanagerFile mirrors the routing file shape. Routing, grouping, and
// escalation live here; the test pins that the critical path pages.
type alertmanagerFile struct {
	Route struct {
		Receiver string   `yaml:"receiver"`
		GroupBy  []string `yaml:"group_by"`
		Routes   []struct {
			Matchers []string `yaml:"matchers"`
			Receiver string   `yaml:"receiver"`
		} `yaml:"routes"`
	} `yaml:"route"`
	Receivers []struct {
		Name string `yaml:"name"`
	} `yaml:"receivers"`
}

func TestAlertmanagerRoutesCriticalToPaging(t *testing.T) {
	raw, err := os.ReadFile(repoFile(t, "alertmanager.yml"))
	if err != nil {
		t.Fatalf("read alertmanager.yml: %v", err)
	}
	var cf alertmanagerFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parse alertmanager.yml: %v", err)
	}

	if cf.Route.Receiver == "" {
		t.Error("no default receiver")
	}
	if len(cf.Route.GroupBy) == 0 {
		t.Error("no group_by: every alert would notify on its own")
	}
	names := map[string]bool{}
	for _, r := range cf.Receivers {
		names[r.Name] = true
	}
	if !names[cf.Route.Receiver] {
		t.Errorf("default receiver %q is not defined", cf.Route.Receiver)
	}

	pages := false
	for _, r := range cf.Route.Routes {
		// An empty receiver inherits the parent route's, which is valid
		// Alertmanager. A set one must name a defined receiver.
		if r.Receiver != "" && !names[r.Receiver] {
			t.Errorf("route receiver %q is not defined", r.Receiver)
		}
		for _, m := range r.Matchers {
			if strings.Contains(m, "critical") && r.Receiver == "paging" {
				pages = true
			}
		}
	}
	if !pages {
		t.Error("no route sends critical severity to paging: pages would never fire")
	}
}
