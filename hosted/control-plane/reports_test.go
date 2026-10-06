package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/achmadss/anywhere-file/internal/otlplog"
)

// TestReportsCarryTheSigningDevice sends a report that claims to come from another device
// and account, and checks the collector is told who really signed it.
func TestReportsCarryTheSigningDevice(t *testing.T) {
	got := make(chan otlplog.Request, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in otlplog.Request
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/logs" || json.Unmarshal(raw, &in) != nil {
			t.Errorf("collector got %s %s", r.URL.Path, raw)
		}
		got <- in
	}))
	defer collector.Close()
	t.Setenv("RFM_OTLP_ENDPOINT", collector.URL)

	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	other := enrolKey(t, h, "other@example.com", "pc2")
	claimed := []otlplog.KeyValue{
		otlplog.String("device.id", derivedDeviceID(other)),
		otlplog.String("account.id", accountIDByEmail(t, pool, "other@example.com")),
	}
	report := otlplog.Request{ResourceLogs: []otlplog.ResourceLogs{{
		Resource: otlplog.Resource{Attributes: claimed},
		ScopeLogs: []otlplog.ScopeLogs{{LogRecords: []otlplog.LogRecord{{
			Body:       otlplog.Value{StringValue: "application unreachable"},
			Attributes: append(claimed, otlplog.String("error.code", "app_unreachable")),
		}}}},
	}}}

	if rec := signedPost(t, h, priv, "/v1/devices/reports", report); rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	out := <-got
	attrs := map[string]string{}
	for _, kv := range out.ResourceLogs[0].Resource.Attributes {
		attrs[kv.Key] = kv.Value.StringValue
	}
	if len(attrs) != len(out.ResourceLogs[0].Resource.Attributes) || attrs["device.id"] != derivedDeviceID(priv) || attrs["account.id"] != accountIDByEmail(t, pool, "owner@example.com") {
		t.Errorf("resource = %v, want the signing device and its account", attrs)
	}
	recAttrs := out.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes
	if len(recAttrs) != 1 || recAttrs[0].Key != "error.code" {
		t.Errorf("record attributes = %v, want only error.code", recAttrs)
	}

	if rec := signedPost(t, h, newDeviceKey(t), "/v1/devices/reports", report); rec.Code != http.StatusNotFound {
		t.Errorf("a device on no account: status = %d, want 404", rec.Code)
	}
}
