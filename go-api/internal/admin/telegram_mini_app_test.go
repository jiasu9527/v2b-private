package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	cfgpkg "forest/go-api/internal/config"
)

func TestTelegramMiniAppURLUsesOnlyConfiguredHTTPSOrigin(t *testing.T) {
	for _, tc := range []struct{ input, expected string }{
		{"https://panel.example.test", "https://panel.example.test/ai-ops-mini"},
		{"https://panel.example.test/sub/path", "https://panel.example.test/ai-ops-mini"},
		{"https://panel.example.test:8443/panel/", "https://panel.example.test:8443/ai-ops-mini"},
		{"https://[2001:db8::1]:8443/", "https://[2001:db8::1]:8443/ai-ops-mini"},
	} {
		if got, err := TelegramMiniAppURL(tc.input); err != nil || got != tc.expected {
			t.Fatal(tc.input, got, err)
		}
	}
	for _, input := range []string{"", "http://panel.example.test", "javascript:alert(1)", "//panel.example.test", "https://", "https://user:secret@panel.example.test", "https://panel.example.test/#fragment", "https://panel.example.test/#", "https://panel.example.test/?token=secret", "https://panel.example.test:0", "https://panel.example.test:65536", "https://panel.example.test:", "https://panel.example.test:bad", "https://panel.example.test\\@other.example.test", "https://panel.example.test\n.evil"} {
		if got, err := TelegramMiniAppURL(input); err == nil || got != "" {
			t.Fatal("invalid configured origin accepted", input, got, err)
		}
	}
}

func TestInstallTelegramMiniAppUsesBooleanSetAndVerifiesMenu(t *testing.T) {
	for _, fromStore := range []bool{true, false} {
		t.Run(map[bool]string{true: "admin store", false: "runtime fallback"}[fromStore], func(t *testing.T) {
			root := t.TempDir()
			values := map[string]any{}
			if fromStore {
				values = map[string]any{"app_url": "https://saved.example.test/nested/", "telegram_bot_enable": 1, "telegram_bot_token": "123:fixture-token"}
			}
			writeAdminJSONFixture(t, root, values)
			oldRoot := adminProjectRoot
			adminProjectRoot = root
			t.Cleanup(func() { adminProjectRoot = oldRoot })
			service := &DBService{cfg: cfgpkg.Config{AppURL: "https://runtime.example.test/panel", TelegramBotEnable: true, TelegramBotToken: "123:fixture-token"}}
			expectedURL := "https://runtime.example.test/ai-ops-mini"
			if fromStore {
				expectedURL = "https://saved.example.test/ai-ops-mini"
			}
			calls := 0
			mockAdminTelegram(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "api.telegram.org" || r.URL.RawQuery != "" || r.Method != http.MethodPost {
					t.Fatal("unsafe menu API request")
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if calls == 1 {
					if !strings.HasSuffix(r.URL.Path, "/setChatMenuButton") || r.PostForm.Get("chat_id") != "" {
						t.Fatal("not global menu", r.URL.Path)
					}
					var menu map[string]any
					if json.Unmarshal([]byte(r.PostForm.Get("menu_button")), &menu) != nil {
						t.Fatal("invalid menu JSON")
					}
					web, _ := menu["web_app"].(map[string]any)
					if menu["type"] != "web_app" || menu["text"] != "AI 运维" || web["url"] != expectedURL {
						t.Fatal("wrong menu", menu)
					}
					return adminTelegramResponse(200, `{"ok":true,"result":true}`), nil
				}
				if calls != 2 || !strings.HasSuffix(r.URL.Path, "/getChatMenuButton") {
					t.Fatal("unexpected verification call", r.URL.Path)
				}
				raw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"type": "web_app", "text": "AI 运维", "web_app": map[string]string{"url": expectedURL}}})
				return adminTelegramResponse(200, string(raw)), nil
			})
			// No request/host is accepted by the capability: unrelated context
			// metadata cannot redirect the configured launch destination.
			type untrustedHostKey struct{}
			ctx := context.WithValue(context.Background(), untrustedHostKey{}, "attacker.example.test")
			out, err := service.InstallTelegramMiniApp(ctx)
			if err != nil || calls != 2 || out["url"] != expectedURL || out["installed"] != true {
				t.Fatal(out, calls, err)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "fixture-token") || strings.Contains(string(raw), "initData") || strings.Contains(string(raw), "auth") {
				t.Fatal("menu result leaked auth", string(raw))
			}
		})
	}
}

