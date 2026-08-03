package user

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const inviteCodeAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func (s *DBService) NoticeDetail(ctx context.Context, id int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if id <= 0 {
		return nil, ErrNoticeNotFound
	}

	row, err := s.querySingleMap(ctx, `SELECT id, title, content, "show", img_url, tags, created_at, updated_at
FROM v2_notice
WHERE id = $1 AND "show" = 1
LIMIT 1`, id)
	if err != nil {
		return nil, fmt.Errorf("query user notice detail: %w", err)
	}
	if row == nil {
		return nil, ErrNoticeNotFound
	}
	return normalizeNoticeRow(row), nil
}

func (s *DBService) Notices(ctx context.Context, current, pageSize int64) ([]map[string]any, int64, error) {
	if s.db == nil {
		return nil, 0, ErrUnavailable
	}
	if current <= 0 {
		current = 1
	}
	if pageSize <= 0 {
		pageSize = 5
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_notice WHERE "show" = 1`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count user notices: %w", err)
	}

	offset := (current - 1) * pageSize
	rows, err := s.queryRowsAsMaps(ctx, `SELECT id, title, content, "show", img_url, tags, created_at, updated_at
FROM v2_notice
WHERE "show" = 1
ORDER BY created_at DESC
LIMIT $1 OFFSET $2`, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query user notices: %w", err)
	}
	for i := range rows {
		rows[i] = normalizeNoticeRow(rows[i])
	}
	return rows, total, nil
}

func (s *DBService) CreateInviteCode(ctx context.Context, userID int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if userID <= 0 {
		return false, ErrNotFound
	}

	cfg := s.currentConfig()
	limit := cfg.InviteGenLimit
	if limit <= 0 {
		limit = 5
	}

	var activeCount int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_invite_code WHERE user_id = $1 AND status = 0`, userID).Scan(&activeCount); err != nil {
		return false, fmt.Errorf("count active invite codes: %w", err)
	}
	if activeCount >= limit {
		return false, ErrInviteLimitReached
	}

	now := time.Now().Unix()
	code, err := randomInviteCode(8)
	if err != nil {
		return false, err
	}

	if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_invite_code (user_id, code, status, pv, created_at, updated_at)
VALUES ($1, $2, 0, 0, $3, $3)`, userID, code, now); err != nil {
		return false, fmt.Errorf("insert invite code: %w", err)
	}

	return true, nil
}

func (s *DBService) InviteOverview(ctx context.Context, userID int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if userID <= 0 {
		return nil, ErrNotFound
	}

	var (
		commissionRate    sql.NullInt64
		commissionBalance int64
	)
	if err := s.db.QueryRowContext(ctx, `SELECT commission_rate, commission_balance
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(&commissionRate, &commissionBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query user invite overview: %w", err)
	}

	codes, err := s.queryRowsAsMaps(ctx, `SELECT id, user_id, code, status, invite_campaign_id, pv, created_at, updated_at
FROM v2_invite_code
WHERE user_id = $1 AND status = 0
ORDER BY id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("query active invite codes: %w", err)
	}
	for i := range codes {
		codes[i]["code"] = strings.TrimSpace(fmt.Sprint(codes[i]["code"]))
	}

	inviteUsers, err := s.count(ctx, `SELECT COUNT(*) FROM v2_user WHERE invite_user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	validCommission, err := s.sumInt64(ctx, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE invite_user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	pendingCommission, err := s.sumInt64(ctx, `SELECT COALESCE(SUM(commission_balance), 0)
FROM v2_order
WHERE status = 3 AND commission_status = 0 AND invite_user_id = $1
  AND type <> 9 AND period <> 'deposit'`, userID)
	if err != nil {
		return nil, err
	}
	cfg := s.currentConfig()
	if cfg.CommissionDistEnabled {
		rate := cfg.CommissionDistL1
		if rate <= 0 {
			rate = 30
		}
		pendingCommission = pendingCommission * rate / 100
	}

	rate := cfg.InviteCommission
	if commissionRate.Valid {
		rate = commissionRate.Int64
	}

	return map[string]any{
		"codes": codes,
		"stat":  []int64{inviteUsers, validCommission, pendingCommission, rate, commissionBalance},
	}, nil
}

func (s *DBService) InviteDetails(ctx context.Context, userID, current, pageSize int64) ([]map[string]any, int64, error) {
	if s.db == nil {
		return nil, 0, ErrUnavailable
	}
	if userID <= 0 {
		return nil, 0, ErrNotFound
	}
	if current <= 0 {
		current = 1
	}
	if pageSize < 10 {
		pageSize = 10
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_commission_log
WHERE invite_user_id = $1 AND get_amount > 0`, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count invite commission details: %w", err)
	}

	offset := (current - 1) * pageSize
	rows, err := s.queryRowsAsMaps(ctx, `SELECT id, trade_no, order_amount, get_amount, created_at
FROM v2_commission_log
WHERE invite_user_id = $1 AND get_amount > 0
ORDER BY created_at DESC
LIMIT $2 OFFSET $3`, userID, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query invite commission details: %w", err)
	}
	return rows, total, nil
}

func (s *DBService) sumInt64(ctx context.Context, query string, userID int64) (int64, error) {
	var result int64
	if err := s.db.QueryRowContext(ctx, query, userID).Scan(&result); err != nil {
		return 0, fmt.Errorf("sum int64 query: %w", err)
	}
	return result, nil
}

func normalizeNoticeRow(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	row["tags"] = decodeNoticeTagsValue(row["tags"])
	if img, ok := row["img_url"]; ok && strings.TrimSpace(fmt.Sprint(img)) == "" {
		row["img_url"] = nil
	}
	return row
}

func decodeNoticeTagsValue(raw any) []string {
	if raw == nil {
		return nil
	}
	value := strings.TrimSpace(fmt.Sprint(raw))
	if value == "" || value == "<nil>" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(value), &tags); err == nil {
		return tags
	}
	parts := strings.Split(value, ",")
	tags = make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
}

func randomInviteCode(length int) (string, error) {
	if length <= 0 {
		length = 8
	}
	buf := make([]byte, length)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate invite code: %w", err)
	}
	result := make([]byte, length)
	for i, b := range buf {
		result[i] = inviteCodeAlphabet[int(b)%len(inviteCodeAlphabet)]
	}
	return string(result), nil
}
