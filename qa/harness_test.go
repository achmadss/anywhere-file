package qa

// The harness of #40. Everything here is a real process: the control plane, PostgreSQL,
// the agent and dufs. The client is this test binary.
//
// Between the agent and the server sits a switchboard, a TCP proxy this file owns. Cutting
// it is what a PC losing its Internet looks like from both ends, and it takes no container
// and no privileges to do.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// dbEnv both gates the suite and configures it. Without a database there is no control
// plane to run, and a suite that quietly passes with nothing running is worse than none.
const dbEnv = "RFM_E2E_DATABASE_URL"

// The account the client stand-in signs in as.
const password = "correct-horse-battery-staple"

var h *harness

type harness struct {
	dir    string // the agent's own directory, the device key included
	shared string // what dufs serves
	dbURL  string

	binServer string
	binAgent  string

	serverAddr  string // host:port the control plane listens on
	serverURL   string
	gatewayAddr string // host:port the agent's LAN gateway listens on
	dufsAddr    string

	server *proc
	agent  *proc
	wire   *switchboard

	admin    *client
	deviceID string
}

func TestMain(m *testing.M) {
	dbURL := os.Getenv(dbEnv)
	if dbURL == "" {
		fmt.Fprintf(os.Stderr, "%s is not set, so the failure suite has nothing to run against\n", dbEnv)
		return
	}
	code, err := runSuite(m, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M, dbURL string) (int, error) {
	// dufs is the application under test as much as the agent is, and an absent one would
	// otherwise show up as an unexplained timeout.
	if _, err := exec.LookPath("dufs"); err != nil {
		return 0, fmt.Errorf("dufs is not on PATH: %w", err)
	}
	root, err := os.MkdirTemp("", "anywhere-file-e2e")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(root)

	h = &harness{
		dir:    filepath.Join(root, "agent"),
		shared: filepath.Join(root, "shared"),
		dbURL:  dbURL,
	}
	for _, dir := range []string{h.dir, h.shared} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return 0, err
		}
	}
	if err := h.build(root); err != nil {
		return 0, err
	}
	if err := h.ports(); err != nil {
		return 0, err
	}
	if out, err := h.serverCmd("migrate", "up").CombinedOutput(); err != nil {
		return 0, fmt.Errorf("migrate: %v\n%s", err, out)
	}
	if err := h.files(); err != nil {
		return 0, err
	}
	if err := h.startServer(); err != nil {
		return 0, err
	}
	defer func() { h.server.kill() }()

	h.wire, err = newSwitchboard(h.serverAddr)
	if err != nil {
		return 0, err
	}
	defer h.wire.close()

	if err := h.enrol(); err != nil {
		return 0, err
	}
	if err := h.startAgent(); err != nil {
		return 0, err
	}
	defer func() { h.agent.kill(); h.killStrays() }()

	// Nothing below is worth running until one request has been through both paths.
	if err := waitFor(2*time.Minute, "the first request through the tunnel", h.remoteWorks); err != nil {
		return 0, fmt.Errorf("%w\nagent log:\n%s\nserver log:\n%s", err, h.agent.tail(), h.server.tail())
	}
	return m.Run(), nil
}

func (h *harness) build(root string) error {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	// Their own directory: `go build -o` writes inside a path that is already a directory,
	// and the agent's state lives in one called agent.
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return err
	}
	h.binServer = filepath.Join(bin, "control-plane"+suffix)
	h.binAgent = filepath.Join(bin, "agent"+suffix)
	for _, b := range [][2]string{{h.binServer, "../hosted/control-plane"}, {h.binAgent, "../device/agent"}} {
		if out, err := exec.Command("go", "build", "-o", b[0], b[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %v\n%s", b[1], err, out)
		}
	}
	return nil
}

func (h *harness) ports() error {
	for _, target := range []*string{&h.serverAddr, &h.gatewayAddr, &h.dufsAddr} {
		port, err := freePort()
		if err != nil {
			return err
		}
		*target = "127.0.0.1:" + strconv.Itoa(port)
	}
	h.serverURL = "http://" + h.serverAddr
	return nil
}

