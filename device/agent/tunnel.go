package main

// The tunnel client (#95, ADR 0005). A PC behind NAT has no inbound port, so the agent
// dials the server and the server sends requests back down the connection the agent
// opened. The handshake is an ordinary signed request; once the server answers 101 the
// roles swap, and the agent runs an HTTP/2 server on that connection with the same
// gateway that serves the LAN answering on it.
//
// Being offline is a normal state. Every failure here is a wait and another attempt.

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"golang.org/x/net/http2"
)

const (
	tunnelPath             = "/v1/tunnel"
	tunnelHandshakeTimeout = 30 * time.Second
	tunnelBackoffMin       = time.Second
	tunnelBackoffMax       = time.Minute
	// The server pings every 30 seconds. Silence for three of those means the path is
	// gone, however healthy the socket still looks from this end.
	tunnelIdle = 90 * time.Second
)

// runTunnel keeps one connection to the server open for as long as ctx lives.
func runTunnel(ctx context.Context, ag *agent, h http.Handler) {
	wait := tunnelBackoffMin
	for {
		connected, err := ag.tunnelOnce(ctx, h)
		if ctx.Err() != nil {
			return
		}
		if connected {
			wait = tunnelBackoffMin
		}
		retry := jittered(wait)
		ag.log.Warn("tunnel down", "err", err, "retry_in", retry.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		wait = min(wait*2, tunnelBackoffMax)
	}
}

// tunnelOnce opens a tunnel and serves it until it ends. It reports whether the tunnel
// came up, which is what decides between backing off further and starting again.
func (a *agent) tunnelOnce(ctx context.Context, h http.Handler) (bool, error) {
	st := a.snapshot()
	if !st.enrolled() {
		return false, errors.New("this PC is not enrolled, so there is no server to call")
	}
	conn, err := dialServer(ctx, st.Server)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// Serving below returns only when the connection ends, so shutdown has to reach the
	// connection itself.
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()

	rest, err := a.handshake(conn, st.Server)
	if err != nil {
		return false, err
	}
	a.log.Info("tunnel up", "server", st.Server, "device", st.DeviceID)
	(&http2.Server{}).ServeConn(&tunnelConn{Conn: conn, r: rest, idle: tunnelIdle}, &http2.ServeConnOpts{
		Context: ctx,
		Handler: h,
	})
	return true, errors.New("the tunnel closed")
}

// handshake sends the signed request and waits for the server to hand the connection
// over. It returns the reader to serve from, because the server's first HTTP/2 bytes can
// arrive in the same read as its answer.
func (a *agent) handshake(conn net.Conn, server string) (io.Reader, error) {
	if err := conn.SetDeadline(time.Now().Add(tunnelHandshakeTimeout)); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, server+tunnelPath, nil)
	if err != nil {
		return nil, err
	}
	devicesig.Sign(req, a.key.priv, nil)
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("tunnel handshake: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("tunnel handshake: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, serverAnswerError(resp.StatusCode, answer)
	}
	// The deadline was for the handshake. A download can take as long as it takes, and
	// reads get their own deadline below.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return br, nil
}

// tunnelConn is the connection the gateway is served on. Each read has to finish inside
// the idle window, so a connection that has quietly stopped carrying traffic ends here
// instead of looking open forever.
type tunnelConn struct {
	net.Conn
	r    io.Reader
	idle time.Duration
}

func (c *tunnelConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func dialServer(ctx context.Context, server string) (net.Conn, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	addr := u.Host
	if u.Port() == "" {
		port := "443"
		if u.Scheme == "http" {
			port = "80"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	dialer := &net.Dialer{Timeout: tunnelHandshakeTimeout}
	if u.Scheme == "https" {
		return (&tls.Dialer{NetDialer: dialer}).DialContext(ctx, "tcp", addr)
	}
	return dialer.DialContext(ctx, "tcp", addr)
}

// jittered spreads the retries out, so a server coming back does not take every agent it
// dropped in the same instant.
func jittered(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}
