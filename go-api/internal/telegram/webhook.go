package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
)

var ticketReplyPattern = regexp.MustCompile(`#\s*([0-9]+)`)

const (
	clientEntryMonitorRunCallback      = "client_entry_monitor:run"
	clientEntryMonitorRecentCallback   = "client_entry_monitor:recent"
	clientEntryMonitorRunButtonText    = "一键检测用户入口组"
	clientEntryMonitorRecentButtonText = "查看近期检测结果"
)

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type replyKeyboardButton struct {
	Text string `json:"text"`
}

type replyKeyboardMarkup struct {
	Keyboard              [][]replyKeyboardButton `json:"keyboard"`
	ResizeKeyboard        bool                    `json:"resize_keyboard"`
	IsPersistent          bool                    `json:"is_persistent"`
	InputFieldPlaceholder string                  `json:"input_field_placeholder,omitempty"`
}

type webhookMessage struct {
	ID        int64
	ChatID    int64
	Text      string
	IsPrivate bool
	ReplyText string
}

type webhookCallbackQuery struct {
	ID        string
	FromID    int64
	ChatID    int64
	Data      string
	IsPrivate bool
}

func (s *Service) HandleWebhook(ctx context.Context, payload map[string]any) error {
	if s == nil || len(payload) == 0 {
		return nil
	}

	if joinPayload, ok := payload["chat_join_request"].(map[string]any); ok {
		return s.handleChatJoinRequest(ctx, joinPayload)
	}
	if callbackPayload, ok := payload["callback_query"].(map[string]any); ok {
		callback, parsed := parseWebhookCallbackQuery(callbackPayload)
		if !parsed {
			return nil
		}
		return s.handleCallbackQuery(ctx, callback)
	}

	messagePayload, ok := payload["message"].(map[string]any)
	if !ok {
		return nil
	}

	message, ok := parseWebhookMessage(messagePayload)
	if !ok {
		return nil
	}

	handled, err := s.handleReplyMessage(ctx, message)
	if err != nil || handled {
		return err
	}

	if action, ok := clientEntryMonitorActionForText(message.Text); ok {
		return s.handleMonitorTextAction(ctx, message, action)
	}

	command, args := parseWebhookCommand(message.Text)
	switch command {
	case "/bind":
		return s.handleBindCommand(ctx, message, args)
	case "/unbind":
		return s.handleUnbindCommand(ctx, message)
	case "/traffic":
		return s.handleTrafficCommand(ctx, message)
	case "/getlatesturl":
		return s.handleGetLatestURLCommand(ctx, message)
	case "/start", "/monitor":
		return s.handleMonitorCommand(ctx, message)
	default:
		return nil
	}
}

func (s *Service) handleMonitorCommand(ctx context.Context, message webhookMessage) error {
	if !message.IsPrivate {
		return nil
	}

	_, authorized, err := s.lookupTelegramOperator(ctx, message.ChatID)
	if err != nil {
		return err
	}
	if !authorized {
		return s.sendMessage(ctx, message.ChatID, "无权限：仅已绑定 Telegram 的管理员和员工可使用入口检测。")
	}

	// Telegram only accepts one reply_markup per message. Send the persistent
	// chat keyboard first, then retain the existing inline actions on a second
	// message so operators can use either interaction style.
	if err := s.sendMessageMarkup(ctx, message.ChatID,
		"🛡 用户入口检测\n\n快捷菜单已固定在输入框下方，可随时发起检测或查看结果。",
		entryMonitorReplyKeyboard()); err != nil {
		return err
	}
	return s.sendMessageMarkup(ctx, message.ChatID,
		"快捷操作",
		entryMonitorInlineKeyboard())
}

func (s *Service) handleCallbackQuery(ctx context.Context, callback webhookCallbackQuery) error {
	// Telegram keeps a loading spinner open until answerCallbackQuery returns.
	// Acknowledge first, then perform database checks or start monitor work.
	if err := s.answerCallback(ctx, callback.ID, "", false); err != nil {
		// Callback acknowledgement is best effort. The business action is
		// idempotent and must still run when Telegram accepted the answer but
		// the HTTP response was lost.
		log.Printf("answer Telegram callback %q: %v", callback.ID, err)
	}
	if !callback.IsPrivate || callback.ChatID != callback.FromID {
		return nil
	}
	if callback.Data != clientEntryMonitorRunCallback && callback.Data != clientEntryMonitorRecentCallback {
		return nil
	}

	return s.handleMonitorAction(ctx, callback.FromID, callback.ChatID, callback.Data, callback.ID)
}

