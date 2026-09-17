package main

// The application registry (#91). A file in the agent's directory lists the applications
// this PC offers and where each one listens. The loopback address stays here: it is what
// the gateway resolves a name to, and it never crosses the wire, so a compromised server
// cannot learn an address it was never given.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"github.com/achmadss/anywhere-file/internal/appname"
)

const (
	stateFileName = "agent.json"
	maxApps       = 32
	// protocolVersion is what a client checks before it talks to this agent. It appears
	// in the discovery document and in the mDNS record (#93).
	protocolVersion = 1
)

// state is the agent's own file, read at startup and written back by enrolment (#94).
type state struct {
	Name string `json:"name"`
	Apps []app  `json:"apps"`
}

// app is one registered application. Address is a host:port the agent dials; only the name
// and the type are ever sent to the server.
type app struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Address string `json:"address"`
}

func (s *state) appNames() []string {
	names := make([]string, 0, len(s.Apps))
	for _, a := range s.Apps {
		names = append(names, a.Name)
	}
	return names
}

// loadState reads the file, writing a starting one the first time. A machine with no
// applications yet is a valid state: it advertises itself and offers nothing.
func loadState(dir string) (*state, error) {
	path := filepath.Join(dir, stateFileName)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s := &state{Name: defaultDeviceName(), Apps: []app{}}
		return s, saveState(dir, s)
	case err != nil:
		return nil, err
	}
	var s state
	// Unknown fields are refused: a typo in a hand-edited file is a silent misconfiguration
	// otherwise, and the first sign of it would be an application nobody can reach.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

func saveState(dir string, s *state) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFileName), append(raw, '\n'), 0o600)
}

func (s *state) validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is empty")
	}
	// The limit is the LAN one: the name goes in a DNS-SD instance name with a piece of
	// the device id after it, and anything longer is dropped in silence by the responders
	// measured in docs/spikes/mdns.md. It is well under the 64 bytes the server takes.
	if len(s.Name) > maxDeviceName {
		return fmt.Errorf("name is %d bytes, the limit is %d", len(s.Name), maxDeviceName)
	}
	if len(s.Apps) > maxApps {
		return fmt.Errorf("%d applications, the server accepts %d", len(s.Apps), maxApps)
	}
	seen := map[string]bool{}
	for _, a := range s.Apps {
		switch {
		case !appname.Valid(a.Name):
			return fmt.Errorf("application name %q: lowercase letters, digits and hyphens only", a.Name)
		case !appname.Valid(a.Type):
			return fmt.Errorf("application %s: type %q: lowercase letters, digits and hyphens only", a.Name, a.Type)
		case seen[a.Name]:
			return fmt.Errorf("application %s is listed twice", a.Name)
		case a.Address == "":
			return fmt.Errorf("application %s has no address", a.Name)
		}
		// The address is dialled, so it is a host and a port and never a URL. A scheme or
		// a path here would be a way to point the gateway somewhere it should not go.
		if _, _, err := net.SplitHostPort(a.Address); err != nil {
			return fmt.Errorf("application %s: address %q: want host:port", a.Name, a.Address)
		}
		seen[a.Name] = true
	}
	return nil
}

func defaultDeviceName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "pc"
	}
	if len(name) > maxDeviceName {
		name = name[:maxDeviceName]
	}
	return name
}
