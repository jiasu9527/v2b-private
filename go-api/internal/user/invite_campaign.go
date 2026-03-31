package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"forest/go-api/internal/config"
)

const (
	inviteCampaignStatusActive = int64(iota)
	inviteCampaignStatusCompleted
	inviteCampaignStatusExpired
	inviteCampaignStatusAbandoned
	inviteCampaignStatusUsed
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

func (s *DBService) CreateInviteCampaign(ctx context.Context, userID, planID int64, period string) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	cfg := s.inviteCampaignConfig()
	if !cfg.InviteCampaignEnable {
		return nil, errors.New("Invite campaign is disabled")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin invite campaign transaction: %w", err)
	}
	defer tx.Rollback()

	plan, ok, err := s.loadPlanTx(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if !ok || !plan.isShown() {
		return nil, ErrPlanNotFound
	}
	targetAmount, ok := plan.priceForPeriod(strings.TrimSpace(period))
	if !ok {
		return nil, ErrPeriodUnavailable
	}

	if existing, ok, err := s.currentInviteCampaignTx(ctx, tx, userID); err != nil {
		return nil, err
	} else if ok {
		now := time.Now().Unix()
		if (existing.Status == inviteCampaignStatusActive || existing.Status == inviteCampaignStatusCompleted) && existing.ExpiredAt <= now {
			if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = $2, updated_at = $3 WHERE id = $1`, existing.ID, inviteCampaignStatusExpired, now); err != nil {
				return nil, fmt.Errorf("expire invite campaign: %w", err)
			}
		} else {
			if existing.Status == inviteCampaignStatusActive && existing.CurrentAmount >= existing.TargetAmount {
				if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = $2, completed_at = $3, updated_at = $3 WHERE id = $1`, existing.ID, inviteCampaignStatusCompleted, now); err != nil {
					return nil, fmt.Errorf("complete invite campaign: %w", err)
				}
			}
			return nil, errors.New("There is already an active invite campaign task")
		}
	}

	inviteCodeID, inviteCode, err := s.ensureInviteCampaignCodeTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	rewardAmount := inviteCampaignRewardAmountFromConfig(cfg)
	expireAt := now + inviteCampaignExpireSecondsFromConfig(cfg)
	var campaignID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_invite_campaign (user_id, plan_id, period, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, invite_code_id, invite_code, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 0, 0, $6, $7, $8, $9, $10, $7, $7)
RETURNING id`,
		userID, planID, strings.TrimSpace(period), rewardAmount, targetAmount, inviteCampaignStatusActive, now, expireAt, inviteCodeID, inviteCode,
	).Scan(&campaignID)
	if err != nil {
		return nil, fmt.Errorf("insert invite campaign: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit invite campaign: %w", err)
	}
	return s.InviteCampaign(ctx, userID)
}

func (s *DBService) InviteCampaign(ctx context.Context, userID int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	cfg := s.inviteCampaignConfig()
	campaign, err := s.currentInviteCampaign(ctx, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"enabled":  cfg.InviteCampaignEnable,
		"settings": s.inviteCampaignSettings(cfg),
		"data":     s.serializeInviteCampaign(ctx, campaign),
	}, nil
}

func (s *DBService) InviteCampaignRecords(ctx context.Context, userID int64, campaignID *int64, current, pageSize int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if current <= 0 {
		current = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	campaign, err := s.resolveUserInviteCampaign(ctx, userID, campaignID)
	if err != nil {
		return nil, err
	}
	if campaign == nil {
		return map[string]any{
			"data":  []map[string]any{},
			"total": int64(0),
		}, nil
	}

	total, err := s.count(ctx, `SELECT COUNT(*) FROM v2_invite_campaign_record WHERE campaign_id = $1`, campaign.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queryRowsAsMaps(ctx, `SELECT id, campaign_id, invitee_user_id, invite_code, reward_amount, created_at, updated_at
FROM v2_invite_campaign_record
WHERE campaign_id = $1
ORDER BY id DESC
LIMIT $2 OFFSET $3`, campaign.ID, pageSize, (current-1)*pageSize)
	if err != nil {
		return nil, fmt.Errorf("query invite campaign records: %w", err)
	}
	for i := range rows {
		rows[i]["invite_code"] = strings.TrimSpace(fmt.Sprint(rows[i]["invite_code"]))
		email, _ := s.fetchInviteCampaignUserEmail(ctx, mapInt64(rows[i]["invitee_user_id"]))
		if email != "" {
			rows[i]["invitee_email"] = email
		} else {
			rows[i]["invitee_email"] = nil
		}
	}
	return map[string]any{
		"data":  rows,
		"total": total,
	}, nil
}

func (s *DBService) AbandonInviteCampaign(ctx context.Context, userID int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	campaign, err := s.currentInviteCampaign(ctx, userID)
	if err != nil {
		return false, err
	}
	if campaign == nil {
		return false, errors.New("Invite campaign task does not exist")
	}

	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `UPDATE v2_invite_campaign