func (s *Service) handleMonitorTextAction(ctx context.Context, message webhookMessage, action string) error {
	if !message.IsPrivate || message.ChatID <= 0 {
		return nil
	}
	// Telegram private messages always carry message_id. Refuse a malformed
	// run request instead of starting it without a stable idempotency key.
	if action == clientEntryMonitorRunCallback && message.ID <= 0 {
		return nil
	}
	requestKey := ""
	if message.ID > 0 {
		requestKey = fmt.Sprintf("telegram-message:%d:%d", message.ChatID, message.ID)
	}
	return s.handleMonitorAction(ctx, message.ChatID, message.ChatID, action, requestKey)
}

func (s *Service) handleMonitorAction(ctx context.Context, telegramID, chatID int64, action, requestKey string) error {
	userID, authorized, err := s.lookupTelegramOperator(ctx, telegramID)
	if err != nil {
		return err
	}
	if !authorized {
		return s.sendMessage(ctx, chatID, "⛔ 无权限\n仅已绑定 Telegram 的管理员和员工可使用入口检测。")
	}

	if s.entryMonitor == nil {
		return s.sendMessage(ctx, chatID, "⚠️ 用户入口检测功能暂不可用，请稍后重试。")
	}

	switch action {
	case clientEntryMonitorRunCallback:
		_, err := s.entryMonitor.StartClientEntryMonitorRun(ctx, userID, chatID, requestKey)
		if err != nil {
			return s.sendMessage(ctx, chatID, "❌ 启动检测失败\n原因："+err.Error())
		}
		message := "🚀 检测任务已启动\n完成后，结果会自动发送到当前 Telegram。"
		return s.sendMessage(ctx, chatID, message)
	case clientEntryMonitorRecentCallback:
		if imageController, ok := s.entryMonitor.(EntryMonitorImageController); ok && s.sendPhoto != nil {
			imageBytes, caption, renderErr := imageController.RecentClientEntryMonitorReportImage(ctx)
			if renderErr == nil && len(imageBytes) > 0 {
				if err := s.sendPhoto(ctx, chatID, imageBytes, caption, entryMonitorInlineKeyboard()); err != nil {
					return fmt.Errorf("send recent client entry monitor image: %w", err)
				}
				return nil
			}
		}
		report, err := s.entryMonitor.RecentClientEntryMonitorReport(ctx)
		if err != nil {
			return s.sendMessage(ctx, chatID, "❌ 获取近期检测结果失败\n原因："+err.Error())
		}
		if strings.TrimSpace(report) == "" {
			report = "📭 暂无近期用户入口检测结果。"
		}
		return s.sendMessageChunks(ctx, chatID, report)
	default:
		return nil
	}
}

func (s *Service) lookupTelegramOperator(ctx context.Context, telegramID int64) (int64, bool, error) {
	if s.db == nil {
		return 0, false, errors.New("telegram service unavailable")
	}

	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT id
FROM v2_user
WHERE telegram_id = $1 AND banned = 0 AND (is_admin = 1 OR is_staff = 1)
ORDER BY id ASC
LIMIT 1`, telegramID).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("lookup telegram operator: %w", err)
	}
	return userID, true, nil
}

func entryMonitorInlineKeyboard() inlineKeyboardMarkup {
	return inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{
		{{Text: clientEntryMonitorRunButtonText, CallbackData: clientEntryMonitorRunCallback}},
		{{Text: clientEntryMonitorRecentButtonText, CallbackData: clientEntryMonitorRecentCallback}},
	}}
}

func entryMonitorReplyKeyboard() replyKeyboardMarkup {
	return replyKeyboardMarkup{
		Keyboard: [][]replyKeyboardButton{
			{{Text: clientEntryMonitorRunButtonText}},
			{{Text: clientEntryMonitorRecentButtonText}},
		},
		ResizeKeyboard:        true,
		IsPersistent:          true,
		InputFieldPlaceholder: "请选择入口检测操作",
	}
}

func clientEntryMonitorActionForText(text string) (string, bool) {
	switch strings.TrimSpace(text) {
	case clientEntryMonitorRunButtonText:
		return clientEntryMonitorRunCallback, true
	case clientEntryMonitorRecentButtonText:
		return clientEntryMonitorRecentCallback, true
	default:
		return "", false
	}
}

func (s *Service) handleBindCommand(ctx context.Context, message webhookMessage, args []string) error {
	if !message.IsPrivate {
		return nil
	}
	if s.db == nil {
		return errors.New("telegram service unavailable")
	}
	if len(args) == 0 {
		return s.sendMessage(ctx, message.ChatID, "参数有误，请携带订阅地址发送")
	}
	token := extractSubscribeToken(args[0])
	if token == "" || s.resolveUserID == nil {
		return s.sendMessage(ctx, message.ChatID, "订阅地址无效")
	}

	userID, err := s.resolveUserID(ctx, token)
	if err != nil || userID <= 0 {
		return s.sendMessage(ctx, message.ChatID, "订阅地址无效")
	}

	var telegramID sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT telegram_id FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&telegramID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.sendMessage(ctx, message.ChatID, "用户不存在")
		}
		return err
	}
	if telegramID.Valid && telegramID.Int64 != 0 {
		return s.sendMessage(ctx, message.ChatID, "该账号已经绑定了Telegram账号")
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user SET telegram_id = $2, updated_at = $3 WHERE id = $1`, userID, message.ChatID, time.Now().Unix())
	if err != nil {
		return s.sendMessage(ctx, message.ChatID, "设置失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return s.sendMessage(ctx, message.ChatID, "设置失败")
	}

	return s.sendMessage(ctx, message.ChatID, "绑定成功")
}

