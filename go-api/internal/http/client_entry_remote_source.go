package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	usersvc "forest/go-api/internal/user"
)

type clientEntryRemoteResolver interface {
	ResolveRemoteIPs(ctx context.Context, group usersvc.ClientEntryGroup) ([]string, error)
}

type clientEntryRemoteCacheEntry struct {
	IPs       []string
	ExpiresAt time.Time
}

type httpClientEntryRemoteResolver struct {
	mu     sync.Mutex
	cache  map[string]clientEntryRemoteCacheEntry
	now    func() time.Time
	client *http.Client
}

type remoteLoginResponse struct {
	Code int    `json:"code"`
	Data string `json:"data"`
	Msg  string `json:"msg"`
}

type remoteNodeStatusResponse struct {
	Code int                     `json:"code"`
	Data []remoteNodeStatusGroup `json:"data"`
	Msg  string                  `json:"msg"`
}

type remoteNodeStatusGroup struct {
	Name    string                   `json:"name"`
	GID     int64                    `json:"gid"`
	Servers []remoteNodeStatusServer `json:"servers"`
}

type remoteNodeStatusServer struct {
	Name   string `json:"name"`
	Online bool   `json:"online"`
	IP4    string `json:"ip4"`
	IP6    string `json:"ip6"`
}

func newHTTPClientEntryRemoteResolver() clientEntryRemoteResolver {
	return &httpClientEntryRemoteResolver{
		cache: make(map[string]clientEntryRemoteCacheEntry),
		now:   time.Now,
		client: &http.Client{
			Timeout: 12 * time.Second,
		},
	}
}

func (r *httpClientEntryRemoteResolver) ResolveRemoteIPs(ctx context.Context, group usersvc.ClientEntryGroup) ([]string, error) {
	if !group.RemoteEnabled {
		return nil, nil
	}

	cacheKey, ttl := buildClientEntryRemoteCacheKey(group)
	now := r.now()

	r.mu.Lock()
	cached, cachedOK := r.cache[cacheKey]
	if cachedOK && now.Before(cached.ExpiresAt) {
		values := append([]string(nil), cached.IPs...)
		r.mu.Unlock()
		return values, nil
	}
	r.mu.Unlock()

	values, err := fetchClientEntryRemoteIPsOverHTTP(ctx, r.client, group)
	if err != nil {
		if cachedOK && len(cached.IPs) > 0 {
			return append([]string(nil), cached.IPs...), nil
		}
		return nil, err
	}

	r.mu.Lock()
	r.cache[cacheKey] = clientEntryRemoteCacheEntry{
		IPs:       append([]string(nil), values...),
		ExpiresAt: now.Add(ttl),
	}
	r.mu.Unlock()
	return values, nil
}

func buildClientEntryRemoteCacheKey(group usersvc.ClientEntryGroup) (string, time.Duration) {
	ttlSec := group.RemoteRefreshSec
	if ttlSec <= 0 {
		ttlSec = 300
	}
	payload, _ := json.Marshal(map[string]any{
		"host":    strings.TrimSpace(group.RemoteHost),
		"port":    group.RemoteSSHPort,
		"user":    strings.TrimSpace(group.RemoteSSHUser),
		"pass":    strings.TrimSpace(group.RemoteSSHPassword),
		"group":   strings.TrimSpace(group.RemoteGroupRef),
		"exclude": group.RemoteExcludeNames,
	})
	return string(payload), time.Duration(ttlSec) * time.Second
}

func fetchClientEntryRemoteIPsOverHTTP(ctx context.Context, client *http.Client, group usersvc.ClientEntryGroup) ([]string, error) {
	baseURL, err := normalizeClientEntryRemoteBaseURL(group.RemoteHost, group.RemoteSSHPort)
	if err != nil {
		return nil, err
	}

	token, err := remoteWebsiteLogin(ctx, client, baseURL, strings.TrimSpace(group.RemoteSSHUser), strings.TrimSpace(group.RemoteSSHPassword))
	if err != nil {
		return nil, err
	}

	statusPayload, err := remoteWebsiteNodeStatus(ctx, client, baseURL, token)
	if err != nil {
		return nil, err
	}

	targetID, targetNames := parseClientEntryRemoteGroupRef(group.RemoteGroupRef)
	excludedNames := buildClientEntryRemoteExcludeNameSet(group.RemoteExcludeNames)
	result := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	foundGroup := false

	for _, nodeGroup := range statusPayload.Data {
		if !matchClientEntryRemoteNodeGroup(nodeGroup, targetID, targetNames) {
			continue
		}
		foundGroup = true
		for _, server := range nodeGroup.Servers {
			if !server.Online {
				continue
			}
			serverName := strings.TrimSpace(server.Name)
			if _, excluded := excludedNames[serverName]; excluded {
				continue
			}
			result = appendClientEntryRemoteResolvedIP(result, seen, server.IP4)
			result = appendClientEntryRemoteResolvedIP(result, seen, server.IP6)
		}
	}

	if !foundGroup {
		return nil, fmt.Errorf("remote group not found: %s", strings.TrimSpace(group.RemoteGroupRef))
	}
	return normalizeRemoteResolvedIPs(result), nil
}

