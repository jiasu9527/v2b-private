package config

import "testing"

func TestTelegramAIWebhookSecretBoundToAppAndBot(t *testing.T) {
	secret := TelegramAIWebhookSecret("fixture-app", "42:fixture-token")
	if len(secret) != 64 || secret != TelegramAIWebhookSecret("fixture-app", " 42:fixture-token ") {
		t.Fatal("webhook secret must be stable and Telegram-compatible")
	}
	for _, value := range []string{TelegramAIWebhookSecret("other", "42:fixture-token"), TelegramAIWebhookSecret("fixture-app", "43:fixture-token"), TelegramAIWebhookSecret("", "42:fixture-token"), TelegramAIWebhookSecret("fixture-app", "")} {
		if value == secret {
			t.Fatal("webhook secret was not tied to app key and token")
		}
	}
}
