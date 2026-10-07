package main

// The settings endpoint (#136, ADR 0006). What this PC shares is set from here: by a page
// in the browser, and by `agent share` in a terminal. Both go through the running agent,
// because it is the one holding the registry and building the gateway's routes from it. A
// command that edited agent.json itself would leave the running agent serving the old list,
// and the alternatives to this are a file watcher (a dependency) or a signal (Windows has
// none).
//
// It listens on loopback and nowhere else. Loopback is not on its own an authorization:
// every account on a shared PC can reach 127.0.0.1. So the endpoint checks a token kept in
// a mode 0600 file beside the agent's own state, which is the same protection the device
// key seed already has.

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/achmadss/anywhere-file/internal/appname"
)

const (
	settingsTokenFile = "settings.token"
	// maxBrowseEntries caps one directory listing. A home directory with a hundred
	// thousand entries in it should not be turned into a hundred thousand lines of HTML.
	maxBrowseEntries = 500
)

//go:embed settings.html
var settingsFiles embed.FS

var settingsPage = template.Must(template.ParseFS(settingsFiles, "settings.html"))

// settingsToken reads the endpoint's token, writing one the first time. Mode 0600 is what
// keeps another account on this PC from reading it, and the directory is already 0700.
func settingsToken(dir string) (string, error) {
	path := filepath.Join(dir, settingsTokenFile)
	switch raw, err := os.ReadFile(path); {
	case err == nil && strings.TrimSpace(string(raw)) != "":
		return strings.TrimSpace(string(raw)), nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// newSettings builds the endpoint. The page and the CLI use the same routes, so what a
// person does in a browser and what they do over ssh end in the same state.
func newSettings(ag *agent, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", servePage(ag.log, token, func() map[string]any {
		return map[string]any{"Device": ag.snapshot().Name, "Fingerprint": ag.key.fingerprint()}
	}))
	mux.HandleFunc("GET /v1/keystore", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"locked": false})
	})
	mux.HandleFunc("GET /v1/apps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"apps": shares(ag.snapshot().Apps)})
	})
	mux.HandleFunc("POST /v1/apps", addShare(ag))
	mux.HandleFunc("DELETE /v1/apps/{name}", removeShare(ag))
	mux.HandleFunc("GET /v1/browse", browse)
	mux.HandleFunc("GET /v1/network", networkStatus)
	mux.HandleFunc("GET /v1/version", versionStatus(ag))
	mux.HandleFunc("GET /v1/account", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ag.account())
	})
	mux.HandleFunc("POST /v1/account/login", signIn(ag))
	mux.HandleFunc("POST /v1/account/logout", signOut(ag))
	mux.HandleFunc("POST /v1/error-reports", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := ag.setErrorReports(in.On); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ag.account())
	})
	return settingsGuard(mux, token, ag.log)
}

// settingsGuard is the whole of the endpoint's authorization. The listener keeps the LAN
// out, the token keeps out every other account on this PC, and the Host check keeps out a
// name in someone else's DNS that resolves to 127.0.0.1, which is how a page in the
// browser would otherwise reach a loopback port.
func settingsGuard(mux http.Handler, token string, log *slog.Logger) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The token is in the address bar of the page the menu entry opens, so nothing
		// this endpoint answers may be sent anywhere that would carry it in a Referer.
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !loopbackHost(r.Host) {
			log.Warn("the settings endpoint was asked for under another name", "host", r.Host)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		got := []byte(bearerToken(r))
		if len(got) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "the settings token is missing or wrong. Open this page from the menu entry, or run `agent settings`",
			})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// bearerToken takes the token from the header the page's own requests send, or from the
// query, which is the only place the entry that opens a browser can put it.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("t")
}

func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func servePage(log *slog.Logger, token string, data func() map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		home, _ := os.UserHomeDir()
		page := data()
		// The token is rendered into the page, so its own requests carry it in a header.
		// Another site cannot read it and cannot set that header, which is what keeps a
		// page in the same browser from re-sharing this PC's disk.
		page["Token"], page["Home"], page["Version"] = token, home, version
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := settingsPage.Execute(w, page); err != nil {
			log.Error("the settings page did not render", "err", err)
		}
	}
}

