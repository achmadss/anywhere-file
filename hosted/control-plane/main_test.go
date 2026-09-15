package main

import "testing"

func TestALPNMatchesCore(t *testing.T) {
	// Must stay identical to rfm_core::ALPN in core/src/lib.rs. Two languages, one
	// constant, no shared header, so it is asserted on both sides.
	if alpn != "rfm/1" {
		t.Fatalf("alpn = %q, want %q", alpn, "rfm/1")
	}
}
