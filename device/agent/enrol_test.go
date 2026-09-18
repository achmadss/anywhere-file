package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/achmadss/anywhere-file/internal/devicesig"
)

// fakeControlPlane is the server side, checked the way the real one checks it: the
// signature is verified over the raw body before anything is decoded.
type fakeControlPlane struct {
	t *testing.T

	enrolStatus int
	enrolError  string
	deviceID    string // what to answer with, empty means the one derived from the key

	enrolBody []byte
	appsBody  []byte
	keys      []string

	// The browser half (#141). A test moves answer along, where the real server has a
	// person pressing a button on another machine.
	mu        sync.Mutex
	answer    string // pending, approved or refused
	startBody []byte
	polls     int
	unenrols  []string
}

// approved is what the person in the browser does, from the test's side.
func (f *fakeControlPlane) answers(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = status
}

func (f *fakeControlPlane) counted() (polls int, unenrols []string, start []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls, slices.Clone(f.unenrols), f.startBody
}

func (f *fakeControlPlane) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/devices/enrol", func(w http.ResponseWriter, r *http.Request) {
		body, key := f.read(w, r)
		if body == nil {
			return
		}
		f.enrolBody, f.keys = body, append(f.keys, key)
		if f.enrolStatus != 0 && f.enrolStatus != http.StatusOK {
			writeJSON(w, f.enrolStatus, map[string]string{"error": f.enrolError})
			return
		}
		id := f.deviceID
		if id == "" {
			id = deviceIDFromKey(f.t, key)
		}
		writeJSON(w, http.StatusOK, map[string]string{"device_id": id, "name": "pc1"})
	})
	mux.HandleFunc("POST /v1/devices/apps", func(w http.ResponseWriter, r *http.Request) {
		body, _ := f.read(w, r)
		if body == nil {
			return
		}
		f.appsBody = body
		writeJSON(w, http.StatusOK, map[string]any{"apps": []string{}})
	})
	mux.HandleFunc("POST /v1/devices/enrolment/start", func(w http.ResponseWriter, r *http.Request) {
		body, _ := f.read(w, r)
		if body == nil {
			return
		}
		f.mu.Lock()
		f.startBody = body
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"user_code":   "BCDF-GHJK",
			"approve_url": "https://example.test/approve?code=BCDF-GHJK",
			// A second is what the tests wait, and the agent takes the interval from here.
			"interval_seconds": 1,
		})
	})
	mux.HandleFunc("POST /v1/devices/enrolment/poll", func(w http.ResponseWriter, r *http.Request) {
		body, _ := f.read(w, r)
		if body == nil {
			return
		}
		f.mu.Lock()
		f.polls++
		answer := f.answer
		f.mu.Unlock()
		switch answer {
		case "refused":
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "the request was refused in the browser"})
		case "approved":
			writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "enrolment_token": "a-token"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "interval_seconds": 1})
		}
	})
	mux.HandleFunc("POST /v1/devices/unenrol", func(w http.ResponseWriter, r *http.Request) {
		body, key := f.read(w, r)
		if body == nil {
			return
		}
		f.mu.Lock()
		f.unenrols = append(f.unenrols, key)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	f.t.Cleanup(srv.Close)
	return srv
}

func (f *fakeControlPlane) read(w http.ResponseWriter, r *http.Request) ([]byte, string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return nil, ""
	}
	key, _, err := devicesig.Verify(r, body)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return nil, ""
	}
	return body, key
}