// settingsSwitch is the endpoint from before the device key loads (#225). Until ready is
// called it answers that the key store is locked, names the store and says what unlocks
// it, and every route that needs the key answers an error. A locked store is a wait and
// never a new key, so nothing here touches the store.
func settingsSwitch(store seedStore, token string, log *slog.Logger) (h http.Handler, ready func(*agent)) {
	name, unlock := keystoreHelp(store)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", servePage(log, token, func() map[string]any {
		return map[string]any{"Device": defaultDeviceName(), "Locked": map[string]string{"Store": name, "Unlock": unlock}}
	}))
	mux.HandleFunc("GET /v1/keystore", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"locked": true, "store": name, "unlock": unlock})
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "this PC's key is in " + name + ", which is locked. " + unlock,
		})
	})
	var current atomic.Pointer[http.Handler]
	locked := settingsGuard(mux, token, log)
	current.Store(&locked)
	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*current.Load()).ServeHTTP(w, r) })
	return h, func(ag *agent) {
		open := newSettings(ag, token)
		current.Store(&open)
	}
}

// keystoreHelp names the store the device key is in and says what unlocks it, in the
// words the settings page shows.
func keystoreHelp(store seedStore) (name, unlock string) {
	if f, ok := store.(fileStore); ok {
		return "the file " + f.path, "Check that the agent's own account can read it, with mode 0600 in a folder with mode 0700."
	}
	switch runtime.GOOS {
	case "darwin":
		return "the macOS login keychain", "Unlock the login keychain in Keychain Access, or log out and in again."
	case "windows":
		return "Windows Credential Manager", "Sign in to Windows as the person this agent runs for."
	}
	return "the Secret Service keyring", "Unlock the login keyring in your desktop's passwords app, or log out and in again."
}

// versionStatus says which release this agent is, and which one is current when the server
// this PC knows has a newer one (#190). The agent asks rather than the page, because the
// page could only reach the server with CORS opened for it. A PC that knows no server, or
// a server that does not answer, gets no warning.
func versionStatus(ag *agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]string{"version": version}
		server := ag.snapshot().Server
		if server == "" {
			writeJSON(w, http.StatusOK, out)
			return
		}
		var latest struct {
			Version string `json:"version"`
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, server+"/v1/version", nil)
		if err == nil {
			if resp, err := ag.hc.Do(req); err == nil {
				if resp.StatusCode == http.StatusOK {
					_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<12)).Decode(&latest)
				}
				resp.Body.Close()
			}
		}
		if newer(latest.Version, version) {
			out["latest"] = latest.Version
			out["download"] = server + "/download"
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// newer reports whether release a comes after release b, both as 1.4.0. Anything else,
// such as a build by hand saying dev, is never older or newer than anything.
func newer(a, b string) bool {
	parse := func(v string) []int {
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			return nil
		}
		out := make([]int, 3)
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return nil
			}
			out[i] = n
		}
		return out
	}
	x, y := parse(a), parse(b)
	return x != nil && y != nil && slices.Compare(x, y) > 0
}

// share is one entry as the page and the CLI show it. The directory is what the person
// chose, so it is read back out of the command rather than stored twice.
type share struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Path    string `json:"path,omitempty"`
}

func shares(apps []app) []share {
	out := make([]share, 0, len(apps))
	for _, a := range apps {
		out = append(out, share{Name: a.Name, Address: a.Address, Path: sharedPath(a)})
	}
	return out
}

// sharedPath is the directory a dufs entry serves. An entry the agent does not start, or
// one written by hand around a different program, has none to report.
func sharedPath(a app) string {
	if len(a.Command) >= 2 && filepath.Base(a.Command[0]) == dufsProgram {
		return a.Command[1]
	}
	return ""
}

func addShare(ag *agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		apps := ag.snapshot().Apps
		a, err := newShare(in.Path, in.Name, apps)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := ag.setApps(append(slices.Clone(apps), a)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// The address and not the name or the path, which are folder names (ADR 0007).
		ag.log.Info("sharing a directory", "address", a.Address)
		writeJSON(w, http.StatusOK, share{Name: a.Name, Address: a.Address, Path: sharedPath(a)})
	}
}