func normalizeClientEntryRemoteBaseURL(raw string, port int64) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("remote website url is empty")
	}
	if !strings.Contains(raw, "://") {
		switch {
		case port > 0 && port != 443 && port != 22 && !strings.Contains(raw, ":"):
			raw = "https://" + raw + ":" + strconv.FormatInt(port, 10)
		default:
			raw = "https://" + raw
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse remote website url: %w", err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("remote website url host is empty")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func remoteWebsiteLogin(ctx context.Context, client *http.Client, baseURL, username, password string) (string, error) {
	var payload remoteLoginResponse
	if err := doRemoteWebsiteJSONRequest(ctx, client, baseURL, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": strings.TrimSpace(username),
		"password": strings.TrimSpace(password),
	}, &payload); err != nil {
		return "", err
	}
	if payload.Code != 0 {
		return "", fmt.Errorf("remote login failed: %s", strings.TrimSpace(payload.Msg))
	}
	token := strings.TrimSpace(payload.Data)
	if token == "" {
		return "", fmt.Errorf("remote login token is empty")
	}
	return token, nil
}

func remoteWebsiteNodeStatus(ctx context.Context, client *http.Client, baseURL, token string) (remoteNodeStatusResponse, error) {
	var payload remoteNodeStatusResponse
	if err := doRemoteWebsiteJSONRequest(ctx, client, baseURL, http.MethodGet, "/api/v1/system/node/status", token, nil, &payload); err != nil {
		return remoteNodeStatusResponse{}, err
	}
	if payload.Code != 0 {
		return remoteNodeStatusResponse{}, fmt.Errorf("remote node status failed: %s", strings.TrimSpace(payload.Msg))
	}
	return payload, nil
}

func doRemoteWebsiteJSONRequest(ctx context.Context, client *http.Client, baseURL, method, path, token string, body any, target any) error {
	endpoint, err := joinRemoteWebsiteURL(baseURL, path)
	if err != nil {
		return err
	}

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal remote request body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("build remote request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", strings.TrimSpace(token))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request remote endpoint %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read remote endpoint %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote endpoint %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode remote endpoint %s: %w", path, err)
	}
	return nil
}

func joinRemoteWebsiteURL(baseURL, path string) (string, error) {
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse remote base url: %w", err)
	}
	reference, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse remote path: %w", err)
	}
	return parsedBase.ResolveReference(reference).String(), nil
}

func parseClientEntryRemoteGroupRef(raw string) (*int64, []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var targetID *int64
	if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
		targetID = &parsed
	} else {
		pattern := regexp.MustCompile(`#(\d+)`)
		if matches := pattern.FindStringSubmatch(raw); len(matches) == 2 {
			if parsed, err := strconv.ParseInt(matches[1], 10, 64); err == nil && parsed > 0 {
				targetID = &parsed
			}
		}
	}

	cleanName := strings.TrimSpace(regexp.MustCompile(`\s*\(#\d+\)\s*$`).ReplaceAllString(raw, ""))
	names := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, candidate := range []string{raw, cleanName} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		names = append(names, candidate)
	}
	return targetID, names
}

func matchClientEntryRemoteNodeGroup(group remoteNodeStatusGroup, targetID *int64, targetNames []string) bool {
	if targetID != nil && group.GID == *targetID {
		return true
	}
	groupName := strings.TrimSpace(group.Name)
	for _, candidate := range targetNames {
		if groupName == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

func buildClientEntryRemoteExcludeNameSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result[value] = struct{}{}
	}
	return result
}

func appendClientEntryRemoteResolvedIP(target []string, seen map[string]struct{}, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || net.ParseIP(raw) == nil {
		return target
	}
	if _, exists := seen[raw]; exists {
		return target
	}
	seen[raw] = struct{}{}
	return append(target, raw)
}

func normalizeRemoteResolvedIPs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || net.ParseIP(value) == nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		leftV4 := strings.Contains(result[i], ".")
		rightV4 := strings.Contains(result[j], ".")
		if leftV4 != rightV4 {
			return leftV4
		}
		return result[i] < result[j]
	})
	return result
}
