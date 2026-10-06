// Package traceparent reads and writes the W3C trace context header (ADR 0007). One user
// action is one trace, and the header carries its id from the app through the server and
// the tunnel to the agent, so the log lines of every hop can be found by that id.
package traceparent

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Header is the header's name. Go canonicalises it on the wire, which the spec allows.
const Header = "traceparent"

// TraceID returns the trace id in a valid header, and "" for anything else.
func TraceID(h string) string {
	if !valid(h) {
		return ""
	}
	return h[3:35]
}

// Next is the header to send on to the next hop: the same trace with a span id for this
// hop, or a new trace when the one that came in is missing or malformed.
func Next(in string) string {
	trace, flags := random(16), "01"
	if valid(in) {
		trace, flags = in[3:35], in[53:55]
	}
	return "00-" + trace + "-" + random(8) + "-" + flags
}

// valid follows the spec's parsing rules: version 00 is exactly 55 characters, a later
// version may add fields after them, and an id of all zeros means none.
func valid(h string) bool {
	if len(h) < 55 || h[2] != '-' || h[35] != '-' || h[52] != '-' {
		return false
	}
	version := h[0:2]
	if version == "ff" || (version == "00" && len(h) != 55) || (len(h) > 55 && h[55] != '-') {
		return false
	}
	for _, part := range []string{version, h[3:35], h[36:52], h[53:55]} {
		if !lowerHex(part) {
			return false
		}
	}
	return strings.Trim(h[3:35], "0") != "" && strings.Trim(h[36:52], "0") != ""
}

func lowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// random is n random bytes in hex. crypto/rand does not fail on the systems Go supports.
func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