// files writes the registry and the two files the tests read: one small enough to compare
// by hand, one too large to arrive in a single burst.
func (h *harness) files() error {
	registry := map[string]any{
		"name": "pc1",
		"apps": []map[string]any{{
			"name":    "files",
			"type":    "http",
			"address": h.dufsAddr,
			"command": []string{
				"dufs", h.shared,
				"--bind", "127.0.0.1", "--port", port(h.dufsAddr),
				"--path-prefix", "/files", "--allow-all",
			},
		}},
	}
	body, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(h.dir, "agent.json"), body, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(h.shared, "hello.txt"), []byte(hello), 0o600); err != nil {
		return err
	}
	// Sparse, so 64MB costs nothing to make and still cannot cross the tunnel in one go.
	big, err := os.Create(filepath.Join(h.shared, "big.bin"))
	if err != nil {
		return err
	}
	defer big.Close()
	return big.Truncate(bigSize)
}

const (
	hello   = "the file on the PC\n"
	bigSize = 64 << 20
)

func (h *harness) serverCmd(args ...string) *exec.Cmd {
	cmd := exec.Command(h.binServer, args...)
	cmd.Env = append(os.Environ(),
		"RFM_ADDR="+h.serverAddr,
		"RFM_DATABASE_URL="+h.dbURL,
		// The client here is not a browser and the harness speaks plain HTTP.
		"RFM_INSECURE_COOKIES=1",
		"RFM_SHUTDOWN_TIMEOUT=1s",
	)
	return cmd
}

func (h *harness) startServer() error {
	p, err := start(h.serverCmd("serve"))
	if err != nil {
		return err
	}
	h.server = p
	if err := waitFor(30*time.Second, "the control plane to answer", h.serverUp); err != nil {
		return fmt.Errorf("%w\n%s", err, p.tail())
	}
	return nil
}

func (h *harness) serverUp() bool {
	resp, err := http.Get(h.serverURL + "/healthz")
	if err != nil {
		return false
	}
	defer drain(resp)
	return resp.StatusCode == http.StatusOK
}

// enrol is what the client does: sign up, mint a token, hand it to the agent. The agent is
// not running yet, so the registry it reads at startup is the one enrolment wrote.
func (h *harness) enrol() error {
	admin, err := newAccount(h.serverURL, "admin@example.com")
	if err != nil {
		return err
	}
	h.admin = admin
	var minted struct {
		Token string `json:"token"`
	}
	if code, err := admin.json(http.MethodPost, "/v1/devices/enrolment-token", nil, &minted); err != nil || code != http.StatusOK {
		return fmt.Errorf("enrolment token: status %d, %v", code, err)
	}
	// Through the switchboard, because that is the only route the PC has to the server.
	if out, err := h.agentCmd("enrol", h.wire.url(), minted.Token).CombinedOutput(); err != nil {
		return fmt.Errorf("enrol: %v\n%s", err, out)
	}
	h.deviceID, err = h.readDeviceID()
	return err
}

// readDeviceID asks the agent binary who this PC is, which is the identity the device key
// gives it rather than anything written down.
func (h *harness) readDeviceID() (string, error) {
	out, err := h.agentCmd("key").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("agent key: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`device id:\s+([0-9a-f]{64})`).FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("no device id in:\n%s", out)
	}
	return string(m[1]), nil
}

func (h *harness) agentCmd(args ...string) *exec.Cmd {
	cmd := exec.Command(h.binAgent, args...)
	cmd.Env = append(os.Environ(),
		"RFM_AGENT_DIR="+h.dir,
		"RFM_AGENT_ADDR="+h.gatewayAddr,
		"RFM_AGENT_MDNS=off",
		// Two agents run at once in the duplicate-key case, and a fixed loopback port
		// would be the first one to refuse to start. The suite sets the registry through
		// agent.json, so nothing here needs the endpoint.
		"RFM_AGENT_SETTINGS_ADDR=off",
		// No unlocked keystore on a CI runner, and the seed file is the NAS case anyway.
		"RFM_AGENT_KEYSTORE=file",
	)
	return cmd
}

