package auth

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/media-ingest/media-ingest/internal/config"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") || ValidateHash(h) != nil {
		t.Fatalf("hash %q", h)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "wrong") {
		t.Fatal("verify")
	}
	for _, bad := range []string{"", "$2a$10$abc", "$argon2id$v=19$m=1,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", h + "$x"} {
		if ValidateHash(bad) == nil || VerifyPassword(bad, "x") {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestClientIPSpoofing(t *testing.T) {
	a := New(&config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}}, nil)
	r := httptest.NewRequest("GET", "/", nil)

	// Direct client: X-Forwarded-For is ignored.
	r.RemoteAddr = "10.1.1.1:5000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if ip := a.ClientIP(r); ip != "10.1.1.1" {
		t.Errorf("direct: %s", ip)
	}
	// Through the proxy: the right-most untrusted hop wins, a forged left entry does not.
	r.RemoteAddr = "192.0.2.10:5000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if ip := a.ClientIP(r); ip != "203.0.113.9" {
		t.Errorf("proxied: %s", ip)
	}
}

func TestCheckCSRF(t *testing.T) {
	tok := strings.Repeat("a1", 32)
	r := httptest.NewRequest("GET", "/", nil)
	if !CheckCSRF(r) {
		t.Error("GET must pass")
	}
	r = httptest.NewRequest("POST", "http://app.example.com/api/x", nil)
	if CheckCSRF(r) {
		t.Error("POST without token passed")
	}
	r.Header.Set("Cookie", CSRFCookie+"="+tok)
	r.Header.Set(CSRFHeader, tok)
	if !CheckCSRF(r) {
		t.Error("valid token rejected")
	}
	r.Header.Set("Origin", "https://evil.example.com")
	if CheckCSRF(r) {
		t.Error("foreign origin passed")
	}
}
