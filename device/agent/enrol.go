package main

// Enrolment (#94). The client mints a short-lived token in the cloud and hands it to the
// agent, over the LAN or on the command line. The agent signs the enrolment request with
// its device key, so the server learns the public key from a request only this PC could
// have made, and the token says which account the PC belongs to.
//
// Nothing is written to disk until the server has accepted. A bad or expired token leaves
// the PC exactly as it was.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
)

const enrolTimeout = 30 * time.Second

// agent is what the commands and the gateway share. The registry changes at runtime, when
// a PC is enrolled, so it is read through snapshot rather than held by a caller.
type agent struct {
	dir string
	key deviceKey
	log *slog.Logger
	hc  *http.Client

	mu sync.Mutex
	st *state

	// onApps is what the rest of the agent does when the application list changes: rebuild
	// the gateway's routes and start or stop what serves them. It is set by `agent run`
	// and nil in the commands that only read the file.
	onApps func([]app)
}

func newAgent(dir string, key deviceKey, st *state, log *slog.Logger) *agent {
	return &agent{
		dir: dir,
		key: key,
		log: log,
		hc:  &http.Client{Timeout: enrolTimeout},
		st:  st,
	}
}

func (a *agent) snapshot() *state {
	a.mu.Lock()
	defer a.mu.Unlock()
	copied := *a.st
	copied.Apps = append([]app(nil), a.st.Apps...)
	return &copied
}

// setApps replaces what this PC shares. Everything that changes the list goes through
// here, so the file on disk, the registry in memory, the gateway's routes and the running
// applications never disagree.
func (a *agent) setApps(apps []app) error {
	a.mu.Lock()
	next := *a.st
	next.Apps = slices.Clone(apps)
	if next.Apps == nil {
		next.Apps = []app{}
	}
	if err := next.validate(); err != nil {
		a.mu.Unlock()
		return err
	}
	// Written before anything starts serving it. A change that reached the gateway and not
	// the disk would come back on the next start.
	if err := saveState(a.dir, &next); err != nil {
		a.mu.Unlock()
		return err
	}
	a.st.Apps = next.Apps
	a.mu.Unlock()

	if a.onApps != nil {
		a.onApps(next.Apps)
	}
	// The server routes remote requests by the list it holds, so a share is unreachable
	// from away until this has gone up. Being offline is normal, so it does not hold up
	// the change or fail it.
	go a.pushApps(context.Background())
	return nil
}

// pushApps tells the server what this PC offers now. A failure is a log line: the next
// change or the next start sends it again.
func (a *agent) pushApps(ctx context.Context) {
	st := a.snapshot()
	if !st.enrolled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, enrolTimeout)
	defer cancel()
	if err := a.syncApps(ctx, st.Server, st); err != nil {
		a.log.Warn("the server has not been told what this PC shares", "err", err)
	}
}

// serverError is what the control plane said. The message is passed back to whoever asked
// for the enrolment, because "enrolment token invalid, expired or used" is the answer.
type serverError struct {
	status  int
	message string
}

func (e serverError) Error() string {
	return fmt.Sprintf("the server answered %d: %s", e.status, e.message)
}

// enrol registers this PC and pushes its application list, then writes both down. The
// application list goes up in the same run so the device appears in the client's list
// with what it offers, rather than empty until something else syncs it.
func (a *agent) enrol(ctx context.Context, server, token string) (*state, error) {
	server, err := cleanServerURL(server)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("no enrolment token")
	}
	st := a.snapshot()

	body, err := json.Marshal(map[string]string{
		"public_key":      a.key.publicHex(),
		"name":            st.Name,
		"enrolment_token": strings.TrimSpace(token),
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		DeviceID string `json:"device_id"`
	}
	if err := a.post(ctx, server, "/v1/devices/enrol", body, &out); err != nil {
		return nil, err
	}
	// The id is derived from the public key we just signed with, so we can check it
	// rather than take it. A different one is a server we should not be talking to.
	if out.DeviceID != a.key.deviceID() {
		return nil, fmt.Errorf("the server answered with device id %s, want %s", out.DeviceID, a.key.deviceID())
	}
	if err := a.syncApps(ctx, server, st); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.st.Server, a.st.DeviceID = server, out.DeviceID
	a.mu.Unlock()
	saved := a.snapshot()
	if err := saveState(a.dir, saved); err != nil {
		return nil, err
	}
	a.log.Info("enrolled", "server", server, "device", saved.DeviceID, "apps", saved.appNames())
	return saved, nil
}

// syncApps tells the server which applications this PC offers. The name and the type go
// up; the address each one listens on does not.
func (a *agent) syncApps(ctx context.Context, server string, st *state) error {
	apps := make([]map[string]string, 0, len(st.Apps))
	for _, app := range st.Apps {
		apps = append(apps, map[string]string{"name": app.Name, "type": app.Type})
	}
	body, err := json.Marshal(map[string]any{"apps": apps})
	if err != nil {
		return err
	}
	return a.post(ctx, server, "/v1/devices/apps", body, nil)
}

// post sends a signed request. The signature covers the body, so the server rejects a
// request a byte of which changed on the way.
func (a *agent) post(ctx context.Context, server, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	devicesig.Sign(req, a.key.priv, body)

	resp, err := a.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return serverAnswerError(resp.StatusCode, answer)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(answer, out)
}

// serverAnswerError turns a refusal into an error carrying what the server said, so the
// person waiting reads "enrolment token expired" rather than "400".
func serverAnswerError(status int, answer []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(answer, &e)
	if e.Error == "" {
		e.Error = http.StatusText(status)
	}
	return serverError{status: status, message: e.Error}
}

// cleanServerURL takes what a person typed and returns the origin to talk to. A path, a
// query or a fragment is a sign the wrong thing was pasted, so it is refused rather than
// dropped in silence.
func cleanServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("no server address")
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return "", fmt.Errorf("server address %q: %w", raw, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("server address %q: want an http or https URL", raw)
	case u.Host == "":
		return "", fmt.Errorf("server address %q: no host", raw)
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "":
		return "", fmt.Errorf("server address %q: want the address only, with no path", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}
