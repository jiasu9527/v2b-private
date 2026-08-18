package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
	"forest/go-api/internal/subscribelink"
	"forest/go-api/internal/user"
)

type fakeClientEntryRemoteResolver struct {
	ipsByCode map[string][]string
	lastGroup user.ClientEntryGroup
	err       error
}

func (f *fakeClientEntryRemoteResolver) ResolveRemoteIPs(_ context.Context, group user.ClientEntryGroup) ([]string, error) {
	f.lastGroup = group
	if f.err != nil {
		return nil, f.err
	}
	values := f.ipsByCode[group.Code]
	result := make([]string, 0, len(values))
	result = append(result, values...)
	return result, nil
}

func TestRouterClientAppGetVersionEndpoint(t *testing.T) {
	t.Setenv("WINDOWS_VERSION", "1.2.3")
	t.Setenv("WINDOWS_DOWNLOAD_URL", "https://example.com/windows.exe")
	t.Setenv("MACOS_VERSION", "2.3.4")
	t.Setenv("MACOS_DOWNLOAD_URL", "https://example.com/macos.dmg")
	t.Setenv("ANDROID_VERSION", "3.4.5")
	t.Setenv("ANDROID_DOWNLOAD_URL", "https://example.com/android.apk")

	router := NewRouter(config.Load())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/app/getVersion", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", payload["data"])
	}
	if data["windows_version"] != "1.2.3" || data["android_download_url"] != "https://example.com/android.apk" {
		t.Fatalf("unexpected version payload: %#v", data)
	}
}