func TestInstallTelegramMiniAppRejectsInvalidConfigurationBeforeNetwork(t *testing.T) {
	root := t.TempDir()
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })
	mockAdminTelegram(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid menu configuration reached Telegram")
		return nil, nil
	})
	for _, values := range []map[string]any{
		{"app_url": "http://panel.example.test", "telegram_bot_enable": 1, "telegram_bot_token": "123:fixture"},
		{"app_url": "https://name:password@panel.example.test", "telegram_bot_enable": 1, "telegram_bot_token": "123:fixture"},
		{"app_url": "https://panel.example.test", "telegram_bot_enable": 0, "telegram_bot_token": "123:fixture"},
		{"app_url": "https://panel.example.test", "telegram_bot_enable": 1, "telegram_bot_token": ""},
	} {
		writeAdminJSONFixture(t, root, values)
		service := &DBService{cfg: cfgpkg.Config{AppURL: "https://must-not-fallback.example.test", TelegramBotEnable: true}}
		if _, err := service.InstallTelegramMiniApp(context.Background()); err == nil {
			t.Fatal("invalid menu configuration accepted", values)
		}
	}
}

func TestInstallTelegramMiniAppRejectsMismatchingActualMenu(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{"app_url": "https://panel.example.test", "telegram_bot_enable": 1, "telegram_bot_token": "123:fixture"})
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })
	mockAdminTelegram(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/setChatMenuButton") {
			return adminTelegramResponse(200, `{"ok":true,"result":true}`), nil
		}
		return adminTelegramResponse(200, `{"ok":true,"result":{"type":"web_app","text":"AI 运维","web_app":{"url":"https://other.example.test/ai-ops-mini"}}}`), nil
	})
	if _, err := (&DBService{}).InstallTelegramMiniApp(context.Background()); err == nil {
		t.Fatal("unverified menu reported success")
	}
}

func TestInstallTelegramMiniAppPrefersConfiguredBackendWebhookOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, saved, runtime, want string
	}{
		{"saved backend first", "https://saved-backend.example.test/hooks", "https://runtime-backend.example.test", "https://saved-backend.example.test/ai-ops-mini"},
		{"runtime backend fallback", "", "https://runtime-backend.example.test/hooks", "https://runtime-backend.example.test/ai-ops-mini"},
		{"app fallback", "", "", "https://frontend.example.test/ai-ops-mini"},
		{"reject invalid saved backend", "http://unsafe-backend.example.test", "https://runtime-backend.example.test", ""},
		{"reject invalid runtime backend", "", "http://unsafe-backend.example.test", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeAdminJSONFixture(t, root, map[string]any{"app_url": "https://frontend.example.test/", "telegram_webhook_url": tc.saved, "telegram_bot_enable": 1, "telegram_bot_token": "123:fixture"})
			oldRoot := adminProjectRoot
			adminProjectRoot = root
			t.Cleanup(func() { adminProjectRoot = oldRoot })
			service := &DBService{cfg: cfgpkg.Config{TelegramWebhookURL: tc.runtime, AppURL: "https://runtime-frontend.example.test"}}
			calls := 0
			mockAdminTelegram(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.want == "" {
					t.Fatal("invalid explicit webhook origin reached network")
				}
				if strings.HasSuffix(r.URL.Path, "/setChatMenuButton") {
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					var menu struct {
						WebApp struct {
							URL string `json:"url"`
						} `json:"web_app"`
					}
					if json.Unmarshal([]byte(r.PostForm.Get("menu_button")), &menu) != nil || menu.WebApp.URL != tc.want {
						t.Fatal("incorrect preferred origin", menu.WebApp.URL)
					}
					return adminTelegramResponse(200, `{"ok":true,"result":true}`), nil
				}
				raw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"type": "web_app", "text": "AI 运维", "web_app": map[string]string{"url": tc.want}}})
				return adminTelegramResponse(200, string(raw)), nil
			})
			out, err := service.InstallTelegramMiniApp(context.Background())
			if tc.want == "" {
				if err == nil || calls != 0 {
					t.Fatal("invalid explicit origin silently fell back", calls, err)
				}
			} else if err != nil || calls != 2 || out["url"] != tc.want {
				t.Fatal(out, calls, err)
			}
		})
	}
}
