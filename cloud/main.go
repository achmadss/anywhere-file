// Command cloud is the control plane.
//
// It distributes signed trust lists, answers the relay's authorization probe, and holds the
// operator's dials. It never signs a trust list (r3 D5) and never grants access — it can
// only decline to hand out what an admin device already signed. See docs/adr/0001.
//
// Nothing is wired up yet. The schema lands in #19, the HTTP surface in #20–#23, relay
// authorization in #24.
package main

import (
	"fmt"
	"os"
)

// alpn is the protocol identifier peers negotiate. The control plane never speaks it — the
// constant is here only so the relay authorization endpoint can report which fleet it serves.
const alpn = "rfm/1"

func main() {
	fmt.Fprintf(os.Stdout, "anywhere-file control plane (fleet %s)\n", alpn)
}
