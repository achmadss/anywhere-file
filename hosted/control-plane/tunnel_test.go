package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/http2"
)

// peer is the agent side of a tunnel: the connection it opened and a channel that closes
// when the server hangs up on it.
type peer struct {
	conn net.Conn
	gone chan struct{}
}

// dialTunnel opens a tunnel the way the agent will. app answers the requests the server
// sends down it; a nil app leaves the connection silent, which is what a wedged agent
// looks like from here.
func dialTunnel(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, app http.Handler) *peer {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/tunnel", nil)
	if err != nil {
		t.Fatal(err)
	}
	devicesig.Sign(req, priv, nil)
	if err := req.Write(conn); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}
	// The server's HTTP/2 preface may already sit in br, so the agent side reads through
	// it rather than from the socket.
	buffered := &bufferedConn{Conn: conn, r: br}

	p := &peer{conn: conn, gone: make(chan struct{})}
	go func() {
		defer close(p.gone)
		if app == nil {
			// No HTTP/2 server here, so nothing ever answers a ping. Read until the
			// server gives up, which is what the caller waits for.
			_, _ = io.Copy(io.Discard, buffered)
			return
		}
		(&http2.Server{}).ServeConn(buffered, &http2.ServeConnOpts{Handler: app})
	}()
	return p
}