func TestRouterClientAppGetConfigEndpoint(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
		},
	}
	router := NewRouter(config.Config{PublicDir: "../public"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/app/getConfig?token=token-1", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "yaml") {
		t.Fatalf("expected yaml content type, got %q", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "VMess-1") || !strings.Contains(body, "proxies:") {
		t.Fatalf("expected generated clash app config, got %q", body)
	}
	if userService.lastClientToken != "token-1" || userService.lastServerUA != "" {
		t.Fatalf("unexpected client auth/request state: token=%q ua=%q", userService.lastClientToken, userService.lastServerUA)
	}
}

func TestRouterUserForestRuntimeProfileEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		subscribe: user.Subscribe{
			Token: "client-token-1",
		},
		servers: []map[string]any{
			{
				"id":      int64(11),
				"type":    "vmess",
				"name":    "JP-1",
				"host":    "jp.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "jp.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "jp.example.com",
					},
				},
			},
			{
				"id":          int64(12),
				"type":        "trojan",
				"name":        "US-1",
				"host":        "us.example.com",
				"port":        int64(443),
				"network":     "tcp",
				"server_name": "us.example.com",
			},
		},
		clientEntryGroups: []user.ClientEntryGroup{
			{
				ID:              int64(7),
				Code:            "asia",
				Name:            "Asia",
				DisplayName:     "Asia Entry",
				Strategy:        "sticky-low-latency",
				HideMemberNodes: true,
				IPs: []user.ClientEntryGroupIP{
					{IP: "1.1.1.1"},
					{IP: "8.8.8.8"},
				},
				Members: []user.ClientEntryGroupMember{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	router := NewRouter(
		config.Config{AppURL: "https://panel.example.com"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/forest/runtime-profile?cap=entry-provider-v1", nil)
	req.Header.Set("Authorization", "jwt-user")
	req.Header.Set("User-Agent", "forest-desktop/1.0")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", payload["data"])
	}
	if data["capability"] != "entry-provider-v1" {
		t.Fatalf("unexpected capability: %#v", data["capability"])
	}
	entryGroups, ok := data["entry_groups"].([]any)
	if !ok || len(entryGroups) != 1 {
		t.Fatalf("expected one entry group, got %#v", data["entry_groups"])
	}
	group, ok := entryGroups[0].(map[string]any)
	if !ok {
		t.Fatalf("expected entry group object, got %#v", entryGroups[0])
	}
	if group["code"] != "asia" || group["display_name"] != "Asia Entry" {
		t.Fatalf("unexpected runtime entry group: %#v", group)
	}
	if group["strategy"] != "ordered-fallback" {
		t.Fatalf("unexpected normalized runtime strategy: %#v", group["strategy"])
	}
	if group["provider_url"] != "https://panel.example.com/api/v1/client/forest/entry-provider?token=client-token-1&code=asia" {
		t.Fatalf("unexpected provider_url: %#v", group["provider_url"])
	}
	memberNames, ok := group["member_names"].([]any)
	if !ok || len(memberNames) != 1 || memberNames[0] != "JP-1" {
		t.Fatalf("unexpected member names: %#v", group["member_names"])
	}
	if userService.lastServerUA != "forest-desktop/1.0" {
		t.Fatalf("expected runtime profile to forward ua, got %q", userService.lastServerUA)
	}
}

func TestRouterClientForestEntryProviderEndpoint(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: int64(10),
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{
			{
				"id":      int64(11),
				"type":    "vmess",
				"name":    "JP-1",
				"host":    "jp.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "jp.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "jp.example.com",
					},
				},
			},
			{
				"id":          int64(12),
				"type":        "trojan",
				"name":        "US-1",
				"host":        "us.example.com",
				"port":        int64(443),
				"network":     "tcp",
				"server_name": "us.example.com",
			},
		},
		clientEntryGroups: []user.ClientEntryGroup{
			{
				ID:          int64(7),
				Code:        "asia",
				Name:        "Asia",
				DisplayName: "Asia Entry",
				Strategy:    "sticky-low-latency",
				IPs: []user.ClientEntryGroupIP{
					{IP: "1.1.1.1"},
					{IP: "8.8.8.8"},
				},
				Members: []user.ClientEntryGroupMember{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	router := NewRouter(config.Config{}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/forest/entry-provider?token=token-1&code=asia", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "yaml") {
		t.Fatalf("expected yaml content type, got %q", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "proxies:") || !strings.Contains(body, "JP-1") {
		t.Fatalf("expected forest provider yaml, got %q", body)
	}
	if strings.Contains(body, "US-1") {
		t.Fatalf("expected provider yaml to exclude non-member nodes, got %q", body)
	}
	if !strings.Contains(body, "1.1.1.1") || !strings.Contains(body, "8.8.8.8") {
		t.Fatalf("expected provider yaml to include entry ips, got %q", body)
	}
	if userService.lastClientToken != "token-1" {
		t.Fatalf("expected provider endpoint to use client token, got %q", userService.lastClientToken)
	}
}

func TestRouterUserForestRuntimeProfileEndpointIncludesRemoteOnlyEntryGroup(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		subscribe: user.Subscribe{
			Token: "client-token-1",
		},
		servers: []map[string]any{
			{
				"id":      int64(11),
				"type":    "vmess",
				"name":    "JP-1",
				"host":    "jp.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "jp.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "jp.example.com",
					},
				},
			},
		},
		clientEntryGroups: []user.ClientEntryGroup{
			{
				ID:                int64(7),
				Code:              "asia",
				Name:              "Asia",
				DisplayName:       "Asia Entry",
				Strategy:          "sticky-low-latency",
				HideMemberNodes:   true,
				RemoteEnabled:     true,
				RemoteHost:        "192.0.2.10",
				RemoteSSHPort:     2222,
				RemoteSSHUser:     "root",
				RemoteSSHPassword: "secret",
				RemoteGroupRef:    "专线直出 (#15)",
				RemoteExcludeNames: []string{
					"alice",
				},
				RemoteRefreshSec: 300,
				Members: []user.ClientEntryGroupMember{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	resolver := &fakeClientEntryRemoteResolver{
		ipsByCode: map[string][]string{
			"asia": {"8.8.8.8"},
		},
	}
	router := NewRouter(
		config.Config{AppURL: "https://panel.example.com"},
		WithSessionService(sessionService),
		WithUserService(userService),
		WithClientEntryRemoteResolver(resolver),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/forest/runtime-profile?cap=entry-provider-v1", nil)
	req.Header.Set("Authorization", "jwt-user")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", payload["data"])
	}
	entryGroups, ok := data["entry_groups"].([]any)
	if !ok || len(entryGroups) != 1 {
		t.Fatalf("expected one remote-backed entry group, got %#v", data["entry_groups"])
	}
	if resolver.lastGroup.Code != "asia" || resolver.lastGroup.RemoteGroupRef != "专线直出 (#15)" || len(resolver.lastGroup.RemoteExcludeNames) != 1 || resolver.lastGroup.RemoteExcludeNames[0] != "alice" {
		t.Fatalf("unexpected resolver contract: %#v", resolver.lastGroup)
	}
}

func TestRouterClientForestEntryProviderEndpointMergesRemoteIPs(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: int64(10),
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{
			{
				"id":      int64(11),
				"type":    "vmess",
				"name":    "JP-1",
				"host":    "jp.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "jp.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "jp.example.com",
					},
				},
			},
		},
		clientEntryGroups: []user.ClientEntryGroup{
			{
				ID:                int64(7),
				Code:              "asia",
				Name:              "Asia",
				DisplayName:       "Asia Entry",
				Strategy:          "sticky-low-latency",
				RemoteEnabled:     true,
				RemoteHost:        "192.0.2.10",
				RemoteSSHPort:     2222,
				RemoteSSHUser:     "root",
				RemoteSSHPassword: "secret",
				RemoteGroupRef:    "专线直出 (#15)",
				RemoteExcludeNames: []string{
					"alice",
				},
				RemoteRefreshSec: 300,
				IPs: []user.ClientEntryGroupIP{
					{IP: "1.1.1.1"},
				},
				Members: []user.ClientEntryGroupMember{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	resolver := &fakeClientEntryRemoteResolver{
		ipsByCode: map[string][]string{
			"asia": {"8.8.8.8", "1.1.1.1"},
		},
	}
	router := NewRouter(
		config.Config{},
		WithUserService(userService),
		WithClientEntryRemoteResolver(resolver),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/forest/entry-provider?token=token-1&code=asia", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "JP-1 @1.1.1.1") || !strings.Contains(body, "JP-1 @8.8.8.8") {
		t.Fatalf("expected provider yaml to include merged manual + remote ips, got %q", body)
	}
	if strings.Count(body, "JP-1 @1.1.1.1") != 1 {
		t.Fatalf("expected provider yaml to dedupe duplicate ips, got %q", body)
	}
	if resolver.lastGroup.Code != "asia" || resolver.lastGroup.RemoteGroupRef != "专线直出 (#15)" {
		t.Fatalf("unexpected resolver contract: %#v", resolver.lastGroup)
	}
}

func TestRouterClientForestEntryProviderEndpointKeepsManualDomains(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: int64(10),
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{
			{
				"id":      int64(11),
				"type":    "vmess",
				"name":    "JP-1",
				"host":    "jp.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "jp.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "jp.example.com",
					},
				},
			},
		},
		clientEntryGroups: []user.ClientEntryGroup{
			{
				ID:          int64(7),
				Code:        "asia",
				Name:        "Asia",
				DisplayName: "Asia Entry",
				Strategy:    "ordered-fallback",
				IPs: []user.ClientEntryGroupIP{
					{IP: "entry-a.example.com"},
				},
				Members: []user.ClientEntryGroupMember{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	router := NewRouter(config.Config{}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/forest/entry-provider?token=token-1&code=asia", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "entry-a.example.com") {
		t.Fatalf("expected provider yaml to keep manual domain entry, got %q", body)
	}
}

func TestRouterClientSubscribeEndpoint(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
			{
				"type":        "trojan",
				"name":        "Trojan-1",
				"host":        "trojan.example.com",
				"port":        int64(443),
				"network":     "tcp",
				"tls":         int64(1),
				"server_name": "trojan.example.com",
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("subscription-userinfo"); got != "upload=11; download=22; total=100; expire=1234567890" {
		t.Fatalf("unexpected subscription-userinfo header: %q", got)
	}
	if got := rec.Header().Get("content-disposition"); got != "" {
		t.Fatalf("expected general subscribe to avoid attachment header, got %q", got)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("expected base64 body: %v", err)
	}
	body := string(decoded)
	if !strings.Contains(body, "vmess://") || !strings.Contains(body, "trojan://") {
		t.Fatalf("unexpected subscribe payload: %q", body)
	}
}

func TestRouterClientSubscribeGuardBlocksTokenBlacklistBeforeUserLookup(t *testing.T) {
	userService := &fakeUserService{resolvedClientUserID: 10, subscribe: user.Subscribe{Email: "guard-user@example.com"}}
	router := NewRouter(config.Config{
		PublicDir:                        "../public",
		SubscribeGuardEnable:             true,
		SubscribeGuardTokenBlacklist:     []string{"token-1"},
		SubscribeGuardRateLimitPerMinute: 0,
	}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.Header.Set("User-Agent", "ClashMeta/1.0")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if userService.lastClientToken != "" {
		t.Fatalf("expected token blacklist to skip both peek and auth lookup, got token %q", userService.lastClientToken)
	}
	if reason := rec.Header().Get("X-Subscribe-Guard"); reason != "token" {
		t.Fatalf("expected token block reason header, got %q", reason)
	}
}

func TestRouterClientSubscribeGuardRecordsResolvedUserID(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userService := &fakeUserService{resolvedClientUserID: 10, peekedClientUserID: 10}
	router := NewRouter(config.Config{
		PublicDir:                        publicDir,
		SubscribeGuardEnable:             true,
		SubscribeGuardUABlacklist:        []string{"curl"},
		SubscribeGuardLogKeepDays:        7,
		SubscribeGuardRateLimitPerMinute: 0,
	}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=dynamic-token", nil)
	req.Header.Set("User-Agent", "curl/8.0")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	events := subscribeGuardEventsSnapshot(config.Config{PublicDir: publicDir, SubscribeGuardLogKeepDays: 7})
	if len(events) != 1 || events[0].UserID != 10 || events[0].Token != "dynamic-token" {
		t.Fatalf("expected resolved user id in guard event, got %#v", events)
	}
}

func TestSubscribeGuardRequestClientIPPrefersForwardedForOverProxyRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("X-Real-IP", "198.51.100.10")
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.10")

	if ip := requestClientIP(req); ip != "203.0.113.9" {
		t.Fatalf("expected real client ip from X-Forwarded-For, got %q", ip)
	}
}

func TestSubscribeGuardRequestClientIPPrefersCloudflareHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.RemoteAddr = "10.0.0.10:12345"
	req.Header.Set("CF-Connecting-IP", "203.0.113.8")
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.10")

	if ip := requestClientIP(req); ip != "203.0.113.8" {
		t.Fatalf("expected Cloudflare connecting ip, got %q", ip)
	}
}

func TestRouterClientSubscribeGuardBlocksBadUserAgent(t *testing.T) {
	userService := &fakeUserService{resolvedClientUserID: 10, subscribe: user.Subscribe{Email: "guard-user@example.com"}}
	router := NewRouter(config.Config{
		PublicDir:                        "../public",
		SubscribeGuardEnable:             true,
		SubscribeGuardUABlacklist:        []string{"curl", "python"},
		SubscribeGuardRateLimitPerMinute: 0,
	}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.Header.Set("User-Agent", "curl/8.0")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if reason := rec.Header().Get("X-Subscribe-Guard"); reason != "ua" {
		t.Fatalf("expected ua block reason header, got %q", reason)
	}
}

func TestRouterClientSubscribeGuardRateLimitsByIP(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			UUID:           "11111111-1111-1111-1111-111111111111",
			Token:          "token-1",
			TransferEnable: 1024,
		},
	}
	router := NewRouter(config.Config{
		PublicDir:                        "../public",
		SubscribeGuardEnable:             true,
		SubscribeGuardRateLimitPerMinute: 1,
	}, WithUserService(userService))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
		req.RemoteAddr = "203.0.113.9:12345"
		req.Header.Set("User-Agent", "ClashMeta/1.0")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if i == 0 && rec.Code != http.StatusOK {
			t.Fatalf("expected first request 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if i == 1 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("expected second request 429, got %d: %s", rec.Code, rec.Body.String())
			}
			if reason := rec.Header().Get("X-Subscribe-Guard"); reason != "rate_limit" {
				t.Fatalf("expected rate_limit reason header, got %q", reason)
			}
		}
	}
}

func TestRouterAdminSubscribeGuardStatsEndpoint(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userService := &fakeUserService{resolvedClientUserID: 10, subscribe: user.Subscribe{Email: "guard-user@example.com"}}
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}
	nodeService := &fakeNodeService{sensitiveStats: map[string]any{
		"top_users": []map[string]any{{"user_id": int64(10), "email": "user@example.com", "count": int64(3)}},
	}}
	router := NewRouter(config.Config{
		PublicDir:                    publicDir,
		AdminPath:                    "localadmin",
		SubscribeGuardEnable:         true,
		SubscribeGuardTokenBlacklist: []string{"token-1"},
	}, WithUserService(userService), WithSessionService(sessionService), WithNodeService(nodeService))

	blockedReq := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	blockedReq.RemoteAddr = "203.0.113.9:12345"
	blockedReq.Header.Set("User-Agent", "ClashMeta/1.0")
	blockedRec := httptest.NewRecorder()
	router.ServeHTTP(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusForbidden {
		t.Fatalf("expected guard request 403, got %d: %s", blockedRec.Code, blockedRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/stats?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", payload["data"])
	}
	if data["blocked"].(float64) != 1 || data["total"].(float64) != 1 {
		t.Fatalf("unexpected guard counters: %#v", data)
	}
	recent, ok := data["recent"].([]any)
	if !ok || len(recent) != 1 {
		t.Fatalf("expected one recent event, got %#v", data["recent"])
	}
	first := recent[0].(map[string]any)
	if first["reason"] != "token" || first["ip"] != "203.0.113.9" {
		t.Fatalf("unexpected recent event: %#v", first)
	}
	sensitive, ok := data["sensitive"].(map[string]any)
	if !ok {
		t.Fatalf("expected sensitive stats, got %#v", data["sensitive"])
	}
	topUsers, ok := sensitive["top_users"].([]any)
	if !ok || len(topUsers) != 1 {
		t.Fatalf("expected sensitive top users, got %#v", sensitive["top_users"])
	}
	subscribeUsers, ok := data["top_subscribe_users"].([]any)
	if !ok || len(subscribeUsers) != 1 {
		t.Fatalf("expected subscribe guard user rank, got %#v", data["top_subscribe_users"])
	}
	subscribeUser, ok := subscribeUsers[0].(map[string]any)
	if !ok || subscribeUser["email"] != "guard-user@example.com" || subscribeUser["ua_count"] != float64(1) || subscribeUser["ip_count"] != float64(1) {
		t.Fatalf("unexpected subscribe guard user rank: %#v", subscribeUsers[0])
	}
	if ips, ok := subscribeUser["ips"].([]any); !ok || len(ips) != 1 || ips[0] != "203.0.113.9" {
		t.Fatalf("unexpected subscribe guard user ips: %#v", subscribeUser["ips"])
	}
}

func TestRouterAdminSubscribeGuardUserDetailEndpoint(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, AdminPath: "localadmin", SubscribeGuardLogKeepDays: 7}
	for _, request := range []struct {
		ip      string
		ua      string
		status  int
		reason  string
		blocked bool
	}{
		{ip: "203.0.113.10", ua: "ClashMeta/1.0", status: http.StatusOK, reason: "pass"},
		{ip: "203.0.113.11", ua: "curl/8.0", status: http.StatusForbidden, reason: "ua", blocked: true},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=user-token", nil)
		req.RemoteAddr = request.ip + ":12345"
		req.Header.Set("User-Agent", request.ua)
		recordSubscribeGuardEvent(cfg, req, request.status, request.reason, request.blocked)
	}
	otherReq := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=other-token", nil)
	otherReq.RemoteAddr = "198.51.100.20:12345"
	recordSubscribeGuardEvent(cfg, otherReq, http.StatusOK, "pass", false)

	adminService := &fakeAdminService{userInfoDetail: map[string]any{
		"id": int64(10), "email": "guard-user@example.com", "token": "user-token", "password": "must-not-leak",
		"banned": int64(0), "plan_id": int64(2), "transfer_enable": int64(1000), "remarks": "重点观察",
		"invite_user": map[string]any{"id": int64(3), "email": "invite@example.com", "token": "invite-token"},
	}}
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}
	router := NewRouter(cfg, WithAdminService(adminService), WithSessionService(sessionService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/user-detail?auth_data=jwt-admin&id=10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if adminService.lastUserInfoID != 10 {
		t.Fatalf("expected user id 10, got %d", adminService.lastUserInfoID)
	}
	if strings.Contains(rec.Body.String(), "must-not-leak") || strings.Contains(rec.Body.String(), "user-token") || strings.Contains(rec.Body.String(), "invite-token") {
		t.Fatalf("sensitive user field leaked: %s", rec.Body.String())
	}
	var payload struct {
		Data struct {
			User  map[string]any `json:"user"`
			Stats map[string]any `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.User["email"] != "guard-user@example.com" || payload.Data.User["remarks"] != "重点观察" {
		t.Fatalf("unexpected user detail: %#v", payload.Data.User)
	}
	if payload.Data.Stats["total"] != float64(2) || payload.Data.Stats["blocked"] != float64(1) || payload.Data.Stats["allowed"] != float64(1) {
		t.Fatalf("unexpected user guard stats: %#v", payload.Data.Stats)
	}
	recent, ok := payload.Data.Stats["recent"].([]any)
	if !ok || len(recent) != 2 {
		t.Fatalf("unexpected user recent stats: %#v", payload.Data.Stats["recent"])
	}
	if cacheControl := rec.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("expected no-store response, got %q", cacheControl)
	}
}

func TestRouterAdminSubscribeGuardUserSearchEndpointOnlyReturnsPublicFields(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AppName: "forest-go", AdminPath: "localadmin", PublicDir: publicDir, SubscribeGuardLogKeepDays: 7}
	if err := writeSubscribeGuardEvents(cfg, []subscribeGuardEvent{{
		Time: time.Now().Unix(), Token: "rotating-token-must-not-leak", CanonicalToken: "secret-token", UA: "ClashMeta/2.0",
	}}); err != nil {
		t.Fatal(err)
	}
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}
	adminService := &fakeAdminService{userList: admin.UserListResult{
		Data: []map[string]any{{
			"id": int64(10), "email": "guard-user@example.com", "banned": int64(0),
			"plan_id": int64(2), "plan_name": "Pro", "u": int64(100), "d": int64(200),
			"transfer_enable": int64(1000), "expired_at": int64(1893456000),
			"password": "must-not-leak", "token": "secret-token", "uuid": "secret-uuid",
			"subscribe_url": "https://example.com/secret-subscribe-url",
		}},
		Total: 1,
	}, clientEntryPolicyMatches: map[int64]*admin.ClientEntryUserPolicyRecord{
		10: {ID: 90, Name: "重点用户入口", Mode: admin.ClientEntryUserPolicyModeSplit, Action: "override", EntryHost: "special-entry.example.com"},
	}}
	router := NewRouter(
		cfg,
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/user-search?auth_data=jwt-admin&keyword=guard%40example.com", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := adminService.lastUserFetch; got.Current != 1 || got.PageSize != 20 || got.Sort != "id" || got.SortType != "DESC" {
		t.Fatalf("unexpected search request: %#v", got)
	}
	if filters := adminService.lastUserFetch.Filters; len(filters) != 1 || filters[0].Key != "email" || filters[0].Condition != "模糊" || filters[0].Value != "guard@example.com" {
		t.Fatalf("unexpected search filters: %#v", filters)
	}
	for _, secret := range []string{"must-not-leak", "secret-token", "rotating-token-must-not-leak", "secret-uuid", "secret-subscribe-url"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("sensitive user field leaked (%s): %s", secret, rec.Body.String())
		}
	}
	var payload struct {
		Data  []map[string]any `json:"data"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Data) != 1 || payload.Data[0]["email"] != "guard-user@example.com" || payload.Data[0]["plan_name"] != "Pro" {
		t.Fatalf("unexpected public user search payload: %#v", payload)
	}
	if got := adminService.lastClientEntryPolicyMatches; len(got) != 1 || got[0].UserID != 10 || got[0].UA != "ClashMeta/2.0" {
		t.Fatalf("unexpected current entry policy match request: %#v", got)
	}
	entryPolicy, ok := payload.Data[0]["entry_policy"].(map[string]any)
	if !ok || entryPolicy["id"] != float64(90) || entryPolicy["name"] != "重点用户入口" || entryPolicy["mode"] != admin.ClientEntryUserPolicyModeSplit || entryPolicy["entry_host"] != "special-entry.example.com" || entryPolicy["evaluated_ua"] != "ClashMeta/2.0" {
		t.Fatalf("unexpected current entry policy: %#v", payload.Data[0]["entry_policy"])
	}
	for _, forbiddenKey := range []string{"password", "token", "uuid", "subscribe_url"} {
		if _, ok := payload.Data[0][forbiddenKey]; ok {
			t.Fatalf("forbidden field %q returned: %#v", forbiddenKey, payload.Data[0])
		}
	}
	if cacheControl := rec.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("expected no-store response, got %q", cacheControl)
	}
}

func TestSubscribeGuardLatestUserSearchUAsUsesNewestDirectAndLegacyEvents(t *testing.T) {
	users := []map[string]any{
		{"id": int64(10), "token": "canonical-10"},
		{"id": int64(20), "token": "canonical-20"},
	}
	events := []subscribeGuardEvent{
		{Time: 200, UserID: 10, UA: "Clash/Newest"},
		{Time: 100, UserID: 10, UA: "Clash/Appended-Later-But-Older"},
		{Time: 150, Token: "dynamic-20", CanonicalToken: "canonical-20", UA: "Surge/Legacy"},
		{Time: 300, UserID: 99, Token: "canonical-20", UA: "Must-Not-Be-Reattributed"},
	}

	latest := subscribeGuardLatestUserSearchUAs(events, users)
	if latest[10] != "Clash/Newest" || latest[20] != "Surge/Legacy" {
		t.Fatalf("unexpected latest user UAs: %#v", latest)
	}
}

func TestRouterAdminSubscribeGuardUserSearchUsesExactNumericID(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{userList: admin.UserListResult{Data: []map[string]any{}, Total: 0}}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/user-search?auth_data=jwt-admin&keyword=0010", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	filters := adminService.lastUserFetch.Filters
	if len(filters) != 1 || filters[0].Key != "id" || filters[0].Condition != "=" || filters[0].Value != "10" {
		t.Fatalf("unexpected numeric search filters: %#v", filters)
	}
}

func TestRouterAdminSubscribeGuardUASearchEndpoint(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, AdminPath: "localadmin", SubscribeGuardLogKeepDays: 7}
	now := time.Now().Unix()
	events := []subscribeGuardEvent{
		{Time: now - 5, UserID: 10, Token: "secret-token-10-a", IP: "203.0.113.10", UA: "curl/8.0", Status: http.StatusOK, Reason: "pass"},
		{Time: now - 3, UserID: 10, Token: "secret-token-10-b", IP: "203.0.113.11", UA: "CURL/8.1", Status: http.StatusForbidden, Reason: "ua", Blocked: true},
		{Time: now - 1, UserID: 20, Token: "secret-token-20", IP: "198.51.100.20", UA: "curl/7.0", Status: http.StatusOK, Reason: "pass"},
		{Time: now - 2, UserID: 20, Token: "clash-token", IP: "198.51.100.21", UA: "Clash/1.0", Status: http.StatusOK, Reason: "pass"},
		{Time: now - 4, Token: "legacy-secret-token", CanonicalToken: "canonical-secret-token", IP: "192.0.2.30", UA: "curl/6.0", Status: http.StatusForbidden, Reason: "ua", Blocked: true},
		{Time: now - 6, IP: "192.0.2.31", UA: "curl/5.0", Status: http.StatusForbidden, Reason: "ua", Blocked: true},
	}
	if err := writeSubscribeGuardEvents(cfg, events); err != nil {
		t.Fatal(err)
	}
	resetSubscribeGuardStateForTest()

	adminService := &fakeAdminService{
		userInfoDetails: map[int64]map[string]any{
			10: {"id": int64(10), "email": "ten@example.com", "banned": int64(0), "plan_id": int64(2), "plan_name": "Pro", "password": "secret-password-10", "token": "secret-user-token-10", "uuid": "secret-uuid-10"},
			20: {"id": int64(20), "email": "twenty@example.com", "banned": int64(1), "plan_id": int64(0), "password": "secret-password-20", "token": "secret-user-token-20", "uuid": "secret-uuid-20"},
			30: {"id": int64(30), "email": "legacy@example.com", "banned": int64(0), "plan_id": int64(3), "password": "secret-password-30", "token": "secret-user-token-30", "uuid": "secret-uuid-30"},
		},
		clientEntryPolicyMatches: map[int64]*admin.ClientEntryUserPolicyRecord{
			10: {ID: 9, Name: "内鬼固定组", Mode: admin.ClientEntryUserPolicyModeSplit, Action: "override", EntryHost: "fixed-entry.example.com", ResolveEntryHost: 1, ExtraNodes: []string{"trojan://must-not-leak@example.com:443"}},
			20: {ID: 7, Name: "Curl 普通入口", Mode: admin.ClientEntryUserPolicyModeStandard, Action: "override", EntryHost: "curl-entry.example.com"},
		},
	}
	userService := &fakeUserService{resolvedClientUserID: 30}
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}
	router := NewRouter(cfg, WithAdminService(adminService), WithSessionService(sessionService), WithUserService(userService))

	request := func(page string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/ua-search?auth_data=jwt-admin&keyword=CuRl&current="+page+"&page_size=1", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := request("1")
	if first.Code != http.StatusOK {
		t.Fatalf("expected first page 200, got %d: %s", first.Code, first.Body.String())
	}
	for _, secret := range []string{"secret-token-10-a", "secret-token-10-b", "legacy-secret-token", "canonical-secret-token", "secret-password-10", "secret-user-token-10", "secret-uuid-10", "must-not-leak"} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatalf("UA search leaked sensitive value %q: %s", secret, first.Body.String())
		}
	}
	var firstPayload struct {
		Data             []map[string]any `json:"data"`
		Total            int64            `json:"total"`
		Current          int64            `json:"current"`
		PageSize         int64            `json:"page_size"`
		MatchedEvents    int64            `json:"matched_events"`
		UnresolvedEvents int64            `json:"unresolved_events"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	if firstPayload.Total != 3 || firstPayload.Current != 1 || firstPayload.PageSize != 1 || firstPayload.MatchedEvents != 5 || firstPayload.UnresolvedEvents != 1 {
		t.Fatalf("unexpected UA search pagination counters: %#v", firstPayload)
	}
	if len(firstPayload.Data) != 1 || firstPayload.Data[0]["user_id"] != float64(20) || firstPayload.Data[0]["email"] != "twenty@example.com" {
		t.Fatalf("unexpected first UA search row: %#v", firstPayload.Data)
	}
	firstPolicy, ok := firstPayload.Data[0]["entry_policy"].(map[string]any)
	if !ok || firstPolicy["id"] != float64(7) || firstPolicy["name"] != "Curl 普通入口" || firstPolicy["entry_host"] != "curl-entry.example.com" || firstPolicy["evaluated_ua"] != "curl/7.0" {
		t.Fatalf("unexpected first UA search entry policy: %#v", firstPayload.Data[0]["entry_policy"])
	}
	if got := adminService.lastClientEntryPolicyMatches; len(got) != 1 || got[0].UserID != 20 || got[0].UA != "curl/7.0" {
		t.Fatalf("unexpected first UA search match request: %#v", got)
	}

	second := request("2")
	if second.Code != http.StatusOK {
		t.Fatalf("expected second page 200, got %d: %s", second.Code, second.Body.String())
	}
	for _, secret := range []string{"secret-token-10-a", "secret-token-10-b", "secret-password-10", "secret-user-token-10", "secret-uuid-10", "must-not-leak"} {
		if strings.Contains(second.Body.String(), secret) {
			t.Fatalf("UA search leaked sensitive value %q: %s", secret, second.Body.String())
		}
	}
	var secondPayload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPayload); err != nil {
		t.Fatal(err)
	}
	if len(secondPayload.Data) != 1 {
		t.Fatalf("expected one second-page row, got %#v", secondPayload.Data)
	}
	row := secondPayload.Data[0]
	if row["user_id"] != float64(10) || row["email"] != "ten@example.com" || row["count"] != float64(2) || row["allowed"] != float64(1) || row["blocked"] != float64(1) || row["ip_count"] != float64(2) || row["ua_count"] != float64(2) {
		t.Fatalf("unexpected second UA search row: %#v", row)
	}
	secondPolicy, ok := row["entry_policy"].(map[string]any)
	if !ok || secondPolicy["id"] != float64(9) || secondPolicy["name"] != "内鬼固定组" || secondPolicy["mode"] != admin.ClientEntryUserPolicyModeSplit || secondPolicy["entry_host"] != "fixed-entry.example.com" || secondPolicy["evaluated_ua"] != "CURL/8.1" {
		t.Fatalf("unexpected second UA search entry policy: %#v", row["entry_policy"])
	}
	if got := adminService.lastClientEntryPolicyMatches; len(got) != 1 || got[0].UserID != 10 || got[0].UA != "CURL/8.1" {
		t.Fatalf("unexpected second UA search match request: %#v", got)
	}
	recent, ok := row["recent"].([]any)
	if !ok || len(recent) != 2 {
		t.Fatalf("expected two safe recent requests, got %#v", row["recent"])
	}
	if cacheControl := second.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("expected no-store response, got %q", cacheControl)
	}

	third := request("3")
	if third.Code != http.StatusOK {
		t.Fatalf("expected third page 200, got %d: %s", third.Code, third.Body.String())
	}
	var thirdPayload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(third.Body.Bytes(), &thirdPayload); err != nil {
		t.Fatal(err)
	}
	if len(thirdPayload.Data) != 1 || thirdPayload.Data[0]["user_id"] != float64(30) || thirdPayload.Data[0]["email"] != "legacy@example.com" || thirdPayload.Data[0]["count"] != float64(1) {
		t.Fatalf("expected legacy token request to resolve to user 30, got %#v", thirdPayload.Data)
	}
	if thirdPayload.Data[0]["entry_policy"] != nil {
		t.Fatalf("expected user without a current rule to have no entry policy, got %#v", thirdPayload.Data[0]["entry_policy"])
	}
	if userService.lastClientToken != "canonical-secret-token" {
		t.Fatalf("expected canonical token to be used for legacy lookup, got %q", userService.lastClientToken)
	}

	tooMany := httptest.NewRecorder()
	tooManyReq := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/ua-search?auth_data=jwt-admin&keyword=curl&page_size=101", nil)
	router.ServeHTTP(tooMany, tooManyReq)
	if tooMany.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid page size 400, got %d: %s", tooMany.Code, tooMany.Body.String())
	}
}

func TestSubscribeGuardStatsSurvivesMemoryResetFromLogFile(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, SubscribeGuardLogKeepDays: 7}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-persist", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("User-Agent", "curl/8.0")

	recordSubscribeGuardEvent(cfg, req, http.StatusForbidden, "ua", true)
	resetSubscribeGuardStateForTest()

	stats := subscribeGuardStatsSnapshot(cfg)
	if stats["total"].(int64) != 1 || stats["blocked"].(int64) != 1 {
		t.Fatalf("expected persisted stats after memory reset, got %#v", stats)
	}
	recent := stats["recent"].([]subscribeGuardEvent)
	if len(recent) != 1 || recent[0].Token != "token-persist" || recent[0].Reason != "ua" {
		t.Fatalf("unexpected persisted recent events: %#v", recent)
	}
}

func TestSubscribeGuardUserStatsMatchesUserIDAndLegacyCanonicalToken(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, SubscribeGuardLogKeepDays: 7}
	now := time.Now().Unix()
	events := []subscribeGuardEvent{
		{Time: now - 3, UserID: 10, Token: "dynamic-a", IP: "203.0.113.1", UA: "Clash/1", Status: 200, Reason: "pass"},
		{Time: now - 2, UserID: 10, Token: "dynamic-b", IP: "203.0.113.2", UA: "Clash/2", Status: 403, Reason: "ua", Blocked: true},
		{Time: now - 1, Token: "canonical-token", IP: "203.0.113.3", UA: "legacy", Status: 200, Reason: "pass"},
		{Time: now, Token: "old-dynamic-token", CanonicalToken: "canonical-token", IP: "203.0.113.4", UA: "legacy-dynamic", Status: 200, Reason: "pass"},
		{Time: now + 1, UserID: 11, Token: "other", IP: "198.51.100.1", UA: "other", Status: 200, Reason: "pass"},
	}
	if err := writeSubscribeGuardEvents(cfg, events); err != nil {
		t.Fatal(err)
	}

	stats := subscribeGuardUserStatsSnapshot(cfg, 10, "canonical-token")
	if stats["total"] != int64(4) || stats["blocked"] != int64(1) || stats["ip_count"] != int64(4) {
		t.Fatalf("unexpected user-id and legacy-token stats: %#v", stats)
	}
}

func TestSubscribeGuardLogCleanupUsesRetentionDays(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, SubscribeGuardLogKeepDays: 1}
	oldEvent := subscribeGuardEvent{Time: time.Now().Add(-48 * time.Hour).Unix(), IP: "203.0.113.1", Token: "old", UA: "curl", Status: 403, Reason: "ua", Blocked: true}
	newEvent := subscribeGuardEvent{Time: time.Now().Unix(), IP: "203.0.113.2", Token: "new", UA: "clash", Status: 200, Reason: "pass", Blocked: false}
	if err := writeSubscribeGuardEvents(cfg, []subscribeGuardEvent{oldEvent, newEvent}); err != nil {
		t.Fatal(err)
	}

	if err := cleanupSubscribeGuardLog(cfg, time.Now()); err != nil {
		t.Fatal(err)
	}

	events := readSubscribeGuardLogEvents(cfg)
	if len(events) != 1 || events[0].Token != "new" {
		t.Fatalf("expected only new event after cleanup, got %#v", events)
	}
}

func TestRouterClientSubscribeClashEndpointUsesAttachmentHeader(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest", PublicDir: "../public"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=clash", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("content-disposition"); !strings.Contains(got, "attachment;") {
		t.Fatalf("expected clash subscribe to keep attachment header, got %q", got)
	}
	if got := rec.Header().Get("profile-title"); got != "base64:"+base64.StdEncoding.EncodeToString([]byte("Forest")) {
		t.Fatalf("expected clash subscribe to keep legacy profile-title header, got %q", got)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "yaml") {
		t.Fatalf("expected yaml content type, got %q", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "proxies:") {
		t.Fatalf("expected clash yaml body, got %q", body)
	}
	if !strings.Contains(body, "proxy-server-nameserver-policy:") ||
		!strings.Contains(body, "+.apt-hcloud.dev:") ||
		!strings.Contains(body, "https://38.207.164.191:8080/dns-query") {
		t.Fatalf("expected clash apt-hcloud node-domain DoH policy, got %q", body)
	}
	if strings.Contains(body, "\n    nameserver-policy:") || strings.HasPrefix(body, "nameserver-policy:") {
		t.Fatalf("clash apt-hcloud DoH should only affect proxy node domains, got %q", body)
	}
}

func TestBuildSubscribeURIJuicityV2node(t *testing.T) {
	uri := buildSubscribeURI("user-uuid", map[string]any{
		"type":               "v2node",
		"protocol":           "juicity",
		"name":               "Juicity 1",
		"host":               "j.example.com",
		"port":               int64(443),
		"congestion_control": "bbr",
		"tls_settings":       map[string]any{"server_name": "j.example.com", "allow_insecure": true},
	})

	if !strings.HasPrefix(uri, "juicity://user-uuid:user-uuid@j.example.com:443?") {
		t.Fatalf("expected juicity uri with uuid auth, got %q", uri)
	}
	if !strings.Contains(uri, "congestion_control=bbr") || !strings.Contains(uri, "sni=j.example.com") || !strings.Contains(uri, "allow_insecure=1") || !strings.Contains(uri, "#Juicity%201") {
		t.Fatalf("expected juicity query/name fields, got %q", uri)
	}
}

func TestBuildSubscribeURIMieruV2node(t *testing.T) {
	uri := buildSubscribeURI("user-uuid", map[string]any{
		"type":             "v2node",
		"protocol":         "mieru",
		"name":             "Mieru 1",
		"host":             "m.example.com",
		"port":             int64(2999),
		"network_settings": map[string]any{"transport": "UDP", "mtu": int64(1280), "multiplexing": "MULTIPLEXING_LOW"},
	})

	if !strings.HasPrefix(uri, "mieru://user-uuid:user-uuid@m.example.com:2999?") {
		t.Fatalf("expected mieru uri with uuid auth, got %q", uri)
	}
	if !strings.Contains(uri, "transport=UDP") || !strings.Contains(uri, "mtu=1280") || !strings.Contains(uri, "multiplexing=MULTIPLEXING_LOW") || !strings.Contains(uri, "#Mieru%201") {
		t.Fatalf("expected mieru query/name fields, got %q", uri)
	}
}

func TestRouterClientSubscribeClashEndpointIncludesMetaProtocols(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vless",
				"name":    "VLESS-1",
				"host":    "vless.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "vless.example.com",
					"fingerprint": "chrome",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "vless.example.com",
					},
				},
			},
			{
				"type":           "tuic",
				"name":           "TUIC-1",
				"host":           "tuic.example.com",
				"port":           int64(443),
				"server_name":    "tuic.example.com",
				"udp_relay_mode": "native",
			},
			{
				"type":          "hysteria",
				"version":       int64(2),
				"name":          "HY2-1",
				"host":          "hy2.example.com",
				"port":          int64(443),
				"server_name":   "hy2.example.com",
				"up_mbps":       int64(30),
				"down_mbps":     int64(200),
				"obfs":          "salamander",
				"obfs_password": "hy-pass",
			},
			{
				"type":        "anytls",
				"name":        "AnyTLS-1",
				"host":        "anytls.example.com",
				"port":        int64(443),
				"server_name": "anytls.example.com",
				"tls_settings": map[string]any{
					"allow_insecure": true,
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest", PublicDir: "../public"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=clash", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, name := range []string{"VLESS-1", "TUIC-1", "HY2-1", "AnyTLS-1"} {
		if !strings.Contains(body, name) {
			t.Fatalf("expected clash yaml to include %s, got %q", name, body)
		}
	}
}

func TestRouterClientSubscribeReqIOSUserAgentUsesYAMLProfile(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest", PublicDir: "../public"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.Header.Set("User-Agent", "req-ios")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "yaml") {
		t.Fatalf("expected req-ios subscribe to return yaml, got %q with body %q", contentType, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "proxies:") {
		t.Fatalf("expected req-ios subscribe body to be clash-like yaml, got %q", rec.Body.String())
	}
}

func TestRouterClientSubscribeSurgeEndpointUsesManagedProfile(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tlsSettings": map[string]any{
					"serverName": "node.example.com",
				},
				"networkSettings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest", AppURL: "https://panel.example.com"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=surge", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "#!MANAGED-CONFIG https://panel.example.com/api/v1/client/subscribe?token=token-1&flag=surge") ||
		!strings.Contains(body, "[Proxy]") ||
		!strings.Contains(body, "VMess-1=vmess") ||
		!strings.Contains(body, "[Host]") ||
		!strings.Contains(body, "*.apt-hcloud.dev = server:https://38.207.164.191:8080/dns-query") {
		t.Fatalf("expected surge profile body with apt-hcloud DoH host rule, got %q", body)
	}
}

func TestRouterClientSubscribeSurfboardEndpointUsesManagedProfile(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "trojan",
				"name":    "Trojan-1",
				"host":    "trojan.example.com",
				"port":    int64(443),
				"network": "tcp",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "trojan.example.com",
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest", AppURL: "https://panel.example.com"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=surfboard", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "#!MANAGED-CONFIG https://panel.example.com/api/v1/client/subscribe?token=token-1&flag=surfboard") ||
		!strings.Contains(body, "[Proxy]") ||
		!strings.Contains(body, "Trojan-1=trojan") {
		t.Fatalf("expected surfboard profile body, got %q", body)
	}
}

func TestRouterClientSubscribeSingboxFlagUsesJSONProfile(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=sing", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected sing-box subscribe to return json, got %q with body %q", contentType, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "\"outbounds\"") || !strings.Contains(body, "\"VMess-1\"") ||
		!strings.Contains(body, "\"tag\":\"apt-hcloud-doh\"") {
		t.Fatalf("expected sing-box json body with apt-hcloud DoH resolver, got %q", body)
	}
	if strings.Contains(body, "\"domain_suffix\":[\"apt-hcloud.dev\"]") {
		t.Fatalf("sing-box apt-hcloud DoH should not be a global DNS rule, got %q", body)
	}
}

func TestRouterClientSubscribeSingboxAptHcloudNodeUsesDedicatedDomainResolver(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":   "shadowsocks",
				"name":   "AptNode",
				"host":   "hk.apt-hcloud.dev",
				"port":   int64(443),
				"cipher": "aes-128-gcm",
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=sing", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "\"server\":\"hk.apt-hcloud.dev\"") ||
		!strings.Contains(body, "\"domain_resolver\":\"apt-hcloud-doh\"") ||
		strings.Contains(body, "\"domain_suffix\":[\"apt-hcloud.dev\"]") {
		t.Fatalf("expected sing-box apt-hcloud node to use outbound domain_resolver only, got %q", body)
	}
}

func TestRouterClientSubscribeSingboxUserAgentUsesJSONProfile(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":        "trojan",
				"name":        "Trojan-1",
				"host":        "trojan.example.com",
				"port":        int64(443),
				"network":     "tcp",
				"tls":         int64(1),
				"server_name": "trojan.example.com",
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.Header.Set("User-Agent", "sing-box 1.12.1")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected sing-box UA subscribe to return json, got %q with body %q", contentType, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "\"outbounds\"") || !strings.Contains(body, "\"Trojan-1\"") {
		t.Fatalf("expected sing-box json body, got %q", body)
	}
}

func TestRouterClientSubscribeLegacySingboxUserAgentUsesLegacyTemplate(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              11,
			D:              22,
			TransferEnable: 100,
			ExpiredAt:      int64Ptr(1234567890),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	req.Header.Set("User-Agent", "sing-box 1.11.9")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected legacy sing-box subscribe to return json, got %q with body %q", contentType, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "\"tag\":\"direct\"") || !strings.Contains(body, "\"VMess-1\"") {
		t.Fatalf("expected legacy sing-box template body, got %q", body)
	}
}

func TestRouterClientSubscribeShadowrocketModuleEndpointReturnsDoHHostModule(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe:            user.Subscribe{UUID: "user-uuid"},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=shadowrocket-module", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Fatalf("expected text/plain module response, got %q", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "#!name=Forest DoH Module") || !strings.Contains(body, "[Host]") {
		t.Fatalf("expected shadowrocket module header, got %q", body)
	}
	if !strings.Contains(body, "apt-hcloud.dev = server:https://38.207.164.191:8080/dns-query") ||
		!strings.Contains(body, "*.apt-hcloud.dev = server:https://38.207.164.191:8080/dns-query") {
		t.Fatalf("expected apt-hcloud DoH host rules, got %q", body)
	}
	if strings.Contains(body, "dns-server") {
		t.Fatalf("module should not override global DNS server, got %q", body)
	}
}

func TestRouterClientSubscribeShadowrocketModuleToleratesFlagAppendedAfterQueryToken(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe:            user.Subscribe{UUID: "user-uuid"},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1?flag=shadowrocket-module", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected malformed module URL to still authenticate, got %d: %s", rec.Code, rec.Body.String())
	}
	if userService.lastClientToken != "token-1" {
		t.Fatalf("expected token to be recovered before stray flag, got %q", userService.lastClientToken)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "#!name=Forest DoH Module") || !strings.Contains(body, "*.apt-hcloud.dev = server:https://38.207.164.191:8080/dns-query") {
		t.Fatalf("expected shadowrocket module body, got %q", body)
	}
}

func TestRouterClientSubscribeShadowrocketEndpointUsesLegacyStatusLine(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              1024 * 1024 * 1024,
			D:              2 * 1024 * 1024 * 1024,
			TransferEnable: 10 * 1024 * 1024 * 1024,
			ExpiredAt:      int64Ptr(1735689600),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
		},
	}
	router := NewRouter(config.Config{AppName: "Forest"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=shadowrocket", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("expected base64 shadowrocket body: %v", err)
	}
	body := string(decoded)
	if !strings.Contains(body, "STATUS=") {
		t.Fatalf("expected shadowrocket body to keep legacy status line, got %q", body)
	}
	if !strings.Contains(body, "Expires:2025-01-01") {
		t.Fatalf("expected shadowrocket body to include legacy expiry text, got %q", body)
	}
	if !strings.Contains(body, "remark=VMess-1") {
		t.Fatalf("expected shadowrocket body to keep legacy vmess format, got %q", body)
	}
}

func TestRouterClientSubscribeClashEndpointCanInjectInfoServers(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			U:              1024,
			D:              2048,
			TransferEnable: 10 * 1024 * 1024 * 1024,
			ExpiredAt:      int64Ptr(1735689600),
			ResetDay:       int64Ptr(3),
			UUID:           "user-uuid",
		},
		servers: []map[string]any{
			{
				"type":    "vmess",
				"name":    "VMess-1",
				"host":    "node.example.com",
				"port":    int64(443),
				"network": "ws",
				"tls":     int64(1),
				"tls_settings": map[string]any{
					"server_name": "node.example.com",
				},
				"network_settings": map[string]any{
					"path": "/ws",
					"headers": map[string]any{
						"Host": "node.example.com",
					},
				},
			},
		},
	}
	router := NewRouter(config.Config{
		AppName:                "Forest",
		PublicDir:              "../public",
		ShowInfoToServerEnable: true,
	}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1&flag=clash", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "剩余流量：") {
		t.Fatalf("expected clash profile to inject remaining traffic server, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "套餐到期：2025-01-01") {
		t.Fatalf("expected clash profile to inject expiry server, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "距离下次重置剩余：3 天") {
		t.Fatalf("expected clash profile to inject reset day server, got %q", rec.Body.String())
	}
}

func TestPrependLegacySubscribeInfoServersDoesNotCloneExtraNodeURI(t *testing.T) {
	raw := "trojan://secret@extra.example.com:443#Extra"
	extra := map[string]any{
		subscribelink.MarkerField: true,
		subscribelink.RawURIField: raw,
		"name":                    "Extra",
	}
	managed := map[string]any{"type": "vmess", "name": "Managed", "host": "managed.example.com", "port": int64(443)}
	cfg := config.Config{ShowInfoToServerEnable: true}
	subscribe := user.Subscribe{TransferEnable: 1024}

	result := prependLegacySubscribeInfoServers(cfg, "shadowrocket", subscribe, []map[string]any{extra, managed})
	if len(result) != 4 {
		t.Fatalf("expected two info nodes plus the original nodes, got %#v", result)
	}
	for index := 0; index < 2; index++ {
		if got := subscribelink.RawURI(result[index]); got != "" {
			t.Fatalf("info node %d retained extra URI %q", index, got)
		}
		if result[index]["host"] != "managed.example.com" {
			t.Fatalf("info node %d did not use the managed template: %#v", index, result[index])
		}
	}
	if got := subscribelink.RawURI(result[2]); got != raw || result[3]["name"] != "Managed" {
		t.Fatalf("original extra-before-managed order changed: %#v", result)
	}

	onlyExtra := prependLegacySubscribeInfoServers(cfg, "shadowrocket", subscribe, []map[string]any{extra})
	if len(onlyExtra) != 1 || subscribelink.RawURI(onlyExtra[0]) != raw {
		t.Fatalf("an extra-only subscription should not synthesize duplicate info nodes: %#v", onlyExtra)
	}
}

func TestRouterClientSubscribeCustomPathEndpoint(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{},
	}
	router := NewRouter(config.Config{SubscribePath: "/forest-sub"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/forest-sub?token=token-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected custom subscribe path to work, got %d: %s", rec.Code, rec.Body.String())
	}

	defaultReq := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=token-1", nil)
	defaultRec := httptest.NewRecorder()
	router.ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("expected default subscribe path to remain compatible, got %d: %s", defaultRec.Code, defaultRec.Body.String())
	}
}

func TestRouterClientSubscribeCustomPathEndpointAllowsTrailingSlash(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{},
	}
	router := NewRouter(config.Config{SubscribePath: "/forest-sub/"}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/forest-sub/?token=token-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected trailing slash custom subscribe path to work, got %d: %s", rec.Code, rec.Body.String())
	}
	if userService.lastClientToken != "token-1" {
		t.Fatalf("expected query token to be preserved, got %q", userService.lastClientToken)
	}
}

func TestRouterClientSubscribeCustomPathTokenSegment(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{},
	}
	router := NewRouter(config.Config{SubscribePath: "/forest-sub", SubscribeTokenInPath: true}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/forest-sub/token-1?flag=clash", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected custom subscribe token segment to work, got %d: %s", rec.Code, rec.Body.String())
	}
	if userService.lastClientToken != "token-1" {
		t.Fatalf("expected token from path segment, got %q", userService.lastClientToken)
	}
}

func TestRouterClientSubscribeCustomPathTokenSegmentShadowrocketModule(t *testing.T) {
	userService := &fakeUserService{
		resolvedClientUserID: 10,
		subscribe: user.Subscribe{
			UUID: "user-uuid",
		},
		servers: []map[string]any{},
	}
	router := NewRouter(config.Config{AppName: "Forest", SubscribePath: "/forest", SubscribeTokenInPath: true}, WithUserService(userService))

	req := httptest.NewRequest(http.MethodGet, "/forest/token-1?flag=shadowrocket-module", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected custom path shadowrocket module to work, got %d: %s", rec.Code, rec.Body.String())
	}
	if userService.lastClientToken != "token-1" {
		t.Fatalf("expected token from path segment, got %q", userService.lastClientToken)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "#!name=Forest DoH Module") || !strings.Contains(body, "apt-hcloud.dev = server:https://38.207.164.191:8080/dns-query") {
		t.Fatalf("expected shadowrocket module body, got %q", body)
	}
}

func TestBuildV2ServerConfigJuicityExternalProtocol(t *testing.T) {
	server := nodeapi.ServerRecord{
		ID:       11,
		NodeType: "v2node",
		Fields: map[string]any{
			"listen_ip":          "0.0.0.0",
			"server_port":        int64(443),
			"protocol":           "juicity",
			"tls":                int64(1),
			"tls_settings":       map[string]any{"server_name": "j.example.com", "certificate_file": "/etc/ssl/j.crt"},
			"congestion_control": "bbr",
		},
	}

	payload := buildV2ServerConfig(config.Config{}, server, nil)

	if payload["protocol"] != "juicity" || payload["server_port"] != int64(443) {
		t.Fatalf("expected common juicity fields, got %#v", payload)
	}
	if payload["external_protocol"] != true {
		t.Fatalf("expected external_protocol=true, got %#v", payload["external_protocol"])
	}
	if payload["traffic_mode"] != "observer" || payload["password_mode"] != "uuid" {
		t.Fatalf("unexpected external modes: traffic=%#v password=%#v", payload["traffic_mode"], payload["password_mode"])
	}
	if payload["congestion_control"] != "bbr" {
		t.Fatalf("expected congestion_control preserved, got %#v", payload["congestion_control"])
	}
	tlsSettings, ok := payload["tls_settings"].(map[string]any)
	if !ok || tlsSettings["server_name"] != "j.example.com" {
		t.Fatalf("expected tls settings preserved, got %#v", payload["tls_settings"])
	}
}

func TestBuildV2ServerConfigMieruExternalProtocol(t *testing.T) {
	server := nodeapi.ServerRecord{
		ID:       12,
		NodeType: "v2node",
		Fields: map[string]any{
			"listen_ip":        "0.0.0.0",
			"server_port":      int64(2999),
			"protocol":         "mieru",
			"network_settings": map[string]any{"transport": "UDP", "mtu": int64(1280), "multiplexing": "MULTIPLEXING_LOW"},
		},
	}

	payload := buildV2ServerConfig(config.Config{}, server, nil)

	if payload["protocol"] != "mieru" || payload["server_port"] != int64(2999) {
		t.Fatalf("expected common mieru fields, got %#v", payload)
	}
	if payload["external_protocol"] != true {
		t.Fatalf("expected external_protocol=true, got %#v", payload["external_protocol"])
	}
	if payload["traffic_mode"] != "metrics" || payload["password_mode"] != "uuid" {
		t.Fatalf("unexpected external modes: traffic=%#v password=%#v", payload["traffic_mode"], payload["password_mode"])
	}
	if payload["transport"] != "UDP" || payload["mtu"] != int64(1280) || payload["multiplexing"] != "MULTIPLEXING_LOW" {
		t.Fatalf("unexpected mieru settings: transport=%#v mtu=%#v multiplexing=%#v", payload["transport"], payload["mtu"], payload["multiplexing"])
	}
}

func TestBuildV2ServerConfigMieruExternalProtocolDefaults(t *testing.T) {
	server := nodeapi.ServerRecord{
		ID:       13,
		NodeType: "v2node",
		Fields: map[string]any{
			"server_port": int64(2999),
			"protocol":    "mieru",
		},
	}

	payload := buildV2ServerConfig(config.Config{}, server, nil)

	if payload["transport"] != "TCP" || payload["mtu"] != int64(1400) {
		t.Fatalf("expected mieru defaults, got transport=%#v mtu=%#v", payload["transport"], payload["mtu"])
	}
}

func TestRouterServerV2ConfigEndpoint(t *testing.T) {
	t.Setenv("SERVER_PUSH_INTERVAL", "90")
	t.Setenv("SERVER_PULL_INTERVAL", "45")
	t.Setenv("SERVER_NODE_REPORT_MIN_TRAFFIC", "2048")
	t.Setenv("SERVER_DEVICE_ONLINE_MIN_TRAFFIC", "4096")
	t.Setenv("SUBSCRIBE_GUARD_SENSITIVE_ENABLE", "1")
	t.Setenv("SUBSCRIBE_GUARD_SENSITIVE_RULES", "suffix:example.com")
	t.Setenv("SUBSCRIBE_GUARD_SENSITIVE_INTERVAL", "30")
	t.Setenv("SUBSCRIBE_GUARD_SENSITIVE_LOG_IP", "1")

	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       9,
			NodeType: "v2node",
			RouteIDs: []int64{3},
			Fields: map[string]any{
				"listen_ip":           "0.0.0.0",
				"send_through":        "198.51.100.7",
				"server_port":         int64(443),
				"network":             "ws",
				"network_settings":    map[string]any{"path": "/ws"},
				"protocol":            "vmess",
				"tls":                 int64(1),
				"tls_settings":        map[string]any{"server_name": "node.example.com"},
				"encryption":          "none",
				"encryption_settings": map[string]any{},
				"flow":                "xtls-rprx-vision",
				"cipher":              "2022-blake3-aes-128-gcm",
				"congestion_control":  "bbr",
				"zero_rtt_handshake":  int64(1),
				"up_mbps":             int64(0),
				"down_mbps":           int64(0),
				"obfs":                "salamander",
				"obfs_password":       "secret",
				"padding_scheme":      []any{"stop=8"},
				"created_at":          int64(1700000000),
			},
		},
		routes: []map[string]any{
			{"id": int64(3), "match": []any{"geosite:cn"}, "action": "direct"},
		},
	}
	router := NewRouter(config.Load(), WithNodeService(nodeService))

	req := httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=&node_id=9", nil)
	req.URL.RawQuery = "token=" + "secret" + "&node_id=9"
	rec := httptest.NewRecorder()

	cfg := config.Load()
	cfg.ServerToken = "secret"
	router = NewRouter(cfg, WithNodeService(nodeService))
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("expected etag header")
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	if payload["listen_ip"] != "0.0.0.0" || payload["protocol"] != "vmess" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload["send_through"] != "198.51.100.7" {
		t.Fatalf("expected send_through to be preserved, got %#v", payload["send_through"])
	}
	if payload["ignore_client_bandwidth"] != true {
		t.Fatalf("expected ignore_client_bandwidth=true, got %#v", payload["ignore_client_bandwidth"])
	}
	baseConfig, ok := payload["base_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected base_config object, got %#v", payload["base_config"])
	}
	if baseConfig["push_interval"] != float64(90) || baseConfig["node_report_min_traffic"] != float64(2048) || baseConfig["device_online_min_traffic"] != float64(4096) {
		t.Fatalf("unexpected base config: %#v", baseConfig)
	}
	sensitiveAudit, ok := payload["sensitive_audit"].(map[string]any)
	if !ok {
		t.Fatalf("expected sensitive_audit object, got %#v", payload["sensitive_audit"])
	}
	if sensitiveAudit["enable"] != true || sensitiveAudit["report_interval"] != float64(30) || sensitiveAudit["log_client_ip"] != true {
		t.Fatalf("unexpected sensitive audit: %#v", sensitiveAudit)
	}
	rules, ok := sensitiveAudit["rules"].([]any)
	if !ok || len(rules) != 1 || rules[0] != "suffix:example.com" {
		t.Fatalf("unexpected sensitive audit rules: %#v", sensitiveAudit["rules"])
	}
}

func TestRouterServerV2ConfigEndpointKeepsPrivateKeys(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       9,
			NodeType: "v2node",
			Fields: map[string]any{
				"listen_ip":           "0.0.0.0",
				"server_port":         int64(443),
				"network":             "tcp",
				"protocol":            "vless",
				"tls":                 int64(2),
				"tls_settings":        map[string]any{"private_key": "tls-private", "public_key": "tls-public"},
				"encryption":          "none",
				"encryption_settings": map[string]any{"private_key": "enc-private", "public_key": "enc-public"},
			},
		},
	}
	cfg := config.Load()
	cfg.ServerToken = "secret"
	router := NewRouter(cfg, WithNodeService(nodeService))

	req := httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=secret&node_id=9", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	tlsSettings, ok := payload["tls_settings"].(map[string]any)
	if !ok || tlsSettings["private_key"] != "tls-private" {
		t.Fatalf("expected tls private key kept, got %#v", payload["tls_settings"])
	}
	encryptionSettings, ok := payload["encryption_settings"].(map[string]any)
	if !ok || encryptionSettings["private_key"] != "enc-private" {
		t.Fatalf("expected encryption private key kept, got %#v", payload["encryption_settings"])
	}
}

func TestRouterServerV2ConfigEndpointMissingTokenKeepsLegacyFailureShape(t *testing.T) {
	router := NewRouter(config.Config{AppName: "forest-go"}, WithNodeService(&fakeNodeService{}))

	req := httptest.NewRequest(http.MethodGet, "/api/v2/server/config?node_id=9", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	if payload["status"] != "fail" || payload["message"] != "token is null" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRouterServerV2ConfigEndpointServerMissingKeepsLegacyFailureShape(t *testing.T) {
	nodeService := &fakeNodeService{err: errors.New("server is not exist")}
	cfg := config.Load()
	cfg.ServerToken = "secret"
	router := NewRouter(cfg, WithNodeService(nodeService))

	req := httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=secret&node_id=9", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	if payload["status"] != "fail" || payload["message"] != "server is not exist" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}