func (s *Service) handleUnbindCommand(ctx context.Context, message webhookMessage) error {
	if !message.IsPrivate {
		return nil
	}
	if s.db == nil {
		return errors.New("telegram service unavailable")
	}

	var userID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE telegram_id = $1 LIMIT 1`, message.ChatID).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.sendMessage(ctx, message.ChatID, "没有查询到您的用户信息，请先绑定账号")
		}
		return err
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user SET telegram_id = NULL, updated_at = $2 WHERE id = $1`, userID, time.Now().Unix())
	if err != nil {
		return s.sendMessage(ctx, message.ChatID, "解绑失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return s.sendMessage(ctx, message.ChatID, "解绑失败")
	}

	return s.sendMessage(ctx, message.ChatID, "解绑成功")
}

func (s *Service) handleTrafficCommand(ctx context.Context, message webhookMessage) error {
	if !message.IsPrivate {
		return nil
	}
	if s.db == nil {
		return errors.New("telegram service unavailable")
	}

	var (
		email          string
		transferEnable int64
		u              int64
		d              int64
	)
	if err := s.db.QueryRowContext(ctx, `SELECT email, transfer_enable, u, d FROM v2_user WHERE telegram_id = $1 LIMIT 1`, message.ChatID).Scan(&email, &transferEnable, &u, &d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.sendMessage(ctx, message.ChatID, "没有查询到您的用户信息，请先绑定账号")
		}
		return err
	}

	remaining := transferEnable - (u + d)
	if remaining < 0 {
		remaining = 0
	}

	text := fmt.Sprintf("流量查询\n邮箱：%s\n计划流量：%s\n已用上行：%s\n已用下行：%s\n剩余流量：%s",
		email,
		formatTrafficBytes(transferEnable),
		formatTrafficBytes(u),
		formatTrafficBytes(d),
		formatTrafficBytes(remaining),
	)
	return s.sendMessage(ctx, message.ChatID, text)
}

func (s *Service) handleGetLatestURLCommand(ctx context.Context, message webhookMessage) error {
	cfg := s.currentConfig()
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "Forest"
	}
	appURL := strings.TrimSpace(cfg.AppURL)
	if appURL == "" {
		appURL = "未配置"
	}
	return s.sendMessage(ctx, message.ChatID, fmt.Sprintf("%s的最新网址是：%s", appName, appURL))
}

func (s *Service) handleReplyMessage(ctx context.Context, message webhookMessage) (bool, error) {
	if !message.IsPrivate || s.db == nil || s.adminService == nil || strings.TrimSpace(message.ReplyText) == "" {
		return false, nil
	}

	match := ticketReplyPattern.FindStringSubmatch(message.ReplyText)
	if len(match) != 2 {
		return false, nil
	}

	ticketID, err := strconv.ParseInt(strings.TrimSpace(match[1]), 10, 64)
	if err != nil || ticketID <= 0 {
		return false, nil
	}

	var (
		adminID int64
		email   string
		isAdmin int64
		isStaff int64
	)
	if err := s.db.QueryRowContext(ctx, `SELECT id, email, is_admin, is_staff FROM v2_user WHERE telegram_id = $1 LIMIT 1`, message.ChatID).Scan(&adminID, &email, &isAdmin, &isStaff); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return true, err
	}
	if isAdmin == 0 && isStaff == 0 {
		return false, nil
	}

	replyText := strings.TrimSpace(message.Text)
	if replyText == "" {
		return true, nil
	}

	_, err = s.adminService.ReplyTicket(ctx, admin.TicketReplyRequest{
		ID:      ticketID,
		Message: replyText,
		AdminID: adminID,
	})
	if err != nil {
		return true, err
	}

	return true, s.sendMessage(ctx, message.ChatID, fmt.Sprintf("#%d 的工单已回复成功", ticketID))
}

