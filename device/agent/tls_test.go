package main

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// lanServer is the gateway as the LAN reaches it: the real listener, wrapped the way
// serve wraps it.
func lanServer(t *testing.T, key deviceKey) *httptest.Server {
	t.Helper()
	cert, err := deviceCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	st := &state{Name: "pc1", Apps: []app{}}
	srv := httptest.NewUnstartedServer(newGateway(newAgent(t.TempDir(), key, st, discard)))
	srv.Listener = tls.NewListener(srv.Listener, lanTLS(cert))
	srv.Start()
	// Start names it http, because the wrapping happened here and not in httptest.
	srv.URL = "https://" + srv.Listener.Addr().String()
	t.Cleanup(srv.Close)
	return srv
}

// pinningClient is what a client does after it has seen a device once: it checks the key
// and nothing else, because there is no authority to ask and the address changes.
func pinningClient(public ed25519.PublicKey) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				return pinned(public, raw)
			},
		}},
	}
}

// The device key is the server's identity on the LAN, so a client that knows the device id
// knows what the handshake has to prove.
func TestTheGatewayAnswersWithTheDeviceKey(t *testing.T) {
	key := testKey(t)
	srv := lanServer(t, key)
	if !strings.HasPrefix(srv.URL, "https://") {
		t.Fatalf("the gateway is at %s, want https", srv.URL)
	}

	resp, err := pinningClient(key.public()).Get(srv.URL + discoveryPath)
	if err != nil {
		t.Fatalf("a client that pins this device's key could not connect: %v", err)
	}
	defer resp.Body.Close()
	var doc struct {
		V        int    `json:"v"`
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.DeviceID != key.deviceID() {
		t.Errorf("device_id = %s, want %s", doc.DeviceID, key.deviceID())
	}
	if doc.V != protocolVersion {
		t.Errorf("v = %d, want %d, the version that says the LAN is encrypted", doc.V, protocolVersion)
	}
}

// #96's acceptance: a second agent claiming a device id a client has already seen is
// refused. The device id is a name, and only the key behind it is proof.
func TestAnImpostorWithTheSameNameIsRefused(t *testing.T) {
	real, impostor := testKey(t), testKey(t)
	srv := lanServer(t, impostor)

	_, err := pinningClient(real.public()).Get(srv.URL + discoveryPath)
	if err == nil {
		t.Fatal("a client that had seen the real device accepted another key")
	}
	if !strings.Contains(err.Error(), "another device") {
		t.Errorf("err = %v, want the pin to be what refused it", err)
	}
	// The same client reaches the device whose key it holds.
	if _, err := pinningClient(impostor.public()).Get(srv.URL + discoveryPath); err != nil {
		t.Errorf("the device that does hold the key was refused: %v", err)
	}
}

// A client that checks the name against the address it dialled has to find it there, and
// the certificate has to be usable as its own trust anchor for a client with no other way
// to trust it.
func TestTheCertificateCoversTheDeviceAndItsAddresses(t *testing.T) {
	key := testKey(t)
	cert, err := deviceCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := cert.Leaf
	if err := leaf.VerifyHostname(key.deviceID() + certDomain); err != nil {
		t.Errorf("the certificate does not name the device: %v", err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("the certificate does not cover loopback: %v", err)
	}
	if time.Until(leaf.NotAfter) < 365*24*time.Hour {
		t.Errorf("the certificate expires %s, which is soon enough to break a running agent", leaf.NotAfter)
	}

	// Installed as the one certificate a client trusts, which is what a client with no
	// pinning of its own has to do.
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: key.deviceID() + certDomain}); err != nil {
		t.Errorf("the certificate cannot be its own trust anchor: %v", err)
	}
}

// The handshake carries the key, so a client learns it from the device itself rather than
// from whatever answered the mDNS query.
func TestTheKeyIsLearnedFromTheHandshake(t *testing.T) {
	key := testKey(t)
	srv := lanServer(t, key)
	conn, err := tls.Dial("tcp", strings.TrimPrefix(srv.URL, "https://"), &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := pinned(key.public(), rawCerts(conn)); err != nil {
		t.Errorf("the first handshake did not carry this device's key: %v", err)
	}
	if v := conn.ConnectionState().Version; v < tls.VersionTLS12 {
		t.Errorf("TLS version %x, want 1.2 at the oldest", v)
	}
}

func rawCerts(conn *tls.Conn) [][]byte {
	var raw [][]byte
	for _, c := range conn.ConnectionState().PeerCertificates {
		raw = append(raw, c.Raw)
	}
	return raw
}

// A certificate with no key behind it is not a pin, so an empty chain is a refusal.
func TestNoCertificateIsNotAPin(t *testing.T) {
	if err := pinned(testKey(t).public(), nil); err == nil {
		t.Error("an empty chain was accepted")
	}
}