func removeShare(ag *agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		apps := ag.snapshot().Apps
		i := slices.IndexFunc(apps, func(a app) bool { return a.Name == name })
		if i < 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this PC does not share " + name})
			return
		}
		kept := slices.Delete(slices.Clone(apps), i, i+1)
		if err := ag.setApps(kept); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ag.log.Info("stopped sharing", "address", apps[i].Address)
		writeJSON(w, http.StatusOK, map[string]any{"apps": shares(kept)})
	}
}

// signIn starts the browser approval (#141) and answers with the code to approve. The
// waiting happens in the agent, so the page asks /v1/account how it went rather than
// holding a request open for as long as the person takes.
func signIn(ag *agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Server string `json:"server"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(in.Server) == "" {
			in.Server = ag.snapshot().Server
		}
		acc, err := ag.startLogin(r.Context(), in.Server)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, acc)
	}
}

// signOut takes this PC off the account it belongs to. The device key is what proves it
// may, so nobody has to be signed in anywhere for this to work.
func signOut(ag *agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := ag.logout(r.Context()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ag.account())
	}
}

// browse lists the directories inside one directory. The agent walks the filesystem
// because a browser cannot hand a server a real path: a file input gives names and bytes,
// never the place they came from.
func browse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		path = home
	}
	path, err := filepath.Abs(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	dirs := []string{}
	for _, e := range entries {
		if len(dirs) == maxBrowseEntries {
			break
		}
		// A symlink to a directory is a directory to whoever is choosing, so what it
		// points at is asked rather than what the entry itself is.
		if info, err := os.Stat(filepath.Join(path, e.Name())); err == nil && info.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	// ponytail: no drive list on Windows, so browsing starts in the home directory and
	// goes up to the root of that drive. Add one when someone shares off a second disk.
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "parent": parent, "dirs": dirs})
}

// dufsProgram is the file server the agent ships with. It is named rather than found, so
// the registry entry works the same on a PC whose PATH has one and a PC whose does not:
// the supervisor looks beside the agent's own binary.
const dufsProgram = "dufs"

// newShare turns a directory into a registry entry. The port, the bind address and the
// command line are filled in here, so nobody choosing a folder has to see any of them.
func newShare(dir, name string, taken []app) (app, error) {
	if strings.TrimSpace(dir) == "" {
		return app{}, errors.New("no directory")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return app{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return app{}, err
	}
	if !info.IsDir() {
		return app{}, fmt.Errorf("%s is not a directory", dir)
	}
	if name == "" {
		name = shareName(dir, taken)
	}
	if !appname.Valid(name) {
		return app{}, fmt.Errorf("name %q: lowercase letters, digits and hyphens only, up to %d", name, appname.MaxLen)
	}
	if slices.ContainsFunc(taken, func(a app) bool { return a.Name == name }) {
		return app{}, fmt.Errorf("this PC already shares something called %s", name)
	}
	port, err := freePort()
	if err != nil {
		return app{}, err
	}
	return app{
		Name:    name,
		Type:    "http",
		Address: net.JoinHostPort("127.0.0.1", port),
		// The prefix is the name, because the gateway leaves the name on the path and an
		// application that is not told its prefix writes links that land nowhere.
		Command: []string{
			dufsProgram, dir,
			"--bind", "127.0.0.1", "--port", port,
			"--path-prefix", "/" + name,
			"--allow-all",
		},
	}, nil
}

// shareName makes a usable name out of the directory the person chose, so the common case
// takes one click and no typing.
func shareName(dir string, taken []app) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0:
			b.WriteRune('-')
		}
	}
	base := strings.Trim(b.String(), "-")
	if len(base) > appname.MaxLen-3 {
		base = base[:appname.MaxLen-3]
	}
	if base == "" {
		base = "share"
	}
	used := func(n string) bool {
		return slices.ContainsFunc(taken, func(a app) bool { return a.Name == n })
	}
	name := base
	for i := 2; used(name); i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	return name
}

// freePort asks the OS for a port nobody is using and gives it straight back, because the
// program that will hold it takes a number rather than an open socket. Something else can
// take it later, and the supervisor then moves the share to a new one (withFreePort).
func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), nil
}
