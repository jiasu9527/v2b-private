package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/config"
	usersvc "forest/go-api/internal/user"
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
	Time           int64  `json:"time"`
	IP             string `json:"ip"`
	Token          string `json:"token"`
	UserID         int64  `json:"user_id,omitempty"`
	CanonicalToken string `json:"canonical_token,omitempty"`
	UA             string `json:"ua"`
	Status         int    `json:"status"`
	Reason         string `json:"reason"`
	Blocked        bool   `json:"blocked"`
}

type subscribeGuardUserEvent struct {
	Time    int64  `json:"time"`
	IP      string `json:"ip"`
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

var subscribeGuardCleanupState = struct {
	sync.Mutex
	lastRun time.Time
}{}

func handleSubscribeGuard(w http.ResponseWriter, r *http.Request, cfg config.Config, userService usersvc.Service) (bool, string) {
	if !cfg.SubscribeGuardEnable {
		return false, ""
	}

	clientIP := requestClientIP(r)
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	ua := strings.TrimSpace(r.UserAgent())

	if listMatchesIP(cfg.SubscribeGuardIPWhitelist, clientIP) {
		return false, "whitelist"
	}

	if listMatchesIP(cfg.SubscribeGuardIPBlacklist, clientIP) {
		writeSubscribeGuardBlock(w, r, cfg, http.StatusForbidden, "ip", 0)
		return true, ""
	}

	if stringInList(cfg.SubscribeGuardTokenBlacklist, token) {
		writeSubscribeGuardBlock(w, r, cfg, http.StatusForbidden, "token", 0)
		return true, ""
	}

	userID := peekSubscribeGuardUserID(r, userService)

	if !listContainsFold(cfg.SubscribeGuardUAWhitelist, ua) {
		if cfg.SubscribeGuardBlockEmptyUA && ua == "" {
			writeSubscribeGuardBlock(w, r, cfg, http.StatusForbidden, "ua", userID)
			return true, ""
		}
		if listContainsFold(cfg.SubscribeGuardUABlacklist, ua) {
			writeSubscribeGuardBlock(w, r, cfg, http.StatusForbidden, "ua", userID)
			return true, ""
		}
		if cfg.SubscribeGuardBlockCrawlerUA && listContainsFold(defaultSubscribeCrawlerUA, ua) {
			writeSubscribeGuardBlock(w, r, cfg, http.StatusForbidden, "ua", userID)
			return true, ""
		}
	}

	if cfg.SubscribeGuardRateLimitPerMinute > 0 && !allowSubscribeGuardRate(clientIP, cfg.SubscribeGuardRateLimitPerMinute) {
		writeSubscribeGuardBlock(w, r, cfg, http.StatusTooManyRequests, "rate_limit", userID)
		return true, ""
	}

	return false, "pass"
}

type subscribeGuardUserResolver interface {
	PeekClientUserID(context.Context, string) (int64, error)
}

func peekSubscribeGuardUserID(r *http.Request, userService usersvc.Service) int64 {
	if userService == nil {
		return 0
	}
	resolver, ok := userService.(subscribeGuardUserResolver)
	if !ok {
		return 0
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		return 0
	}
	userID, err := resolver.PeekClientUserID(r.Context(), token)
	if err != nil || userID <= 0 {
		return 0
	}
	return userID
}

func writeSubscribeGuardBlock(w http.ResponseWriter, r *http.Request, cfg config.Config, status int, reason string, userID int64) {
	recordSubscribeGuardEvent(cfg, r, status, reason, true, userID)
	w.Header().Set("X-Subscribe-Guard", reason)
	writePlainText(w, status, "Forbidden")
}

func recordSubscribeGuardEvent(cfg config.Config, r *http.Request, status int, reason string, blocked bool, values ...any) {
	var userID int64
	if len(values) > 0 {
		switch value := values[0].(type) {
		case int64:
			userID = value
		case int:
			userID = int64(value)
		}
	}
	canonicalToken := ""
	if len(values) > 1 {
		canonicalToken, _ = values[1].(string)
		canonicalToken = strings.TrimSpace(canonicalToken)
	}
	event := subscribeGuardEvent{
		Time:           time.Now().Unix(),
		IP:             requestClientIP(r),
		Token:          strings.TrimSpace(r.URL.Query().Get("token")),
		UserID:         userID,
		CanonicalToken: canonicalToken,
		UA:             strings.TrimSpace(r.UserAgent()),
		Status:         status,
		Reason:         reason,
		Blocked:        blocked,
	}
	subscribeGuardEventState.Lock()
	defer subscribeGuardEventState.Unlock()
	subscribeGuardEventState.events = append(subscribeGuardEventState.events, event)
	if len(subscribeGuardEventState.events) > 1000 {
		subscribeGuardEventState.events = append([]subscribeGuardEvent(nil), subscribeGuardEventState.events[len(subscribeGuardEventState.events)-1000:]...)
	}
	_ = appendSubscribeGuardLogEvent(cfg, event)
	maybeCleanupSubscribeGuardLog(cfg, time.Now())
}

func subscribeGuardStatsSnapshot(cfg config.Config) map[string]any {
	events := subscribeGuardEventsSnapshot(cfg)

	total := int64(len(events))
	blocked := int64(0)
	reasonCounts := map[string]int64{}
	ipCounts := map[string]int64{}
	tokenCounts := map[string]int64{}
	uaCounts := map[string]int64{}
	userCounts := map[int64]int64{}
	userUAs := map[int64]map[string]struct{}{}
	userIPs := map[int64]map[string]struct{}{}
	legacyTokenCounts := map[string]int64{}
	legacyTokenUAs := map[string]map[string]struct{}{}
	legacyTokenIPs := map[string]map[string]struct{}{}
	legacyTokenCanonical := map[string]string{}
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
		if event.UserID > 0 {
			userCounts[event.UserID]++
			if _, ok := userUAs[event.UserID]; !ok {
				userUAs[event.UserID] = map[string]struct{}{}
			}
			if _, ok := userIPs[event.UserID]; !ok {
				userIPs[event.UserID] = map[string]struct{}{}
			}
			if event.UA != "" {
				userUAs[event.UserID][event.UA] = struct{}{}
			}
			if event.IP != "" {
				userIPs[event.UserID][event.IP] = struct{}{}
			}
		} else if event.Token != "" {
			legacyTokenCounts[event.Token]++
			if event.CanonicalToken != "" {
				legacyTokenCanonical[event.Token] = event.CanonicalToken
			}
			if _, ok := legacyTokenUAs[event.Token]; !ok {
				legacyTokenUAs[event.Token] = map[string]struct{}{}
			}
			if _, ok := legacyTokenIPs[event.Token]; !ok {
				legacyTokenIPs[event.Token] = map[string]struct{}{}
			}
			if event.UA != "" {
				legacyTokenUAs[event.Token][event.UA] = struct{}{}
			}
			if event.IP != "" {
				legacyTokenIPs[event.Token][event.IP] = struct{}{}
			}
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
		"total":                  total,
		"allowed":                total - blocked,
		"blocked":                blocked,
		"reason_counts":          reasonCounts,
		"top_ips":                topSubscribeGuardItems(ipCounts, "ip", 20),
		"top_tokens":             topSubscribeGuardItems(tokenCounts, "token", 20),
		"top_subscribe_user_ids": topSubscribeGuardUserIDItems(userCounts, userUAs, userIPs, 20),
		"top_subscribe_tokens":   topSubscribeGuardTokenItems(legacyTokenCounts, legacyTokenUAs, legacyTokenIPs, legacyTokenCanonical, 20),
		"top_uas":                topSubscribeGuardItems(uaCounts, "ua", 20),
		"recent":                 recent,
	}
}

func subscribeGuardEventsSnapshot(cfg config.Config) []subscribeGuardEvent {
	maybeCleanupSubscribeGuardLog(cfg, time.Now())
	subscribeGuardEventState.Lock()
	events := append([]subscribeGuardEvent(nil), subscribeGuardEventState.events...)
	subscribeGuardEventState.Unlock()
	if persisted := readSubscribeGuardLogEvents(cfg); len(persisted) > 0 {
		events = persisted
	}
	return events
}

func subscribeGuardUserStatsSnapshot(cfg config.Config, userID int64, token string) map[string]any {
	token = strings.TrimSpace(token)
	events := subscribeGuardEventsSnapshot(cfg)
	matched := make([]subscribeGuardEvent, 0)
	ipCounts := map[string]int64{}
	uaCounts := map[string]int64{}
	reasonCounts := map[string]int64{}
	blocked := int64(0)
	for _, event := range events {
		matchesUserID := userID > 0 && event.UserID == userID
		matchesLegacyToken := event.UserID == 0 && token != "" && (event.Token == token || event.CanonicalToken == token)
		if !matchesUserID && !matchesLegacyToken {
			continue
		}
		matched = append(matched, event)
		if event.Blocked {
			blocked++
		}
		if event.Reason != "" {
			reasonCounts[event.Reason]++
		}
		if event.IP != "" {
			ipCounts[event.IP]++
		}
		if event.UA != "" {
			uaCounts[event.UA]++
		}
	}
	recent := make([]subscribeGuardUserEvent, 0, minInt(len(matched), 100))
	for index := len(matched) - 1; index >= 0 && len(recent) < 100; index-- {
		event := matched[index]
		recent = append(recent, subscribeGuardUserEvent{
			Time: event.Time, IP: event.IP, UA: event.UA, Status: event.Status, Reason: event.Reason, Blocked: event.Blocked,
		})
	}
	return map[string]any{
		"total":         int64(len(matched)),
		"allowed":       int64(len(matched)) - blocked,
		"blocked":       blocked,
		"reason_counts": reasonCounts,
		"ip_count":      int64(len(ipCounts)),
		"ua_count":      int64(len(uaCounts)),
		"ips":           topSubscribeGuardItems(ipCounts, "ip", minInt(len(ipCounts), 100)),
		"uas":           topSubscribeGuardItems(uaCounts, "ua", minInt(len(uaCounts), 100)),
		"recent":        recent,
	}
}

func topSubscribeGuardUserIDItems(counts map[int64]int64, uas map[int64]map[string]struct{}, ips map[int64]map[string]struct{}, limit int) []map[string]any {
	type item struct {
		userID int64
		count  int64
	}
	items := make([]item, 0, len(counts))
	for userID, count := range counts {
		items = append(items, item{userID: userID, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].userID < items[j].userID
		}
		return items[i].count > items[j].count
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]map[string]any, 0, len(items))
	for _, current := range items {
		uaValues := sortedSubscribeGuardSet(uas[current.userID])
		ipValues := sortedSubscribeGuardSet(ips[current.userID])
		result = append(result, map[string]any{
			"user_id": current.userID, "count": current.count,
			"ua_count": int64(len(uaValues)), "uas": uaValues,
			"ip_count": int64(len(ipValues)), "ips": ipValues,
		})
	}
	return result
}

func sortedSubscribeGuardSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func topSubscribeGuardTokenItems(counts map[string]int64, uas map[string]map[string]struct{}, ips map[string]map[string]struct{}, canonicalTokens map[string]string, limit int) []map[string]any {
	items := topSubscribeGuardItems(counts, "token", limit)
	for _, item := range items {
		token, _ := item["token"].(string)
		uaValues := sortedSubscribeGuardSet(uas[token])
		ipValues := sortedSubscribeGuardSet(ips[token])

		item["ua_count"] = int64(len(uaValues))
		item["uas"] = uaValues
		item["ip_count"] = int64(len(ipValues))
		item["ips"] = ipValues
		if canonicalToken := strings.TrimSpace(canonicalTokens[token]); canonicalToken != "" {
			item["canonical_token"] = canonicalToken
		}
	}
	return items
}

func subscribeGuardLogPath(cfg config.Config) string {
	publicDir := strings.TrimSpace(cfg.PublicDir)
	if publicDir == "" {
		return filepath.Join("storage", "logs", "subscribe_guard.jsonl")
	}
	return filepath.Join(publicDir, "..", "storage", "logs", "subscribe_guard.jsonl")
}

func appendSubscribeGuardLogEvent(cfg config.Config, event subscribeGuardEvent) error {
	path := subscribeGuardLogPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

func readSubscribeGuardLogEvents(cfg config.Config) []subscribeGuardEvent {
	path := subscribeGuardLogPath(cfg)
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	events := make([]subscribeGuardEvent, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event subscribeGuardEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

func writeSubscribeGuardEvents(cfg config.Config, events []subscribeGuardEvent) error {
	path := subscribeGuardLogPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(raw, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func cleanupSubscribeGuardLog(cfg config.Config, now time.Time) error {
	if cfg.SubscribeGuardLogKeepDays <= 0 {
		return nil
	}
	path := subscribeGuardLogPath(cfg)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	cutoff := now.Add(-time.Duration(cfg.SubscribeGuardLogKeepDays) * 24 * time.Hour).Unix()
	events := readSubscribeGuardLogEvents(cfg)
	filtered := events[:0]
	for _, event := range events {
		if event.Time == 0 || event.Time >= cutoff {
			filtered = append(filtered, event)
		}
	}
	if len(filtered) == len(events) {
		return nil
	}
	return writeSubscribeGuardEvents(cfg, filtered)
}

func maybeCleanupSubscribeGuardLog(cfg config.Config, now time.Time) {
	if cfg.SubscribeGuardLogKeepDays <= 0 {
		return
	}
	subscribeGuardCleanupState.Lock()
	if !subscribeGuardCleanupState.lastRun.IsZero() && now.Sub(subscribeGuardCleanupState.lastRun) < time.Hour {
		subscribeGuardCleanupState.Unlock()
		return
	}
	subscribeGuardCleanupState.lastRun = now
	subscribeGuardCleanupState.Unlock()
	_ = cleanupSubscribeGuardLog(cfg, now)
}

func resetSubscribeGuardStateForTest() {
	subscribeGuardRateState.Lock()
	subscribeGuardRateState.buckets = map[string]subscribeGuardBucket{}
	subscribeGuardRateState.Unlock()
	subscribeGuardEventState.Lock()
	subscribeGuardEventState.events = nil
	subscribeGuardEventState.Unlock()
	subscribeGuardCleanupState.Lock()
	subscribeGuardCleanupState.lastRun = time.Time{}
	subscribeGuardCleanupState.Unlock()
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
