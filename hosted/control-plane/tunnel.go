package main

// The tunnel endpoint (#87, ADR 0005). A PC behind NAT has no inbound port, so the agent
// dials us and the server sends HTTP down the connection the agent opened. The handshake
// is an ordinary signed agent request. Once it passes, the connection stops being a
// request: the server takes it over and speaks HTTP/2 as the client, with the agent's
// gateway answering on the other end.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/http2"
)

const (
	tunnelLimit = 30
	// A dead connection can stay open at the socket level for minutes, so liveness is a
	// ping the agent has to answer rather than whatever TCP still believes.
	tunnelPingEvery   = 30 * time.Second
	tunnelPingTimeout = 10 * time.Second
)

// Why a tunnel ended, recorded on the disconnect audit row.
const (
	reasonReplaced = "replaced"
	reasonTimeout  = "heartbeat timeout"
	reasonClosed   = "closed"
)

// tunnel is one live agent connection. cc sends requests down it; closing conn ends both.
type tunnel struct {
	deviceID string
	cc       *http2.ClientConn
	conn     net.Conn
}

func (t *tunnel) close() {
	_ = t.cc.Close()
	_ = t.conn.Close()
}

// tunnelRegistry maps a device to its live connection. One device has at most one: a
// second connection replaces the first, so a restarted agent is never shadowed by the
// dead session it left behind.
type tunnelRegistry struct {
	mu   sync.Mutex
	live map[string]*tunnel

	pingEvery   time.Duration
	pingTimeout time.Duration
}

func newTunnelRegistry() *tunnelRegistry {
	return &tunnelRegistry{
		live:        map[string]*tunnel{},
		pingEvery:   tunnelPingEvery,
		pingTimeout: tunnelPingTimeout,
	}
}

// put registers t and returns the connection it displaced, if there was one.
func (reg *tunnelRegistry) put(t *tunnel) *tunnel {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	old := reg.live[t.deviceID]
	reg.live[t.deviceID] = t
	return old
}

// drop removes t if it is still the live tunnel for its device, and reports whether it
// was. A tunnel that was already replaced returns false, so the replacement stands and
// only one disconnect is recorded.
func (reg *tunnelRegistry) drop(t *tunnel) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.live[t.deviceID] != t {
		return false
	}
	delete(reg.live, t.deviceID)
	return true
}

// get returns the live tunnel for a device, or nil when it is offline. Remote request
// routing (#88) answers "device offline" on nil rather than waiting for a connection.
func (reg *tunnelRegistry) get(deviceID string) *tunnel {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.live[deviceID]
}

func (reg *tunnelRegistry) online(deviceID string) bool {
	return reg.get(deviceID) != nil
}

// count is how many devices are reachable right now, which is what the gauge publishes.
func (reg *tunnelRegistry) count() int {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return len(reg.live)
}

// setPing changes the heartbeat, which tests turn down so a dead tunnel is noticed inside
// a test rather than inside half a minute.
func (reg *tunnelRegistry) setPing(every, timeout time.Duration) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.pingEvery, reg.pingTimeout = every, timeout
}

// watch pings the agent until it stops answering, and returns why the tunnel ended.
func (reg *tunnelRegistry) watch(t *tunnel) string {
	for {
		reg.mu.Lock()
		every, timeout := reg.pingEvery, reg.pingTimeout
		reg.mu.Unlock()

		time.Sleep(every)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := t.cc.Ping(ctx)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, context.DeadlineExceeded):
			return reasonTimeout
		default:
			return reasonClosed
		}
	}
}

func registerTunnelRoutes(mux *http.ServeMux, db *pgxpool.Pool, log *slog.Logger, m *Metrics, reg *tunnelRegistry) {
	limiter := newRateLimiter(tunnelLimit, rateWindow)
	mux.Handle("POST /v1/tunnel", limiter.middleware(openTunnel(db, log, m, reg)))
}

// openTunnel authenticates the agent, takes the connection over and holds it. The handler
// returns when the tunnel ends, which is the connection's whole life.
func openTunnel(db *pgxpool.Pool, log *slog.Logger, m *Metrics, reg *tunnelRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		deviceKey, err := verifyAgentSignature(r, db, body)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var deviceID, status string
		err = db.QueryRow(r.Context(),
			`SELECT device_id, status FROM devices WHERE public_key = $1`,
			deviceKey).Scan(&deviceID, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeAuthError(w, http.StatusNotFound, "device not enrolled")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		if status != "active" {
			writeAuthError(w, http.StatusForbidden, "device disabled")
			return
		}

		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "try again later")
			return
		}
		// Anything sent before our answer would be read as the start of HTTP/2 below, so
		// an agent that did not wait gets no tunnel.
		if buf.Reader.Buffered() > 0 {
			_ = conn.Close()
			return
		}
		if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: rfm-tunnel\r\nConnection: Upgrade\r\n\r\n")); err != nil {
			_ = conn.Close()
			return
		}
		cc, err := (&http2.Transport{AllowHTTP: true}).NewClientConn(conn)
		if err != nil {
			_ = conn.Close()
			return
		}

		// The request context ends with this handler, and the audit rows below outlive it.
		ctx := context.WithoutCancel(r.Context())
		t := &tunnel{deviceID: deviceID, cc: cc, conn: conn}
		if old := reg.put(t); old != nil {
			old.close()
			m.RecordTunnelDisconnect(reasonReplaced)
			reg.record(ctx, db, log, old, ActionTunnelDisconnected, reasonReplaced)
		}
		m.RecordTunnelConnect()
		m.SetLiveTunnels(reg.count())
		reg.record(ctx, db, log, t, ActionTunnelConnected, "")

		reason := reg.watch(t)
		t.close()
		if reg.drop(t) {
			m.RecordTunnelDisconnect(reason)
			m.SetLiveTunnels(reg.count())
			reg.record(ctx, db, log, t, ActionTunnelDisconnected, reason)
		}
	}
}

// record writes the audit row for a tunnel event and logs it. A failed write is logged
// and nothing else: the connection is already up or already gone either way.
func (reg *tunnelRegistry) record(ctx context.Context, db execer, log *slog.Logger, t *tunnel, action, reason string) {
	if err := appendAudit(ctx, db, t.deviceID, t.deviceID, action, AuditDetails{Reason: reason}); err != nil {
		log.Error("tunnel audit", "device", t.deviceID, "action", action, "err", err)
	}
	log.Info(action, "device", t.deviceID, "reason", reason)
}
