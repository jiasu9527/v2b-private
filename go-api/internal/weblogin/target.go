// Package weblogin constrains a browser job's outbound network to its one
// explicitly submitted public HTTPS origin. It does not expose a generic proxy.
package weblogin

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidTarget = errors.New("invalid public HTTPS website address")
	ErrBlockedTarget = errors.New("website does not resolve only to public IP addresses")
	ErrUnavailable   = errors.New("website connection is unavailable")
)

// ParseTarget accepts a URL for a normal HTTPS browser login. A canonical
// hostname without an explicit default port is shared by Go and the browser.
// Dotless names, alternate IP literal spellings, ambiguous paths, query
// strings, URL credentials, and non-default ports are intentionally excluded.
func ParseTarget(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\?#") ||
		strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return nil, ErrInvalidTarget
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.User != nil || target.Opaque != "" || target.Host == "" {
		return nil, ErrInvalidTarget
	}
	host := strings.ToLower(target.Hostname())
	if !validPublicHostname(host) || !strings.EqualFold(target.Host, host) && !strings.EqualFold(target.Host, host+":443") {
		return nil, ErrInvalidTarget
	}
	if hasAmbiguousEscapes(target.EscapedPath()) || !utf8.ValidString(target.Path) || strings.ContainsAny(target.Path, "\\?#") ||
		strings.ContainsFunc(target.Path, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return nil, ErrInvalidTarget
	}
	for part := range strings.SplitSeq(target.Path, "/") {
		if part == "." || part == ".." {
			return nil, ErrInvalidTarget
		}
	}
	target.Host = host
	return target, nil
}

func validPublicHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	labels := strings.Split(host, ".")
	tld := labels[len(labels)-1]
	// Do not give WHATWG browsers a final numeric component to reinterpret as
	// an address (including forms such as 0x7f.0x1 and 127.1).
	if len(tld) < 2 || tld[0] < 'a' || tld[0] > 'z' {
		return false
	}
	for _, reserved := range []string{"arpa", "corp", "example", "home", "internal", "invalid", "lan", "local", "localdomain", "localhost", "onion", "test"} {
		if tld == reserved {
			return false
		}
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Percent-encoded spaces and UTF-8 path bytes are useful for ordinary pages.
// Escaped delimiters, dot segments, and nested escapes give different clients
// an unnecessarily ambiguous interpretation, so reject them up front.
func hasAmbiguousEscapes(path string) bool {
	for i := 0; i < len(path); i++ {
		if path[i] != '%' {
			continue
		}
		if i+2 >= len(path) {
			return true
		}
		hi, lo := hexDigit(path[i+1]), hexDigit(path[i+2])
		if hi < 0 || lo < 0 {
			return true
		}
		c := byte(16*hi + lo)
		if c < 0x80 && !(c == ' ' || c == '-' || c == '_' || c == '~' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return true
		}
		i += 2
	}
	return false
}

func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// Origin returns the exact HTTPS origin of a URL accepted by ParseTarget.
func Origin(target *url.URL) string {
	if target == nil {
		return ""
	}
	return "https://" + target.Hostname()
}

var restrictedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	// Azure's platform wire server uses a globally scoped spelling.
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

var restrictedIPv6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}
var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

func isPublicAnswer(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() {
		return false
	}
	ip = ip.Unmap()
	prefixes := restrictedIPv4
	if ip.Is6() {
		if !ipv6GlobalUnicast.Contains(ip) {
			return false
		}
		prefixes = restrictedIPv6
	}
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
