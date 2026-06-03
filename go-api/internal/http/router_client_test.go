package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
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
	if !strings.Contains(rec.Body.String(), "proxies:") {
		t.Fatalf("expected clash yaml body, got %q", rec.Body.String())
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
		!strings.Contains(body, "VMess-1=vmess") {
		t.Fatalf("expected surge profile body, got %q", body)
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
	if body := rec.Body.String(); !strings.Contains(body, "\"outbounds\"") || !strings.Contains(body, "\"VMess-1\"") {
		t.Fatalf("expected sing-box json body, got %q", body)
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

func TestRouterServerV2ConfigEndpoint(t *testing.T) {
	t.Setenv("SERVER_PUSH_INTERVAL", "90")
	t.Setenv("SERVER_PULL_INTERVAL", "45")
	t.Setenv("SERVER_NODE_REPORT_MIN_TRAFFIC", "2048")
	t.Setenv("SERVER_DEVICE_ONLINE_MIN_TRAFFIC", "4096")

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