func (h *harness) startAgent() error {
	p, err := start(h.agentCmd("run"))
	if err != nil {
		return err
	}
	h.agent = p
	if err := waitFor(60*time.Second, "the gateway to serve dufs", h.lanWorks); err != nil {
		return fmt.Errorf("%w\n%s", err, p.tail())
	}
	return nil
}

// killStrays removes a dufs the agent was killed before it could stop. A supervisor that
// is shot leaves its child running, and the next agent cannot have the port.
func (h *harness) killStrays() {
	_ = exec.Command("pkill", "-f", "dufs "+h.shared).Run()
}

// settle puts the world back the way the next test expects to find it.
func (h *harness) settle(t *testing.T) {
	t.Helper()
	h.wire.restore()
	if !h.serverUp() {
		if err := h.startServer(); err != nil {
			t.Fatal(err)
		}
	}
	if !h.agent.alive() {
		h.killStrays()
		if err := h.startAgent(); err != nil {
			t.Fatal(err)
		}
	}
	h.until(t, 2*time.Minute, "the world to come back", h.remoteWorks)
}

// until is waitFor with the logs attached, which is the difference between a timeout and
// a reason.
func (h *harness) until(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	h.untilEvery(t, d, 200*time.Millisecond, what, ok)
}

// untilEvery asks less often, for the endpoints that are rate limited. Sixty a minute is
// what the control plane allows a client, and a poll is a client like any other.
func (h *harness) untilEvery(t *testing.T, d, every time.Duration, what string, ok func() bool) {
	t.Helper()
	if err := waitEvery(d, every, what, ok); err != nil {
		t.Fatalf("%v\nagent log:\n%s\nserver log:\n%s", err, h.agent.tail(), h.server.tail())
	}
}

// The two paths a client has to an application, which is what most of the suite compares.

func (h *harness) remote(c *client, path string) (*http.Response, error) {
	return c.do(http.MethodGet, "/d/"+h.deviceID+"/files"+path, nil)
}

func (h *harness) remoteWorks() bool {
	resp, err := h.remote(h.admin, "/hello.txt")
	if err != nil {
		return false
	}
	defer drain(resp)
	return resp.StatusCode == http.StatusOK
}

func (h *harness) lanURL(path string) string { return "https://" + h.gatewayAddr + "/files" + path }

func (h *harness) lanWorks() bool {
	resp, err := lanClient.Get(h.lanURL("/hello.txt"))
	if err != nil {
		return false
	}
	defer drain(resp)
	return resp.StatusCode == http.StatusOK
}

// lanGet reads a file the way a client on the same network does, over the agent's own
// certificate.
func (h *harness) lanGet(t *testing.T, path string) []byte {
	t.Helper()
	resp, err := lanClient.Get(h.lanURL(path))
	if err != nil {
		t.Fatalf("GET %s on the LAN: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s on the LAN: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s on the LAN: %v", path, err)
	}
	return body
}

// deviceOnline is what the client's device list shows, which is where a PC that is off has
// to stop looking reachable.
func (h *harness) deviceOnline(t *testing.T) bool {
	t.Helper()
	var out struct {
		Devices []struct {
			DeviceID string `json:"device_id"`
			Online   bool   `json:"online"`
		} `json:"devices"`
	}
	if code, err := h.admin.json(http.MethodGet, "/v1/devices", nil, &out); err != nil || code != http.StatusOK {
		t.Fatalf("device list: status %d, %v", code, err)
	}
	for _, d := range out.Devices {
		if d.DeviceID == h.deviceID {
			return d.Online
		}
	}
	t.Fatalf("device %s is not in the list", h.deviceID)
	return false
}

// metric reads one sample out of the control plane's /metrics, so the suite can see what
// the server did to a tunnel rather than guess from the outside.
func (h *harness) metric(t *testing.T, name string) float64 {
	t.Helper()
	resp, err := http.Get(h.serverURL + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	for line := range strings.Lines(string(body)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), name)
		if !ok || !strings.HasPrefix(rest, " ") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			t.Fatalf("metric %s: %v", name, err)
		}
		return v
	}
	// A counter with no sample yet is a counter at zero.
	return 0
}

