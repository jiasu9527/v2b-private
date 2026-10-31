package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"forest/go-api/internal/aiops"
)

// AIOpsController owns independent operator binding and durable AI execution.
// The ordinary subscription-token binding is never an AI authorization source.
type AIOpsController interface {
	HandleUpdate(context.Context, aiops.TelegramInput) (bool, error)
}

type verifiedAIWebhookKey struct{}

// WithVerifiedAIWebhook is set only by the HTTP adapter after validating the
// independently authenticated Telegram secret header, never from update JSON.
func WithVerifiedAIWebhook(ctx context.Context) context.Context {
	return context.WithValue(ctx, verifiedAIWebhookKey{}, true)
}

func (s *Service) WithAIOps(controller AIOpsController) *Service {
	s.aiOps = controller
	return s
}

func (s *Service) AITransport() aiops.TelegramTransport {
	return &aiTransport{service: s}
}

type aiTransport struct{ service *Service }

// AITransportError deliberately excludes URL, token, request body and Telegram
// descriptions. RetryAfter is consumable through an interface without creating
// an aiops -> telegram package dependency.
type AITransportError struct {
	StatusCode        int
	RetryAfterSeconds int
}

func (e *AITransportError) Error() string {
	if e.StatusCode == http.StatusTooManyRequests {
		return "Telegram rate limited delivery"
	}
	return "Telegram delivery unavailable"
}

func (e *AITransportError) RetryAfter() time.Duration {
	return time.Duration(e.RetryAfterSeconds) * time.Second
}

func telegramBotIdentity(token string) (string, bool) {
	parts := strings.Split(token, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return "", false
	}
	for _, r := range parts[1] {
		if r != '_' && r != '-' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "", false
		}
	}
	return strconv.FormatInt(id, 10), true
}

func (t *aiTransport) Identity() (string, bool) {
	if t == nil || t.service == nil {
		return "", false
	}
	cfg := t.service.currentConfig()
	if !cfg.TelegramBotEnable {
		return "", false
	}
	return telegramBotIdentity(strings.TrimSpace(cfg.TelegramBotToken))
}

func (t *aiTransport) Send(ctx context.Context, chatID int64, text string, buttons [][]aiops.TelegramButton) (int64, error) {
	return t.deliver(ctx, "sendMessage", chatID, 0, text, buttons, false)
}

// Optional extension: disabling notifications still delivers every answer
// chunk, rather than silently truncating the result to the edited draft.
func (t *aiTransport) SendSilent(ctx context.Context, chatID int64, text string, buttons [][]aiops.TelegramButton) (int64, error) {
	return t.deliver(ctx, "sendMessage", chatID, 0, text, buttons, true)
}

func (t *aiTransport) Edit(ctx context.Context, chatID, messageID int64, text string, buttons [][]aiops.TelegramButton) error {
	if messageID <= 0 {
		return &AITransportError{}
	}
	_, err := t.deliver(ctx, "editMessageText", chatID, messageID, text, buttons, false)
	return err
}

func (t *aiTransport) deliver(ctx context.Context, method string, chatID, messageID int64, text string, buttons [][]aiops.TelegramButton, silent bool) (int64, error) {
	if _, ok := t.Identity(); !ok || chatID <= 0 || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(utf16.Encode([]rune(text))) > telegramMessageLimit {
		return 0, &AITransportError{}
	}
	if buttons == nil {
		buttons = [][]aiops.TelegramButton{}
	}
	payload := map[string]any{"chat_id": chatID, "text": text, "reply_markup": map[string]any{"inline_keyboard": buttons}, "link_preview_options": map[string]any{"is_disabled": true}}
	if silent {
		payload["disable_notification"] = true
	}
	if messageID > 0 {
		payload["message_id"] = messageID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, &AITransportError{}
	}
	cfg := t.service.currentConfig()
	if _, ok := telegramBotIdentity(strings.TrimSpace(cfg.TelegramBotToken)); !cfg.TelegramBotEnable || !ok {
		return 0, &AITransportError{}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+strings.TrimSpace(cfg.TelegramBotToken)+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return 0, &AITransportError{}
	}
	request.Header.Set("Content-Type", "application/json")
	if t.service.client == nil {
		return 0, &AITransportError{}
	}
	client := *t.service.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &AITransportError{}
	}
	defer response.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result)
	if response.StatusCode == http.StatusTooManyRequests || result.ErrorCode == http.StatusTooManyRequests {
		retry := result.Parameters.RetryAfter
		if retry < 1 {
			retry, _ = strconv.Atoi(response.Header.Get("Retry-After"))
		}
		if retry < 1 {
			retry = 1
		}
		if retry > 86400 {
			retry = 86400
		}
		return 0, &AITransportError{StatusCode: http.StatusTooManyRequests, RetryAfterSeconds: retry}
	}
	if method == "editMessageText" && response.StatusCode == http.StatusBadRequest && result.ErrorCode == http.StatusBadRequest && strings.Contains(strings.ToLower(result.Description), "message is not modified") {
		return messageID, nil
	}
	if decodeErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK || result.Result.MessageID <= 0 {
		return 0, &AITransportError{StatusCode: response.StatusCode}
	}
	return result.Result.MessageID, nil
}

