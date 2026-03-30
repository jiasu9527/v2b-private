package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type inviteCampaignRow struct {
	ID            int64
	UserID        int64
	PlanID        int64
	Period        string
	InviteCodeID  sql.NullInt64
	InviteCode    sql.NullString
	RewardAmount  int64
	TargetAmount  int64
	CurrentAmount int64
	InviteCount   int64
	Status        int64
	StartedAt     int64
	ExpiredAt     int64
	CompletedAt   sql.NullInt64
	AbandonedAt   sql.NullInt64
	UsedAt        sql.NullInt64
	CreatedAt     int64
	UpdatedAt     int64
}

func (s *DBService) ListInviteCampaigns(ctx context.Context, req InviteCampaignListRequest) (InviteCampaignListResult, error) {
	if s.db == nil {
		return InviteCampaignListResult{}, ErrUnavailable
	}
	if req.Current <= 0 {
		req.Current = 1
	}
	if req.PageSize < 10 {
		req.PageSize = 10
	}

	whereSQL, args, err := s.inviteCampaignFilterSQL(ctx, req.Filters)
	if err != nil {
		return InviteCampaignListResult{}, err
	}

	total, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_invite_campaign`+whereSQL, args...)
	if err != nil {
		return InviteCampaignListResult{}, err
	}

	pageArgs := append(append([]any(nil), args...), req.PageSize, (req.Current-1)*req.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at
FROM v2_invite_campaign`+whereSQL+`
ORDER BY created_at DESC
LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2), pageArgs...)
	if err != nil {
		return InviteCampaignListResult{}, fmt.Errorf("query invite campaigns: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		row, err := scanInviteCampaignRow(rows)
		if err != nil {
			return InviteCampaignListResult{}, err
		}
		if err := s.refreshInviteCampaignStatus(ctx, &row); err != nil {
			return InviteCampaignListResult{}, err
		}

		plan, _ := s.fetchPlanSummary(ctx, row.PlanID)
		item := serializeInviteCampaign(row, plan)
		if user, _ := s.fetchUserSummary(ctx, row.UserID); user != nil {
			item["user_email"] = user["email"]
		} else {
			item["user_email"] = nil
		}
		if plan != nil {
			item["plan_name"] = plan["name"]
		} else {
			item["plan_name"] = nil
		}
		if usedOrder, _ := s.fetchCampaignUsedOrder(ctx, row.ID); usedOrder != nil {
			item["used_order_trade_no"] = usedOrder["trade_no"]
			item["used_order_status"] = usedOrder["status"]
		} else {
			item["used_order_trade_no"] = nil
			item["used_order_status"] = nil
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return InviteCampaignListResult{}, fmt.Errorf("iterate invite campaigns: %w", err)
	}

	return InviteCampaignListResult{Data: result, Total: total}, nil
}

func (s *DBService) GetInviteCampaign(ctx context.Context, id int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if id <= 0 {
		return nil, errors.New("任务不存在")
	}

	row, err := s.fetchInviteCampaignRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.refreshInviteCampaignStatus(ctx, &row); err != nil {
		return nil, err
	}

	plan, _ := s.fetchPlanSummary(ctx, row.PlanID)
	data := serializeInviteCampaign(row, plan)
	user, _ := s.fetchUserSummary(ctx, row.UserID)
	usedOrder, _ := s.fetchCampaignUsedOrder(ctx, row.ID)
	data["user"] = user
	data["used_order"] = usedOrder
	return data, nil
}

func (s *DBService) ListInviteCampaignRecords(ctx context.Context, req InviteCampaignRecordListRequest) (InviteCampaignRecordListResult, error) {
	if s.db == nil {
		return InviteCampaignRecordListResult{}, ErrUnavailable
	}
	if req.CampaignID <= 0 {
		return InviteCampaignRecordListResult{}, errors.New("任务不存在")
	}
	if req.Current <= 0 {
		req.Current = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 1
	}
	if _, err := s.fetchInviteCampaignRow(ctx, req.CampaignID); err != nil {
		return InviteCampaignRecordListResult{}, err
	}

	total, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_invite_campaign_record WHERE campaign_id = $1`, req.CampaignID)
	if err != nil {
		return InviteCampaignRecordListResult{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, campaign_id, invitee_user_id, invite_code, reward_amount, created_at, updated_at
FROM v2_invite_campaign_record
WHERE campaign_id = $1
ORDER BY id DESC
LIMIT $2 OFFSET $3`, req.CampaignID, req.PageSize, (req.Current-1)*req.PageSize)
	if err != nil {
		return InviteCampaignRecordListResult{}, fmt.Errorf("query invite campaign records: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id            int64
			campaignID    int64
			inviteeUserID int64
			inviteCode    string
			rewardAmount  int64
			createdAt     int64
			updatedAt     int64
		)
		if err := rows.Scan(&id, &campaignID, &inviteeUserID, &inviteCode, &rewardAmount, &createdAt, &updatedAt); err != nil {
			return InviteCampaignRecordListResult{}, fmt.Errorf("scan invite campaign record: %w", err)
		}
		email, _ := s.fetchUserEmail(ctx, inviteeUserID)
		var inviteeEmail any
		if email != "" {
			inviteeEmail = email
		}
		result = append(result, map[string]any{
			"id":              id,
			"campaign_id":     campaignID,
			"invitee_user_id": inviteeUserID,
			"invite_code":     inviteCode,
			"reward_amount":   rewardAmount,
			"created_at":      createdAt,
			"updated_at":      updatedAt,
			"invitee_email":   inviteeEmail,
		})
	}
	if err := rows.Err(); err != nil {
		return InviteCampaignRecordListResult{}, fmt.Errorf("iterate invite campaign records: %w", err)
	}

	return InviteCampaignRecordListResult{Data: result, Total: total}, nil
}

func (s *DBService) inviteCampaignFilterSQL(ctx context.Context, filters []InviteCampaignFilter) (string, []any, error) {
	clauses := make([]string, 0)
	args := make([]any, 0)
	for _, filter := range filters {
		key := strings.TrimSpace(filter.Key)
		condition := strings.TrimSpace(filter.Condition)
		value := filter.Value
		if key == "" {
			continue
		}
		if condition == "" {
			condition = "="
		}
		switch key {
		case "email":
			pattern := "%" + value + "%"
			var userID int64
			err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE email LIKE $1 ORDER BY id ASC LIMIT 1`, pattern).Scan(&userID)
			if err != nil && err != sql.ErrNoRows {
				return "", nil, fmt.Errorf("query invite campaign user filter: %w", err)
			}
			args = append(args, userID)
			clauses = append(clauses, "user_id = $"+strconv.Itoa(len(args)))
		case "invite_code":
			if condition == "模糊" {
				condition = "LIKE"
				value = "%" + value + "%"
			}
			args = append(args, value)
			clauses = append(clauses, "invite_code "+safeInviteCondition(condition)+" $"+strconv.Itoa(len(args)))
		case "status":
			args = append(args, value)
			clauses = append(clauses, "status = $"+strconv.Itoa(len(args)))
		}
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func safeInviteCondition(condition string) string {
	switch strings.ToUpper(strings.TrimSpace(condition)) {
	case "LIKE":
		return "LIKE"
	default:
		return "="
	}
}

