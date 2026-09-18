package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstRunWritesARegistry(t *testing.T) {
	dir := agentDir(t)
	st, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name == "" {
		t.Error("name is empty, want this machine's hostname")
	}
	if len(st.Apps) != 0 {
		t.Errorf("apps = %v, want none on a machine that offers nothing yet", st.Apps)
	}
	// A file to edit, where the operator expects it.
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); err != nil {
		t.Fatalf("no registry was written: %v", err)
	}
	again, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != st.Name {
		t.Errorf("name = %q after a reload, want %q", again.Name, st.Name)
	}
}

// The registry is edited by hand. A mistake in it has to say so at startup, because the
// alternative is an application nobody can reach and no explanation of why.
func TestBadRegistryIsRefused(t *testing.T) {
	for what, body := range map[string]string{
		"a misspelled field":      `{"name":"pc1","applications":[]}`,
		"an uppercase name":       `{"name":"pc1","apps":[{"name":"Dufs","type":"http","address":"127.0.0.1:5000"}]}`,
		"a name with a slash":     `{"name":"pc1","apps":[{"name":"media/files","type":"http","address":"127.0.0.1:5000"}]}`,
		"a duplicate name":        `{"name":"pc1","apps":[{"name":"a","type":"http","address":"127.0.0.1:1"},{"name":"a","type":"http","address":"127.0.0.1:2"}]}`,
		"a missing address":       `{"name":"pc1","apps":[{"name":"dufs","type":"http"}]}`,
		"a URL for an address":    `{"name":"pc1","apps":[{"name":"dufs","type":"http","address":"http://127.0.0.1:5000/x"}]}`,
		"an address with no port": `{"name":"pc1","apps":[{"name":"dufs","type":"http","address":"127.0.0.1"}]}`,
		"a name that is too long": `{"name":"` + strings.Repeat("n", 65) + `","apps":[]}`,
		"broken json":             `{"name":`,
		// An application the agent starts and puts on the LAN itself is one the gateway
		// cannot keep anybody out of.
		"a started app on the LAN":  `{"name":"pc1","apps":[{"name":"dufs","type":"http","address":"192.168.1.9:5000","command":["dufs","/srv"]}]}`,
		"a command with no program": `{"name":"pc1","apps":[{"name":"dufs","type":"http","address":"127.0.0.1:5000","command":[""]}]}`,
	} {
		dir := agentDir(t)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadState(dir); err == nil {
			t.Errorf("%s was accepted, want a refusal", what)
		}
	}
}

// The rule the server enforces on a sync is the rule the file is held to, so an agent
// cannot register a name the server will refuse.
func TestRegistryTakesWhatTheServerTakes(t *testing.T) {
	dir := agentDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"pc1","apps":[{"name":"dufs","type":"http","address":"127.0.0.1:5000"}]}`
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Apps) != 1 || st.Apps[0].Address != "127.0.0.1:5000" {
		t.Fatalf("apps = %v, want one dufs on loopback", st.Apps)
	}
	if names := st.appNames(); len(names) != 1 || names[0] != "dufs" {
		t.Errorf("appNames = %v, want [dufs]", names)
	}
}
