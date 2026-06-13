package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"sort"
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

type subscribeGuardEvent struct {
	Time    int64  `json:"time"`
	IP      string `json:"ip"`
	Token   string `json:"token"`
	UA      string `json:"ua"`
	Status  int    `json:"status"`
	Reason  string `json:"reason"`
	Blocked bool   `json:"blocked"`
}

var subscribeGuardRateState = struct {
	sync.Mutex
	buckets map[string]subscribeGuardBucket
}{buckets: map[string]subscribeGuardBucket{}}

var subscribeGuardEventState = struct {
	sync.Mutex
	events []subscribeGuardEvent
}{events: []subscribeGuardEvent{}}

func handleSubscribeGuard(w http.ResponseWriter, r *http.Request, cfg config.Config) bool {
	if !cfg.SubscribeGuardEnable {
		return false
	}

	clientIP := requestClientIP(r)
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	ua := strings.TrimSpace(r.UserAgent())

	if listMatchesIP(cfg.SubscribeGuardIPWhitelist, clientIP) {
		recordSubscribeGuardEvent(r, http.StatusOK, "whitelist", false)
		return false
	}

	if listMatchesIP(cfg.SubscribeGuardIPBlacklist, clientIP) {
		writeSubscribeGuardBlock(w, r, http.StatusForbidden, "ip")
		return true
	}

	if stringInList(cfg.SubscribeGuardTokenBlacklist, token) {
		writeSubscribeGuardBlock(w, r, http.StatusForbidden, "token")
		return true
	}

	if !listContainsFold(cfg.SubscribeGuardUAWhitelist, ua) {
		if cfg.SubscribeGuardBlockEmptyUA && ua == "" {
			writeSubscribeGuardBlock(w, r, http.StatusForbidden, "ua")
			return true
		}
		if listContainsFold(cfg.SubscribeGuardUABlacklist, ua) {
			writeSubscribeGuardBlock(w, r, http.StatusForbidden, "ua")
			return true
		}
		if cfg.SubscribeGuardBlockCrawlerUA && listContainsFold(defaultSubscribeCrawlerUA, ua) {
			writeSubscribeGuardBlock(w, r, http.StatusForbidden, "ua")
			return true
		}
	}

	if cfg.SubscribeGuardRateLimitPerMinute > 0 && !allowSubscribeGuardRate(clientIP, cfg.SubscribeGuardRateLimitPerMinute) {
		writeSubscribeGuardBlock(w, r, http.StatusTooManyRequests, "rate_limit")
		return true
	}

	recordSubscribeGuardEvent(r, http.StatusOK, "pass", false)
	return false
}

func writeSubscribeGuardBlock(w http.ResponseWriter, r *http.Request, status int, reason string) {
	recordSubscribeGuardEvent(r, status, reason, true)
	w.Header().Set("X-Subscribe-Guard", reason)
	writePlainText(w, status, "Forbidden")
}

func recordSubscribeGuardEvent(r *http.Request, status int, reason string, blocked bool) {
	event := subscribeGuardEvent{
		Time:    time.Now().Unix(),
		IP:      requestClientIP(r),
		Token:   strings.TrimSpace(r.URL.Query().Get("token")),
		UA:      strings.TrimSpace(r.UserAgent()),
		Status:  status,
		Reason:  reason,
		Blocked: blocked,
	}
	subscribeGuardEventState.Lock()
	defer subscribeGuardEventState.Unlock()
	subscribeGuardEventState.events = append(subscribeGuardEventState.events, event)
	if len(subscribeGuardEventState.events) > 1000 {
		subscribeGuardEventState.events = append([]subscribeGuardEvent(nil), subscribeGuardEventState.events[len(subscribeGuardEventState.events)-1000:]...)
	}
}

func subscribeGuardStatsSnapshot() map[string]any {
	subscribeGuardEventState.Lock()
	events := append([]subscribeGuardEvent(nil), subscribeGuardEventState.events...)
	subscribeGuardEventState.Unlock()

	total := int64(len(events))
	blocked := int64(0)
	reasonCounts := map[string]int64{}
	ipCounts := map[string]int64{}
	tokenCounts := map[string]int64{}
	uaCounts := map[string]int64{}
	for _, event := range events {
		if event.Blocked {
			blocked++
		}
		if event.Reason != "" {
			reasonCounts[event.Reason]++
		}
		if event.IP != "" {
			ipCounts[event.IP]++
		}
		if event.Token != "" {
			tokenCounts[event.Token]++
		}
		if event.UA != "" {
			uaCounts[event.UA]++
		}
	}

	recent := make([]subscribeGuardEvent, 0, minInt(len(events), 100))
	for i := len(events) - 1; i >= 0 && len(recent) < 100; i-- {
		recent = append(recent, events[i])
	}

	return map[string]any{
		"total":         total,
		"allowed":       total - blocked,
		"blocked":       blocked,
		"reason_counts": reasonCounts,
		"top_ips":       topSubscribeGuardItems(ipCounts, "ip", 20),
		"top_tokens":    topSubscribeGuardItems(tokenCounts, "token", 20),
		"top_uas":       topSubscribeGuardItems(uaCounts, "ua", 20),
		"recent":        recent,
	}
}

func resetSubscribeGuardStateForTest() {
	subscribeGuardRateState.Lock()
	subscribeGuardRateState.buckets = map[string]subscribeGuardBucket{}
	subscribeGuardRateState.Unlock()
	subscribeGuardEventState.Lock()
	subscribeGuardEventState.events = nil
	subscribeGuardEventState.Unlock()
}

func topSubscribeGuardItems(counts map[string]int64, key string, limit int) []map[string]any {
	type item struct {
		value string
		count int64
	}
	items := make([]item, 0, len(counts))
	for value, count := range counts {
		items = append(items, item{value: value, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].value < items[j].value
		}
		return items[i].count > items[j].count
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{key: item.value, "count": item.count})
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func requestClientIP(r *http.Request) string {
	for _, header := range []string{"CF-Connecting-IP", "True-Client-IP", "X-Client-IP"} {
		if ip := strings.TrimSpace(r.Header.Get(header)); ip != "" {
			return ip
		}
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
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
