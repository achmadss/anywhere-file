package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// lanServer is the gateway as the LAN reaches it: the real listener, wrapped the way serve
// wraps it.
func lanServer(t *testing.T, key deviceKey) *httptest.Server {
	t.Helper()
	st := &state{Name: "pc1", Apps: []app{}}
	srv := httptest.NewUnstartedServer(newGateway(newAgent(t.TempDir(), key, st, discard)))
	srv.Listener = tls.NewListener(srv.Listener, lanTLS(&lanCert{key: key}))
	srv.Start()
	// Start named it http, because the wrapping happened here and not in httptest.
	srv.URL = "https://" + srv.Listener.Addr().String()
	t.Cleanup(srv.Close)
	return srv
}

// document is what a client reads first, and the certificate it read it over.
type document struct {
	V         int    `json:"v"`
	DeviceID  string `json:"device_id"`
	PublicKey string `json:"public_key"`
	TLSProof  string `json:"tls_proof"`
}

func readDocument(t *testing.T, url string) (document, *x509.Certificate) {
	t.Helper()
	var leaf *x509.Certificate
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			// A client has nothing to check the certificate against until it has read
			// the document below, so it keeps the certificate and checks after.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				cert, err := x509.ParseCertificate(raw[0])
				leaf = cert
				return err
			},
		}},
	}
	resp, err := client.Get(url + discoveryPath)
	if err != nil {
		t.Fatalf("reading the discovery document: %v", err)
	}
	defer resp.Body.Close()
	var doc document
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc, leaf
}

// The document says which device this is and the device key signs the certificate the
// document arrived over, so a client that knows a device id can tell whether it is talking
// to that PC.
func TestTheDocumentProvesTheCertificateBelongsToTheDevice(t *testing.T) {
	key := testKey(t)
	srv := lanServer(t, key)
	if !strings.HasPrefix(srv.URL, "https://") {
		t.Fatalf("the gateway is at %s, want https", srv.URL)
	}

	doc, leaf := readDocument(t, srv.URL)
	if doc.DeviceID != key.deviceID() {
		t.Errorf("device_id = %s, want %s", doc.DeviceID, key.deviceID())
	}
	if doc.V != protocolVersion {
		t.Errorf("v = %d, want %d, the version that says the LAN is encrypted", doc.V, protocolVersion)
	}
	if err := verifyDeviceProof(doc.DeviceID, doc.PublicKey, doc.TLSProof, leaf); err != nil {
		t.Errorf("the document does not prove this PC's certificate: %v", err)
	}
}

// #96's acceptance. Another PC can claim a device id and copy the document that goes with
// it; what it cannot do is serve a certificate the device key has signed.
func TestAnImpostorWithTheSameDeviceIDIsRefused(t *testing.T) {
	real, impostor := testKey(t), testKey(t)
	realDoc, _ := readDocument(t, lanServer(t, real).URL)

	_, theirLeaf := readDocument(t, lanServer(t, impostor).URL)
	err := verifyDeviceProof(realDoc.DeviceID, realDoc.PublicKey, realDoc.TLSProof, theirLeaf)
	if err == nil {
		t.Fatal("another PC replaying the real device's document was accepted")
	}
	if !strings.Contains(err.Error(), "another PC") {
		t.Errorf("err = %v, want the signature to be what refused it", err)
	}

	// A device id that does not match the key in the document is the cheaper lie, and it
	// has to fail before the signature is even looked at.
	ourDoc, ourLeaf := readDocument(t, lanServer(t, impostor).URL)
	if err := verifyDeviceProof(real.deviceID(), ourDoc.PublicKey, ourDoc.TLSProof, ourLeaf); err == nil {
		t.Error("a document claiming another device id was accepted")
	}
}

// The key is derived from the device key's seed, so a client that pins it keeps its pin
// across a restart and across a new certificate.
func TestTheCertificateKeyIsTheSameAfterEveryStart(t *testing.T) {
	key := testKey(t)
	first, err := deviceCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deviceCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Leaf.Equal(second.Leaf) {
		// Different certificates are expected. The same key in them is the point.
		if string(first.Leaf.RawSubjectPublicKeyInfo) != string(second.Leaf.RawSubjectPublicKeyInfo) {
			t.Error("a second certificate holds a different key, so a pinned client would be locked out")
		}
	}
	other, err := deviceCertificate(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(other.Leaf.RawSubjectPublicKeyInfo) == string(first.Leaf.RawSubjectPublicKeyInfo) {
		t.Error("two devices derived the same TLS key")
	}
}

// Every TLS stack has to be able to use this certificate. Ed25519 is refused by Schannel,
// by LibreSSL and by browser engines, and a certificate that says it is an authority is
// refused by Firefox.
func TestTheCertificateIsOneEveryClientCanUse(t *testing.T) {
	key := testKey(t)
	cert, err := deviceCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := cert.Leaf
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("the certificate holds a %s key, want ECDSA", leaf.PublicKeyAlgorithm)
	}
	if leaf.IsCA {
		t.Error("the certificate says it is a certificate authority, which Firefox refuses")
	}
	if life := leaf.NotAfter.Sub(leaf.NotBefore); life > 398*24*time.Hour {
		t.Errorf("the certificate lasts %s, longer than the 398 days browsers look past", life)
	}
	if err := leaf.VerifyHostname(key.deviceID() + certDomain); err != nil {
		t.Errorf("the certificate does not name the device: %v", err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("the certificate does not cover loopback: %v", err)
	}
}

// A certificate near its end is replaced, because a PC nobody reboots is the ordinary case.
func TestACertificateNearItsEndIsReplaced(t *testing.T) {
	c := &lanCert{key: testKey(t)}
	first, err := c.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.get(nil); again != first {
		t.Error("a second handshake built a second certificate")
	}
	// As it will look on the day it is a month from running out.
	c.cert.Leaf.NotAfter = time.Now().Add(certRenewBefore - time.Hour)
	renewed, err := c.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if renewed == first {
		t.Fatal("the certificate was not replaced, so the gateway will serve an expired one")
	}
	if time.Until(renewed.Leaf.NotAfter) < 300*24*time.Hour {
		t.Error("the new certificate expires as soon as the old one")
	}
}

// A document with nothing in it is not a proof.
func TestAnEmptyProofIsRefused(t *testing.T) {
	key := testKey(t)
	cert, err := deviceCertificate(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyDeviceProof(key.deviceID(), "", "", cert.Leaf); err == nil {
		t.Error("a document with no key and no proof was accepted")
	}
}
