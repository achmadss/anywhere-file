package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for dufs, see fakeDufs.
func TestMain(m *testing.M) {
	if os.Getenv("AGENT_FAKE_DUFS") != "" {
		fakeDufs(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

// fakeDufs prints what dufs 0.46.0 prints, checked against the real one: a banner that
// ends in the prefix, an error naming a folder that is gone, and a line per request
// unless DUFS_LOG_FORMAT is set and empty. It is run under the name dufs, so the agent
// treats it as the shared folder's server it is.
func fakeDufs(args []string) {
	root := args[0]
	fs := flag.NewFlagSet("dufs", flag.ExitOnError)
	bind := fs.String("bind", "", "")
	port := fs.String("port", "", "")
	prefix := fs.String("path-prefix", "", "")
	fs.Bool("allow-all", false, "")
	_ = fs.Parse(args[1:])
	if _, err := os.Stat(root); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Path `%s` doesn't exist\n", root)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(*bind, *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	fmt.Printf("Listening on http://%s%s/\n", ln.Addr(), *prefix)
	files := http.StripPrefix(*prefix, http.FileServer(http.Dir(root)))
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files.ServeHTTP(w, r)
		if v, ok := os.LookupEnv("DUFS_LOG_FORMAT"); !ok || v != "" {
			fmt.Printf("%s INFO - 127.0.0.1 %q 200\n", time.Now().Format(time.RFC3339), r.Method+" "+r.URL.Path)
		}
	}))
}

// ADR 0007: no file or folder name in a log. A share is served through the gateway by a
// dufs that logs what it is asked for, a second share's folder has gone, and an absolute
// request is refused. Every name below has "secret" in it, and the log has none.
func TestServingAShareLogsNoFileOrFolderName(t *testing.T) {
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dufs := filepath.Join(bin, dufsProgram)
	if runtime.GOOS == "windows" {
		dufs += ".exe"
	}
	copyFile(t, exe, dufs)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_FAKE_DUFS", "1")
	shortBackoff(t)

	root := filepath.Join(t.TempDir(), "secret-folder")
	write(t, filepath.Join(root, "secret-sub", "secret-file.txt"), "x")
	served, err := newShare(root, "secret-share", nil)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "secret-gone-folder")
	write(t, filepath.Join(gone, "x"), "x")
	missing, err := newShare(gone, "secret-gone", []app{served})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	log, read := logToBuffer()
	ctx, cancel := context.WithCancel(t.Context())
	wait := supervising(ctx, log, served, missing)
	defer wait()
	defer cancel()
	st := &state{Name: "pc1", Apps: []app{served, missing}}
	gw := httptest.NewServer(newGateway(newAgent(t.TempDir(), testKey(t), st, log)))
	defer gw.Close()

	waitFor(t, 30*time.Second, func() bool {
		resp, err := gw.Client().Get(gw.URL + "/secret-share/secret-sub/secret-file.txt")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "the share was never served")
	for _, path := range []string{"/secret-share/secret-sub/", "/secret-gone/secret-file.txt"} {
		resp, err := gw.Client().Get(gw.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	conn, err := net.Dial("tcp", hostPort(t, gw.URL))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(conn, "GET http://example.com/secret-share/secret-file.txt HTTP/1.1\r\nHost: example.com\r\n\r\n")
	if _, err := readStatusLine(conn); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	waitFor(t, 30*time.Second, func() bool { return strings.Contains(read(), "doesn't exist") },
		"the missing folder's error never reached the log")

	logged := read()
	// What proves the lines were looked at: dufs's banner, and the agent's own.
	for _, want := range []string{"Listening on", "application unreachable", "refused an absolute request"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log has no %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "secret") {
		t.Errorf("the log names a file or folder:\n%s", logged)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0o755); err != nil {
		t.Fatal(err)
	}
}

// The log stops at its cap and keeps the set number of old files, the newest first.
func TestTheLogFileIsRotatedAtItsCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	l, err := openLogFile(path, 1000, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	line := strings.Repeat("x", 49) + "\n"
	for i := range 200 {
		if _, err := fmt.Fprintf(l, "%03d%s", i, line[3:]); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 1000 {
			t.Errorf("%s is %d bytes, past the cap of 1000", name, info.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("a third old file is kept, want two: %v", err)
	}
	// 20 lines fill a file, so the last 20 are current and the 20 before are in .1.
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), "180") || !strings.Contains(string(raw), "199") {
		t.Errorf("the current file does not hold the newest lines:\n%s", raw)
	}
	if raw, _ := os.ReadFile(path + ".1"); !strings.HasPrefix(string(raw), "160") {
		t.Errorf("agent.log.1 does not hold the lines before those:\n%s", raw)
	}
}

// Each job writes its own file, because Windows will not rename a file another process
// holds open, and launchd keeps only stderr.
func TestEachServiceJobWritesItsOwnLog(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		in := testPlanInput(goos)
		p, err := servicePlanFor(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.files {
			want := "agent.log"
			if strings.Contains(f.path, "menubar") || strings.Contains(f.path, "tray") {
				want = "menubar.log"
			}
			if !strings.Contains(f.body, "RFM_AGENT_LOG_FILE="+filepath.Join(in.dir, want)) {
				t.Errorf("%s: %s does not write %s:\n%s", goos, f.path, want, f.body)
			}
			if strings.Contains(f.body, "StandardOutPath") {
				t.Errorf("%s: launchd still writes the agent's log:\n%s", goos, f.body)
			}
		}
	}
}
