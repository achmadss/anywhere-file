package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sharedFolder shares a directory through the gateway, with a stand-in for dufs that says
// whether upload is allowed. The directory holds a.txt, b.txt and sub/c.txt.
func sharedFolder(t *testing.T, upload bool) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")
	write(t, filepath.Join(root, "b.txt"), "b")
	write(t, filepath.Join(root, "sub", "c.txt"), "c")
	dufs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("json") {
			t.Errorf("dufs was asked for %s", r.URL)
		}
		fmt.Fprintf(w, `{"allow_upload": %t}`, upload)
	}))
	t.Cleanup(dufs.Close)
	st := &state{Name: "pc1", Apps: []app{{Name: "files", Type: "http", Address: hostPort(t, dufs.URL),
		Command: []string{dufsProgram, root}}}}
	gw := httptest.NewServer(newGateway(newAgent(t.TempDir(), testKey(t), st, discard)))
	t.Cleanup(gw.Close)
	return gw, root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func compressIn(t *testing.T, gw *httptest.Server, dir, name string, entries ...string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "entries": entries})
	resp, err := gw.Client().Post(gw.URL+"/files/"+dir+"?compress", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// zipped lists what a zip holds, with each file's contents.
func zipped(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

// onlyFiles reports what is left in dir, so a test can see no temp file stayed behind.
func onlyFiles(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestCompress(t *testing.T) {
	cases := []struct {
		name    string
		dir     string
		entries []string
		want    map[string]string
	}{
		{"one file", "", []string{"a.txt"}, map[string]string{"a.txt": "a"}},
		{"a folder", "", []string{"sub"}, map[string]string{"sub/": "", "sub/c.txt": "c"}},
		{"several entries", "", []string{"a.txt", "b.txt", "sub"},
			map[string]string{"a.txt": "a", "b.txt": "b", "sub/": "", "sub/c.txt": "c"}},
		{"inside a folder", "sub/", []string{"c.txt"}, map[string]string{"c.txt": "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gw, root := sharedFolder(t, true)
			if got := compressIn(t, gw, c.dir, "out.zip", c.entries...); got != http.StatusCreated {
				t.Fatalf("status %d, want 201", got)
			}
			got := zipped(t, filepath.Join(root, c.dir, "out.zip"))
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("the zip holds %v, want %v", got, c.want)
			}
			if names := onlyFiles(t, filepath.Join(root, c.dir)); slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, ".tmp") }) {
				t.Errorf("a temp file was left behind: %v", names)
			}
		})
	}
}

func TestCompressRefusesATakenName(t *testing.T) {
	gw, root := sharedFolder(t, true)
	if got := compressIn(t, gw, "", "b.txt.zip", "a.txt"); got != http.StatusCreated {
		t.Fatalf("status %d, want 201", got)
	}
	if got := compressIn(t, gw, "", "b.txt.zip", "b.txt"); got != http.StatusConflict {
		t.Fatalf("status %d, want 409", got)
	}
	if got := zipped(t, filepath.Join(root, "b.txt.zip")); got["a.txt"] != "a" || len(got) != 1 {
		t.Errorf("the zip that was there was overwritten: %v", got)
	}
}

func TestCompressRefusesPathsOutsideTheFolder(t *testing.T) {
	gw, root := sharedFolder(t, true)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "secret")

	if got := compressIn(t, gw, "", "out.zip", "../"+filepath.Base(outside)); got != http.StatusBadRequest {
		t.Errorf("an entry with .. in it: status %d, want 400", got)
	}
	if got := compressIn(t, gw, "", "../out.zip", "a.txt"); got != http.StatusBadRequest {
		t.Errorf("a name with .. in it: status %d, want 400", got)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if got := compressIn(t, gw, "", "out.zip", "a.txt", "link"); got != http.StatusForbidden {
		t.Errorf("a symlink out of the folder: status %d, want 403", got)
	}
	if got := compressIn(t, gw, "link/", "out.zip", "secret.txt"); got != http.StatusNotFound {
		t.Errorf("a folder that is a symlink out: status %d, want 404", got)
	}
	if names := onlyFiles(t, root); slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "out.zip") }) {
		t.Errorf("a refused zip was left behind: %v", names)
	}
}

func TestCompressNeedsUpload(t *testing.T) {
	gw, root := sharedFolder(t, false)
	if got := compressIn(t, gw, "", "out.zip", "a.txt"); got != http.StatusForbidden {
		t.Fatalf("status %d, want 403", got)
	}
	if _, err := os.Stat(filepath.Join(root, "out.zip")); err == nil {
		t.Error("the zip was made anyway")
	}
}
