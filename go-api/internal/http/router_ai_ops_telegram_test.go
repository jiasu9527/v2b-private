package httpapi

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"forest/go-api/internal/telegram"
)

type fakeAITelegram struct {
	calls         int
	actor, target int64
}

func (f *fakeAITelegram) Status(_ context.Context, actor int64) (aiops.TelegramStatus, error) {
	f.calls++
	f.actor = actor
	return aiops.TelegramStatus{Models: []string{"fixture"}, Bindings: []aiops.TelegramBinding{}}, nil
}
func (f *fakeAITelegram) SaveConfig(_ context.Context, actor int64, cfg aiops.TelegramConfig) (aiops.TelegramConfig, error) {
	f.calls++
	f.actor = actor
	return cfg, nil
}
func (f *fakeAITelegram) CreateBindingCode(_ context.Context, actor, target int64) (aiops.TelegramBindingCode, error) {
	f.calls++
	f.actor, f.target = actor, target
	return aiops.TelegramBindingCode{Code: "fixture"}, nil
}
func (f *fakeAITelegram) RevokeBinding(_ context.Context, actor, target int64) error {
	f.calls++
	f.actor, f.target = actor, target
	return nil
}

func TestAITelegramHTTPManagementPermissionsAndValidation(t *testing.T) {
	admin := &session.Identity{ID: 7, IsAdmin: 1}
	staff := &session.Identity{ID: 8, IsStaff: 1}
	for _, tc := range []struct {
		method, action, body string
		identity             *session.Identity
		status, calls        int
	}{
		{"GET", "status", "", admin, 200, 1},
		{"GET", "status", "", staff, 200, 1},
		{"GET", "status", "", nil, 403, 0},
		{"GET", "status", "", &session.Identity{ID: 9}, 403, 0},
		{"POST", "config", `{"enabled":true,"default_model":"fixture","default_access_mode":"full","stream_updates":true,"notify_completion":true}`, admin, 200, 1},
		{"POST", "config", `{}`, staff, 403, 0},
		{"POST", "binding-code", `{"actor_id":8}`, admin, 200, 1},
		{"POST", "binding-code", `{"actor_id":8}`, staff, 403, 0},
		{"POST", "binding-code", `{"actor_id":0}`, admin, 400, 0},
		{"POST", "binding-code", `{"actor_id":8,"telegram_id":123}`, admin, 400, 0},
		{"POST", "revoke", `{"actor_id":8}`, staff, 200, 1},
		{"POST", "revoke", `{"actor_id":7}`, staff, 403, 0},
		{"POST", "revoke", `{"actor_id":8}`, admin, 200, 1},
		{"POST", "revoke", `{"actor_id":8}{}`, admin, 400, 0},
		{"POST", "webhook", `{}`, staff, 403, 0},
		{"POST", "webhook", `{"telegram_bot_token":"do-not-accept"}`, admin, 400, 0},
		{"GET", "config", "", admin, 405, 0},
		{"POST", "status", `{}`, admin, 405, 0},
	} {
		t.Run(tc.method+tc.action+tc.body, func(t *testing.T) {
			f := &fakeAITelegram{}
			router := NewRouter(config.Config{AdminPath: "test"}, WithSessionService(strictAppleIDSession{identity: tc.identity}), WithAIOpsTelegram(f))
			req := httptest.NewRequest(tc.method, "/api/v1/test/ai-ops/telegram/"+tc.action, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || f.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, f.calls, w.Body.String())
			}
			if !json.Valid(w.Body.Bytes()) {
				t.Fatalf("response must be one JSON document, without a second router response: %s", w.Body.String())
			}
			if f.calls > 0 && f.actor != tc.identity.ID {
				t.Fatal("client supplied actor identity used")
			}
			if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("binding configuration must not be cached")
			}
		})
	}
}

type captureTelegramAI struct{ calls int }

func (c *captureTelegramAI) HandleUpdate(_ context.Context, in aiops.TelegramInput) (bool, error) {
	c.calls++
	return true, nil
}

func TestAITelegramWebhookRequiresIndependentSecretHeader(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("APP_KEY", "")
	cfg := config.Config{TelegramBotEnable: true, TelegramBotToken: "42:fixture-token", AppKey: "fixture-app"}
	for _, header := range []string{"", "forged", config.TelegramAIWebhookSecret(cfg.AppKey, cfg.TelegramBotToken)} {
		controller := &captureTelegramAI{}
		bot := telegram.NewService(cfg, nil).WithAIOps(controller)
		// Plain text with no legacy command avoids any outbound Telegram request.
		body := `{"update_id":1,"message":{"message_id":2,"from":{"id":123,"is_bot":false},"chat":{"id":123,"type":"private"},"text":"fixture status"}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/telegram/webhook?access_token="+fmt.Sprintf("%x", md5.Sum([]byte(cfg.TelegramBotToken))), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", header)
		w := httptest.NewRecorder()
		handleGuestTelegramWebhook(w, req, cfg, bot)
		want := 0
		if header == config.TelegramAIWebhookSecret(cfg.AppKey, cfg.TelegramBotToken) {
			want = 1
		}
		if w.Code != http.StatusOK || controller.calls != want {
			t.Fatalf("untrusted webhook reached AI: status=%d calls=%d want=%d", w.Code, controller.calls, want)
		}
	}
}

func TestAITelegramTypedNilUnavailable(t *testing.T) {
	var bridge *aiops.TelegramBridge
	router := NewRouter(config.Config{AdminPath: "test"}, WithAIOpsTelegram(bridge), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test/ai-ops/telegram/status", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("typed nil must be unavailable, got %d %s", w.Code, w.Body.String())
	}
}
