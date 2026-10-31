package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TelegramAIWebhookSecret authenticates privileged bot updates independently of
// the legacy webhook URL token, which can appear in proxy/access logs.
func TelegramAIWebhookSecret(appKey, botToken string) string {
	if strings.TrimSpace(appKey) == "" || strings.TrimSpace(botToken) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(appKey))
	_, _ = mac.Write([]byte("forest-telegram-ai-webhook-v1:" + strings.TrimSpace(botToken)))
	return hex.EncodeToString(mac.Sum(nil))
}
