package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
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
		{Name: "dufs", Type: "http", Address: "127.0.0.1:5000"},
		{Name: "jellyfin", Type: "http", Address: "127.0.0.1:8096"},
	}}
	text := strings.Join(txtRecords(s, key), " ")
	for _, want := range []string{fmt.Sprintf("v=%d", protocolVersion), "id=" + key.deviceID(), "name=pc1", "apps=dufs,jellyfin"} {
		if !strings.Contains(text, want) {
			t.Errorf("TXT = %q, want it to carry %q", text, want)
		}
	}
	// The one thing that never leaves the PC.
	if strings.Contains(text, "5000") {
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

// The responder has to bind 5353 beside the one the OS already runs: mDNSResponder on
// macOS, the DNS Client service on Windows, Avahi where it is installed. This runs on
// every runner and is the check the issue asks for.
func TestTheResponderBindsBesideTheSystemOne(t *testing.T) {
	key := testKey(t)
	s := &state{Name: "pc1", Apps: []app{{Name: "dufs", Type: "http", Address: "127.0.0.1:5000"}}}

	ad := newAdvertiser(7433, discard)
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("binding 5353: %v", err)
	}
	// Advertising again replaces the registration, which is how a changed name or
	// application list reaches the LAN. The second bind is the one that would fail if the
	// first socket were not released.
	s.Apps = append(s.Apps, app{Name: "jellyfin", Type: "http", Address: "127.0.0.1:8096"})
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("re-advertising: %v", err)
	}
	ad.close()
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("advertising after a close: %v", err)
	}
	ad.close()
}

// The acceptance case: another process resolves the record, with TXT intact, in under two
// seconds. It needs a machine that can actually carry multicast, which the hosted macOS
// and Windows runners cannot, so CI sets RFM_TEST_MULTICAST where it can and checks that
// this test ran.
func TestTheAdvertisedRecordIsFound(t *testing.T) {
	if os.Getenv("RFM_TEST_MULTICAST") == "" {
		t.Skip("set RFM_TEST_MULTICAST on a machine whose network carries multicast")
	}
	key := testKey(t)
	s := &state{Name: "pc-" + key.deviceID()[:6], Apps: []app{{Name: "dufs", Type: "http", Address: "127.0.0.1:5000"}}}

	ad := newAdvertiser(7433, discard)
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("advertising: %v", err)
	}
	t.Cleanup(ad.close)

	entry := mustFind(t, key.deviceID(), 2*time.Second)
	if entry.Name != s.Name {
		t.Errorf("name = %q, want %q", entry.Name, s.Name)
	}
	if entry.Version != fmt.Sprint(protocolVersion) {
		t.Errorf("v = %q, want %d", entry.Version, protocolVersion)
	}
	if strings.Join(entry.Apps, ",") != "dufs" {
		t.Errorf("apps = %v, want dufs", entry.Apps)
	}
	if !strings.HasSuffix(entry.Address, ":7433") {
		t.Errorf("address = %q, want the gateway port", entry.Address)
	}

	// A changed application list reaches the LAN by advertising again.
	s.Apps = append(s.Apps, app{Name: "jellyfin", Type: "http", Address: "127.0.0.1:8096"})
	if err := ad.advertise(s, key); err != nil {
		t.Fatalf("re-advertising: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		entry := mustFind(t, key.deviceID(), 2*time.Second)
		if strings.Join(entry.Apps, ",") == "dufs,jellyfin" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the new application list was not advertised, last seen %v", entry.Apps)
		}
	}
}

// mustFind browses for our own record and ignores anything else on the network, which on
// a developer's LAN may include another PC running this agent.
func mustFind(t *testing.T, deviceID string, within time.Duration) found {
	t.Helper()
	seen, err := discover(t.Context(), within, discard)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, entry := range seen {
		if entry.DeviceID == deviceID {
			return entry
		}
	}
	t.Fatalf("the record was not resolved within %v, saw %v", within, seen)
	return found{}
}
