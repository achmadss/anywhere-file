package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mailLog is a logger and the messages the mailer wrote to it. With no SMTP server
// configured the mailer logs what it would have sent, which is the local and test path
// and is what these tests read the links out of.
type mailLog struct {
	log *slog.Logger
	buf *bytes.Buffer
}

func newMailLog() *mailLog {
	buf := &bytes.Buffer{}
	return &mailLog{log: slog.New(slog.NewJSONHandler(buf, nil)), buf: buf}
}

// link returns the one URL in the last message sent to an address.
func (m *mailLog) link(t *testing.T, to string) string {
	t.Helper()
	found := ""
	for _, line := range strings.Split(m.buf.String(), "\n") {
		var rec struct {
			Msg  string `json:"msg"`
			To   string `json:"to"`
			Body string `json:"body"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.To != to || rec.Body == "" {
			continue
		}
		for _, word := range strings.Fields(rec.Body) {
			if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") {
				found = word
			}
		}
	}
	if found == "" {
		t.Fatalf("no link for %s in the log:\n%s", to, m.buf)
	}
	return found
}

func getPage(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// No database: the pages are templates and a route table.
func TestEveryAccountPageRenders(t *testing.T) {
	mux := http.NewServeMux()
	registerWebRoutes(mux)

	for path, p := range pages {
		rec := getPage(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q, want HTML", path, ct)
		}
		// html/template writes a slash in a JavaScript string as \/, which is the
		// same character to the browser and a different one to strings.Contains.
		body := strings.ReplaceAll(rec.Body.String(), `\/`, "/")
		if !strings.Contains(body, p.Action) {
			t.Errorf("%s: page does not post to %s", path, p.Action)
		}
		for _, f := range p.Fields {
			if !strings.Contains(body, `name="`+f.Name+`"`) {
				t.Errorf("%s: no input named %s", path, f.Name)
			}
		}
	}
}

// The reset and verification links carry a token in the query string. A Referer would
// hand it to whatever the page linked to next.
func TestThePagesSendNoReferrer(t *testing.T) {
	mux := http.NewServeMux()
	registerWebRoutes(mux)

	rec := getPage(t, mux, "/reset/confirm?token=secret")
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// No database: a line break in an address would become a header of its own in the
// message the mailer builds.
func TestAnAddressWithALineBreakIsRefused(t *testing.T) {
	for _, email := range []string{
		"alice\nBcc:evil@example.test",
		"alice\rBcc:evil@example.test",
		"alice\t@example.test",
		"alice @example.test",
	} {
		if validEmail(email) {
			t.Errorf("validEmail(%q) = true, want false", email)
		}
	}
	if !validEmail("alice@example.test") {
		t.Error("validEmail refused an ordinary address")
	}
}

func TestTheMailerRefusesAnAddressWithALineBreak(t *testing.T) {
	m := newMailLog()
	newMailer(m.log).send("alice\nBcc:evil@example.test", "hello", "http://example.test/verify?token=x")
	if strings.Contains(m.buf.String(), "Bcc") {
		t.Errorf("the mailer built a message for an address with a line break:\n%s", m.buf)
	}
}

func TestSignupSendsALinkThatConfirmsTheAddress(t *testing.T) {
	pool := freshDB(t, 4)
	m := newMailLog()
	h := newHandler(pool, m.log, NewMetrics())

	if rec := signupReq(t, h, "jane@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	link, err := url.Parse(m.link(t, "jane@example.test"))
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	if link.Path != "/verify" {
		t.Fatalf("link path = %q, want /verify", link.Path)
	}
	token := link.Query().Get("token")
	if token == "" {
		t.Fatal("the link carries no token")
	}

	// The page the link opens, then what that page posts.
	if rec := getPage(t, h, link.RequestURI()); rec.Code != http.StatusOK {
		t.Fatalf("the verify page: status = %d, want 200", rec.Code)
	}
	rec := doJSON(t, h, http.MethodPost, "/v1/auth/verify", map[string]string{"token": token}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	session := signinToken(t, h, "jane@example.test", "correct-horse-123")
	rec = doJSON(t, h, http.MethodGet, "/v1/me", nil, bearer(session))
	var me struct {
		EmailVerified bool `json:"email_verified"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || !me.EmailVerified {
		t.Errorf("me = %q, want email_verified true", rec.Body)
	}
}

func TestTheResetLinkOpensTheConfirmPage(t *testing.T) {
	pool := freshDB(t, 4)
	m := newMailLog()
	h := newHandler(pool, m.log, NewMetrics())

	if rec := signupReq(t, h, "kim@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	rec := doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/request",
		map[string]string{"email": "kim@example.test"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset request: status = %d (body %s)", rec.Code, rec.Body)
	}

	link, err := url.Parse(m.link(t, "kim@example.test"))
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	if link.Path != "/reset/confirm" {
		t.Fatalf("link path = %q, want /reset/confirm", link.Path)
	}
	if rec := getPage(t, h, link.RequestURI()); rec.Code != http.StatusOK {
		t.Fatalf("the confirm page: status = %d, want 200", rec.Code)
	}

	rec = doJSON(t, h, http.MethodPost, "/v1/auth/password-reset/confirm",
		map[string]string{"token": link.Query().Get("token"), "new_password": "brand-new-horse-9"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if rec := signinReq(t, h, "kim@example.test", "brand-new-horse-9"); rec.Code != http.StatusOK {
		t.Errorf("new password: status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

func TestBaseURLDecidesWhereTheLinksPoint(t *testing.T) {
	t.Setenv("RFM_BASE_URL", "https://anywhere.example/")
	pool := freshDB(t, 4)
	m := newMailLog()
	h := newHandler(pool, m.log, NewMetrics())

	if rec := signupReq(t, h, "lee@example.test", "correct-horse-123"); rec.Code != http.StatusOK {
		t.Fatalf("signup: status = %d (body %s)", rec.Code, rec.Body)
	}
	if got := m.link(t, "lee@example.test"); !strings.HasPrefix(got, "https://anywhere.example/verify?token=") {
		t.Errorf("link = %q, want it under https://anywhere.example", got)
	}
}

// fakeSMTP answers one message and returns what it was given. It speaks the smallest
// dialect net/smtp will talk to: no extensions, so no STARTTLS and no AUTH.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		in := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		say("220 fake ready")
		var data strings.Builder
		inData := false
		for {
			line, err := in.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					say("250 taken")
					got <- data.String()
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				say("250 fake")
			case strings.HasPrefix(line, "DATA"):
				inData = true
				say("354 go ahead")
			case strings.HasPrefix(line, "QUIT"):
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

// No database: the message the mailer builds has to be a message a server accepts.
func TestTheMailerSendsThroughSMTP(t *testing.T) {
	addr, got := fakeSMTP(t)
	t.Setenv("RFM_SMTP_ADDR", addr)
	t.Setenv("RFM_MAIL_FROM", "no-reply@anywhere.example")

	m := newMailLog()
	newMailer(m.log).send("nina@example.test", "Confirm your address",
		"Confirm it here:\n\nhttps://anywhere.example/verify?token=abc")

	select {
	case msg := <-got:
		for _, want := range []string{
			"From: no-reply@anywhere.example",
			"To: nina@example.test",
			"Subject: Confirm your address",
			"https://anywhere.example/verify?token=abc",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("the message has no %q:\n%s", want, msg)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing reached the server. The log says:\n%s", m.buf)
	}
	if strings.Contains(m.buf.String(), "error") {
		t.Errorf("the mailer logged an error:\n%s", m.buf)
	}
}
