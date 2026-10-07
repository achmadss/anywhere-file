package main

// Error reports (ADR 0007, #209). When something fails, this PC tells the server, so the
// owner can see a failure that happened on the Wi-Fi, where the server was not in the path.
// A report is a log line turned into an OpenTelemetry log record: the line's message, its
// trace id, and the few attributes named in reportAttrs. Nothing else on the line goes, so
// an "err" that quotes a path stays in the local log.
//
// Only a PC on an account with the checkbox on reports anything. Reports wait in a file
// while the server is out of reach, and go up signed with the device key, the way the
// application list does.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/achmadss/anywhere-file/internal/otlplog"
)

const (
	reportQueueFile = "reports.jsonl"
	// maxReportQueue is what the file may grow to. Past it the oldest reports go.
	maxReportQueue = 2 << 20
	// maxReportBatch keeps one upload under the 64 KiB the server reads of a signed body.
	maxReportBatch = 48 << 10
	reportEvery    = 30 * time.Second
	reportsPath    = "/v1/devices/reports"
)

// reportAttrs are the attributes a report may carry, and the names they go out under. A
// line reports itself by carrying error_code. Any line at error level reports too.
var reportAttrs = map[string]string{
	"error_code": "error.code",
	"step":       "step",
	"status":     "http.status_code",
}

// reportQueue is the file reports wait in.
type reportQueue struct {
	path string
	mu   sync.Mutex
	// on says whether a report should be kept at all. It is nil until the agent is open.
	on func() bool
	// tooLarge counts reports dropped for not fitting in one upload, until flush logs it.
	tooLarge int
}

func newReportQueue(dir string) *reportQueue {
	return &reportQueue{path: filepath.Join(dir, reportQueueFile)}
}

func (q *reportQueue) setOn(on func() bool) {
	q.mu.Lock()
	q.on = on
	q.mu.Unlock()
}

func (q *reportQueue) add(rec otlplog.LogRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.on == nil || !q.on() {
		return
	}
	// A report that cannot fit in one upload would stay at the head of the queue for good.
	if len(line) >= maxReportBatch {
		q.tooLarge++
		return
	}
	f, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	info, err := f.Stat()
	_ = f.Close()
	if err == nil && info.Size() > maxReportQueue {
		q.trim()
	}
}

// trim drops the oldest reports until the file is a quarter under the cap, so it is not
// rewritten on every report once full. The caller holds mu.
func (q *reportQueue) trim() {
	raw, err := os.ReadFile(q.path)
	if err != nil {
		return
	}
	for len(raw) > maxReportQueue*3/4 {
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			raw = nil
			break
		}
		raw = raw[i+1:]
	}
	_ = os.WriteFile(q.path, raw, 0o600)
}

// reporting is whether this PC sends error reports: it is on an account and the checkbox
// is on.
func (a *agent) reporting() bool {
	st := a.snapshot()
	return st.enrolled() && !st.ErrorReportsOff
}

// flush sends what is waiting, a batch at a time, until the file is empty or the server
// refuses. A PC that is off its account, or whose checkbox is off, throws its queue away.
func (q *reportQueue) flush(ctx context.Context, ag *agent) error {
	q.mu.Lock()
	tooLarge := q.tooLarge
	q.tooLarge = 0
	q.mu.Unlock()
	if tooLarge > 0 {
		ag.log.Info("error reports too large to send were dropped", "count", tooLarge)
	}
	st := ag.snapshot()
	if !ag.reporting() {
		q.mu.Lock()
		_ = os.Remove(q.path)
		q.mu.Unlock()
		return nil
	}
	for {
		q.mu.Lock()
		raw, _ := os.ReadFile(q.path)
		q.mu.Unlock()
		batch := raw
		if len(batch) > maxReportBatch {
			if i := bytes.LastIndexByte(batch[:maxReportBatch], '\n'); i >= 0 {
				batch = batch[:i+1]
			}
		}
		if len(batch) == 0 {
			return nil
		}
		var records []otlplog.LogRecord
		for line := range bytes.Lines(batch) {
			var rec otlplog.LogRecord
			if json.Unmarshal(line, &rec) == nil {
				records = append(records, rec)
			}
		}
		body, err := json.Marshal(otlplog.Request{ResourceLogs: []otlplog.ResourceLogs{{
			// The server sets who sent it. What the agent says about itself is its version.
			Resource: otlplog.Resource{Attributes: []otlplog.KeyValue{otlplog.String("service.version", version)}},
			ScopeLogs: []otlplog.ScopeLogs{{
				Scope:      otlplog.Scope{Name: "agent"},
				LogRecords: records,
			}},
		}}})
		if err != nil {
			return err
		}
		if err := ag.post(ctx, st.Server, reportsPath, body, nil); err != nil {
			return err
		}
		q.mu.Lock()
		// ponytail: a trim while the batch was on its way shifts the file, and then the
		// batch stays and goes again. It takes a few MB of failures in one upload.
		if now, err := os.ReadFile(q.path); err == nil && bytes.HasPrefix(now, batch) {
			_ = os.WriteFile(q.path, now[len(batch):], 0o600)
		}
		q.mu.Unlock()
	}
}

// run sends reports every reportEvery for as long as the agent runs. Offline is normal, so
// a failed send is a debug line and the next round tries again.
func (q *reportQueue) run(ctx context.Context, ag *agent) {
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := q.flush(ctx, ag); err != nil {
			ag.log.Debug("error reports not sent yet", "err", err)
		}
	}
}

// reportHandler sits in front of the log's own handler and copies the lines that report
// into the queue.
type reportHandler struct {
	slog.Handler
	q     *reportQueue
	attrs []slog.Attr
}

func (h reportHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn || h.Handler.Enabled(ctx, l)
}

func (h reportHandler) Handle(ctx context.Context, r slog.Record) error {
	if rec, ok := h.report(r); ok {
		h.q.add(rec)
	}
	if !h.Handler.Enabled(ctx, r.Level) {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

func (h reportHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return reportHandler{h.Handler.WithAttrs(as), h.q, append(slices.Clip(h.attrs), as...)}
}

func (h reportHandler) WithGroup(name string) slog.Handler {
	return reportHandler{h.Handler.WithGroup(name), h.q, h.attrs}
}

func (h reportHandler) report(r slog.Record) (otlplog.LogRecord, bool) {
	rec := otlplog.LogRecord{
		TimeUnixNano:   strconv.FormatInt(r.Time.UnixNano(), 10),
		SeverityNumber: otlplog.SeverityWarn,
		SeverityText:   "WARN",
		Body:           otlplog.Value{StringValue: r.Message},
	}
	if r.Level >= slog.LevelError {
		rec.SeverityNumber, rec.SeverityText = otlplog.SeverityError, "ERROR"
	}
	code := ""
	take := func(a slog.Attr) bool {
		switch v := a.Value.Resolve().String(); {
		case a.Key == "trace_id":
			rec.TraceID = v
		case reportAttrs[a.Key] != "":
			if a.Key == "error_code" {
				code = v
			}
			rec.Attributes = append(rec.Attributes, otlplog.String(reportAttrs[a.Key], v))
		}
		return true
	}
	for _, a := range h.attrs {
		take(a)
	}
	r.Attrs(take)
	if code == "" {
		if r.Level < slog.LevelError {
			return rec, false
		}
		rec.Attributes = append(rec.Attributes, otlplog.String("error.code", "agent_error"))
	}
	return rec, true
}