func (s *DBService) fetchInviteCampaignRow(ctx context.Context, id int64) (inviteCampaignRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at
FROM v2_invite_campaign
WHERE id = $1
LIMIT 1`, id)
	record, err := scanInviteCampaignRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return inviteCampaignRow{}, errors.New("任务不存在")
		}
		return inviteCampaignRow{}, err
	}
	return record, nil
}

func scanInviteCampaignRow(scanner interface{ Scan(...any) error }) (inviteCampaignRow, error) {
	var row inviteCampaignRow
	if err := scanner.Scan(
		&row.ID,
		&row.UserID,
		&row.PlanID,
		&row.Period,
		&row.InviteCodeID,
		&row.InviteCode,
		&row.RewardAmount,
		&row.TargetAmount,
		&row.CurrentAmount,
		&row.InviteCount,
		&row.Status,
		&row.StartedAt,
		&row.ExpiredAt,
		&row.CompletedAt,
		&row.AbandonedAt,
		&row.UsedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return inviteCampaignRow{}, err
	}
	return row, nil
}

func (s *DBService) refreshInviteCampaignStatus(ctx context.Context, row *inviteCampaignRow) error {
	now := time.Now().Unix()
	nextStatus := row.Status
	var timestampField string

	if (row.Status == 0 || row.Status == 1) && row.ExpiredAt <= now {
		nextStatus = 2
	} else if row.Status == 0 && row.CurrentAmount >= row.TargetAmount {
		nextStatus = 1
		timestampField = "completed_at"
	}
	if nextStatus == row.Status {
		return nil
	}

	if timestampField == "completed_at" {
		if _, err := s.db.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = $2, completed_at = $3, updated_at = $4 WHERE id = $1`, row.ID, nextStatus, now, now); err != nil {
			return fmt.Errorf("refresh invite campaign status: %w", err)
		}
		row.CompletedAt = sql.NullInt64{Int64: now, Valid: true}
	} else {
		if _, err := s.db.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = $2, updated_at = $3 WHERE id = $1`, row.ID, nextStatus, now); err != nil {
			return fmt.Errorf("refresh invite campaign status: %w", err)
		}
	}
	row.Status = nextStatus
	row.UpdatedAt = now
	return nil
}

func serializeInviteCampaign(row inviteCampaignRow, plan map[string]any) map[string]any {
	targetAmount := row.TargetAmount
	currentAmount := row.CurrentAmount
	remainingAmount := int64(0)
	if targetAmount > currentAmount {
		remainingAmount = targetAmount - currentAmount
	}
	discountAmount := currentAmount
	if discountAmount > targetAmount {
		discountAmount = targetAmount
	}

	return map[string]any{
		"id":               row.ID,
		"user_id":          row.UserID,
		"plan_id":          row.PlanID,
		"period":           row.Period,
		"invite_code_id":   nullInt64Any(row.InviteCodeID),
		"invite_code":      nullStringValue(row.InviteCode),
		"reward_amount":    row.RewardAmount,
		"target_amount":    targetAmount,
		"current_amount":   currentAmount,
		"remaining_amount": remainingAmount,
		"discount_amount":  discountAmount,
		"invite_count":     row.InviteCount,
		"status":           row.Status,
		"started_at":       row.StartedAt,
		"expired_at":       row.ExpiredAt,
		"completed_at":     nullInt64Any(row.CompletedAt),
		"abandoned_at":     nullInt64Any(row.AbandonedAt),
		"used_at":          nullInt64Any(row.UsedAt),
		"plan":             plan,
	}
}

func (s *DBService) fetchPlanSummary(ctx context.Context, id int64) (map[string]any, error) {
	if id <= 0 {
		return nil, nil
	}
	var planID int64
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT id, name FROM v2_plan WHERE id = $1 LIMIT 1`, id).Scan(&planID, &name)
	if err == nil {
		return map[string]any{"id": planID, "name": name}, nil
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return nil, fmt.Errorf("query invite campaign plan: %w", err)
}

func (s *DBService) fetchUserSummary(ctx context.Context, id int64) (map[string]any, error) {
	if id <= 0 {
		return nil, nil
	}
	var userID int64
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT id, email FROM v2_user WHERE id = $1 LIMIT 1`, id).Scan(&userID, &email)
	if err == nil {
		return map[string]any{"id": userID, "email": email}, nil
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return nil, fmt.Errorf("query invite campaign user: %w", err)
}

func (s *DBService) fetchCampaignUsedOrder(ctx context.Context, campaignID int64) (map[string]any, error) {
	var (
		id                           int64
		tradeNo                      string
		status                       int64
		inviteCampaignDiscountAmount int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, trade_no, status, invite_campaign_discount_amount
FROM v2_order
WHERE invite_campaign_id = $1
ORDER BY id DESC
LIMIT 1`, campaignID).Scan(&id, &tradeNo, &status, &inviteCampaignDiscountAmount)
	if err == nil {
		return map[string]any{
			"id":                              id,
			"trade_no":                        tradeNo,
			"status":                          status,
			"invite_campaign_discount_amount": inviteCampaignDiscountAmount,
		}, nil
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return nil, fmt.Errorf("query invite campaign order: %w", err)
}

func nullInt64Any(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
