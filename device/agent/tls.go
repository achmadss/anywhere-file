package main

// TLS on the LAN (#96). The gateway serves HTTPS with a certificate the agent signs with
// the device key, so the key that identifies this PC to the server is the key that proves
// it to a client on the LAN. There is no certificate authority to ask: the client pins the
// public key the first time it connects and refuses a different one afterwards, the way
// ssh does with a host key. mDNS is not trusted for the key, the handshake is.
//
// The certificate is made at every start and kept in memory. What a client pins is the
// key, which outlives any certificate made from it, so there is nothing here to keep on
// disk and nothing to renew.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// certDomain is the name the certificate carries, under a suffix that resolves nowhere.
// The address a client dials comes from mDNS and changes with the network; the name is the
// device id, which does not.
const certDomain = ".anywhere-file"

// certLifetime outlives any agent process. Nothing checks this certificate against a
// calendar except a client's own TLS stack, and an agent that has been up for years
// should not start failing handshakes.
const certLifetime = 10 * 365 * 24 * time.Hour

// deviceCertificate builds the certificate the LAN gateway answers with.
func deviceCertificate(key deviceKey) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: key.deviceID() + certDomain},
		DNSNames:     []string{key.deviceID() + certDomain},
		IPAddresses:  localAddresses(),
		// An hour back, because a PC that has just woken up can have a clock that is
		// still wrong and a handshake is the first thing the client tries.
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Its own issuer, so a client that has no way to trust a bare leaf can install it
		// as a trust anchor of one certificate. A client that pins the public key ignores
		// all of this.
		IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.public(), key.priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("device certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key.priv, Leaf: leaf}, nil
}

// lanTLS is the configuration the gateway's listener is wrapped in.
func lanTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		// Ed25519 needs 1.2 at the oldest, and a client this old is a client that cannot
		// verify the certificate at all.
		MinVersion: tls.VersionTLS12,
		// The gateway speaks HTTP/1.1. Said out loud, because a client that insists on
		// ALPN gets no answer from a server that offers nothing.
		NextProtos: []string{"http/1.1"},
	}
}

// pinned reports whether a certificate chain was signed by this device's key. It is what a
// client does, kept here because the agent's own tests are the only thing that checks it
// until there is a client.
func pinned(public ed25519.PublicKey, raw [][]byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("the device offered no certificate")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	got, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("the certificate holds a %T, want an Ed25519 key", leaf.PublicKey)
	}
	if !got.Equal(public) {
		return fmt.Errorf("the certificate is signed by another key, so this is another device")
	}
	return nil
}

// localAddresses is every address this machine answers on, so a client that checks the
// name against the address it dialled finds it there. Loopback is first and always, for a
// client on the PC itself.
func localAddresses() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() {
			continue
		}
		ips = append(ips, n.IP)
	}
	return ips
}
