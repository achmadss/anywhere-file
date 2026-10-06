package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// lanBlocked asks Windows for the profile of the network the agent advertises on. Reading
// it needs no administrator.
//
// ponytail: PowerShell takes about a second to start, and the page asks on load and when
// its tab comes back into focus. Call the NetworkListManager COM interface directly if
// that ever feels slow.
func lanBlocked() lanProblem {
	iface := multicastInterface()
	if iface == nil {
		return lanFine
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-NetConnectionProfile -InterfaceIndex "+strconv.Itoa(iface.Index)+").NetworkCategory")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return lanFine
	}
	if strings.TrimSpace(string(out)) == "Public" {
		return lanPublic
	}
	return lanFine
}
