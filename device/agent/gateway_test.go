package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// seenRequest is what the application behind the gateway was actually asked for.
type seenRequest struct {
	method string
	path   string
	query  string
	host   string
	body   string
}

func testKey(t *testing.T) deviceKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return deviceKey{priv}
}

// gatewayWithApp puts one application behind the gateway and reports what it saw.
func gatewayWithApp(t *testing.T, name string, h http.Handler) (*httptest.Server, <-chan seenRequest) {
	t.Helper()
	seen := make(chan seenRequest, 8)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- seenRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Host, string(body)}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)

	st := &state{Name: "pc1", Apps: []app{{Name: name, Type: "http", Address: hostPort(t, backend.URL)}}}
	gw := httptest.NewServer(newGateway(newAgent(t.TempDir(), testKey(t), st, discard)))
	t.Cleanup(gw.Close)
	return gw, seen
}

func hostPort(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func get(t *testing.T, gw *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gw.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gw.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestGatewayForwardsToARegisteredApplication(t *testing.T) {
	gw, seen := gatewayWithApp(t, "dufs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the file")
	}))

	resp := get(t, gw, "/dufs/files/a%20b.txt?dl=1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "the file" {
		t.Errorf("body = %q, want the application's own answer", body)
	}
	// The name stays on the path, because the application is told the prefix it is served
	// under and strips it itself. The query and the escaping survive.
	req := <-seen
	if req.path != "/dufs/files/a b.txt" || req.query != "dl=1" {
		t.Errorf("the application saw %q?%q, want /dufs/files/a b.txt?dl=1", req.path, req.query)
	}
}

// The agent's root is the application's root. The server sends `/{app}` with no trailing
// slash for this case, and a redirect would send the caller somewhere that does not exist.
func TestApplicationRootIsForwarded(t *testing.T) {
	gw, seen := gatewayWithApp(t, "dufs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "index")
	}))
	gw.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp := get(t, gw, "/dufs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 and not a redirect to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if req := <-seen; req.path != "/dufs/" {
		t.Errorf("the application saw %q, want /dufs/", req.path)
	}
}

// The gateway forwards to names it knows and to nothing else. Everything here would be a
// way to reach a host the registry never named.
func TestGatewayRefusesWhatIsNotRegistered(t *testing.T) {
	gw, seen := gatewayWithApp(t, "dufs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for what, path := range map[string]string{
		"an unregistered name":      "/jellyfin/",
		"the root":                  "/",
		"a name that is not a name": "/..%2f..%2fetc%2fpasswd",
		"a nested unknown name":     "/dufs2/files",
	} {
		if resp := get(t, gw, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", what, resp.StatusCode)
		}
	}

	// An absolute request URI is how a caller asks a proxy for another machine. The agent
	// is not a proxy.
	conn, err := net.Dial("tcp", hostPort(t, gw.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET http://example.com/dufs/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	status, err := readStatusLine(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "404") {
		t.Errorf("absolute request URI: %q, want 404", status)
	}

	// A Host header naming another machine decides nothing: the address comes from the
	// registry, and that is where the request goes.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gw.URL+"/dufs/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "nas.example.com"
	resp, err := gw.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("foreign Host header: status = %d, want 200", resp.StatusCode)
	}
	if got := (<-seen).host; got == "nas.example.com" {
		t.Errorf("the application was asked for host %q, want the registered address", got)
	}
}

func TestUnreachableApplicationIsABadGateway(t *testing.T) {
	st := &state{Name: "pc1", Apps: []app{{Name: "dufs", Type: "http", Address: "127.0.0.1:1"}}}
	gw := httptest.NewServer(newGateway(newAgent(t.TempDir(), testKey(t), st, discard)))
	t.Cleanup(gw.Close)
	if resp := get(t, gw, "/dufs/"); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// What a client reads after it finds the agent on the LAN.
func TestDiscoveryDocument(t *testing.T) {
	key := testKey(t)
	st := &state{Name: "pc1", Apps: []app{
		{Name: "dufs", Type: "http", Address: "127.0.0.1:5000"},
		{Name: "jellyfin", Type: "http", Address: "127.0.0.1:8096"},
	}}
	gw := httptest.NewServer(newGateway(newAgent(t.TempDir(), key, st, discard)))
	t.Cleanup(gw.Close)

	resp := get(t, gw, discoveryPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		V        int      `json:"v"`
		DeviceID string   `json:"device_id"`
		Name     string   `json:"name"`
		Apps     []string `json:"apps"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(key.public())
	if doc.DeviceID != hex.EncodeToString(sum[:]) {
		t.Errorf("device_id = %s, want the sha256 of the public key", doc.DeviceID)
	}
	if doc.Name != "pc1" || doc.V != protocolVersion {
		t.Errorf("name = %q, v = %d, want pc1 and %d", doc.Name, doc.V, protocolVersion)
	}
	if strings.Join(doc.Apps, ",") != "dufs,jellyfin" {
		t.Errorf("apps = %v, want dufs and jellyfin", doc.Apps)
	}
	// The loopback address is the one thing that never leaves the PC.
	if strings.Contains(string(body), "5000") {
		t.Error("the discovery document carries an application's address")
	}
}

// A large upload and a large download, which is what this service is for. Nothing is
// buffered: the first chunk of the answer arrives before the last one is written.
func TestGatewayStreamsBothWays(t *testing.T) {
	const size = 16 << 20
	const head = "first chunk"
	second := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(second) }) }
	gw, seen := gatewayWithApp(t, "dufs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A declared length is the ordinary case for a file download, and it is the case
		// a proxy is free to buffer unless it is told not to.
		w.Header().Set("Content-Length", strconv.Itoa(len(head)+size))
		_, _ = w.Write([]byte(head))
		w.(http.Flusher).Flush()
		<-second
		_, _ = io.CopyN(w, zeros{}, size)
	}))

	// Registered after the servers, so it runs before they are closed: a failure below
	// must not leave the test server waiting on a request that can never finish.
	t.Cleanup(release)

	// A deadline on the whole exchange: a gateway that holds the answer until the
	// application has finished writing it never arrives here at all.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gw.URL+"/dufs/up", io.LimitReader(zeros{}, size))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := gw.Client().Do(req)
	if err != nil {
		t.Fatalf("the response never started: %v", err)
	}
	defer resp.Body.Close()

	// Reading the first chunk before the application has written the rest is the proof
	// that nothing waits for a whole response.
	got := make([]byte, len(head))
	read := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, got); read <- err }()
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("reading the first chunk: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first chunk did not arrive while the application was still writing")
	}
	if string(got) != head {
		t.Fatalf("first chunk = %q, want %q", got, head)
	}
	release()

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("reading the rest: %v", err)
	}
	if n != size {
		t.Errorf("downloaded %d bytes, want %d", n, size)
	}
	if got := len((<-seen).body); got != size {
		t.Errorf("the application received %d bytes of upload, want %d", got, size)
	}
}

// zeros is an endless body, so a large transfer costs no memory in the test either.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { return len(p), nil }

func readStatusLine(conn net.Conn) (string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(buf[:n]), "\r\n")
	return line, nil
}
