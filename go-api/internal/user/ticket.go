package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *DBService) Tickets(ctx context.Context, userID int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	rows, err := s.queryRowsAsMaps(ctx, `SELECT id, user_id, subject, level, status, reply_status, created_at, updated_at
FROM v2_ticket
WHERE user_id = $1
ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("query user tickets: %w", err)
	}
	return rows, nil
}

func (s *DBService) TicketDetail(ctx context.Context, userID, id int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if id <= 0 {
		return nil, errors.New("Ticket does not exist")
	}

	ticket, err := s.querySingleMap(ctx, `SELECT id, user_id, subject, level, status, reply_status, created_at, updated_at
FROM v2_ticket
WHERE id = $1 AND user_id = $2
LIMIT 1`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("query user ticket detail: %w", err)
	}
	if ticket == nil {
		return nil, errors.New("Ticket does not exist")
	}

	messages, err := s.queryRowsAsMaps(ctx, `SELECT id, user_id, ticket_id, message, created_at, updated_at
FROM v2_ticket_message
WHERE ticket_id = $1
ORDER BY id ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("query user ticket messages: %w", err)
	}
	for i := range messages {
		messages[i]["is_me"] = mapInt64(messages[i]["user_id"]) == userID
	}
	ticket["message"] = messages
	return ticket, nil
}

func (s *DBService) CreateTicket(ctx context.Context, userID int64, req TicketCreateRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	req.Subject = strings.TrimSpace(req.Subject)
	req.Message = strings.TrimSpace(req.Message)
	if req.Subject == "" {
		return false, errors.New("Ticket subject cannot be empty")
	}
	if req.Message == "" {
		return false, errors.New("Message cannot be empty")
	}
	if req.Level < 0 || req.Level > 2 {
		return false, errors.New("Incorrect ticket level format")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("Failed to open ticket")
	}
	defer tx.Rollback()

	var hasOpen bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ticket WHERE status = 0 AND user_id = $1)`, userID).Scan(&hasOpen); err != nil {
		return false, errors.New("Failed to open ticket")
	}
	if hasOpen {
		return false, errors.New("There are other unresolved tickets")
	}

	switch s.currentConfig().TicketStatus {
	case 0:
	case 1:
		var hasPaidOrder bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_order WHERE user_id = $1 AND status IN (3, 4))`, userID).Scan(&hasPaidOrder); err != nil {
			return false, errors.New("Failed to open ticket")
		}
		if !hasPaidOrder {
			return false, errors.New("请先购买套餐")
		}
	case 2:
		return false, errors.New("当前套餐不允许发起工单")
	default:
		return false, errors.New("未知的工单状态")
	}

	now := time.Now().Unix()
	var ticketID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_ticket (user_id, subject, level, status, reply_status, created_at, updated_at)
VALUES ($1, $2, $3, 0, 0, $4, $4)
RETURNING id`, userID, req.Subject, req.Level, now).Scan(&ticketID); err != nil {
		return false, errors.New("Failed to open ticket")
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_ticket_message (user_id, ticket_id, message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)`, userID, ticketID, req.Message, now); err != nil {
		return false, errors.New("Failed to open ticket")
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("Failed to open ticket")
	}
	_ = s.notifyTicketAdmins(ctx, ticketAdminNotification{
		TicketID: ticketID,
		UserID:   userID,
		Subject:  req.Subject,
		Message:  req.Message,
	})
	return true, nil
}

func (s *DBService) ReplyTicket(ctx context.Context, userID, id int64, message string) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("Invalid parameter")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return false, errors.New("Message cannot be empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("Ticket reply failed")
	}
	defer tx.Rollback()

	var (
		status  int64
		subject string
	)
	if err := tx.QueryRowContext(ctx, `SELECT status, subject FROM v2_ticket WHERE id = $1 AND user_id = $2 LIMIT 1`, id, userID).Scan(&status, &subject); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("Ticket does not exist")
		}
		return false, errors.New("Ticket reply failed")
	}
	if status != 0 {
		return false, errors.New("The ticket is closed and cannot be replied")
	}

	var lastMessageUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM v2_ticket_message WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&lastMessageUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("Ticket reply failed")
		}
		return false, errors.New("Ticket reply failed")
	}
	if lastMessageUserID == userID {
		return false, errors.New("Please wait for the technical enginneer to reply")
	}

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_ticket_message (user_id, ticket_id, message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)`, userID, id, message, now); err != nil {
		return false, errors.New("Ticket reply failed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_ticket SET reply_status = 0, updated_at = $2 WHERE id = $1`, id, now); err != nil {
		return false, errors.New("Ticket reply failed")
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("Ticket reply failed")
	}
	_ = s.notifyTicketAdmins(ctx, ticketAdminNotification{
		TicketID: id,
		UserID:   userID,
		Subject:  subject,
		Message:  message,
	})
	return true, nil
}

func (s *DBService) CloseTicket(ctx context.Context, userID, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("Invalid parameter")
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_ticket SET status = 1, updated_at = $3 WHERE id = $1 AND user_id = $2`, id, userID, time.Now().Unix())
	if err != nil {
		return false, errors.New("Close failed")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("Close failed")
	}
	if affected == 0 {
		return false, errors.New("Ticket does not exist")
	}
	return true, nil
}

func (s *DBService) WithdrawTicket(ctx context.Context, userID int64, method, account string) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	method = strings.TrimSpace(method)
	account = strings.TrimSpace(account)
	if method == "" {
		return false, errors.New("The withdrawal method cannot be empty")
	}
	if account == "" {
		return false, errors.New("The withdrawal account cannot be empty")
	}
	cfg := s.currentConfig()
	if cfg.WithdrawCloseEnable {
		return false, errors.New("user.ticket.withdraw.not_support_withdraw")
	}

	allowedMethods := cfg.CommissionWithdrawMethods
	if len(allowedMethods) == 0 {
		allowedMethods = []string{"支付宝", "USDT", "Paypal"}
	}
	if !containsString(allowedMethods, method) {
		return false, errors.New("Unsupported withdrawal method")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("Failed to open ticket")
	}
	defer tx.Rollback()

	var commissionBalance int64
	if err := tx.QueryRowContext(ctx, `SELECT commission_balance FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&commissionBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, errors.New("Failed to open ticket")
	}

	if cfg.CommissionWithdrawLimit > (commissionBalance / 100) {
		return false, fmt.Errorf("The current required minimum withdrawal commission is %d", cfg.CommissionWithdrawLimit)
	}

	now := time.Now().Unix()
	var ticketID int64
	subject := "[Commission Withdrawal Request] This ticket is opened by the system"
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_ticket (user_id, subject, level, status, reply_status, created_at, updated_at)
VALUES ($1, $2, 2, 0, 0, $3, $3)
RETURNING id`, userID, subject, now).Scan(&ticketID); err != nil {
		return false, errors.New("Failed to open ticket")
	}

	message := fmt.Sprintf("Withdrawal method：%s\r\nWithdrawal account：%s", method, account)
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_ticket_message (user_id, ticket_id, message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)`, userID, ticketID, message, now); err != nil {
		return false, errors.New("Failed to open ticket")
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("Failed to open ticket")
	}
	_ = s.notifyTicketAdmins(ctx, ticketAdminNotification{
		TicketID: ticketID,
		UserID:   userID,
		Subject:  subject,
		Message:  message,
	})
	return true, nil
}
