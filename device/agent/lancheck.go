package main

// The network warning on the settings page (#219). A PC can be running and still out of
// reach of every device on its Wi-Fi, and before this only the log said so. Each system
// has its own cause, so each has its own check in a platform file, and the page shows the
// fix.

import "net/http"

type lanProblem string

const (
	lanFine lanProblem = ""
	// lanPublic is Windows calling the current network public. The installer opens the
	// firewall on private and domain networks only (packaging/windows/anywhere-file.wxs).
	lanPublic lanProblem = "public"
	// lanDenied is macOS refusing the agent its Local Network permission, which leaves
	// the PC unannounced (packaging/README.md).
	lanDenied lanProblem = "local-network"
)

// lanCheck is asked on every request, so the warning goes away once it is fixed with no
// restart. Tests replace it.
var lanCheck = lanBlocked

var lanWarnings = map[lanProblem]struct {
	Message string
	Steps   []string
}{
	lanPublic: {
		Message: "Devices on this Wi-Fi can't reach this PC. Windows treats this network as public, and its firewall keeps them out.",
		Steps: []string{
			"Open Settings, then Network and internet.",
			"Open Wi-Fi or Ethernet, whichever this PC uses, then the properties of this network.",
			"Under Network profile type, choose Private network.",
		},
	},
	lanDenied: {
		Message: "Devices on this Wi-Fi can't find this PC. macOS has not allowed anywhere-file to use the local network.",
		Steps: []string{
			"Open System Settings, then Privacy and Security, then Local Network.",
			"Turn on anywhere-file.",
		},
	},
}

func networkStatus(w http.ResponseWriter, r *http.Request) {
	p := lanCheck()
	out := map[string]any{"problem": p}
	if warn, ok := lanWarnings[p]; ok {
		out["message"] = warn.Message
		out["steps"] = warn.Steps
	}
	writeJSON(w, http.StatusOK, out)
}