// bufferedConn is a connection whose reads start with what a bufio.Reader already took
// off it.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func waitOnline(t *testing.T, reg *tunnelRegistry, deviceID string, want bool) {
	t.Helper()
	for range 200 {
		if reg.online(deviceID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("device online = %v after 2s, want %v", !want, want)
}

// waitAudit waits for the tunnel's own goroutine to write want rows and returns how many
// there are. The connection is registered a moment before its audit row lands.
func waitAudit(t *testing.T, pool *pgxpool.Pool, action string, want int) int {
	t.Helper()
	for range 200 {
		if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1`, action); n >= want {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1`, action)
}

// disconnectReasons waits for want rows and returns their reasons. The server writes them
// from the connection's own goroutine, so a moment after the peer notices the hang-up.
func disconnectReasons(t *testing.T, pool *pgxpool.Pool, want int) []string {
	t.Helper()
	waitAudit(t, pool, ActionTunnelDisconnected, want)
	rows, err := pool.Query(t.Context(),
		`SELECT details->>'reason' FROM audit_events WHERE action = $1 ORDER BY id`, ActionTunnelDisconnected)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func tunnelHandler(t *testing.T, pool *pgxpool.Pool) (http.Handler, *tunnelRegistry, *httptest.Server) {
	t.Helper()
	reg := newTunnelRegistry()
	h := newHandlerWithTunnels(pool, discard, NewMetrics(), reg)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, reg, srv
}

// The point of the whole endpoint: a request the server makes reaches the agent, over a
// connection the agent opened outbound.
func TestTunnelCarriesARequestToItsDevice(t *testing.T) {
	pool := freshDB(t, 4)
	h, reg, srv := tunnelHandler(t, pool)
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)

	dialTunnel(t, srv, priv, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "agent answered "+r.URL.Path)
	}))
	waitOnline(t, reg, deviceID, true)

	req, err := http.NewRequest(http.MethodGet, "http://device/copyparty/files", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := reg.get(deviceID).cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip down the tunnel: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "agent answered /copyparty/files" {
		t.Errorf("tunnel response = %d %q", resp.StatusCode, body)
	}

	if n := waitAudit(t, pool, ActionTunnelConnected, 1); n != 1 {
		t.Errorf("connect audit rows = %d, want 1", n)
	}

	// The client reads the same liveness the routing layer will.
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	rec := doJSON(t, h, http.MethodGet, "/v1/devices", nil, bearer(owner))
	var list struct {
		Devices []struct {
			DeviceID string `json:"device_id"`
			Online   bool   `json:"online"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("device list: status = %d body %s err %v", rec.Code, rec.Body, err)
	}
	if len(list.Devices) != 1 || list.Devices[0].DeviceID != deviceID || !list.Devices[0].Online {
		t.Errorf("device list = %+v, want pc1 online", list.Devices)
	}
}

// A restarted agent dials again while the server still holds the connection its old
// process left behind. The new one wins and the old one is closed, or every request goes
// to a session nobody is listening to.
func TestSecondTunnelReplacesTheFirst(t *testing.T) {
	pool := freshDB(t, 4)
	h, reg, srv := tunnelHandler(t, pool)
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)

	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	first := dialTunnel(t, srv, priv, app)
	waitOnline(t, reg, deviceID, true)
	firstConn := reg.get(deviceID)

	dialTunnel(t, srv, priv, app)
	select {
	case <-first.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the first tunnel was still serving after the second connected")
	}
	// One live tunnel, and it is the second one.
	for range 200 {
		if live := reg.get(deviceID); live != nil && live != firstConn {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	live := reg.get(deviceID)
	if live == nil || live == firstConn {
		t.Fatalf("live tunnel = %v, want the second connection", live)
	}
	reg.mu.Lock()
	n := len(reg.live)
	reg.mu.Unlock()
	if n != 1 {
		t.Errorf("registry holds %d tunnels for one device, want 1", n)
	}
	if got := disconnectReasons(t, pool, 1); len(got) != 1 || got[0] != reasonReplaced {
		t.Errorf("disconnect reasons = %v, want one %q", got, reasonReplaced)
	}
}

// An agent that stops answering keeps its TCP connection open for as long as the network
// lets it. Without the ping the device stays "online" and every request to it hangs.
func TestTunnelWithoutHeartbeatIsRemoved(t *testing.T) {
	pool := freshDB(t, 4)
	h, reg, srv := tunnelHandler(t, pool)
	reg.setPing(50*time.Millisecond, 100*time.Millisecond)
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)

	dialTunnel(t, srv, priv, nil)
	waitOnline(t, reg, deviceID, true)
	waitOnline(t, reg, deviceID, false)

	if got := disconnectReasons(t, pool, 1); len(got) != 1 || got[0] != reasonTimeout {
		t.Errorf("disconnect reasons = %v, want one %q", got, reasonTimeout)
	}
}

func TestTunnelHandshakeRefusals(t *testing.T) {
	pool := freshDB(t, 4)
	h, _, _ := tunnelHandler(t, pool)
	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)

	handshake := func(p ed25519.PrivateKey, sign func(*http.Request, []byte)) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/tunnel", nil)
		if sign == nil {
			sign = func(r *http.Request, b []byte) { devicesig.Sign(r, p, b) }
		}
		sign(req, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := handshake(priv, func(*http.Request, []byte) {}); code != http.StatusUnauthorized {
		t.Errorf("unsigned handshake: status = %d, want %d", code, http.StatusUnauthorized)
	}

	// The nonce is claimed before the device is looked up, so a replay of any signed
	// handshake is refused whatever it asked for.
	stranger := newDeviceKey(t)
	nonce := hex.EncodeToString([]byte("replayed-handshake"))
	replay := func(r *http.Request, b []byte) { devicesig.SignAt(r, stranger, b, nonce, time.Now()) }
	if code := handshake(stranger, replay); code != http.StatusNotFound {
		t.Errorf("unenrolled key: status = %d, want %d", code, http.StatusNotFound)
	}
	if code := handshake(stranger, replay); code != http.StatusUnauthorized {
		t.Errorf("replayed handshake: status = %d, want %d", code, http.StatusUnauthorized)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE devices SET status = 'disabled' WHERE device_id = $1`, deviceID); err != nil {
		t.Fatal(err)
	}
	if code := handshake(priv, nil); code != http.StatusForbidden {
		t.Errorf("disabled device: status = %d, want %d", code, http.StatusForbidden)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1`, ActionTunnelConnected); n != 0 {
		t.Errorf("connect audit rows = %d after refusals, want 0", n)
	}
}
