package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	cfgpkg "forest/go-api/internal/config"
)

type adminTelegramRoundTrip func(*http.Request) (*http.Response, error)

func (f adminTelegramRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func adminTelegramResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func mockAdminTelegram(t *testing.T, fn adminTelegramRoundTrip) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = fn
	t.Cleanup(func() { http.DefaultTransport = old })
}

func TestTelegramAdminAPIAcceptsBoolAndObjectResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"boolean", `{"ok":true,"result":true}`, 200, true},
		{"object", `{"ok":true,"result":{"id":123}}`, 200, true},
		{"false result", `{"ok":true,"result":false}`, 200, false},
		{"missing result", `{"ok":true}`, 200, false},
		{"string result", `{"ok":true,"result":"true"}`, 200, false},
		{"http error", `{"ok":true,"result":true}`, 503, false},
		{"telegram error", `{"ok":false,"error_code":401,"description":"secret-token credential"}`, 200, false},
		{"bad json", `secret-token invalid JSON`, 200, false},
		{"oversize", strings.Repeat(" ", 1<<20) + `{"ok":true,"result":true}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: adminTelegramRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Fatal("unsafe request", r.Method, r.URL.RawQuery)
				}
				return adminTelegramResponse(tc.status, tc.body), nil
			})}
			err := telegramAPICall(context.Background(), client, "123:secret-token", "setWebhook", map[string]string{"secret_token": "secret-header"})
			if (err == nil) != tc.valid {
				t.Fatal("wrong result contract", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-token") {
				t.Fatal("leaked response credential", err)
			}
		})
	}
	client := &http.Client{Transport: adminTelegramRoundTrip(func(*http.Request) (*http.Response, error) {
		return adminTelegramResponse(200, `{"ok":true,"result":true}`), nil
	})}
	if _, err := telegramAPIResultMap(context.Background(), client, "123:fixture", "getWebhookInfo", nil); err == nil {
		t.Fatal("map helper accepted boolean")
	}
}

func TestSetTelegramWebhookStandardResponseAndIndependentSecret(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		t.Run(map[bool]string{true: "explicit token", false: "configured token fallback"}[explicit], func(t *testing.T) {
			root := t.TempDir()
			writeAdminJSONFixture(t, root, map[string]any{"app_url": "https://panel.example.test", "telegram_bot_token": "123:fixture-token"})
			oldRoot := adminProjectRoot
			adminProjectRoot = root
			t.Cleanup(func() { adminProjectRoot = oldRoot })
			service := &DBService{cfg: cfgpkg.Config{AppURL: "https://fallback.example.test", AppKey: "fixture-app-key"}}
			expectedURL := "https://panel.example.test/api/v1/guest/telegram/webhook?access_token=" + telegramWebhookAccessToken("123:fixture-token")
			methods := []string{}
			mockAdminTelegram(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.telegram.org" || r.URL.RawQuery != "" || r.Method != http.MethodPost {
					t.Fatal("request leaked secret into query", r.Method, r.URL.Host, r.URL.RawQuery)
				}
				method := strings.TrimPrefix(r.URL.Path, "/bot123:fixture-token/")
				methods = append(methods, method)
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				switch method {
				case "getMe":
					return adminTelegramResponse(200, `{"ok":true,"result":{"id":123,"is_bot":true}}`), nil
				case "setWebhook":
					if r.PostForm.Get("url") != expectedURL || r.PostForm.Get("secret_token") != cfgpkg.TelegramAIWebhookSecret("fixture-app-key", "123:fixture-token") {
						t.Fatal("webhook url or secret mismatch", r.PostForm.Get("url"))
					}
					return adminTelegramResponse(200, `{"ok":true,"result":true,"description":"Webhook was set"}`), nil
				case "getWebhookInfo":
					raw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"url": expectedURL}})
					return adminTelegramResponse(200, string(raw)), nil
				default:
					t.Fatal("unexpected API method", method)
					return nil, errors.New("unexpected method")
				}
			})
			token := ""
			if explicit {
				token = "123:fixture-token"
			}
			if ok, err := service.SetTelegramWebhook(context.Background(), token); err != nil || !ok {
				t.Fatal("normal Telegram webhook result failed", ok, err)
			}
			if strings.Join(methods, ",") != "getMe,setWebhook,getWebhookInfo" {
				t.Fatal("wrong verification order", methods)
			}
		})
	}
}

func TestTelegramAdminAPIRejectsRedirectAndSanitizesNetworkError(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: adminTelegramRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		resp := adminTelegramResponse(307, "")
		resp.Header.Set("Location", "https://other.example.test/collect")
		return resp, nil
	})}
	if err := telegramAPICall(context.Background(), client, "123:secret-token", "setWebhook", map[string]string{"secret_token": "secret-header"}); err == nil || calls != 1 {
		t.Fatal("followed credential redirect", calls, err)
	}
	client.Transport = adminTelegramRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("failed endpoint with secret-token")
	})
	if err := telegramAPICall(context.Background(), client, "123:secret-token", "getMe", nil); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal("unsafe network error", err)
	}
}

func TestSetTelegramWebhookRejectsUnverifiedActualURL(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{"app_url": "https://panel.example.test"})
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })
	mockAdminTelegram(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getWebhookInfo") {
			return adminTelegramResponse(200, `{"ok":true,"result":{"url":""}}`), nil
		}
		return adminTelegramResponse(200, `{"ok":true,"result":true}`), nil
	})
	service := &DBService{cfg: cfgpkg.Config{AppKey: "fixture-app-key"}}
	if ok, err := service.SetTelegramWebhook(context.Background(), "123:fixture-token"); err == nil || ok {
		t.Fatal("empty registered URL reported success", ok, err)
	}
}
