package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TelegramMiniAppURL derives the fixed public entry point exclusively from a
// configured application origin, never from a request's Host or proxy headers.
func TelegramMiniAppURL(appURL string) (string, error) {
	appURL = strings.TrimSpace(appURL)
	u, err := url.Parse(appURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(appURL, "#\\\r\n\t ") || u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("小程序需要有效的 HTTPS 网站地址，不能包含账号、查询参数或片段")
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("小程序网站地址端口无效")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("小程序网站地址端口无效")
		}
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment, u.RawFragment = "/ai-ops-mini", "", "", "", ""
	return u.String(), nil
}

// InstallTelegramMiniApp only installs the public launch URL. Possession of
// the menu button grants no AI authorization; initData and the independent
// operator binding are checked by the Mini App backend on every request.
func (s *DBService) InstallTelegramMiniApp(ctx context.Context) (map[string]any, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return nil, err
	}
	current := s.currentConfig()
	if cfg.int64Value("telegram_bot_enable", boolToInt64(current.TelegramBotEnable)) == 0 {
		return nil, errors.New("请先开启 Telegram 机器人")
	}
	token := strings.TrimSpace(cfg.stringValue("telegram_bot_token", current.TelegramBotToken))
	if token == "" {
		return nil, errors.New("请先填写 Telegram 机器人 Token")
	}
	// A split frontend/backend deployment may serve app_url on a static user
	// frontend. Prefer the explicitly configured Bot backend origin, where the
	// Go Mini App route lives; an invalid explicit value must fail, not fall back.
	configuredURL := strings.TrimSpace(cfg.stringValue("telegram_webhook_url", ""))
	if configuredURL == "" {
		configuredURL = strings.TrimSpace(current.TelegramWebhookURL)
	}
	if configuredURL == "" {
		configuredURL = cfg.stringValue("app_url", current.AppURL)
	}
	miniURL, err := TelegramMiniAppURL(configuredURL)
	if err != nil {
		return nil, err
	}
	menu := map[string]any{"type": "web_app", "text": "AI 运维", "web_app": map[string]any{"url": miniURL}}
	encoded, err := json.Marshal(menu)
	if err != nil {
		return nil, errors.New("生成小程序菜单失败")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if err := telegramAPICall(ctx, client, token, "setChatMenuButton", map[string]string{"menu_button": string(encoded)}); err != nil {
		return nil, err
	}
	actual, err := telegramAPIResultMap(ctx, client, token, "getChatMenuButton", nil)
	if err != nil {
		return nil, err
	}
	actualApp, _ := actual["web_app"].(map[string]any)
	if actual["type"] != "web_app" || actual["text"] != "AI 运维" || actualApp["url"] != miniURL {
		return nil, errors.New("Telegram 小程序菜单未生效，请核对机器人配置")
	}
	return map[string]any{"url": miniURL, "installed": true}, nil
}