// Parse identifiers without the permissive float truncation used by legacy
// Telegram commands. Telegram IDs fit within JSON's exact integer range.
func aiTelegramID(raw any, allowZero bool) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	if f, ok := raw.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f || f > 9007199254740991) {
		return 0, false
	}
	if _, ok := raw.(float32); ok {
		return 0, false
	}
	id, ok := anyToInt64(raw)
	return id, ok && id <= 9007199254740991 && (id > 0 || allowZero && id == 0)
}

func (s *Service) handleAIWebhook(ctx context.Context, payload map[string]any) (bool, error) {
	if _, edited := payload["edited_message"]; edited {
		return true, nil
	}
	if _, channel := payload["channel_post"]; channel {
		return true, nil
	}
	if _, channel := payload["edited_channel_post"]; channel {
		return true, nil
	}
	message, _ := payload["message"].(map[string]any)
	from, _ := message["from"].(map[string]any)
	text, _ := message["text"].(string)
	callbackData := ""
	isAICallback := false
	if callback, ok := payload["callback_query"].(map[string]any); ok {
		callbackData, _ = callback["data"].(string)
		if !strings.HasPrefix(callbackData, "ai:") {
			return false, nil
		}
		isAICallback = true
		// AI callbacks never fall through to ticket/monitor callbacks. A failed
		// acknowledgement does not replay or prevent the durable business task.
		if id, ok := callback["id"].(string); ok && id != "" && s.answerCallback != nil {
			_ = s.answerCallback(ctx, id, "", false)
		} else {
			return true, nil
		}
		message, _ = callback["message"].(map[string]any)
		from, _ = callback["from"].(map[string]any)
		text = ""
	}
	if message == nil || from == nil {
		return isAICallback, nil
	}
	if !isAICallback && legacyTelegramInput(message, text) {
		return false, nil
	}
	botID, validBot := s.AITransport().Identity()
	chat, _ := message["chat"].(map[string]any)
	chatType, _ := chat["type"].(string)
	isBot, hasIsBot := from["is_bot"].(bool)
	updateID, validUpdate := aiTelegramID(payload["update_id"], true)
	messageID, validMessage := aiTelegramID(message["message_id"], false)
	fromID, validFrom := aiTelegramID(from["id"], false)
	chatID, validChat := aiTelegramID(chat["id"], false)
	if !validBot || !validUpdate || !validMessage || !validFrom || !validChat || !hasIsBot || isBot || chatType != "private" || fromID != chatID || !utf8.ValidString(text) || (callbackData == "" && strings.TrimSpace(text) == "") {
		return isAICallback, nil
	}
	verified, _ := ctx.Value(verifiedAIWebhookKey{}).(bool)
	if !verified {
		command, _ := parseWebhookCommand(text)
		if isAICallback || strings.HasPrefix(command, "/ai") {
			_, err := s.AITransport().Send(ctx, chatID, "请管理员在后台更新 Telegram Webhook 后使用 AI 运维", nil)
			return true, err
		}
		return false, nil
	}
	if s.aiOps == nil {
		return isAICallback, nil
	}
	handled, err := s.aiOps.HandleUpdate(ctx, aiops.TelegramInput{BotID: botID, UpdateID: updateID, FromID: fromID, ChatID: chatID, MessageID: messageID, Text: text, Callback: callbackData, IsPrivate: true})
	return handled || isAICallback, err
}

func legacyTelegramInput(message map[string]any, text string) bool {
	command, _ := parseWebhookCommand(text)
	switch command {
	case "/bind", "/unbind", "/traffic", "/getlatesturl", "/start", "/monitor":
		return true
	}
	if _, ok := clientEntryMonitorActionForText(text); ok {
		return true
	}
	reply, _ := message["reply_to_message"].(map[string]any)
	replyText, _ := reply["text"].(string)
	firstLine := strings.TrimSpace(strings.SplitN(replyText, "\n", 2)[0])
	if !ticketReplyPattern.MatchString(firstLine) {
		return false
	}
	// Current ticket notifications begin with 工单提醒. Do not route every
	// reply containing #123 to tickets: AI session/action cards also use IDs.
	return strings.HasPrefix(firstLine, "工单提醒 #") ||
		(strings.HasPrefix(firstLine, "#") && (strings.Contains(strings.ToLower(firstLine), "ticket") || strings.Contains(firstLine, "的工单已回复成功")))
}

var _ aiops.TelegramTransport = (*aiTransport)(nil)
var _ interface{ RetryAfter() time.Duration } = (*AITransportError)(nil)
var _ error = (*AITransportError)(nil)
