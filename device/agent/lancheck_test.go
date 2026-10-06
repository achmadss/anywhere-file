package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The page shows whatever the endpoint answers, and the answer follows the check on every
// request, so a fixed network clears the warning with no restart.
func TestTheSettingsEndpointSaysWhenTheWiFiCannotReachThisPC(t *testing.T) {
	_, srv, _, token := settingsFor(t)
	t.Cleanup(func() { lanCheck = lanBlocked })
	for _, c := range []struct {
		problem lanProblem
		want    string
	}{
		{lanPublic, "choose Private network"},
		{lanDenied, "Turn on anywhere-file"},
		{lanFine, ""},
	} {
		lanCheck = func() lanProblem { return c.problem }
		status, body := ask(t, srv, http.MethodGet, "/v1/network", token, "")
		if status != http.StatusOK {
			t.Fatalf("%q: answered %d: %s", c.problem, status, body)
		}
		var out struct {
			Problem lanProblem
			Message string
			Steps   []string
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		if out.Problem != c.problem {
			t.Errorf("problem %q, want %q", out.Problem, c.problem)
		}
		steps := strings.Join(out.Steps, " ")
		if c.want == "" && (out.Message != "" || steps != "") {
			t.Errorf("a fine network still warns: %s", body)
		}
		if c.want != "" && (out.Message == "" || !strings.Contains(steps, c.want)) {
			t.Errorf("%q: steps %q do not say %q", c.problem, steps, c.want)
		}
	}
}

// The real check, on whichever system runs the tests. What it finds depends on the
// machine, so it is only logged.
func TestTheNetworkCheckRunsHere(t *testing.T) {
	t.Logf("lanBlocked() = %q", lanBlocked())
}
