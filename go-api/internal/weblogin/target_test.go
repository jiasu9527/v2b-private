package weblogin

import (
	"errors"
	"net/netip"
	"testing"
)

func TestParseTargetCanonical(t *testing.T) {
	for _, tc := range []struct{ raw, address, origin string }{
		{"https://Panel.Example.COM:443/login", "https://panel.example.com/login", "https://panel.example.com"},
		{"https://xn--bcher-kva.example.com", "https://xn--bcher-kva.example.com", "https://xn--bcher-kva.example.com"},
		{"https://www.example.com/登录", "https://www.example.com/%E7%99%BB%E5%BD%95", "https://www.example.com"},
		{"https://www.example.com/entry%20config", "https://www.example.com/entry%20config", "https://www.example.com"},
	} {
		got, err := ParseTarget(tc.raw)
		if err != nil || got.String() != tc.address || Origin(got) != tc.origin {
			t.Fatalf("ParseTarget(%q) = %v, %v; want %q and %q", tc.raw, got, err, tc.address, tc.origin)
		}
	}
}

func TestParseTargetRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		"", "https://", "https://localhost", "https://admin.local", "https://admin.localhost",
		"https://admin.internal", "https://admin.invalid", "https://192.168.0.1/", "https://8.8.8.8/",
		"https://127.1/", "https://0x7f.0x1/", "https://2130706433/", "https://0177.0.0.01/",
		"https://[::1]/", "https://[::ffff:8.8.8.8]/", "https://foo.example.com./",
		"https://-.example.com", "https://a..example.com", "https://foō.example.com",
		"http://foo.example.com", "file:///etc/passwd", "https://foo.example.com:8443",
		"https://foo.example.com:", "https://foo.example.com:0443",
		"https://user:pass@foo.example.com/", "https://admin.example.com\x00.evil.example.net/",
		"https://admin.example.com\\@evil.example.net/", "https://foo.example.com/\nlogin",
		"https://foo.example.com/?", "https://foo.example.com/?login=abc", "https://foo.example.com/#",
		"https://foo.example.com/%2Flogin", "https://foo.example.com/%5clogin", "https://foo.example.com/%3Flogin",
		"https://foo.example.com/%23login", "https://foo.example.com/%40login", "https://foo.example.com/%252Flogin",
		"https://foo.example.com/../login", "https://foo.example.com/%2e%2e/login", "https://foo.example.com/%00login",
		"https://foo.example.com/%7Flogin", "https://foo.example.com/%C0%AFlogin", "https://foo.example.com/%E2%80%8Blogin",
	} {
		if got, err := ParseTarget(raw); !errors.Is(err, ErrInvalidTarget) || got != nil {
			t.Errorf("unexpected URL accepted: %q -> %v, %v", raw, got, err)
		}
	}
}

func TestPublicDNSAnswerClassification(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "10.0.1.2", "100.100.100.200", "127.1.1.1", "168.63.129.16",
		"169.254.169.254", "172.16.0.1", "192.0.0.1", "192.0.2.8", "192.88.99.9", "192.168.1.1", "198.19.1.21",
		"198.51.100.7", "203.0.113.8", "224.1.2.3", "241.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:192.168.1.2", "64:ff9b::a00:1", "2001:db8::1", "2002:7f00:1::", "3fff::1", "fd00::1",
		"fe80::1%eth0", "ff02::1",
	} {
		if isPublicAnswer(netip.MustParseAddr(raw)) {
			t.Errorf("special or internal address accepted: %q", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !isPublicAnswer(netip.MustParseAddr(raw)) {
			t.Errorf("public address rejected: %q", raw)
		}
	}
}