// The client stand-in.

type client struct {
	base  string
	token string
	hc    *http.Client
}

// lanClient trusts the agent's own certificate without checking it. What makes that
// certificate the right one is the device key, and #127 is where a client checks it.
var lanClient = &http.Client{
	Timeout: 2 * time.Minute,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

func newAccount(base, email string) (*client, error) {
	c := &client{base: base, hc: &http.Client{Timeout: 2 * time.Minute}}
	creds := map[string]string{"email": email, "password": password}
	if code, err := c.json(http.MethodPost, "/v1/auth/signup", creds, nil); err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("signup %s: status %d, %v", email, code, err)
	}
	var out struct {
		Token string `json:"token"`
	}
	if code, err := c.json(http.MethodPost, "/v1/auth/signin", creds, &out); err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("signin %s: status %d, %v", email, code, err)
	}
	c.token = out.Token
	return c, nil
}

func (c *client) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.hc.Do(req)
}

func (c *client) json(method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.do(method, path, body)
	if err != nil {
		return 0, err
	}
	defer drain(resp)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// The switchboard: the wire between the PC and the server.

type switchboard struct {
	target string
	ln     net.Listener

	mu   sync.Mutex
	open bool
	live []net.Conn
}

func newSwitchboard(target string) (*switchboard, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &switchboard{target: target, ln: ln, open: true}
	go s.accept()
	return s, nil
}

func (s *switchboard) url() string { return "http://" + s.ln.Addr().String() }

func (s *switchboard) accept() {
	for {
		down, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		open := s.open
		s.mu.Unlock()
		if !open {
			_ = down.Close()
			continue
		}
		up, err := net.DialTimeout("tcp", s.target, 5*time.Second)
		if err != nil {
			_ = down.Close()
			continue
		}
		s.mu.Lock()
		s.live = append(s.live, down, up)
		s.mu.Unlock()
		go copyBoth(down, up)
	}
}

func copyBoth(a, b net.Conn) {
	done := func() { _ = a.Close(); _ = b.Close() }
	go func() { _, _ = io.Copy(a, b); done() }()
	_, _ = io.Copy(b, a)
	done()
}

// cut is the PC losing its Internet: what is open goes away without a goodbye, and nothing
// new connects. A close rather than a black hole, because a cable pulled out is noticed by
// the machine that pulled it and this suite is about what happens next.
func (s *switchboard) cut() {
	s.mu.Lock()
	s.open = false
	live := s.live
	s.live = nil
	s.mu.Unlock()
	for _, c := range live {
		_ = c.Close()
	}
}

func (s *switchboard) restore() {
	s.mu.Lock()
	s.open = true
	s.mu.Unlock()
}

func (s *switchboard) close() { _ = s.ln.Close() }

// Processes.

type proc struct {
	cmd    *exec.Cmd
	log    *lockedBuffer
	exited chan struct{}
}

func start(cmd *exec.Cmd) (*proc, error) {
	log := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proc{cmd: cmd, log: log, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.exited) }()
	return p, nil
}

func (p *proc) alive() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// stop is a shutdown the process is told about, which is how a service manager stops one.
func (p *proc) stop() {
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		p.kill()
	}
}

// kill is the power cut: no signal it can handle, no chance to tidy up.
func (p *proc) kill() {
	_ = p.cmd.Process.Kill()
	<-p.exited
}

func (p *proc) tail() string {
	const most = 4 << 10
	s := p.log.String()
	if len(s) > most {
		s = s[len(s)-most:]
	}
	return s
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Odds and ends.

func waitFor(d time.Duration, what string, ok func() bool) error {
	return waitEvery(d, 200*time.Millisecond, what, ok)
}

func waitEvery(d, every time.Duration, what string, ok func() bool) error {
	deadline := time.Now().Add(d)
	for {
		if ok() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(every)
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func port(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

// drain lets the connection be reused instead of being thrown away with a body still on it.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
