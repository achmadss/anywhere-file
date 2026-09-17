package main

import (
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// A name one byte over the DNS-SD limit is dropped in silence by every responder measured
// in docs/spikes/mdns.md, so it has to be a refusal here instead.
func TestInstanceNameIsCappedAtTheDNSSDLimit(t *testing.T) {
	key := testKey(t)
	fits := &state{Name: strings.Repeat("n", maxInstanceName-instanceIDLen-1)}
	name, err := instanceName(fits, key)
	if err != nil {
		t.Fatalf("a name that fits was refused: %v", err)
	}
	if len(name) != maxInstanceName {
		t.Errorf("instance name is %d bytes, want exactly %d", len(name), maxInstanceName)
	}
	if !strings.HasPrefix(name, fits.Name+" ") || !strings.HasSuffix(name, key.deviceID()[:instanceIDLen]) {
		t.Errorf("instance name = %q, want the display name and the start of the device id", name)
	}

	over := &state{Name: fits.Name + "n"}
	if _, err := instanceName(over, key); err == nil {
		t.Errorf("a %d byte instance name was accepted, want a refusal", len(over.Name)+1+instanceIDLen)
	}
	// And the registry refuses it before anything gets that far, which is the loud
	// failure at startup.
	if err := over.validate(); err == nil {
		t.Error("the registry accepted a name too long to advertise")
	}

	// Two PCs with one name are still two entries.
	other, err := instanceName(fits, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if other == name {
		t.Error("two devices with the same display name produced one instance name")
	}
}

func TestTXTCarriesWhatTheClientNeeds(t *testing.T) {
	key := testKey(t)
	s := &state{Name: "pc1", Apps: []app{
		{Name: "copyparty", Type: "http", Address: "127.0.0.1:3923"},
		{Name: "jellyfin", Type: "http", Address: "127.0.0.1:8096"},
	}}
	text := strings.Join(txtRecords(s, key), " ")
	for _, want := range []string{"v=1", "id=" + key.deviceID(), "name=pc1", "apps=copyparty,jellyfin"} {
		if !strings.Contains(text, want) {
			t.Errorf("TXT = %q, want it to carry %q", text, want)
		}
	}
	// The one thing that never leaves the PC.
	if strings.Contains(text, "3923") {
		t.Errorf("TXT = %q, want no application address in it", text)
	}

	// A device with more applications than fit is still found. The gateway's discovery
	// document has the rest.
	many := &state{Name: "pc1"}
	for i := range maxApps {
		many.Apps = append(many.Apps, app{Name: "application-" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	if got := len(appsTXT(many.appNames())); got > maxAppsTXT {
		t.Errorf("apps TXT is %d bytes, want at most %d", got, maxAppsTXT)
	}
	if strings.HasSuffix(appsTXT(many.appNames()), ",") {
		t.Error("the apps list was cut in the middle of a name")
	}
}

// The acceptance case: another process on the network resolves the record, with TXT
// intact, in under two seconds. It also answers the question the issue asks, which is
// whether 5353 binds beside the responder each OS already runs.
func TestTheAdvertisedRecordIsFound(t *testing.T) {
	key := testKey(t)
	s := &state{Name: "pc-" + key.deviceID()[:6], Apps: []app{{Name: "copyparty", Type: "http", Address: "127.0.0.1:3923"}}}

	ad := newAdvertiser(7433, discard)
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("advertising: %v", err)
	}
	t.Cleanup(ad.close)

	entry := browse(t, key.deviceID(), 2*time.Second)
	if entry == nil {
		t.Fatal("the record was not resolved within 2s")
	}
	if entry.Port != 7433 {
		t.Errorf("port = %d, want the gateway's 7433", entry.Port)
	}
	text := strings.Join(entry.InfoFields, " ")
	for _, want := range []string{"v=1", "name=" + s.Name, "apps=copyparty"} {
		if !strings.Contains(text, want) {
			t.Errorf("TXT = %q, want it to carry %q", text, want)
		}
	}

	// A changed application list reaches the LAN by advertising again.
	s.Apps = append(s.Apps, app{Name: "jellyfin", Type: "http", Address: "127.0.0.1:8096"})
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("re-advertising: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entry := browse(t, key.deviceID(), 2*time.Second)
		if entry != nil && strings.Contains(strings.Join(entry.InfoFields, " "), "apps=copyparty,jellyfin") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the new application list was not advertised, last TXT %v", entry)
		}
	}
}

// browse looks for our own record and ignores anything else on the network, which on a
// developer's LAN may include another PC running this agent.
func browse(t *testing.T, deviceID string, within time.Duration) *mdns.ServiceEntry {
	t.Helper()
	entries := make(chan *mdns.ServiceEntry, 16)
	done := make(chan *mdns.ServiceEntry, 1)
	go func() {
		var found *mdns.ServiceEntry
		for entry := range entries {
			for _, txt := range entry.InfoFields {
				if txt == "id="+deviceID {
					found = entry
				}
			}
		}
		done <- found
	}()
	err := mdns.QueryContext(t.Context(), &mdns.QueryParam{
		Service: mdnsService,
		Domain:  strings.TrimSuffix(mdnsDomain, "."),
		Timeout: within,
		Entries: entries,
		Logger:  log.New(io.Discard, "", 0),
	})
	close(entries)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return <-done
}
