package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// echoApp answers as the agent's gateway would and reports what it was handed, so a test
// can see exactly what crossed the tunnel.
type seenRequest struct {
	Path      string `json:"path"`
	Query     string `json:"query"`
	Method    string `json:"method"`
	Body      string `json:"body"`
	RequestID string `json:"request_id"`
	Cookie    string `json:"cookie"`
	Auth      string `json:"auth"`
}

func echoApp() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(seenRequest{
			Path:      r.URL.Path,
			Query:     r.URL.RawQuery,
			Method:    r.Method,
			Body:      string(body),
			RequestID: r.Header.Get(headerRequestID),
			Cookie:    r.Header.Get("Cookie"),
			Auth:      r.Header.Get("Authorization"),
		})
	})
}

type remoteSetup struct {
	pool     *pgxpool.Pool
	h        http.Handler
	srv      *httptest.Server
	m        *Metrics
	reg      *tunnelRegistry
	deviceID string
	priv     ed25519.PrivateKey
	session  string
	ownerID  string
}

// remoteScenario builds the state a remote request needs: an enrolled device with the
// caller bound to it as admin, one application, an active subscription and a live tunnel.
// Each test then removes exactly one of those.
func remoteScenario(t *testing.T, app http.Handler) *remoteSetup {
	t.Helper()
	pool := freshDB(t, 6)
	reg := newTunnelRegistry()
	m := NewMetrics()
	h := newHandlerWithTunnels(pool, discard, m, reg)
	// A dead tunnel is noticed in milliseconds here rather than in half a minute.
	reg.setPing(50*time.Millisecond, 200*time.Millisecond)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	priv := enrolKey(t, h, "owner@example.com", "pc1")
	deviceID := derivedDeviceID(priv)
	if rec := syncAppsReq(t, h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("sync apps: status = %d (body %s)", rec.Code, rec.Body)
	}
	dialTunnel(t, srv, priv, app)
	waitOnline(t, reg, deviceID, true)

	return &remoteSetup{
		pool: pool, h: h, srv: srv, m: m, reg: reg,
		deviceID: deviceID, priv: priv,
		session: signinToken(t, h, "owner@example.com", "correct horse battery"),
		ownerID: accountIDByEmail(t, pool, "owner@example.com"),
	}
}

