// Package devicesig signs and verifies requests with a device's Ed25519 key.
//
// The agent signs, the control plane verifies, and both use this package so the format is
// written once. A signed request carries four headers:
//
//	X-Device-Key:       the public key, 32 bytes hex
//	X-Device-Nonce:     random, at least 16 characters, accepted once
//	X-Device-Timestamp: unix seconds, within 5 minutes of now
//	X-Device-Signature: 64 bytes hex, over
//	    METHOD\npath\ndevice_key\nnonce\ntimestamp\nhex(sha256(body))
//
// The path excludes the query string. Verify checks everything except nonce reuse, which
// needs storage; the caller claims the nonce it returns exactly once.
package devicesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderKey       = "X-Device-Key"
	HeaderNonce     = "X-Device-Nonce"
	HeaderTimestamp = "X-Device-Timestamp"
	HeaderSignature = "X-Device-Signature"

	ClockSkew   = 5 * time.Minute
	MinNonceLen = 16
)

// Sign adds the signature headers to req for the body the caller is about to send.
func Sign(req *http.Request, priv ed25519.PrivateKey, body []byte) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		panic("devicesig: " + err.Error())
	}
	SignAt(req, priv, body, hex.EncodeToString(nonce), time.Now())
}

// SignAt is Sign with the nonce and clock chosen by the caller. Tests use it to build
// replayed and stale requests.
func SignAt(req *http.Request, priv ed25519.PrivateKey, body []byte, nonce string, at time.Time) {
	key := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	stamp := strconv.FormatInt(at.Unix(), 10)
	sig := ed25519.Sign(priv, payload(req.Method, path(req), key, nonce, stamp, body))
	req.Header.Set(HeaderKey, key)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderTimestamp, stamp)
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
}

// Verify checks the headers on r against body. It returns the device key as sent and the
// nonce, which the caller must refuse to accept a second time.
func Verify(r *http.Request, body []byte) (deviceKey, nonce string, err error) {
	deviceKey = strings.TrimSpace(r.Header.Get(HeaderKey))
	nonce = r.Header.Get(HeaderNonce)
	stamp := strings.TrimSpace(r.Header.Get(HeaderTimestamp))
	sigHex := strings.TrimSpace(r.Header.Get(HeaderSignature))
	if deviceKey == "" || nonce == "" || stamp == "" || sigHex == "" {
		return "", "", errors.New("missing device signature headers")
	}
	if len(nonce) < MinNonceLen {
		return "", "", errors.New("nonce too short")
	}
	pub, err := hex.DecodeString(deviceKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", "", errors.New("device key is not 32 bytes hex")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", "", errors.New("signature is not 64 bytes hex")
	}
	secs, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return "", "", errors.New("bad timestamp")
	}
	if dt := time.Since(time.Unix(secs, 0)); dt > ClockSkew || dt < -ClockSkew {
		return "", "", errors.New("timestamp outside window")
	}
	if !ed25519.Verify(pub, payload(r.Method, path(r), deviceKey, nonce, stamp, body), sig) {
		return "", "", errors.New("bad signature")
	}
	return deviceKey, nonce, nil
}

func payload(method, path, key, nonce, stamp string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{method, path, key, nonce, stamp, hex.EncodeToString(sum[:])}, "\n"))
}

func path(r *http.Request) string {
	if p := r.URL.EscapedPath(); p != "" {
		return p
	}
	return r.URL.Path
}
