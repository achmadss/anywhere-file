package devicesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func signed(t *testing.T, priv ed25519.PrivateKey, body string, at time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/devices/register?x=1", strings.NewReader(body))
	SignAt(req, priv, []byte(body), "0123456789abcdef", at)
	return req
}

func TestRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"name":"desk"}`
	req := signed(t, priv, body, time.Now())

	key, nonce, err := Verify(req, []byte(body))
	if err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if key != hex.EncodeToString(pub) || nonce != "0123456789abcdef" {
		t.Errorf("got key %q nonce %q", key, nonce)
	}

	cases := map[string]*http.Request{
		"tampered body":   signed(t, priv, body, time.Now()),
		"stale timestamp": signed(t, priv, body, time.Now().Add(-time.Hour)),
		"other key":       signed(t, priv, body, time.Now()),
		"unsigned":        httptest.NewRequest(http.MethodPost, "/v1/devices/register", strings.NewReader(body)),
		"short nonce":     signed(t, priv, body, time.Now()),
		"wrong path":      signed(t, priv, body, time.Now()),
	}
	bodies := map[string]string{"tampered body": `{"name":"nas"}`}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cases["other key"].Header.Set(HeaderKey, hex.EncodeToString(other.Public().(ed25519.PublicKey)))
	cases["short nonce"].Header.Set(HeaderNonce, "short")
	cases["wrong path"].URL.Path = "/v1/devices/other"
	for name, r := range cases {
		b := body
		if v, ok := bodies[name]; ok {
			b = v
		}
		if _, _, err := Verify(r, []byte(b)); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}