SET status = $2, abandoned_at = $3, updated_at = $3
WHERE id = $1`, campaign.ID, inviteCampaignStatusAbandoned, now)
	if err != nil {
		return false, fmt.Errorf("abandon invite campaign: %w", err)
	}
	return true, nil
}

func (s *DBService) currentInviteCampaign(ctx context.Context, userID int64) (*inviteCampaignRow, error) {
	row, err := s.fetchLatestInviteCampaign(ctx, userID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	if err := s.refreshInviteCampaignStatus(ctx, row); err != nil {
		return nil, err
	}
	if row.Status != inviteCampaignStatusActive && row.Status != inviteCampaignStatusCompleted {
		return nil, nil
	}
	return row, nil
}

func (s *DBService) resolveUserInviteCampaign(ctx context.Context, userID int64, campaignID *int64) (*inviteCampaignRow, error) {
	if campaignID == nil {
		return s.currentInviteCampaign(ctx, userID)
	}

	row, err := s.fetchInviteCampaignByID(ctx, *campaignID, userID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	if err := s.refreshInviteCampaignStatus(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *DBService) fetchLatestInviteCampaign(ctx context.Context, userID int64) (*inviteCampaignRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at
FROM v2_invite_campaign
WHERE user_id = $1 AND status IN (0, 1)
ORDER BY id DESC
LIMIT 1`, userID)
	record, err := scanInviteCampaignRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query latest invite campaign: %w", err)
	}
	return &record, nil
}

func (s *DBService) fetchInviteCampaignByID(ctx context.Context, id, userID int64) (*inviteCampaignRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at
FROM v2_invite_campaign
WHERE id = $1 AND user_id = $2
LIMIT 1`, id, userID)
	record, err := scanInviteCampaignRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query invite campaign by id: %w", err)
	}
	return &record, nil
}

func (s *DBService) refreshInviteCampaignStatus(ctx context.Context, row *inviteCampaignRow) error {
	now := time.Now().Unix()
	nextStatus := row.Status
	var setCompleted bool

	if (row.Status == inviteCampaignStatusActive || row.Status == inviteCampaignStatusCompleted) && row.ExpiredAt <= now {
		nextStatus = inviteCampaignStatusExpired
	} else if row.Status == inviteCampaignStatusActive && row.CurrentAmount >= row.TargetAmount {
		nextStatus = inviteCampaignStatusCompleted
		setCompleted = true
	}
	if nextStatus == row.Status {
		return nil
	}

	if setCompleted {
		if _, err := s.db.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = $2, completed_at = $3, updated_at = $3 WHERE id = $1`, row.ID, nextStatus, now); err != nil {
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

func (s *DBService) ensureInviteCampaignCodeTx(ctx context.Context, tx *sql.Tx, userID int64) (int64, string, error) {
	var (
		id   int64
		code string
	)
	err := tx.QueryRowContext(ctx, `SELECT id, code
FROM v2_invite_code
WHERE user_id = $1 AND status = 0
ORDER BY id ASC
LIMIT 1
FOR UPDATE`, userID).Scan(&id, &code)
	if err == nil {
		return id, strings.TrimSpace(code), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("query invite code: %w", err)
	}

	now := time.Now().Unix()
	code, err = randomInviteCode(8)
	if err != nil {
		return 0, "", err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_invite_code (user_id, code, status, pv, created_at, updated_at)
VALUES ($1, $2, 0, 0, $3, $3)
RETURNING id`, userID, code, now).Scan(&id)
	if err != nil {
		return 0, "", fmt.Errorf("insert invite code: %w", err)
	}
	return id, code, nil
}

func (s *DBService) inviteCampaignSettings(cfg config.Config) map[string]any {
	return map[string]any{
		"reward_amount":               inviteCampaignRewardAmountFromConfig(cfg),
		"expire_hours":                inviteCampaignExpireHoursFromConfig(cfg),
		"invitee_try_out_plan_id":     cfg.InviteTryOutPlanID,
		"invitee_try_out_transfer_gb": cfg.InviteTryOutTransferGB,
		"invitee_try_out_hours":       cfg.InviteTryOutHours,
	}
}

func (s *DBService) serializeInviteCampaign(ctx context.Context, row *inviteCampaignRow) map[string]any {
	if row == nil {
		return nil
	}

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

	plan, _ := s.findPlanMap(ctx, row.PlanID)
	return map[string]any{
		"id":               row.ID,
		"user_id":          row.UserID,
		"plan_id":          row.PlanID,
		"period":           strings.TrimSpace(row.Period),
		"invite_code_id":   nullInt64Any(row.InviteCodeID),
		"invite_code":      trimNullStringAny(row.InviteCode),
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

func (s *DBService) fetchInviteCampaignUserEmail(ctx context.Context, userID int64) (string, error) {
	if userID <= 0 {
		return "", nil
	}
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("query invite campaign user email: %w", err)
	}
	return email, nil
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
	row.Period = strings.TrimSpace(row.Period)
	if row.InviteCode.Valid {
		row.InviteCode.String = strings.TrimSpace(row.InviteCode.String)
	}
	return row, nil
}

func trimNullStringAny(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return strings.TrimSpace(value.String)
}

func (s *DBService) inviteCampaignRewardAmount() int64 {
	return inviteCampaignRewardAmountFromConfig(s.inviteCampaignConfig())
}

func (s *DBService) inviteCampaignExpireHours() int64 {
	return inviteCampaignExpireHoursFromConfig(s.inviteCampaignConfig())
}

func (s *DBService) inviteCampaignExpireSeconds() int64 {
	return s.inviteCampaignExpireHours() * 3600
}

func (s *DBService) inviteCampaignConfig() config.Config {
	return config.Load()
}

func inviteCampaignRewardAmountFromConfig(cfg config.Config) int64 {
	if cfg.InviteCampaignRewardAmount > 0 {
		return cfg.InviteCampaignRewardAmount
	}
	return 1000
}

func inviteCampaignExpireHoursFromConfig(cfg config.Config) int64 {
	if cfg.InviteCampaignExpireHours > 0 {
		return cfg.InviteCampaignExpireHours
	}
	return 48
}

func inviteCampaignExpireSecondsFromConfig(cfg config.Config) int64 {
	return inviteCampaignExpireHoursFromConfig(cfg) * 3600
}
