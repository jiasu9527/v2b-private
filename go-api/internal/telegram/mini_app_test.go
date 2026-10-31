package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

func TestMiniAppBotTokenCapabilityUsesCurrentEnabledIdentityOnly(t *testing.T) {
	s := aiTelegramService()
	capability, ok := s.AITransport().(interface{ MiniBotToken() string })
	if !ok || capability.MiniBotToken() != "98765:fixture-token" {
		t.Fatal("internal token capability unavailable")
	}
	runtime := &config.RuntimeState{}
	runtime.SetForTest(config.Config{TelegramBotEnable: true, TelegramBotToken: "54321:new-token"})
	s.WithRuntimeConfig(runtime)
	if capability.MiniBotToken() != "54321:new-token" {
		t.Fatal("old token survived runtime rotation")
	}
	runtime.SetForTest(config.Config{TelegramBotEnable: false, TelegramBotToken: "54321:new-token"})
	if capability.MiniBotToken() != "" {
		t.Fatal("disabled bot token exposed")
	}
	runtime.SetForTest(config.Config{TelegramBotEnable: true, TelegramBotToken: "54321:invalid/token"})
	if capability.MiniBotToken() != "" {
		t.Fatal("invalid bot token exposed")
	}
}

func TestMiniAppCommandsOnlySendPublicFixedURLAndBypassAI(t *testing.T) {
	for _, command := range []string{"/aiapp", "/miniapp", "/aiapp@fixture_bot"} {
		s := aiTelegramService().WithAIOps(fakeAIController(func(context.Context, aiops.TelegramInput) (bool, error) {
			t.Fatal("public launch entered AI")
			return false, nil
		}))
		s.cfg.AppURL = "https://trusted.example.test/backend/"
		calls := 0
		s.sendMessageMarkup = func(_ context.Context, chatID int64, text string, markup any) error {
			calls++
			raw, err := json.Marshal(markup)
			if err != nil {
				t.Fatal(err)
			}
			if chatID != 123 || !strings.Contains(string(raw), `"web_app":{"url":"https://trusted.example.test/ai-ops-mini"}`) || !strings.Contains(text, "/ai") {
				t.Fatal("incorrect mini app launch", string(raw))
			}
			for _, forbidden := range []string{"fixture-token", "initData", "access_token", "authorization", "attacker"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatal("public launch leaked credentials or untrusted URL", string(raw))
				}
			}
			return nil
		}
		payload := aiWebhookMessage(command)
		payload["host"] = "attacker.example.test"
		payload["x-forwarded-host"] = "attacker.example.test"
		if err := s.HandleWebhook(context.Background(), payload); err != nil || calls != 1 {
			t.Fatal(command, calls, err)
		}
	}
}

func TestMiniAppCommandsRejectGroupsAndBadConfiguredURLs(t *testing.T) {
	s := aiTelegramService()
	var prompts int
	s.sendMessageMarkup = func(context.Context, int64, string, any) error { t.Fatal("unsafe mini app button sent"); return nil }
	s.sendMessage = func(context.Context, int64, string) error { prompts++; return nil }
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["chat"].(map[string]any)["type"] = "group" },
		func(m map[string]any) { m["from"].(map[string]any)["id"] = 222 },
		func(m map[string]any) { m["from"].(map[string]any)["is_bot"] = true },
	} {
		payload := aiWebhookMessage("/miniapp")
		mutate(payload["message"].(map[string]any))
		if err := s.HandleWebhook(context.Background(), payload); err != nil {
			t.Fatal(err)
		}
	}
	if prompts != 0 {
		t.Fatal("non-private launch got reply")
	}
	s.cfg.AppURL = "http://insecure.example.test"
	if err := s.HandleWebhook(context.Background(), aiWebhookMessage("/aiapp")); err != nil || prompts != 1 {
		t.Fatal("bad configured origin not rejected", prompts, err)
	}
}

func TestMiniAppCommandPrefersBackendOriginWithoutIgnoringInvalidExplicitURL(t *testing.T) {
	for _, tc := range []struct{ webhook, want string }{
		{"https://backend.example.test/hooks", "https://backend.example.test/ai-ops-mini"},
		{"", "https://frontend.example.test/ai-ops-mini"},
		{"http://invalid-backend.example.test", ""},
	} {
		s := aiTelegramService()
		s.cfg.TelegramWebhookURL, s.cfg.AppURL = tc.webhook, "https://frontend.example.test"
		var buttons, failures int
		s.sendMessageMarkup = func(_ context.Context, _ int64, _ string, markup any) error {
			buttons++
			raw, _ := json.Marshal(markup)
			if tc.want == "" || !strings.Contains(string(raw), `"url":"`+tc.want+`"`) {
				t.Fatal("wrong command origin", string(raw))
			}
			return nil
		}
		s.sendMessage = func(context.Context, int64, string) error { failures++; return nil }
		if err := s.HandleWebhook(context.Background(), aiWebhookMessage("/miniapp")); err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if buttons != 0 || failures != 1 {
				t.Fatal("invalid explicit origin silently ignored", buttons, failures)
			}
		} else if buttons != 1 || failures != 0 {
			t.Fatal(buttons, failures)
		}
	}
}