func (s *Service) handleChatJoinRequest(ctx context.Context, payload map[string]any) error {
	if s.db == nil {
		return errors.New("telegram service unavailable")
	}

	chatPayload, ok := payload["chat"].(map[string]any)
	if !ok {
		return nil
	}
	fromPayload, ok := payload["from"].(map[string]any)
	if !ok {
		return nil
	}

	chatID, ok := anyToInt64(chatPayload["id"])
	if !ok {
		return nil
	}
	userID, ok := anyToInt64(fromPayload["id"])
	if !ok {
		return nil
	}

	var (
		id             int64
		banned         int64
		transferEnable int64
		u              int64
		d              int64
		expiredAt      sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, banned, transfer_enable, u, d, expired_at FROM v2_user WHERE telegram_id = $1 LIMIT 1`, userID).
		Scan(&id, &banned, &transferEnable, &u, &d, &expiredAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if s.declineJoin != nil {
				return s.declineJoin(ctx, chatID, userID)
			}
			return nil
		}
		return err
	}

	available := banned == 0 && transferEnable-(u+d) > 0
	if expiredAt.Valid && expiredAt.Int64 > 0 && expiredAt.Int64 <= time.Now().Unix() {
		available = false
	}

	if available {
		if s.approveJoin != nil {
			return s.approveJoin(ctx, chatID, userID)
		}
		return nil
	}
	if s.declineJoin != nil {
		return s.declineJoin(ctx, chatID, userID)
	}
	return nil
}

func parseWebhookMessage(payload map[string]any) (webhookMessage, bool) {
	text := strings.TrimSpace(anyToString(payload["text"]))
	if text == "" {
		return webhookMessage{}, false
	}

	chatPayload, ok := payload["chat"].(map[string]any)
	if !ok {
		return webhookMessage{}, false
	}

	chatID, ok := anyToInt64(chatPayload["id"])
	if !ok {
		return webhookMessage{}, false
	}
	messageID, _ := anyToInt64(payload["message_id"])

	message := webhookMessage{
		ID:        messageID,
		ChatID:    chatID,
		Text:      text,
		IsPrivate: strings.EqualFold(strings.TrimSpace(anyToString(chatPayload["type"])), "private"),
	}

	if replyPayload, ok := payload["reply_to_message"].(map[string]any); ok {
		message.ReplyText = strings.TrimSpace(anyToString(replyPayload["text"]))
	}

	return message, true
}

func parseWebhookCallbackQuery(payload map[string]any) (webhookCallbackQuery, bool) {
	callback := webhookCallbackQuery{
		ID:   strings.TrimSpace(anyToString(payload["id"])),
		Data: strings.TrimSpace(anyToString(payload["data"])),
	}
	if callback.ID == "" || callback.Data == "" {
		return webhookCallbackQuery{}, false
	}

	fromPayload, ok := payload["from"].(map[string]any)
	if !ok {
		return webhookCallbackQuery{}, false
	}
	callback.FromID, ok = anyToInt64(fromPayload["id"])
	if !ok {
		return webhookCallbackQuery{}, false
	}

	messagePayload, ok := payload["message"].(map[string]any)
	if !ok {
		return webhookCallbackQuery{}, false
	}
	chatPayload, ok := messagePayload["chat"].(map[string]any)
	if !ok {
		return webhookCallbackQuery{}, false
	}
	callback.ChatID, ok = anyToInt64(chatPayload["id"])
	if !ok {
		return webhookCallbackQuery{}, false
	}
	callback.IsPrivate = strings.EqualFold(strings.TrimSpace(anyToString(chatPayload["type"])), "private")
	return callback, true
}

func parseWebhookCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil
	}

	command := strings.TrimSpace(fields[0])
	if index := strings.Index(command, "@"); index >= 0 {
		command = command[:index]
	}
	return command, fields[1:]
}

func extractSubscribeToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if token := strings.TrimSpace(parsed.Query().Get("token")); token != "" {
		return token
	}

	path := strings.Trim(strings.TrimSpace(parsed.EscapedPath()), "/")
	if path != "" {
		parts := strings.Split(path, "/")
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" && !strings.EqualFold(last, "subscribe") {
			if unescaped, err := url.PathUnescape(last); err == nil {
				last = strings.TrimSpace(unescaped)
			}
			return last
		}
	}

	if !strings.Contains(raw, "://") && !strings.Contains(raw, "/") && !strings.Contains(raw, "?") {
		return raw
	}
	return ""
}

func formatTrafficBytes(value int64) string {
	if value < 0 {
		value = 0
	}

	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	size := float64(value)
	index := 0
	for size >= 1024 && index < len(units)-1 {
		size /= 1024
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%d %s", value, units[index])
	}
	return fmt.Sprintf("%.2f %s", size, units[index])
}

func anyToString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

func anyToInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
