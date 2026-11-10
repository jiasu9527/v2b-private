package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	usersvc "forest/go-api/internal/user"
)

func quantumultXHTTPService() *fakeUserService {
	return &fakeUserService{
		resolvedClientUserID: 10,
		subscribe:            usersvc.Subscribe{UUID: "11111111-2222-3333-4444-555555555555", TransferEnable: 1000},
		servers:              []map[string]any{{"id": int64(7), "type": "vmess", "name": "Test node", "host": "node.example.test", "port": 54101, "network": "tcp"}},
	}
}

func TestRouterQuantumultXNativeResource(t *testing.T) {
	for _, ua := range []string{"Quantumult X/1.5.6", "Quantumult%20X/1.4.1 (iPhone; iOS 18.0)", "QuantumultX/1.5.6"} {
		for _, flag := range []string{"", "quantumult x", "quantumult-x", "quantumult_x", "quantumultx", "quanx", "qx"} {
			t.Run(ua+"/"+flag, func(t *testing.T) {
				cfg := config.Config{AppName: "Forest", SubscribeGuardEnable: true, SubscribeGuardBlockCrawlerUA: true, SubscribeGuardBlockEmptyUA: true}
				service := quantumultXHTTPService()
				router := NewRouter(cfg, WithUserService(service))
				path := "/api/v1/client/subscribe?token=fixture-token"
				if flag != "" {
					path += "&flag=" + url.QueryEscape(flag)
				}
				req := httptest.NewRequest("GET", path, nil)
				req.Header.Set("User-Agent", ua)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "vmess=node.example.test:54101,") {
					t.Fatalf("expected native QX nodes, status=%d body=%q", w.Code, w.Body.String())
				}
				if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Header().Get("X-Subscribe-Guard") != "" || !strings.Contains(w.Header().Get("subscription-userinfo"), "total=1000") {
					t.Fatalf("unexpected metadata: %v", w.Header())
				}
				if service.lastServerUA != ua {
					t.Fatalf("entry matching UA changed: got %q, want %q", service.lastServerUA, ua)
				}
			})
		}
	}
}

func TestRouterQuantumultXCustomPathAndMetadata(t *testing.T) {
	service := quantumultXHTTPService()
	cfg := config.Config{AppName: "Forest", SubscribePath: "/forest-sub", SubscribeTokenInPath: true, ShowInfoToServerEnable: true}
	router := NewRouter(cfg, WithUserService(service))
	req := httptest.NewRequest(http.MethodGet, "/forest-sub/fixture-token?flag=quantumult%20x", nil)
	req.Header.Set("User-Agent", "Quantumult%20X/1.5.6")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || service.lastClientToken != "fixture-token" {
		t.Fatalf("custom token path failed: status=%d", w.Code)
	}
	if lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "vmess=") {
		t.Fatalf("metadata must not add fake nodes to QX: %q", w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), ".txt") || !strings.Contains(w.Header().Get("subscription-userinfo"), "total=1000") {
		t.Fatalf("missing resource metadata: %v", w.Header())
	}
}

func TestQuantumultXSubscribeFlag(t *testing.T) {
	for _, flag := range []string{"Quantumult X/1.5.6", "Quantumult%20X/1.5.6", "quantumult-x", "quantumult_x", "quantumultx", " QX ", "QuanX"} {
		if !isQuantumultXSubscribeFlag(flag) {
			t.Errorf("QX flag not recognized: %q", flag)
		}
	}
	for _, flag := range []string{"", "Quantumult/2.2", "clash", "raw", "sing-box", "Surge/5.0", "Shadowrocket/2.2"} {
		if isQuantumultXSubscribeFlag(flag) {
			t.Errorf("non-QX flag recognized: %q", flag)
		}
	}
}

func TestRouterQuantumultXDoesNotFallbackForUnsupportedOrEmptyNodes(t *testing.T) {
	for _, empty := range []bool{false, true} {
		service := quantumultXHTTPService()
		service.servers = nil
		if !empty {
			service.servers = []map[string]any{{"id": 8, "type": "tuic", "host": "tuic.example.test", "port": 443}}
		}
		router := NewRouter(config.Config{}, WithUserService(service))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=fixture-token", nil)
		req.Header.Set("User-Agent", "Quantumult%20X/1.5.6")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "" {
			t.Fatalf("must not fabricate compatible nodes or fall back to raw URIs: %d %q", w.Code, w.Body.String())
		}
	}
}

func TestRouterQuantumultXKeepsExplicitFormatAndGuard(t *testing.T) {
	for _, flag := range []string{"clash", "raw"} {
		t.Run(flag, func(t *testing.T) {
			router := NewRouter(config.Config{}, WithUserService(quantumultXHTTPService()))
			req := httptest.NewRequest("GET", "/api/v1/client/subscribe?token=fixture-token&flag="+flag, nil)
			req.Header.Set("User-Agent", "Quantumult%20X/1.5.6")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if flag == "clash" {
				if !strings.Contains(w.Header().Get("Content-Type"), "yaml") || !strings.Contains(w.Body.String(), "proxies:") {
					t.Fatal("explicit Clash flag was ignored")
				}
			} else if decoded, err := base64.StdEncoding.DecodeString(w.Body.String()); err != nil || !strings.Contains(string(decoded), "vmess://") {
				t.Fatalf("explicit raw subscription changed: %v", err)
			}
		})
	}
	cfg := config.Config{SubscribeGuardEnable: true, SubscribeGuardUABlacklist: []string{"quantumult"}}
	router := NewRouter(cfg, WithUserService(quantumultXHTTPService()))
	req := httptest.NewRequest("GET", "/api/v1/client/subscribe?token=fixture-token&flag=qx", nil)
	req.Header.Set("User-Agent", "Quantumult X/1.5.6")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || w.Header().Get("X-Subscribe-Guard") != "ua" {
		t.Fatalf("format selection bypassed guard: %d %v", w.Code, w.Header())
	}
}

func TestRouterClassicQuantumultKeepsGenericSubscription(t *testing.T) {
	router := NewRouter(config.Config{}, WithUserService(quantumultXHTTPService()))
	req := httptest.NewRequest("GET", "/api/v1/client/subscribe?token=fixture-token", nil)
	req.Header.Set("User-Agent", "Quantumult/2.2")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if _, err := base64.StdEncoding.DecodeString(w.Body.String()); w.Code != 200 || err != nil {
		t.Fatalf("classic Quantumult was incorrectly treated as X: %d %v", w.Code, err)
	}
}
