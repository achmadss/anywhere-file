package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strings"
)

// mailer delivers the links that signup and password reset mint. With RFM_SMTP_ADDR unset
// it writes them to the log instead, which is what local development and the tests run on.
//
// The settings are read here rather than in config.go for the same reason insecureCookies
// is read in auth.go: the handlers take a database and a logger, not a config.
//
//	RFM_SMTP_ADDR      host:port of the SMTP server. Empty means log the message.
//	RFM_SMTP_USER      username, when the server wants one
//	RFM_SMTP_PASSWORD  password for that username
//	RFM_MAIL_FROM      the From address
//	RFM_BASE_URL       the address links point at, when a load balancer hides the real one
type mailer struct {
	addr string
	from string
	auth smtp.Auth
	log  *slog.Logger
}

func newMailer(log *slog.Logger) *mailer {
	m := &mailer{
		addr: os.Getenv("RFM_SMTP_ADDR"),
		from: env("RFM_MAIL_FROM", "no-reply@localhost"),
		log:  log,
	}
	if user := os.Getenv("RFM_SMTP_USER"); user != "" {
		host, _, err := net.SplitHostPort(m.addr)
		if err != nil {
			host = m.addr
		}
		m.auth = smtp.PlainAuth("", user, os.Getenv("RFM_SMTP_PASSWORD"), host)
	}
	return m
}

// send delivers one message. A failure is a log line and no more: the account exists by
// the time this runs, and an answer that changed when the mail failed would tell the
// caller whether the address was already known.
//
// ponytail: this dials SMTP inside the request, so a slow server holds signup open for as
// long as it takes. Put it behind a queue if that shows up in the latency.
func (m *mailer) send(to, subject, body string) {
	// validEmail already refuses control characters. This is the boundary where one
	// would become a header, so it is checked again here.
	if strings.ContainsAny(to, "\r\n") {
		m.log.Error("refusing to mail an address with a line break in it")
		return
	}
	if m.addr == "" {
		m.log.Info("no mailer configured, so the message is here instead",
			"to", to, "subject", subject, "body", body)
		return
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		m.from, to, subject, body)
	if err := smtp.SendMail(m.addr, m.auth, m.from, []string{to}, []byte(msg)); err != nil {
		m.log.Error("send mail", "to", to, "subject", subject, "error", err)
	}
}

// linkBase is the address the links in a message point at. RFM_BASE_URL wins, because
// behind a load balancer the request carries an internal address and nobody can click it.
// Without the variable the request is right, which covers a local run.
func linkBase(r *http.Request) string {
	if v := os.Getenv("RFM_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
