package main

// TLS on the LAN (#96). The gateway serves HTTPS with a certificate it signs itself. There
// is no authority to ask, so what a client trusts is the device key: the discovery document
// carries the device's public key and that key's signature over the certificate's public
// key. A client checks that the device id it was looking for is the digest of the public
// key, then that the signature covers the key the handshake actually used. An impostor can
// copy the document and cannot make its own certificate match it.
//
// The certificate holds a P-256 key rather than the device's own Ed25519 key. Schannel on
// Windows, LibreSSL on macOS and every browser engine refuse an Ed25519 certificate
// outright, and a gateway nothing can open is not a gateway. The P-256 key is derived from
// the device key's seed, so it is still one key per PC, the same after every restart, and
// it never leaves the machine.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

const (
	// certDomain is the name the certificate carries, under a suffix that resolves
	// nowhere. The address comes from mDNS and changes with the network; the device id
	// does not.
	certDomain = ".anywhere-file"
	// certKeyInfo separates this key from anything else ever derived from the seed.
	certKeyInfo = "anywhere-file lan tls key v1"
	// proofContext separates this signature from everything else the device key signs,
	// starting with the requests the agent sends the server.
	proofContext = "anywhere-file lan-tls v1\n"
	// certLifetime is under the 398 days browsers refuse to look past. The agent makes a
	// new certificate before this one runs out.
	certLifetime    = 397 * 24 * time.Hour
	certRenewBefore = 30 * 24 * time.Hour
)

// certKey is the key the certificate carries, derived from the device key's seed.
func certKey(k deviceKey) (*ecdsa.PrivateKey, error) {
	// A P-256 scalar is a 32 byte number below the curve's order. Almost every draw is,
	// and the counter is for the one in four billion that is not.
	for counter := range 256 {
		b, err := hkdf.Key(sha256.New, k.priv.Seed(), []byte{byte(counter)}, certKeyInfo, 32)
		if err != nil {
			return nil, err
		}
		// ParseRawPrivateKey is the range check: zero and anything past the order are
		// refused, which is what the counter is for.
		key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), b)
		if err != nil {
			continue
		}
		return key, nil
	}
	return nil, fmt.Errorf("no usable TLS key came out of the device key")
}

// deviceCertificate builds the certificate the LAN gateway answers with.
func deviceCertificate(k deviceKey) (tls.Certificate, error) {
	key, err := certKey(k)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	name := k.deviceID() + certDomain
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		IPAddresses:  localAddresses(),
		// An hour back, because a PC that has just woken up can have a clock that is
		// still wrong and the handshake is the first thing a client tries.
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Not a certificate authority. Firefox refuses to talk to a server whose
		// certificate says it is one, and nothing here has to sign anything else.
		IsCA: false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("device certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// lanCert holds the certificate the gateway is serving and replaces it before it runs out.
// An agent that has been up for a year is an ordinary thing on a PC nobody reboots.
type lanCert struct {
	key deviceKey

	mu   sync.Mutex
	cert *tls.Certificate
}

func (c *lanCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert == nil || time.Now().After(c.cert.Leaf.NotAfter.Add(-certRenewBefore)) {
		cert, err := deviceCertificate(c.key)
		if err != nil {
			return nil, err
		}
		c.cert = &cert
	}
	return c.cert, nil
}

// lanTLS is what the gateway's listener is wrapped in.
func lanTLS(c *lanCert) *tls.Config {
	return &tls.Config{
		GetCertificate: c.get,
		// The client that proved hardest to satisfy is the oldest one: TLS 1.2 is what a
		// system WebView on an older phone offers.
		MinVersion: tls.VersionTLS12,
		// The gateway speaks HTTP/1.1. Said out loud, because a client that insists on
		// ALPN gets no answer from a server that offers nothing.
		NextProtos: []string{"http/1.1"},
	}
}

// deviceProof is the device key's word that the certificate on this connection is this
// PC's. It goes in the discovery document, which is the first thing a client reads.
func deviceProof(k deviceKey) (string, error) {
	key, err := certKey(k)
	if err != nil {
		return "", err
	}
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(ed25519.Sign(k.priv, proofMessage(spki))), nil
}

// verifyDeviceProof is what a client does with the document it just read: it decides
// whether the PC holding the device key is the PC on the other end of this connection.
// The agent has no client yet, so this is here to be tested against the agent itself.
func verifyDeviceProof(deviceID, publicKey, proof string, leaf *x509.Certificate) error {
	public, err := hex.DecodeString(publicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("the device offered no usable public key")
	}
	sum := sha256.Sum256(public)
	if hex.EncodeToString(sum[:]) != deviceID {
		return fmt.Errorf("the public key is not device %s", deviceID)
	}
	sig, err := hex.DecodeString(proof)
	if err != nil {
		return fmt.Errorf("the proof is not hexadecimal")
	}
	if !ed25519.Verify(public, proofMessage(leaf.RawSubjectPublicKeyInfo), sig) {
		return fmt.Errorf("the device key did not sign this certificate, so this is another PC")
	}
	return nil
}

func proofMessage(spki []byte) []byte {
	sum := sha256.Sum256(spki)
	return append([]byte(proofContext), sum[:]...)
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
