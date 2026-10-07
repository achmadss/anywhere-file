package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/achmadss/anywhere-file/internal/otlplog"
	"github.com/achmadss/anywhere-file/internal/traceparent"
)

// reportServer takes signed uploads to the reports route, or refuses them while down is set.
type reportServer struct {
	mu     sync.Mutex
	down   bool
	bodies []string
}

func (s *reportServer) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if _, _, err := devicesig.Verify(r, body); err != nil || r.URL.Path != reportsPath {
			t.Errorf("%s: %v", r.URL.Path, err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.down {
			http.Error(w, `{"error":"try again later"}`, http.StatusServiceUnavailable)
			return
		}
		s.bodies = append(s.bodies, string(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *reportServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// reportingAgent is an agent on an account at server, logging the way `agent run` does.
func reportingAgent(t *testing.T, server string, apps []app) (*agent, *reportQueue) {
	t.Helper()
	key := testKey(t)
	dir := agentDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	q := newReportQueue(dir)
	st := &state{Name: "pc1", Server: server, DeviceID: key.deviceID(), Apps: apps}
	ag := newAgent(dir, key, st, slog.New(reportHandler{Handler: discard.Handler(), q: q}))
	q.setOn(ag.reporting)
	return ag, q
}

func TestErrorReportsGoOnlyWhenSignedInWithTheBoxOn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()

	rs := &reportServer{}
	srv := rs.start(t)
	ag, q := reportingAgent(t, srv.URL, []app{{Name: "secret-share", Type: "http", Address: dead}})
	gw := httptest.NewServer(newGateway(ag))
	t.Cleanup(gw.Close)
	const trace = "4bf92f3577b34da6a3ce929d0e0e4736"
	fail := func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, gw.URL+"/secret-share/secret-folder/secret-file.txt", nil)
		req.Header.Set(traceparent.Header, "00-"+trace+"-00f067aa0ba902b7-01")
		resp, err := gw.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", resp.StatusCode)
		}
		if err := q.flush(t.Context(), ag); err != nil {
			t.Fatal(err)
		}
	}

	if err := ag.setErrorReports(false); err != nil {
		t.Fatal(err)
	}
	fail()
	if got := rs.received(); len(got) != 0 {
		t.Fatalf("sent with the box off: %v", got)
	}

	if err := ag.setErrorReports(true); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	ag.st.Server, ag.st.DeviceID = "", ""
	ag.mu.Unlock()
	fail()
	if got := rs.received(); len(got) != 0 {
		t.Fatalf("sent from a PC on no account: %v", got)
	}
	if _, err := os.Stat(q.path); err == nil {
		t.Error("a PC on no account kept reports on disk")
	}

	ag.mu.Lock()
	ag.st.Server, ag.st.DeviceID = srv.URL, ag.key.deviceID()
	ag.mu.Unlock()
	fail()
	got := rs.received()
	if len(got) != 1 {
		t.Fatalf("got %d uploads, want 1", len(got))
	}
	var sent otlplog.Request
	if err := json.Unmarshal([]byte(got[0]), &sent); err != nil {
		t.Fatal(err)
	}
	rec := sent.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.TraceID != trace || rec.Body.StringValue != "application unreachable" ||
		len(rec.Attributes) == 0 || rec.Attributes[0] != otlplog.String("error.code", "app_unreachable") {
		t.Errorf("record = %+v, want the trace, the reason and its code", rec)
	}
	if strings.Contains(got[0], "secret") || strings.Contains(got[0], dead) {
		t.Errorf("the report names a share, a file or an address: %s", got[0])
	}
}

func TestTheReportQueueKeepsUnderItsCapAndSendsWhenTheServerIsBack(t *testing.T) {
	rs := &reportServer{down: true}
	ag, q := reportingAgent(t, rs.start(t).URL, nil)
	for range 20000 {
		ag.log.Error("the settings endpoint stopped")
	}
	info, err := os.Stat(q.path)
	if err != nil || info.Size() > maxReportQueue {
		t.Fatalf("queue: %v, %v, want at most %d bytes", info.Size(), err, maxReportQueue)
	}
	if err := q.flush(t.Context(), ag); err == nil {
		t.Fatal("flush to a server that is down succeeded")
	}
	if after, _ := os.Stat(q.path); after.Size() != info.Size() {
		t.Fatal("a refused upload lost reports")
	}

	rs.mu.Lock()
	rs.down = false
	rs.mu.Unlock()
	if err := q.flush(t.Context(), ag); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(q.path); len(raw) != 0 {
		t.Errorf("%d bytes still queued after the server came back", len(raw))
	}
	got := rs.received()
	if len(got) < 2 {
		t.Errorf("%d uploads, want the queue sent in batches", len(got))
	}
	for _, b := range got {
		if len(b) > 1<<16 {
			t.Errorf("an upload of %d bytes, over what the server reads", len(b))
		}
	}
}

func TestAReportTooLargeToSendIsDropped(t *testing.T) {
	rs := &reportServer{}
	ag, q := reportingAgent(t, rs.start(t).URL, nil)
	ag.log.Error(strings.Repeat("x", maxReportBatch))
	ag.log.Error("the settings endpoint stopped")
	if err := q.flush(t.Context(), ag); err != nil {
		t.Fatal(err)
	}
	got := rs.received()
	if len(got) != 1 || strings.Contains(got[0], "xxxx") || !strings.Contains(got[0], "the settings endpoint stopped") {
		t.Fatalf("uploads = %d, want one with only the report that fits", len(got))
	}
	if raw, _ := os.ReadFile(q.path); len(raw) != 0 {
		t.Errorf("%d bytes still queued", len(raw))
	}
}