func (s *remoteSetup) get(t *testing.T, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func (s *remoteSetup) cookies() map[string]string {
	// The session as a cookie, next to one the application set for itself.
	return map[string]string{"Cookie": sessionCookie + "=" + s.session + "; app_pref=dark"}
}

// The whole point: a signed-in user reaches an application on their own PC, and the
// application sees the request the user made, minus the credentials that got them here.
func TestRemoteRequestReachesTheApplication(t *testing.T) {
	s := remoteScenario(t, echoApp())

	req := httptest.NewRequest(http.MethodPost, "/d/"+s.deviceID+"/dufs/files/holiday?sort=name", strings.NewReader("hello"))
	req.Header.Set("Cookie", sessionCookie+"="+s.session+"; app_pref=dark")
	req.Header.Set("Authorization", "Bearer "+s.session)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	var got seenRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	if got.Path != "/dufs/files/holiday" {
		t.Errorf("agent saw path %q, want the same shape it serves on the LAN", got.Path)
	}
	if got.Query != "sort=name" || got.Method != http.MethodPost || got.Body != "hello" {
		t.Errorf("agent saw %+v, want the method, query and body the user sent", got)
	}
	if got.RequestID == "" {
		t.Errorf("agent saw no %s header", headerRequestID)
	}
	if strings.Contains(got.Cookie, s.session) || strings.Contains(got.Auth, s.session) {
		t.Errorf("session token reached the agent: cookie %q auth %q", got.Cookie, got.Auth)
	}
	if got.Cookie != "app_pref=dark" {
		t.Errorf("agent saw cookies %q, want only the application's own", got.Cookie)
	}
}

// One test per check in the order #88 sets, each with everything else in place, so a
// passing case proves that step alone refused.
func TestRemoteRequestDenials(t *testing.T) {
	s := remoteScenario(t, echoApp())
	path := "/d/" + s.deviceID + "/dufs/files"

	if code := s.get(t, path, nil).Code; code != http.StatusUnauthorized {
		t.Errorf("no session: status = %d, want 401", code)
	}

	// A signed-in stranger with no binding to this device.
	signupReq(t, s.h, "stranger@example.com", "correct horse battery")
	stranger := signinToken(t, s.h, "stranger@example.com", "correct horse battery")
	if code := s.get(t, path, bearer(stranger)).Code; code != http.StatusNotFound {
		t.Errorf("unbound account: status = %d, want 404", code)
	}

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE device_users SET revoked_at = now() WHERE device_id = $1`, s.deviceID); err != nil {
		t.Fatal(err)
	}
	if code := s.get(t, path, s.cookies()).Code; code != http.StatusNotFound {
		t.Errorf("revoked binding: status = %d, want 404", code)
	}
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE device_users SET revoked_at = NULL WHERE device_id = $1`, s.deviceID); err != nil {
		t.Fatal(err)
	}

	if code := s.get(t, "/d/"+s.deviceID+"/jellyfin/library", s.cookies()).Code; code != http.StatusNotFound {
		t.Errorf("application the device does not offer: status = %d, want 404", code)
	}

	if _, err := s.pool.Exec(t.Context(),
		`UPDATE subscriptions SET status = 'suspended' WHERE account_id = $1::uuid`, s.ownerID); err != nil {
		t.Fatal(err)
	}
	if code := s.get(t, path, s.cookies()).Code; code != http.StatusPaymentRequired {
		t.Errorf("suspended subscription: status = %d, want 402", code)
	}
	if _, err := s.pool.Exec(t.Context(),
		`UPDATE subscriptions SET status = 'active' WHERE account_id = $1::uuid`, s.ownerID); err != nil {
		t.Fatal(err)
	}

	// Offline last: it takes the tunnel away for good. The answer is the same whether the
	// connection died a moment ago and is still mapped or the heartbeat has already
	// removed it, so the connection is killed first and unmapped after.
	live := s.reg.get(s.deviceID)
	live.close()
	if code := s.get(t, path, s.cookies()).Code; code != http.StatusServiceUnavailable {
		t.Errorf("tunnel died mid-request: status = %d, want 503", code)
	}
	s.reg.drop(live)
	if code := s.get(t, path, s.cookies()).Code; code != http.StatusServiceUnavailable {
		t.Errorf("no live tunnel: status = %d, want 503", code)
	}

	body := scrape(t, s.m)
	for _, reason := range []string{denyNoSession, denyNoApp, denySubscription} {
		if want := `rfm_remote_denials_total{reason="` + reason + `"} 1`; !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	for reason, want := range map[string]int{denyNoBinding: 2, denyOffline: 2} {
		if line := `rfm_remote_denials_total{reason="` + reason + `"} ` + strconv.Itoa(want); !strings.Contains(body, line) {
			t.Errorf("metrics missing %q", line)
		}
	}
}

// Changing the device id in the URL is the obvious attack on a shared namespace.
func TestRemoteRequestCannotCrossToAnotherDevice(t *testing.T) {
	s := remoteScenario(t, echoApp())
	// The neighbour's PC offers the same application, so the binding is the only thing
	// between the caller and it.
	priv := enrolKey(t, s.h, "neighbour@example.com", "pc2")
	if rec := syncAppsReq(t, s.h, priv, `{"apps":[{"name":"dufs","type":"http"}]}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("sync apps on pc2: status = %d (body %s)", rec.Code, rec.Body)
	}

	rec := s.get(t, "/d/"+derivedDeviceID(priv)+"/dufs/files", s.cookies())
	if rec.Code != http.StatusNotFound {
		t.Errorf("another user's device: status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

// dufs moves large files, so the body must flow through rather than be collected
// here. This is a smaller stream than the 2 GB of #88, run both ways against a real
// socket; buffering either direction would hold it all at once.
func TestRemoteRequestStreamsBothWays(t *testing.T) {
	const size = 16 << 20
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sum := sha256.New()
			n, err := io.Copy(sum, r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, strconv.FormatInt(n, 10)+" "+hex.EncodeToString(sum.Sum(nil)))
			return
		}
		_, _ = io.CopyN(w, repeating{}, size)
	})
	s := remoteScenario(t, app)

	up, err := http.NewRequest(http.MethodPost, s.srv.URL+"/d/"+s.deviceID+"/dufs/upload", io.LimitReader(repeating{}, size))
	if err != nil {
		t.Fatal(err)
	}
	up.Header.Set("Authorization", "Bearer "+s.session)
	resp, err := s.srv.Client().Do(up)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	sum := sha256.New()
	_, _ = io.CopyN(sum, repeating{}, size)
	want := strconv.Itoa(size) + " " + hex.EncodeToString(sum.Sum(nil))
	if string(got) != want {
		t.Errorf("upload: agent received %q, want %q", got, want)
	}

	down, err := http.NewRequest(http.MethodGet, s.srv.URL+"/d/"+s.deviceID+"/dufs/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	down.Header.Set("Authorization", "Bearer "+s.session)
	resp, err = s.srv.Client().Do(down)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != size {
		t.Errorf("download: %d bytes (err %v), want %d", n, err, size)
	}
}

// repeating is an endless, cheap byte source.
type repeating struct{}

func (repeating) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i)
	}
	return len(p), nil
}
