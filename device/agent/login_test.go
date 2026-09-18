package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// loginWait is what the tests give a goroutine to notice an answer. The fake server asks
// to be polled once a second, and everything here is loopback.
const loginWait = 10 * time.Second

// The acceptance: a person approves the PC in a browser and the PC is signed in. What the
// agent holds is the device key, so the token it collects is one only this PC can use.
func TestApprovingInTheBrowserSignsThePCIn(t *testing.T) {
	ag, dir := enrolAgent(t)
	ag.st.Server, ag.st.DeviceID = "", ""
	cp := &fakeControlPlane{t: t}
	srv := cp.server()

	acc, err := ag.startLogin(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if acc.Login.Code == "" || acc.Login.URL == "" || !acc.Login.Waiting {
		t.Fatalf("login = %+v, want a code to approve", acc.Login)
	}
	// The PC names itself when it asks, because whoever approves it is shown that name.
	_, _, start := cp.counted()
	var sent map[string]string
	if err := json.Unmarshal(start, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["name"] != "pc1" {
		t.Errorf("start body = %v, want the PC's name", sent)
	}

	// Nothing is bound until somebody answers.
	if ag.snapshot().enrolled() {
		t.Fatal("the PC enrolled before anyone approved it")
	}
	cp.answers("approved")
	waitFor(t, loginWait, func() bool { return ag.account().Enrolled }, "the PC was never signed in")

	acc = ag.account()
	if acc.Server != srv.URL || acc.DeviceID != ag.key.deviceID() {
		t.Errorf("account = %+v, want the server and this device", acc)
	}
	if acc.Login.Waiting || acc.Login.Code != "" {
		t.Errorf("login = %+v, want nothing left to approve", acc.Login)
	}
	// On disk, so a restart comes back signed in.
	reloaded, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.enrolled() {
		t.Errorf("reloaded state = %+v, want an enrolled PC", reloaded)
	}
}

// A refusal is an answer. The agent stops asking and says what happened, and the PC is
// left exactly as it was.
func TestARefusalLeavesThePCSignedOutAndStopsTheAsking(t *testing.T) {
	ag, _ := enrolAgent(t)
	ag.st.Server, ag.st.DeviceID = "", ""
	cp := &fakeControlPlane{t: t}
	cp.answers("refused")
	srv := cp.server()

	if _, err := ag.startLogin(t.Context(), srv.URL); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, loginWait, func() bool { return ag.account().Login.Error != "" }, "the refusal never arrived")

	acc := ag.account()
	if acc.Enrolled || acc.Login.Waiting {
		t.Errorf("account = %+v, want a PC that is still signed out", acc)
	}
	if !strings.Contains(acc.Login.Error, "refused") {
		t.Errorf("error = %q, want what the browser said", acc.Login.Error)
	}
	polls, _, _ := cp.counted()
	// Two intervals later it is still the one ask. Retrying a refusal would be asking
	// somebody who has already said no.
	time.Sleep(2500 * time.Millisecond)
	if again, _, _ := cp.counted(); again != polls {
		t.Errorf("polled %d times after the refusal, want %d", again, polls)
	}
}

// Signing out is the PC removing itself, proved by its own key. Nothing about the account
// is needed for it, because the agent holds nothing about the account.
func TestAPCSignsItselfOutAndKeepsServingTheLAN(t *testing.T) {
	ag, dir := enrolAgent(t)
	cp := &fakeControlPlane{t: t}
	srv := cp.server()
	if _, err := ag.enrol(t.Context(), srv.URL, "a-token"); err != nil {
		t.Fatal(err)
	}

	if _, err := ag.logout(t.Context()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	_, unenrols, _ := cp.counted()
	if len(unenrols) != 1 || unenrols[0] != ag.key.publicHex() {
		t.Errorf("unenrol signed by %v, want the device key", unenrols)
	}
	after := ag.snapshot()
	if after.enrolled() {
		t.Errorf("state = %+v, want a PC that belongs to no account", after)
	}
	// What it shares is untouched: the LAN needs no account.
	if len(after.Apps) != 1 {
		t.Errorf("apps = %v, want the share to survive signing out", after.appNames())
	}
	reloaded, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.enrolled() || len(reloaded.Apps) != 1 {
		t.Errorf("reloaded state = %+v, want signed out with its share", reloaded)
	}
}

// Moving a PC between accounts is a sign out and a sign in, in that order. Doing it in one
// step would be a way to re-bind a PC that is already somebody's.
func TestSigningInIsRefusedWhileThePCBelongsToAnAccount(t *testing.T) {
	ag, _ := enrolAgent(t)
	cp := &fakeControlPlane{t: t}
	srv := cp.server()
	if _, err := ag.enrol(t.Context(), srv.URL, "a-token"); err != nil {
		t.Fatal(err)
	}

	if _, err := ag.startLogin(t.Context(), srv.URL); err == nil {
		t.Fatal("signing in again was allowed")
	}
	if polls, _, _ := cp.counted(); polls != 0 {
		t.Errorf("polled %d times, want none", polls)
	}
}

// The settings page is the other way in, and it is the same flow: the page shows the code,
// the person approves it somewhere else, and the page asks how it went.
func TestTheSettingsPageSignsThePCInAndOut(t *testing.T) {
	ag, srvSettings, _, token := settingsFor(t)
	cp := &fakeControlPlane{t: t}
	server := cp.server()

	status, body := ask(t, srvSettings, http.MethodPost, "/v1/account/login", token,
		`{"server":"`+server.URL+`"}`)
	if status != http.StatusOK {
		t.Fatalf("login answered %d: %s", status, body)
	}
	var acc account
	if err := json.Unmarshal([]byte(body), &acc); err != nil {
		t.Fatal(err)
	}
	if !acc.Login.Waiting || acc.Login.Code == "" {
		t.Fatalf("login = %+v, want a code to approve", acc.Login)
	}

	cp.answers("approved")
	waitFor(t, loginWait, func() bool {
		_, body := ask(t, srvSettings, http.MethodGet, "/v1/account", token, "")
		return strings.Contains(body, `"enrolled":true`)
	}, "the PC was never signed in")

	status, body = ask(t, srvSettings, http.MethodPost, "/v1/account/logout", token, `{}`)
	if status != http.StatusOK {
		t.Fatalf("logout answered %d: %s", status, body)
	}
	if strings.Contains(body, `"enrolled":true`) {
		t.Errorf("account after signing out = %s", body)
	}
	if ag.snapshot().enrolled() {
		t.Error("the agent still thinks it belongs to an account")
	}
}

// The page is one file with the account section in it, and a template that will not
// execute is a blank page nobody can share a folder from either.
func TestTheSettingsPageRendersTheAccountSection(t *testing.T) {
	_, srv, _, token := settingsFor(t)

	status, body := ask(t, srv, http.MethodGet, "/?t="+token, "", "")
	if status != http.StatusOK {
		t.Fatalf("the page answered %d", status)
	}
	for _, want := range []string{`id="signin"`, `id="signout"`, `/v1/account/login`, `/v1/account/logout`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page has no %s in it", want)
		}
	}
}
