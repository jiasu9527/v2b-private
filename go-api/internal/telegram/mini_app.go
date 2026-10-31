package telegram

import (
	"context"
	"strings"

	"forest/go-api/internal/admin"
)

func (s *Service) handleMiniAppCommand(ctx context.Context, message webhookMessage, raw map[string]any) error {
	from, _ := raw["from"].(map[string]any)
	fromID, valid := aiTelegramID(from["id"], false)
	isBot, hasIsBot := from["is_bot"].(bool)
	if !message.IsPrivate || message.ChatID <= 0 || !valid || fromID != message.ChatID || !hasIsBot || isBot {
		return nil
	}
	if _, ready := s.AITransport().Identity(); !ready {
		return nil
	}
	cfg := s.currentConfig()
	configuredURL := strings.TrimSpace(cfg.TelegramWebhookURL)
	if configuredURL == "" {
		configuredURL = cfg.AppURL
	}
	miniURL, err := admin.TelegramMiniAppURL(configuredURL)
	if err != nil {
		return s.sendMessage(ctx, message.ChatID, "AI 运维小程序尚未配置，请联系管理员设置 HTTPS 网站地址。")
	}
	markup := map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "打开 AI 运维", "web_app": map[string]string{"url": miniURL}}}}}
	return s.sendMessageMarkup(ctx, message.ChatID, "打开 AI 运维小程序。仅已独立绑定并获得后台运维权限的账号可使用；私聊 /ai 仍可继续使用。", markup)
}
