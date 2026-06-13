package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/config"
)

var defaultSubscribeCrawlerUA = []string{
	"curl",
	"wget",
	"python",
	"go-http-client",
	"java/",
	"okhttp",
	"httpclient",
	"node-fetch",
	"axios",
	"postman",
}

type subscribeGuardBucket struct {
	windowStart time.Time
	count       int64
}

var subscribeGuardRateState = struct {
	sync.Mutex
	buckets map[string]subscribeGuardBucket
}{buckets: map[string]subscribeGuardBucket{}}

func handleSubscribeGuard(w http.ResponseWriter, r *http.Request, cfg config.Config) bool {
	if !cfg.SubscribeGuardEnable {
		return false
	}

	clientIP := requestClientIP(r)
	if listMatchesIP(cfg.SubscribeGuardIPWhitelist, clientIP) {
		return false
	}

	if listMatchesIP(cfg.SubscribeGuardIPBlacklist, clientIP) {
		writeSubscribeGuardBlock(w, http.StatusForbidden, "ip")
		return true
	}

	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if stringInList(cfg.SubscribeGuardTokenBlacklist, token) {
		writeSubscribeGuardBlock(w, http.StatusForbidden, "token")
		return true
	}

	ua := strings.TrimSpace(r.UserAgent())
	if !listContainsFold(cfg.SubscribeGuardUAWhitelist, ua) {
		if cfg.SubscribeGuardBlockEmptyUA && ua == "" {
			writeSubscribeGuardBlock(w, http.StatusForbidden, "ua")
			return true
		}
		if listContainsFold(cfg.SubscribeGuardUABlacklist, ua) {
			writeSubscribeGuardBlock(w, http.StatusForbidden, "ua")
			return true
		}
		if cfg.SubscribeGuardBlockCrawlerUA && listContainsFold(defaultSubscribeCrawlerUA, ua) {
			writeSubscribeGuardBlock(w, http.StatusForbidden, "ua")
			return true
		}
	}

	if cfg.SubscribeGuardRateLimitPerMinute > 0 && !allowSubscribeGuardRate(clientIP, cfg.SubscribeGuardRateLimitPerMinute) {
		writeSubscribeGuardBlock(w, http.StatusTooManyRequests, "rate_limit")
		return true
	}

	return false
}

func writeSubscribeGuardBlock(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("X-Subscribe-Guard", reason)
	writePlainText(w, status, "Forbidden")
}

func requestClientIP(r *http.Request) string {
	for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		if ip := strings.TrimSpace(r.Header.Get(header)); ip != "" {
			return ip
		}
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func allowSubscribeGuardRate(ip string, limit int64) bool {
	if ip == "" {
		ip = "-"
	}
	now := time.Now()
	window := now.Truncate(time.Minute)

	subscribeGuardRateState.Lock()
	defer subscribeGuardRateState.Unlock()

	bucket := subscribeGuardRateState.buckets[ip]
	if bucket.windowStart.IsZero() || !bucket.windowStart.Equal(window) {
		bucket = subscribeGuardBucket{windowStart: window}
	}
	bucket.count++
	subscribeGuardRateState.buckets[ip] = bucket
	return bucket.count <= limit
}

func stringInList(values []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}

func listContainsFold(values []string, haystack string) bool {
	haystack = strings.ToLower(strings.TrimSpace(haystack))
	if haystack == "" {
		return false
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && strings.Contains(haystack, value) {
			return true
		}
	}
	return false
}

func listMatchesIP(values []string, ip string) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return false
	}
	parsedIP, ipErr := netip.ParseAddr(ip)
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			prefix, err := netip.ParsePrefix(value)
			if err == nil && ipErr == nil && prefix.Contains(parsedIP) {
				return true
			}
			continue
		}
		if value == ip {
			return true
		}
	}
	return false
}
