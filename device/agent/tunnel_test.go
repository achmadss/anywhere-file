package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/achmadss/anywhere-file/internal/devicesig"
	"golang.org/x/net/http2"
)

// tunnelServer is the control plane's side of the tunnel, which is what the agent has to
// speak to: a signed request, an answer of 101, then HTTP/2 the other way round.
type tunnelServer struct {
	t *testing.T

	refuse  int    // status to answer with instead of taking the connection over
	message string // what the refusal says

	conns chan *http2.ClientConn

	mu   sync.Mutex
	keys []string
}

func newTunnelServer(t *testing.T) (*tunnelServer, *httptest.Server) {
	t.Helper()
	ts := &tunnelServer{t: t, conns: make(chan *http2.ClientConn, 4)}
	srv := httptest.NewServer(http.HandlerFunc(ts.open))
	t.Cleanup(srv.Close)
	return ts, srv
}

func (ts *tunnelServer) open(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	key, _, err := devicesig.Verify(r, body)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	ts.mu.Lock()
	ts.keys = append(ts.keys, key)
	ts.mu.Unlock()

	if ts.refuse != 0 {
		writeJSON(w, ts.refuse, map[string]string{"error": ts.message})
		return
	}
	conn, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		ts.t.Errorf("hijack: %v", err)
		return
	}
	if buf.Reader.Buffered() > 0 {
		ts.t.Error("the agent sent something before the answer")
		_ = conn.Close()
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: rfm-tunnel\r\nConnection: Upgrade\r\n\r\n")); err != nil {
		_ = conn.Close()
		return
	}
	cc, err := (&http2.Transport{AllowHTTP: true}).NewClientConn(conn)
	if err != nil {
		ts.t.Errorf("client conn: %v", err)
		_ = conn.Close()
		return
	}
	ts.conns <- cc
	<-r.Context().Done()
}

func (ts *tunnelServer) signedWith() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.keys...)
}

// tunnelAgent is an enrolled agent whose tunnel is already running against srv.
func tunnelAgent(t *testing.T, srv *httptest.Server, apps ...app) *agent {
	t.Helper()
	ag := newAgent(agentDir(t), testKey(t), &state{Name: "pc1", Apps: apps}, discard)
	ag.st.Server, ag.st.DeviceID = srv.URL, ag.key.deviceID()
	go runTunnel(t.Context(), ag, newGateway(ag))
	return ag
}

func waitForTunnel(t *testing.T, ts *tunnelServer) *http2.ClientConn {
	t.Helper()
	select {
	case cc := <-ts.conns:
		return cc
	case <-time.After(10 * time.Second):
		t.Fatal("no tunnel after 10s")
		return nil
	}
}

// down asks the server side for something through the tunnel, the way remote routing
// does: the path is the application's, and the host is the device id.
func down(t *testing.T, cc *http2.ClientConn, ag *agent, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ag.key.deviceID()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("request down the tunnel: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestTheServerReachesTheGatewayDownTheTunnel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "the file at "+r.URL.Path)
	}))
	t.Cleanup(backend.Close)

	ts, srv := newTunnelServer(t)
	ag := tunnelAgent(t, srv, app{Name: "dufs", Type: "http", Address: hostPort(t, backend.URL)})
	cc := waitForTunnel(t, ts)

	// The handshake is signed with the device key, so the server knows which PC opened it.
	if keys := ts.signedWith(); len(keys) != 1 || keys[0] != ag.key.publicHex() {
		t.Fatalf("handshake signed with %v, want [%s]", keys, ag.key.publicHex())
	}

	resp := down(t, cc, ag, "/dufs/files/a.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "the file at /files/a.txt" {
		t.Errorf("body = %q, want the application's answer at its own root", body)
	}

	// The same gateway, so what the LAN cannot reach the server cannot reach either.
	if resp := down(t, cc, ag, "/nothing-here"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unregistered app: status = %d, want 404", resp.StatusCode)
	}
}

func TestTheTunnelComesBackAfterTheServerDropsIt(t *testing.T) {
	ts, srv := newTunnelServer(t)
	ag := tunnelAgent(t, srv)
	first := waitForTunnel(t, ts)

	// A server restart looks like this from the agent: the connection goes away with no
	// warning and nothing asks the agent to do anything about it.
	_ = first.Close()

	second := waitForTunnel(t, ts)
	resp := down(t, second, ag, discoveryPath)
	var got struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != ag.key.deviceID() {
		t.Errorf("device id = %s, want %s", got.DeviceID, ag.key.deviceID())
	}
}

func TestTheServersRefusalIsWhatTheAgentReports(t *testing.T) {
	ts, srv := newTunnelServer(t)
	ts.refuse, ts.message = http.StatusNotFound, "device not enrolled"

	ag := newAgent(agentDir(t), testKey(t), &state{Name: "pc1"}, discard)
	ag.st.Server, ag.st.DeviceID = srv.URL, ag.key.deviceID()

	connected, err := ag.tunnelOnce(t.Context(), http.NotFoundHandler())
	if connected {
		t.Error("connected = true after a refusal")
	}
	if err == nil || !strings.Contains(err.Error(), "device not enrolled") {
		t.Fatalf("err = %v, want the server's own message", err)
	}
}

func TestAnUnenrolledAgentHasNothingToDial(t *testing.T) {
	ag := newAgent(agentDir(t), testKey(t), &state{Name: "pc1"}, discard)
	_, err := ag.tunnelOnce(t.Context(), http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("err = %v, want a refusal to dial anything", err)
	}
}

func TestASilentConnectionEndsInsteadOfLookingOpen(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	tc := &tunnelConn{Conn: client, r: client, idle: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		_, err := tc.Read(make([]byte, 1))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("err = %v, want a timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read with nothing on the connection never returned")
	}
}
