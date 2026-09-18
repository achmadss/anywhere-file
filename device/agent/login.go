package main

// Signing a PC in and out (#141, ADR 0006). The password never reaches the agent. The
// browser signs the person in, the device key proves which PC is asking, and the two meet
// at a code the person approves.
//
// This is the agent's end of #139. Ask for a code, show it, poll until someone answers,
// then enrol with the token that comes back. A refusal or an expiry is an answer, so the
// polling stops on either.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	// loginPollEvery is used only if the server names no interval. It says how often to
	// ask, and the server's own answer wins.
	loginPollEvery = 5 * time.Second
	// loginMaxWait is how long a sign in started from the settings page keeps polling
	// after the request that started it has been answered. The code expires first.
	loginMaxWait = 15 * time.Minute
)

// login is a sign in that is still happening, or what the last one left behind. The code
// and the URL are what the person needs to approve it somewhere else.
type login struct {
	Code    string `json:"code,omitempty"`
	URL     string `json:"url,omitempty"`
	Waiting bool   `json:"waiting"`
	Error   string `json:"error,omitempty"`
}

// account is what this PC belongs to. There is no name or email in it, because the agent
// is never told either: it holds a device key and the address of a server.
type account struct {
	Server   string `json:"server,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Enrolled bool   `json:"enrolled"`
	Login    login  `json:"login"`
}

func (a *agent) account() account {
	st := a.snapshot()
	a.mu.Lock()
	defer a.mu.Unlock()
	return account{Server: st.Server, DeviceID: st.DeviceID, Enrolled: st.enrolled(), Login: a.login}
}

// startLogin asks the server for a code and leaves a goroutine waiting on the answer. It
// returns as soon as there is a code to show, because the person has to go and approve it
// before anything else happens. The waiting outlives ctx: whoever pressed the button is
// not going to hold a request open while they walk to another machine.
func (a *agent) startLogin(ctx context.Context, server string) (account, error) {
	server, err := cleanServerURL(server)
	if err != nil {
		return account{}, err
	}
	if st := a.snapshot(); st.enrolled() {
		// Moving a PC from one account to another is a sign out and a sign in, so the
		// person doing it says which way round it goes.
		return account{}, fmt.Errorf("this PC already belongs to the account at %s, so sign it out first", st.Server)
	}
	a.mu.Lock()
	waiting := a.login.Waiting
	a.mu.Unlock()
	// Asking twice would mint a second code and leave the first one hanging, and the
	// person is already looking at the first one.
	if waiting {
		return a.account(), nil
	}

	body, err := json.Marshal(map[string]string{"name": a.snapshot().Name})
	if err != nil {
		return account{}, err
	}
	var out struct {
		UserCode        string `json:"user_code"`
		ApproveURL      string `json:"approve_url"`
		IntervalSeconds int    `json:"interval_seconds"`
	}
	start, cancel := context.WithTimeout(ctx, enrolTimeout)
	defer cancel()
	if err := a.post(start, server, "/v1/devices/enrolment/start", body, &out); err != nil {
		return account{}, err
	}
	if out.UserCode == "" {
		return account{}, fmt.Errorf("the server gave no code to approve")
	}
	a.mu.Lock()
	a.login = login{Code: out.UserCode, URL: out.ApproveURL, Waiting: true}
	a.mu.Unlock()

	every := time.Duration(out.IntervalSeconds) * time.Second
	if every <= 0 {
		every = loginPollEvery
	}
	wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginMaxWait)
	go func() {
		defer cancel()
		a.awaitApproval(wait, server, out.UserCode, every)
	}()
	a.log.Info("waiting for this PC to be approved", "server", server, "approve", out.ApproveURL)
	return a.account(), nil
}

// awaitApproval records how the sign in ended. A finished one leaves nothing behind, so
// the settings page has nothing to show; a refused one leaves what to tell the person.
func (a *agent) awaitApproval(ctx context.Context, server, code string, every time.Duration) {
	err := a.pollUntilAnswered(ctx, server, code, every)
	a.mu.Lock()
	if err == nil {
		a.login = login{}
	} else {
		a.login.Waiting, a.login.Error = false, err.Error()
	}
	a.mu.Unlock()
	if err != nil {
		a.log.Warn("this PC was not signed in", "err", err)
	}
}

// pollUntilAnswered asks until the request is approved, refused or gone. The first ask is
// immediate, because the person may have approved it before the agent finished printing
// the code.
func (a *agent) pollUntilAnswered(ctx context.Context, server, code string, every time.Duration) error {
	body, err := json.Marshal(map[string]string{"user_code": code})
	if err != nil {
		return err
	}
	for {
		var out struct {
			Status          string `json:"status"`
			EnrolmentToken  string `json:"enrolment_token"`
			IntervalSeconds int    `json:"interval_seconds"`
		}
		err := a.post(ctx, server, "/v1/devices/enrolment/poll", body, &out)
		var refused serverError
		switch {
		case errors.As(err, &refused) && answered(refused.status):
			// Refused, expired or already collected. Asking again would get the same
			// answer for as long as anybody let it.
			return errors.New(refused.message)
		case err != nil:
			// Anything else is the network or the server having a moment, and being
			// offline for a while is normal here.
			a.log.Warn("asking whether this PC was approved failed", "err", err)
		case out.Status != "pending":
			_, err := a.enrol(ctx, server, out.EnrolmentToken)
			return err
		}
		if d := time.Duration(out.IntervalSeconds) * time.Second; d > every {
			every = d
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// answered reports whether the server's refusal is the end of it. A rate limit is not: it
// says to ask more slowly, and the interval the server gives is how slowly.
func answered(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusTooManyRequests
}

// logout takes this PC off the account. The request is signed with the device key, which
// is the only thing the agent holds that proves it is this PC, and it is enough: a PC can
// always remove itself. Nothing local is touched until the server has accepted, and the
// LAN goes on working either way.
func (a *agent) logout(ctx context.Context) (*state, error) {
	st := a.snapshot()
	if !st.enrolled() {
		return st, nil
	}
	ctx, cancel := context.WithTimeout(ctx, enrolTimeout)
	defer cancel()
	if err := a.post(ctx, st.Server, "/v1/devices/unenrol", []byte("{}"), nil); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.st.Server, a.st.DeviceID = "", ""
	a.login = login{}
	a.mu.Unlock()
	after := a.snapshot()
	if err := saveState(a.dir, after); err != nil {
		return nil, err
	}
	a.log.Info("signed out", "server", st.Server, "device", st.DeviceID)
	return after, nil
}

// loginCheckEvery is how often the command asks the running agent how the approval went.
// It is a loopback request, and the person is watching the terminal.
const loginCheckEvery = 2 * time.Second

// loginCommand signs this PC in from a terminal, which is the only way on a PC with no
// screen. It goes through the running agent where there is one, for the reason `agent
// share` does: two processes writing agent.json would disagree about what this PC is.
func loginCommand(ctx context.Context, cfg config, log *slog.Logger, out io.Writer, server string) error {
	c, err := dialSettings(ctx, cfg)
	if err != nil {
		return err
	}
	var acc account
	var status func() (account, error)
	if c != nil {
		if err := c.do(ctx, http.MethodPost, "/v1/account/login", map[string]string{"server": server}, &acc); err != nil {
			return err
		}
		status = func() (account, error) {
			var a account
			err := c.do(ctx, http.MethodGet, "/v1/account", nil, &a)
			return a, err
		}
	} else {
		ag, err := openAgent(ctx, cfg, log)
		if err != nil {
			return err
		}
		if acc, err = ag.startLogin(ctx, server); err != nil {
			return err
		}
		status = func() (account, error) { return ag.account(), nil }
	}

	fmt.Fprintf(out, "code:    %s\n", acc.Login.Code)
	fmt.Fprintf(out, "approve: %s\n", acc.Login.URL)
	// A PC with no screen has nothing to open the address with, which is why it is
	// printed first: the code is approved from a phone instead.
	if err := openBrowser(acc.Login.URL); err != nil {
		fmt.Fprintln(out, "nothing here opens a browser, so approve the address above from another device")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(loginCheckEvery):
		}
		acc, err := status()
		switch {
		case err != nil:
			return err
		case acc.Login.Waiting:
			continue
		case acc.Login.Error != "":
			return errors.New(acc.Login.Error)
		}
		fmt.Fprintf(out, "signed in to %s\n", acc.Server)
		fmt.Fprintf(out, "device id: %s\n", acc.DeviceID)
		return nil
	}
}

// logoutCommand takes this PC off its account. What proves it may is the device key, so
// this works with nobody signed in anywhere.
func logoutCommand(ctx context.Context, cfg config, log *slog.Logger, out io.Writer) error {
	c, err := dialSettings(ctx, cfg)
	if err != nil {
		return err
	}
	if c != nil {
		err = c.do(ctx, http.MethodPost, "/v1/account/logout", struct{}{}, nil)
	} else {
		var ag *agent
		if ag, err = openAgent(ctx, cfg, log); err == nil {
			_, err = ag.logout(ctx)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "this PC belongs to no account. It goes on serving the LAN")
	return nil
}
