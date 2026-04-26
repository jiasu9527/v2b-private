package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"forest/go-api/internal/user"
)

func TestHTTPClientEntryRemoteResolverUsesNodeStatusAndExcludeNames(t *testing.T) {
	t.Helper()

	var loginCalls int
	var statusCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST login, got %s", r.Method)
			}
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode login payload: %v", err)
			}
			if payload["username"] != "admin" || payload["password"] != "secret" {
				t.Fatalf("unexpected login payload: %#v", payload)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": "token-1",
				"msg":  "登录成功",
			})
		case "/api/v1/system/node/status":
			statusCalls++
			if r.Header.Get("Authorization") != "token-1" {
				t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []map[string]any{
					{
						"name": "专线直出",
						"gid":  15,
						"servers": []map[string]any{
							{"name": "0", "online": true, "ip4": "212.135.213.133"},
							{"name": "1", "online": true, "ip4": "23.147.172.93", "ip6": "2400:c620:28:9be::1"},
							{"name": "2", "online": false, "ip4": "198.51.100.8", "ip6": "2001:db8::8"},
							{"name": "3", "online": true, "ip4": "invalid-ip"},
						},
					},
					{
						"name": "别的组",
						"gid":  16,
						"servers": []map[string]any{
							{"name": "x", "online": true, "ip4": "198.51.100.99"},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resolver := newHTTPClientEntryRemoteResolver()
	ips, err := resolver.ResolveRemoteIPs(context.Background(), user.ClientEntryGroup{
		RemoteEnabled:     true,
		RemoteHost:        server.URL,
		RemoteSSHUser:     "admin",
		RemoteSSHPassword: "secret",
		RemoteGroupRef:    "专线直出 (#15)",
		RemoteExcludeNames: []string{
			"1",
		},
		RemoteRefreshSec: 300,
	})
	if err != nil {
		t.Fatalf("resolve remote ips: %v", err)
	}

	expected := []string{"212.135.213.133"}
	if len(ips) != len(expected) {
		t.Fatalf("expected %d ips, got %#v", len(expected), ips)
	}
	for index, value := range expected {
		if ips[index] != value {
			t.Fatalf("expected ip[%d]=%q, got %#v", index, value, ips)
		}
	}
	if loginCalls != 1 || statusCalls != 1 {
		t.Fatalf("expected one login and one status call, got login=%d status=%d", loginCalls, statusCalls)
	}
}

func TestNormalizeClientEntryRemoteBaseURLIgnoresLegacySSHPortDefault(t *testing.T) {
	t.Helper()

	baseURL, err := normalizeClientEntryRemoteBaseURL("iso.sllbaidu.com", 22)
	if err != nil {
		t.Fatalf("normalize base url: %v", err)
	}
	if baseURL != "https://iso.sllbaidu.com" {
		t.Fatalf("expected https base url without legacy port, got %q", baseURL)
	}

	customURL, err := normalizeClientEntryRemoteBaseURL("iso.sllbaidu.com", 8443)
	if err != nil {
		t.Fatalf("normalize custom base url: %v", err)
	}
	if customURL != "https://iso.sllbaidu.com:8443" {
		t.Fatalf("expected custom https base url, got %q", customURL)
	}
}
