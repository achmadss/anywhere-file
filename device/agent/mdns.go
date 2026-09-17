package main

// LAN discovery (#93). The agent advertises itself so a client on the same network finds
// it with no account, no Internet and no configuration, which is what local mode is.
//
// The record carries the device id, the display name, the application names and the
// protocol version. It does not carry an address an application listens on: the gateway
// port is the only port anyone needs, and the registry stays on the PC.

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/grandcat/zeroconf"
)

const (
	mdnsService = "_anywhere-file._tcp"
	mdnsDomain  = "local."

	// DNS-SD caps an instance name at 63 bytes. Every responder measured in
	// docs/spikes/mdns.md drops a longer one in silence, so it is checked before
	// registering and the agent refuses to start rather than advertising nothing.
	maxInstanceName = 63
	// The device id is 64 hex characters. Eight of them keep two PCs with the same
	// display name apart and stay short enough to read.
	instanceIDLen = 8
	// maxDeviceName is what is left for the display name once the id and its separator
	// are in the instance name.
	maxDeviceName = maxInstanceName - instanceIDLen - 1
	// TXT travels in every answer, so it stays small. The application names here are a
	// convenience for the device list; the gateway's discovery document is the full one.
	maxAppsTXT = 200
)

// advertiser owns the mDNS registration. Re-advertising replaces it, which is how a
// changed name or application list reaches the LAN.
type advertiser struct {
	port int
	log  *slog.Logger

	mu  sync.Mutex
	srv *zeroconf.Server
}

func newAdvertiser(port int, log *slog.Logger) *advertiser {
	return &advertiser{port: port, log: log}
}

// advertise registers the current state, replacing an earlier registration.
func (a *advertiser) advertise(s *state, key deviceKey) error {
	instance, err := instanceName(s, key)
	if err != nil {
		return err
	}
	text := txtRecords(s, key)
	srv, err := zeroconf.Register(instance, mdnsService, mdnsDomain, a.port, text, nil)
	if err != nil {
		return fmt.Errorf("mdns: %w", err)
	}
	a.mu.Lock()
	old := a.srv
	a.srv = srv
	a.mu.Unlock()
	if old != nil {
		old.Shutdown()
	}
	a.log.Info("advertising on the LAN",
		"instance", instance, "service", mdnsService, "port", a.port, "txt", text)
	return nil
}

func (a *advertiser) close() {
	a.mu.Lock()
	srv := a.srv
	a.srv = nil
	a.mu.Unlock()
	if srv != nil {
		srv.Shutdown()
	}
}

// instanceName is what a person sees in a list of devices: the display name and enough of
// the device id that two PCs called the same thing are still two entries.
func instanceName(s *state, key deviceKey) (string, error) {
	name := s.Name + " " + key.deviceID()[:instanceIDLen]
	if len(name) > maxInstanceName {
		return "", fmt.Errorf("mdns: instance name %q is %d bytes, DNS-SD allows %d", name, len(name), maxInstanceName)
	}
	return name, nil
}

// txtRecords is what a client reads before it connects to anything.
func txtRecords(s *state, key deviceKey) []string {
	return []string{
		fmt.Sprintf("v=%d", protocolVersion),
		"id=" + key.deviceID(),
		"name=" + s.Name,
		"apps=" + appsTXT(s.appNames()),
	}
}

// appsTXT joins the names and stops at the budget, on a whole name. A device with more
// applications than fit is still found; the client reads the rest from the gateway.
func appsTXT(names []string) string {
	var b strings.Builder
	for _, name := range names {
		if b.Len()+len(name)+1 > maxAppsTXT {
			break
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
	}
	return b.String()
}
