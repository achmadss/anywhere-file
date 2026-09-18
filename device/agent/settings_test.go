package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// settingsFor puts the endpoint and the gateway in front of one agent, which is what
// `agent run` does. The gateway is here because the point of a change is that it reaches
// the routes without the agent being restarted.
func settingsFor(t *testing.T) (*agent, *httptest.Server, *gateway, string) {
	t.Helper()
	dir := t.TempDir()
	ag := newAgent(dir, testKey(t), &state{Name: "pc1", Apps: []app{}}, discard)
	gw := newGateway(ag)
	ag.onApps = func([]app) { gw.rebuild() }
	token, err := settingsToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newSettings(ag, token))
	t.Cleanup(srv.Close)
	return ag, srv, gw, token
}

func ask(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(answer)
}

// Loopback is not an authorization: every account on a shared PC can reach 127.0.0.1, and
// this endpoint decides what the whole machine shares.
func TestTheSettingsEndpointRefusesWithoutTheToken(t *testing.T) {
	_, srv, _, token := settingsFor(t)

	for _, c := range []struct{ what, token string }{
		{"no token", ""},
		{"the wrong token", strings.Repeat("a", len(token))},
	} {
		if status, _ := ask(t, srv, http.MethodGet, "/v1/apps", c.token, ""); status != http.StatusUnauthorized {
			t.Errorf("%s: answered %d, want 401", c.what, status)
		}
	}
	if status, _ := ask(t, srv, http.MethodGet, "/v1/apps", token, ""); status != http.StatusOK {
		t.Errorf("the right token answered %d, want 200", status)
	}
}

// A name in somebody else's DNS that resolves to 127.0.0.1 is how a page in the browser
// reaches a loopback port it is not supposed to know about.
func TestTheSettingsEndpointRefusesAnotherName(t *testing.T) {
	_, srv, _, token := settingsFor(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/apps", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = "settings.example.com"
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("answered %d under another name, want 404", resp.StatusCode)
	}
}

// The token is read by the CLI and by nothing else on the machine.
func TestTheSettingsTokenIsReadableOnlyByItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes on Windows are not the POSIX ones the agent asks for")
	}
	dir := t.TempDir()
	token, err := settingsToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, settingsTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the token file is %04o, want 0600", mode)
	}
	// Asked twice, the same token: a new one on every start would break the entry in the
	// menu and every terminal already holding it.
	again, err := settingsToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != token {
		t.Errorf("the token changed on the second read")
	}
}

// The whole point: a folder chosen here is served without the agent being restarted, and
// one removed stops being served.
func TestASharedFolderReachesTheGatewayWithoutARestart(t *testing.T) {
	ag, srv, gw, token := settingsFor(t)
	shared := t.TempDir()

	status, body := ask(t, srv, http.MethodPost, "/v1/apps", token,
		`{"path":`+quote(shared)+`}`)
	if status != http.StatusOK {
		t.Fatalf("sharing answered %d: %s", status, body)
	}
	var added share
	if err := json.Unmarshal([]byte(body), &added); err != nil {
		t.Fatal(err)
	}
	if added.Path != shared {
		t.Errorf("shared %q, want %q", added.Path, shared)
	}

	// The gateway knows the route now. Nothing is listening behind it in a test, so the
	// answer is the gateway's own 502 and never a 404, which is what an unknown name gets.
	if status := gatewayStatus(t, gw, "/"+added.Name+"/"); status == http.StatusNotFound {
		t.Errorf("the gateway answers 404 for %s, so the route was not rebuilt", added.Name)
	}
	if names := ag.snapshot().appNames(); !slices.Contains(names, added.Name) {
		t.Errorf("the registry holds %v, want %s in it", names, added.Name)
	}

	status, body = ask(t, srv, http.MethodDelete, "/v1/apps/"+added.Name, token, "")
	if status != http.StatusOK {
		t.Fatalf("removing answered %d: %s", status, body)
	}
	if status := gatewayStatus(t, gw, "/"+added.Name+"/"); status != http.StatusNotFound {
		t.Errorf("the gateway answers %d for %s after it was removed, want 404", status, added.Name)
	}
	// A PC with nothing shared is a working PC: it still announces itself and offers
	// nothing.
	if apps := ag.snapshot().Apps; len(apps) != 0 {
		t.Errorf("the registry holds %v, want it empty", apps)
	}
	// And what is on disk agrees, so a restart does not bring the share back.
	saved, err := loadState(ag.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Apps) != 0 {
		t.Errorf("%s holds %v, want it empty", stateFileName, saved.Apps)
	}
}

