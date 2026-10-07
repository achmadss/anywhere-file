// Package otlplog is the part of the OpenTelemetry log format (OTLP/HTTP with JSON) that
// error reports use (ADR 0007). The agent writes it and the server reads it, stamps it and
// sends it on to the collector. Decoding into these types is also the server's filter:
// anything the agent sends that is not one of these fields is dropped.
package otlplog

// Request is the body of a POST to a collector's /v1/logs.
type Request struct {
	ResourceLogs []ResourceLogs `json:"resourceLogs"`
}

type ResourceLogs struct {
	Resource  Resource    `json:"resource"`
	ScopeLogs []ScopeLogs `json:"scopeLogs"`
}

type Resource struct {
	Attributes []KeyValue `json:"attributes"`
}

type ScopeLogs struct {
	Scope      Scope       `json:"scope"`
	LogRecords []LogRecord `json:"logRecords"`
}

type Scope struct {
	Name string `json:"name"`
}

// LogRecord is one report. TraceID is hex, as the JSON form of OTLP has it.
type LogRecord struct {
	TimeUnixNano   string     `json:"timeUnixNano"`
	SeverityNumber int        `json:"severityNumber"`
	SeverityText   string     `json:"severityText"`
	Body           Value      `json:"body"`
	TraceID        string     `json:"traceId,omitempty"`
	Attributes     []KeyValue `json:"attributes,omitempty"`
}

type KeyValue struct {
	Key   string `json:"key"`
	Value Value  `json:"value"`
}

// Value holds strings only, which is all a report carries.
type Value struct {
	StringValue string `json:"stringValue"`
}

func String(key, value string) KeyValue { return KeyValue{Key: key, Value: Value{StringValue: value}} }

// Severity numbers from the OpenTelemetry log data model.
const (
	SeverityWarn  = 13
	SeverityError = 17
)
