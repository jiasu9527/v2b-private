// Package ipgeo resolves reported client IPs using local, offline databases.
package ipgeo

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/service"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// Location contains only aggregate geography, never the original IP address.
// CountryCode is ISO 3166-1 alpha-2; ProvinceCode is a six-digit Chinese adcode.
type Location struct {
	CountryCode   string `json:"country_code"`
	CountryName   string `json:"country_name"`
	ProvinceCode  string `json:"province_code"`
	ProvinceName  string `json:"province_name"`
	CityName      string `json:"city_name"`
	Known         bool   `json:"known"`
	UnknownReason string `json:"unknown_reason,omitempty"`
}

type GeoStatus struct {
	Status  string `json:"status"`
	IPv4    bool   `json:"ipv4"`
	IPv6    bool   `json:"ipv6"`
	Source  string `json:"source"`
	Version string `json:"version"`
}

type Resolver interface {
	Resolve(rawIP string) Location
	Status() GeoStatus
}

type regionSearcher interface {
	Search(any) (string, error)
	Close()
}

// Database uses small concurrency-safe file searcher pools rather than keeping
// both complete databases in RAM. It does not make network requests.
type Database struct {
	mu     sync.RWMutex
	v4     regionSearcher
	v6     regionSearcher
	status GeoStatus
}

// New tolerates a missing or invalid database so analytics cannot prevent the
// panel from starting. Status tells the administrator which IP versions work.
func New(dir string) *Database {
	db := &Database{status: GeoStatus{Source: "ip2region", Version: "2026-10-18"}}
	if cfg, err := service.NewV4Config(service.VIndexCache, filepath.Join(dir, "ip2region_v4.xdb"), 4); err == nil {
		if searcher, err := service.NewIp2Region(cfg, nil); err == nil {
			db.v4 = searcher
		}
	}
	if cfg, err := service.NewV6Config(service.VIndexCache, filepath.Join(dir, "ip2region_v6.xdb"), 4); err == nil {
		if searcher, err := service.NewIp2Region(nil, cfg); err == nil {
			db.v6 = searcher
		}
	}
	db.updateStatus()
	return db
}

func (db *Database) updateStatus() {
	db.status.IPv4 = db.v4 != nil
	db.status.IPv6 = db.v6 != nil
	switch {
	case db.status.IPv4 && db.status.IPv6:
		db.status.Status = "ready"
	case db.status.IPv4 || db.status.IPv6:
		db.status.Status = "partial"
	default:
		db.status.Status = "unavailable"
	}
}

func (db *Database) Status() GeoStatus {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.status
}

func (db *Database) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.v4 != nil {
		db.v4.Close()
		db.v4 = nil
	}
	if db.v6 != nil {
		db.v6.Close()
		db.v6 = nil
	}
	db.updateStatus()
	return nil
}

func (db *Database) Resolve(rawIP string) Location {
	ip, err := ParseAddress(rawIP)
	if err != nil {
		return unknown("invalid_ip")
	}
	if !publicIP(ip) {
		return unknown("non_public_ip")
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	searcher := db.v6
	if ip.Is4() {
		searcher = db.v4
	}
	if searcher == nil {
		return unknown("database_unavailable")
	}
	region, err := searcher.Search(ip.String())
	if err != nil {
		return unknown("not_found")
	}
	return parseRegion(region)
}

// ParseAddress accepts a numeric IP or a socket address reported by a node.
// It never resolves hostnames, chooses an address from a forwarded chain, or
// treats an ambiguous unbracketed IPv6 suffix as a port.
func ParseAddress(raw string) (netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		if endpoint, endpointErr := netip.ParseAddrPort(raw); endpointErr == nil {
			ip, err = endpoint.Addr(), nil
		} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
			ip, err = netip.ParseAddr(raw[1 : len(raw)-1])
		}
	}
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("invalid numeric IP address")
	}
	return ip.Unmap(), nil
}

func unknown(reason string) Location {
	return Location{UnknownReason: reason}
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicIP(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// The pinned ip2region release uses country|province|city|ISP|ISO country code.
// Unknown data stays unknown; older incompatible layouts are not guessed.
func parseRegion(raw string) Location {
	parts := strings.Split(raw, "|")
	if len(parts) != 5 {
		return unknown("not_found")
	}
	code := strings.ToUpper(strings.TrimSpace(parts[4]))
	if len(code) != 2 || code < "AA" || code > "ZZ" || code == "ZZ" {
		return unknown("not_found")
	}
	region, err := language.ParseRegion(code)
	if err != nil || !region.IsCountry() {
		return unknown("not_found")
	}
	location := Location{CountryCode: code, CountryName: display.SimplifiedChinese.Regions().Name(region), Known: true}
	switch code {
	case "CN":
		location.ProvinceCode, location.ProvinceName = chinaProvince(parts[1])
	case "HK":
		location.CountryName = "中国香港"
		location.ProvinceCode, location.ProvinceName = "810000", "香港"
	case "MO":
		location.CountryName = "中国澳门"
		location.ProvinceCode, location.ProvinceName = "820000", "澳门"
	case "TW":
		location.CountryName = "中国台湾"
		location.ProvinceCode, location.ProvinceName = "710000", "台湾"
	}
	if location.ProvinceCode != "" {
		location.CityName = normalizeCityName(parts[2])
	}
	return location
}

// Keep the database's city evidence separate from map-boundary matching. An
// absent city must not turn into the province capital or a fabricated district.
func normalizeCityName(raw string) string {
	name := strings.TrimSpace(raw)
	switch strings.ToLower(name) {
	case "", "0", "未知", "未知城市", "城市未知", "unknown", "null", "n/a", "-":
		return ""
	}
	if len([]rune(name)) > 128 {
		return ""
	}
	return name
}

// NormalizeChinaCityName provides a stable province-local key without claiming
// a match against a particular map edition. Real city evidence stays visible in
// the ranking even when that city's polygon is missing from the map.
func NormalizeChinaCityName(raw string) string {
	name := normalizeCityName(raw)
	for _, suffix := range []string{"地区", "市"} {
		if strings.HasSuffix(name, suffix) && len([]rune(name)) > len([]rune(suffix)) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return name
}