func deviceIDFromKey(t *testing.T, publicHex string) string {
	t.Helper()
	raw, err := hex.DecodeString(publicHex)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func enrolAgent(t *testing.T) (*agent, string) {
	t.Helper()
	dir := agentDir(t)
	st := &state{Name: "pc1", Apps: []app{{Name: "dufs", Type: "http", Address: "127.0.0.1:5000"}}}
	if err := saveState(dir, st); err != nil {
		t.Fatal(err)
	}
	return newAgent(dir, testKey(t), st, discard), dir
}

func TestEnrolStoresWhatTheServerAcceptedAndPushesTheApps(t *testing.T) {
	ag, dir := enrolAgent(t)
	cp := &fakeControlPlane{t: t}
	srv := cp.server()

	st, err := ag.enrol(t.Context(), srv.URL, "a-token")
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if st.Server != srv.URL || st.DeviceID != ag.key.deviceID() {
		t.Errorf("state = %s / %s, want %s / %s", st.Server, st.DeviceID, srv.URL, ag.key.deviceID())
	}

	// The request is signed with the device key and carries the token and the name.
	var sent map[string]string
	if err := json.Unmarshal(cp.enrolBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["enrolment_token"] != "a-token" || sent["public_key"] != ag.key.publicHex() || sent["name"] != "pc1" {
		t.Errorf("enrol body = %v", sent)
	}
	if len(cp.keys) == 0 || cp.keys[0] != ag.key.publicHex() {
		t.Errorf("the request was signed by %v, want the device key", cp.keys)
	}

	// The application list follows in the same run, so the device is not listed empty.
	// It carries the name and the type, and never the address.
	if cp.appsBody == nil {
		t.Fatal("the application list was not pushed")
	}
	if got := string(cp.appsBody); !strings.Contains(got, `"dufs"`) || strings.Contains(got, "5000") {
		t.Errorf("apps body = %s, want the name and no address", got)
	}

	// And it survives a restart, because it is on disk.
	reloaded, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.enrolled() || reloaded.Server != srv.URL {
		t.Errorf("reloaded state = %+v, want an enrolled PC", reloaded)
	}
}

// Enrolling again with a new token re-binds the same key, which is what an account
// transfer or a second admin looks like from here.
func TestEnrolAgainReBindsTheSameKey(t *testing.T) {
	ag, _ := enrolAgent(t)
	cp := &fakeControlPlane{t: t}
	srv := cp.server()

	if _, err := ag.enrol(t.Context(), srv.URL, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.enrol(t.Context(), srv.URL, "second"); err != nil {
		t.Fatalf("second enrolment: %v", err)
	}
	if len(cp.keys) != 2 || cp.keys[0] != cp.keys[1] {
		t.Errorf("the two enrolments used keys %v, want one key twice", cp.keys)
	}
}

// A bad or expired token leaves the PC exactly as it was, and says why.
func TestARefusedEnrolmentChangesNothing(t *testing.T) {
	ag, dir := enrolAgent(t)
	cp := &fakeControlPlane{
		t:           t,
		enrolStatus: http.StatusUnauthorized,
		enrolError:  "enrolment token invalid, expired or used",
	}
	srv := cp.server()

	_, err := ag.enrol(t.Context(), srv.URL, "stale")
	if err == nil {
		t.Fatal("a refused enrolment reported success")
	}
	if !strings.Contains(err.Error(), "enrolment token invalid, expired or used") {
		t.Errorf("error = %v, want the server's own message", err)
	}
	if cp.appsBody != nil {
		t.Error("the application list was pushed after the enrolment was refused")
	}
	if ag.snapshot().enrolled() {
		t.Error("the agent considers itself enrolled")
	}
	reloaded, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.enrolled() {
		t.Error("the refused enrolment was written to disk")
	}
}

// The id is derived from the key we signed with, so it is checked rather than taken.
func TestAnUnexpectedDeviceIDIsRefused(t *testing.T) {
	ag, _ := enrolAgent(t)
	cp := &fakeControlPlane{t: t, deviceID: strings.Repeat("a", 64)}
	srv := cp.server()

	if _, err := ag.enrol(t.Context(), srv.URL, "a-token"); err == nil {
		t.Fatal("an enrolment answering with another device's id was accepted")
	}
	if ag.snapshot().enrolled() {
		t.Error("the agent considers itself enrolled")
	}
}

func TestBadServerAddressesAreRefused(t *testing.T) {
	// Checked without reaching the network, or an address that is merely unreachable
	// would look the same as one that is wrong.
	for what, server := range map[string]string{
		"empty":           "",
		"no scheme":       "cloud.example.com",
		"a file URL":      "file:///etc/passwd",
		"with a path":     "https://cloud.example.com/v1/devices",
		"with a query":    "https://cloud.example.com?token=x",
		"with a fragment": "https://cloud.example.com#x",
		"no host":         "https://",
	} {
		if got, err := cleanServerURL(server); err == nil {
			t.Errorf("%s: %q was accepted as %q, want a refusal", what, server, got)
		}
	}
	// A trailing slash is a paste, not a mistake.
	if got, err := cleanServerURL("https://cloud.example.com/"); err != nil || got != "https://cloud.example.com" {
		t.Errorf("cleanServerURL trimmed to %q (err %v)", got, err)
	}

	ag, _ := enrolAgent(t)
	if _, err := ag.enrol(t.Context(), "not a url", "a-token"); err == nil {
		t.Error("enrol accepted an address that is not a URL")
	}
	if _, err := ag.enrol(t.Context(), "https://cloud.example.com", "   "); err == nil {
		t.Error("enrol accepted an empty token")
	}
}

// The client on the LAN enrols the PC it found, by handing over a server address and a
// token it minted for the signed-in account.
func TestTheLocalEndpointEnrols(t *testing.T) {
	ag, _ := enrolAgent(t)
	cp := &fakeControlPlane{t: t}
	srv := cp.server()
	gw := httptest.NewServer(newGateway(ag))
	t.Cleanup(gw.Close)

	resp := postJSON(t, gw, enrolPath, `{"server":"`+srv.URL+`","enrolment_token":"a-token"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (body %s)", resp.StatusCode, body)
	}
	var out struct {
		DeviceID string   `json:"device_id"`
		Apps     []string `json:"apps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.DeviceID != ag.key.deviceID() || len(out.Apps) != 1 {
		t.Errorf("answer = %+v, want this device and its one application", out)
	}

	// The discovery document says so too, which is how the client knows it worked.
	doc := get(t, gw, discoveryPath)
	var found struct {
		Enrolled bool `json:"enrolled"`
	}
	if err := json.NewDecoder(doc.Body).Decode(&found); err != nil {
		t.Fatal(err)
	}
	if !found.Enrolled {
		t.Error("the discovery document still says this PC is not enrolled")
	}
}

// Whatever the server said is what the person in front of the client reads.
func TestTheLocalEndpointPassesTheRefusalOn(t *testing.T) {
	ag, _ := enrolAgent(t)
	cp := &fakeControlPlane{
		t:           t,
		enrolStatus: http.StatusUnauthorized,
		enrolError:  "enrolment token invalid, expired or used",
	}
	srv := cp.server()
	gw := httptest.NewServer(newGateway(ag))
	t.Cleanup(gw.Close)

	resp := postJSON(t, gw, enrolPath, `{"server":"`+srv.URL+`","enrolment_token":"stale"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "enrolment token invalid, expired or used") {
		t.Errorf("body = %s, want the server's own message", body)
	}

	// A server that is not there at all is a different answer, and still not a crash.
	if resp := postJSON(t, gw, enrolPath, `{"server":"http://127.0.0.1:1","enrolment_token":"x"}`); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("unreachable server: status = %d, want 502", resp.StatusCode)
	}
	if resp := postJSON(t, gw, enrolPath, `not json`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a broken body: status = %d, want 400", resp.StatusCode)
	}
}

func postJSON(t *testing.T, gw *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gw.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := gw.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
