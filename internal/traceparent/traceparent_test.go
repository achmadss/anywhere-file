package traceparent

import "testing"

const sample = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestNextKeepsTheTraceAndChangesTheSpan(t *testing.T) {
	out := Next(sample)
	if TraceID(out) != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("Next(%q) = %q, want the same trace id", sample, out)
	}
	if out[36:52] == sample[36:52] || out[53:] != "01" {
		t.Errorf("Next(%q) = %q, want a new span id and the same flags", sample, out)
	}
}

func TestAMalformedHeaderStartsANewTrace(t *testing.T) {
	for _, in := range []string{
		"",
		"garbage",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01", // no trace
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", // no span
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", // upper case
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", // version ff
		sample + "-extra", // version 00 has no more fields
	} {
		if TraceID(in) != "" {
			t.Errorf("TraceID(%q) = %q, want none", in, TraceID(in))
		}
		out := Next(in)
		if TraceID(out) == "" {
			t.Errorf("Next(%q) = %q, not a valid header", in, out)
		}
	}
	if TraceID("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-future") == "" {
		t.Error("a later version with more fields was refused")
	}
}
