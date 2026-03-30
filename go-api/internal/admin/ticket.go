package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ticketRow struct {
	ID          int64
	UserID      int64
	Subject     string
	Level       int64
	Status      int64
	ReplyStatus int64
	CreatedAt   int64
	UpdatedAt   int64
}

type ticketMessageRow struct {
	ID        int64
	UserID    int64
	TicketID  int64
	Message   string
	CreatedAt int64
	UpdatedAt int64
}

func (s *DBService) ListTickets(ctx context.Context, req TicketListRequest) (TicketListResult, error) {
	if s.db == nil {
		return TicketListResult{}, ErrUnavailable
	}

	current := req.Current
	if current <= 0 {
		current = 1
	}
	pageSize := req.PageSize
	if pageSize < 10 {
		pageSize = 10
	}

	clauses := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if req.Status != nil {
		args = append(args, *req.Status)
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(req.ReplyStatus) > 0 {
		clause, nextArgs := appendInt64InClause(args, "reply_status", req.ReplyStatus)
		args = nextArgs
		clauses = append(clauses, clause)
	}

	email := strings.TrimSpace(req.Email)
	if email != "" {
		var userID int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE email = $1 LIMIT 1`, email).Scan(&userID)
		switch {
		case err == nil:
			args = append(args, userID)
			clauses = append(clauses, fmt.Sprintf("user_id = $%d", len(args)))
		case errors.Is(err, sql.ErrNoRows):
		default:
			return TicketListResult{}, fmt.Errorf("query ticket user: %w", err)
		}
	}

	whereClause := ""
	if len(clauses) > 0 {
		whereClause = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ticket`+whereClause, args...).Scan(&total); err != nil {
		return TicketListResult{}, fmt.Errorf("count tickets: %w", err)
	}

	queryArgs := append(append([]any{}, args...), pageSize, (current-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, user_id, subject, level, status, reply_status, created_at, updated_at
FROM v2_ticket
%s
ORDER BY updated_at DESC
LIMIT $%d OFFSET $%d`, whereClause, len(queryArgs)-1, len(queryArgs)), queryArgs...)
	if err != nil {
		return TicketListResult{}, fmt.Errorf("query tickets: %w", err)
	}
	defer rows.Close()

	result := make([]TicketRecord, 0)
	for rows.Next() {
		row, err := scanTicketRow(rows)
		if err != nil {
			return TicketListResult{}, err
		}
		result = append(result, ticketRecord(row))
	}
	if err := rows.Err(); err != nil {
		return TicketListResult{}, fmt.Errorf("iterate tickets: %w", err)
	}

	return TicketListResult{Data: result, Total: total}, nil
}

func (s *DBService) GetTicket(ctx context.Context, id int64) (TicketDetail, error) {
	if s.db == nil {
		return TicketDetail{}, ErrUnavailable
	}

	row, err := scanTicketRow(s.db.QueryRowContext(ctx, `SELECT id, user_id, subject, level, status, reply_status, created_at, updated_at
FROM v2_ticket
WHERE id = $1
LIMIT 1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TicketDetail{}, errors.New("工单不存在")
		}
		return TicketDetail{}, fmt.Errorf("query ticket: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, ticket_id, message, created_at, updated_at
FROM v2_ticket_message
WHERE ticket_id = $1
ORDER BY id ASC`, id)
	if err != nil {
		return TicketDetail{}, fmt.Errorf("query ticket messages: %w", err)
	}
	defer rows.Close()

	messages := make([]TicketMessageRecord, 0)
	for rows.Next() {
		messageRow, err := scanTicketMessageRow(rows)
		if err != nil {
			return TicketDetail{}, err
		}
		messages = append(messages, ticketMessageRecord(messageRow, row.UserID))
	}
	if err := rows.Err(); err != nil {
		return TicketDetail{}, fmt.Errorf("iterate ticket messages: %w", err)
	}

	return TicketDetail{
		TicketRecord: ticketRecord(row),
		Messages:     messages,
	}, nil
}

func (s *DBService) ReplyTicket(ctx context.Context, req TicketReplyRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	if req.ID <= 0 {
		return false, errors.New("参数错误")
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		return false, errors.New("消息不能为空")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("工单回复失败")
	}
	defer tx.Rollback()

	var (
		userID  int64
		subject string
		email   string
	)
	if err := tx.QueryRowContext(ctx, `SELECT t.user_id, t.subject, COALESCE(u.email, '')
FROM v2_ticket t
LEFT JOIN v2_user u ON u.id = t.user_id
WHERE t.id = $1
LIMIT 1`, req.ID).Scan(&userID, &subject, &email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("工单不存在")
		}
		return false, errors.New("工单回复失败")
	}

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_ticket_message (user_id, ticket_id, message, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)`, req.AdminID, req.ID, req.Message, now, now); err != nil {
		return false, errors.New("工单回复失败")
	}

	replyStatus := int64(0)
	if req.AdminID != userID {
		replyStatus = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE v2_ticket
SET status = 0, reply_status = $2, updated_at = $3
WHERE id = $1`, req.ID, replyStatus, now)
	if err != nil {
		return false, errors.New("工单回复失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("工单回复失败")
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("工单回复失败")
	}
	_ = s.notifyTicketReply(ctx, email, subject, req.Message)
	return true, nil
}

func (s *DBService) CloseTicket(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数错误")
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_ticket SET status = 1, updated_at = $2 WHERE id = $1`, id, time.Now().Unix())
	if err != nil {
		return false, errors.New("关闭失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("关闭失败")
	}
	if affected == 0 {
		return false, errors.New("工单不存在")
	}
	return true, nil
}

func scanTicketRow(scanner interface{ Scan(...any) error }) (ticketRow, error) {
	var row ticketRow
	if err := scanner.Scan(
		&row.ID,
		&row.UserID,
		&row.Subject,
		&row.Level,
		&row.Status,
		&row.ReplyStatus,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return ticketRow{}, err
	}
	return row, nil
}

func ticketRecord(row ticketRow) TicketRecord {
	return TicketRecord{
		ID:          row.ID,
		UserID:      row.UserID,
		Subject:     row.Subject,
		Level:       row.Level,
		Status:      row.Status,
		ReplyStatus: row.ReplyStatus,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func scanTicketMessageRow(scanner interface{ Scan(...any) error }) (ticketMessageRow, error) {
	var row ticketMessageRow
	if err := scanner.Scan(
		&row.ID,
		&row.UserID,
		&row.TicketID,
		&row.Message,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return ticketMessageRow{}, err
	}
	return row, nil
}

func ticketMessageRecord(row ticketMessageRow, ticketUserID int64) TicketMessageRecord {
	return TicketMessageRecord{
		ID:        row.ID,
		UserID:    row.UserID,
		TicketID:  row.TicketID,
		Message:   row.Message,
		IsMe:      row.UserID != ticketUserID,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func appendInt64InClause(args []any, column string, values []int64) (string, []any) {
	placeholders := make([]string, 0, len(values))
	for _, value := range values {
		args = append(args, value)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ", ")), args
}
