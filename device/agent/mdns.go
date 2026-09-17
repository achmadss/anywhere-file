package main

// LAN discovery (#93). The agent advertises itself so a client on the same network finds
// it with no account, no Internet and no configuration, which is what local mode is.
//
// The record carries the device id, the display name, the application names and the
// protocol version. It does not carry an address an application listens on: the gateway
// port is the only port anyone needs, and the registry stays on the PC.

import (
	"fmt"
	"log"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/hashicorp/mdns"
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
	srv *mdns.Server
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
	// The host name is derived from the device id rather than taken from the machine,
	// because a PC's own name can be anything and this one has to be a legal label. The
	// addresses are passed in so nothing here depends on the machine resolving itself.
	host := "af-" + key.deviceID()[:instanceIDLen] + "." + mdnsDomain
	service, err := mdns.NewMDNSService(instance, mdnsService, mdnsDomain, host, a.port, localAddrs(), text)
	if err != nil {
		return fmt.Errorf("mdns: %w", err)
	}
	// hashicorp/mdns binds 5353 with net.ListenMulticastUDP, which sets SO_REUSEADDR on
	// every platform and SO_REUSEPORT on the BSDs. That is what lets the agent listen
	// beside Bonjour, Avahi and the Windows resolver instead of losing the port to them.
	srv, err := mdns.NewServer(&mdns.Config{
		Zone:   service,
		Logger: log.New(slogWriter{a.log}, "", 0),
	})
	if err != nil {
		return fmt.Errorf("mdns: %w", err)
	}

	a.mu.Lock()
	old := a.srv
	a.srv = srv
	a.mu.Unlock()
	if old != nil {
		_ = old.Shutdown()
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
		_ = srv.Shutdown()
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

// localAddrs is every address a client could reach this PC on. Loopback is the fallback,
// so a machine with nothing else still advertises a usable record.
func localAddrs() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLinkLocalUnicast() {
				ips = append(ips, ipnet.IP)
			}
		}
	}
	if len(ips) == 0 {
		return []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	return ips
}

// slogWriter sends what the mdns package writes with the standard logger into ours, so an
// agent's output is one stream of structured lines.
type slogWriter struct{ log *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	w.log.Debug("mdns", "msg", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