func gatewayStatus(t *testing.T, gw *gateway, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

// The entry the agent writes is the one the registry documents, so what a person chose in
// a browser and what somebody else wrote by hand are the same thing.
func TestSharingADirectoryWritesAWorkingEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Holiday Photos")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := newShare(dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "holiday-photos" {
		t.Errorf("called it %q, want holiday-photos", a.Name)
	}
	if a.Type != "http" {
		t.Errorf("type %q, want http", a.Type)
	}
	if !strings.HasPrefix(a.Address, "127.0.0.1:") {
		t.Errorf("address %q, want one on loopback", a.Address)
	}
	// The port appears in the command the application listens with and in the address the
	// gateway dials, and the two have to be the same one.
	_, port, _ := strings.Cut(a.Address, ":")
	if i := slices.Index(a.Command, "--port"); i < 0 || a.Command[i+1] != port {
		t.Errorf("the command is %v, want --port %s in it", a.Command, port)
	}
	if i := slices.Index(a.Command, "--path-prefix"); i < 0 || a.Command[i+1] != "/"+a.Name {
		t.Errorf("the command is %v, want --path-prefix /%s in it", a.Command, a.Name)
	}
	// validate is what the agent applies to a hand-edited file, and this has to pass it.
	st := &state{Name: "pc1", Apps: []app{a}}
	if err := st.validate(); err != nil {
		t.Errorf("the entry the agent wrote is one it would refuse to read: %v", err)
	}

	// A second folder of the same name gets its own, because the name is a route.
	second, err := newShare(dir, "", []app{a})
	if err != nil {
		t.Fatal(err)
	}
	if second.Name == a.Name {
		t.Errorf("both are called %s, and the gateway routes by name", a.Name)
	}
	if _, err := newShare(dir, a.Name, []app{a}); err == nil {
		t.Errorf("asking for a name that is taken was accepted")
	}
	if _, err := newShare(filepath.Join(dir, "no-such-folder"), "", nil); err == nil {
		t.Errorf("sharing a folder that is not there was accepted")
	}
}

// Browsing is how a folder is chosen, because a browser cannot hand a server a real path.
func TestBrowsingListsDirectoriesOnly(t *testing.T) {
	_, srv, _, token := settingsFor(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pictures"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, body := ask(t, srv, http.MethodGet, "/v1/browse?path="+dir, token, "")
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}
	var out struct {
		Path   string   `json:"path"`
		Parent string   `json:"parent"`
		Dirs   []string `json:"dirs"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Dirs, []string{"pictures"}) {
		t.Errorf("listed %v, want only the directory", out.Dirs)
	}
	if out.Parent == "" || out.Parent == out.Path {
		t.Errorf("parent of %q is %q, so there is no way back up", out.Path, out.Parent)
	}
}

// With the agent stopped nothing holds the registry, so the command writes the file. This
// is the headless path: ssh in, share a folder, start the agent.
func TestTheCommandWritesTheRegistryWhenTheAgentIsStopped(t *testing.T) {
	cfg := config{dir: t.TempDir(), settings: "127.0.0.1:1"}
	shared := t.TempDir()

	added, err := addShareTo(t.Context(), cfg, shared, "files")
	if err != nil {
		t.Fatal(err)
	}
	if added.Name != "files" || added.Path != shared {
		t.Errorf("shared %+v, want files at %s", added, shared)
	}
	st, err := loadState(cfg.dir)
	if err != nil {
		t.Fatal(err)
	}
	if names := st.appNames(); !slices.Equal(names, []string{"files"}) {
		t.Fatalf("%s holds %v, want files", stateFileName, names)
	}

	list, err := currentShares(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Path != shared {
		t.Errorf("listed %+v, want the one folder", list)
	}

	if err := removeShareFrom(t.Context(), cfg, "files"); err != nil {
		t.Fatal(err)
	}
	if st, err := loadState(cfg.dir); err != nil || len(st.Apps) != 0 {
		t.Errorf("after removing it: %v, %v", st, err)
	}
	if err := removeShareFrom(t.Context(), cfg, "files"); err == nil {
		t.Errorf("removing what is not shared was accepted")
	}
}

// The endpoint changes what the whole PC shares, so an address the LAN could reach is
// refused where it is read rather than left to be noticed later.
func TestASettingsAddressOffLoopbackIsRefused(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7434", "192.168.1.10:7434", ":7434", "7434"} {
		if err := checkLoopback(addr); err == nil {
			t.Errorf("%s was accepted", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:7434", "localhost:7434", "[::1]:7434"} {
		if err := checkLoopback(addr); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
}

func quote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// `share add ~/Shared --name files` is the order it reads in, and the flag package stops
// at the first argument that is not an option.
func TestTheNameCanComeAfterTheDirectory(t *testing.T) {
	cfg := config{dir: t.TempDir(), settings: "127.0.0.1:1"}
	shared := t.TempDir()

	var out strings.Builder
	if err := shareCommand(t.Context(), cfg, &out, []string{"add", shared, "--name", "files"}); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(cfg.dir)
	if err != nil {
		t.Fatal(err)
	}
	if names := st.appNames(); !slices.Equal(names, []string{"files"}) {
		t.Errorf("shared %v, want files", names)
	}
}
