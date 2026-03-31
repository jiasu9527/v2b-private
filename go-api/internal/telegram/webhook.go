package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
)

var ticketReplyPattern = regexp.MustCompile(`#\s*([0-9]+)`)

type webhookMessage struct {
	ChatID    int64
	Text      string
	IsPrivate bool
	ReplyText string
}

func (s *Service) HandleWebhook(ctx context.Context, payload map[string]any) error {
	if s == nil || len(payload) == 0 {
		return nil
	}

	if joinPayload, ok := payload["chat_join_request"].(map[string]any); ok {
		return s.handleChatJoinRequest(ctx, joinPayload)
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
	default:
		return nil
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
		appName = "V2Board"
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

	message := webhookMessage{
		ChatID:    chatID,
		Text:      text,
		IsPrivate: strings.EqualFold(strings.TrimSpace(anyToString(chatPayload["type"])), "private"),
	}

	if replyPayload, ok := payload["reply_to_message"].(map[string]any); ok {
		message.ReplyText = strings.TrimSpace(anyToString(replyPayload["text"]))
	}

	return message, true
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
	return strings.TrimSpace(parsed.Query().Get("token"))
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
