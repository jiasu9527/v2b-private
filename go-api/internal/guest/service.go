package guest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"forest/go-api/internal/config"
)

var ErrUnavailable = errors.New("guest service unavailable")

type Service interface {
	Config(context.Context) (map[string]any, error)
	Plans(context.Context) ([]map[string]any, error)
	InvitePreview(context.Context, string) (map[string]any, error)
}

type StaticService struct {
	ConfigPayload        map[string]any
	PlansPayload         []map[string]any
	InvitePreviewPayload map[string]any
	ConfigErr            error
	PlansErr             error
	InvitePreviewErr     error
}

func (s StaticService) Config(context.Context) (map[string]any, error) {
	return s.ConfigPayload, s.ConfigErr
}

func (s StaticService) Plans(context.Context) ([]map[string]any, error) {
	return s.PlansPayload, s.PlansErr
}

func (s StaticService) InvitePreview(context.Context, string) (map[string]any, error) {
	return s.InvitePreviewPayload, s.InvitePreviewErr
}

type DBService struct {
	cfg config.Config
	db  *sql.DB
}

func NewDBService(cfg config.Config, db *sql.DB) DBService {
	return DBService{cfg: cfg, db: db}
}

func (s DBService) Config(context.Context) (map[string]any, error) {
	return map[string]any{
		"tos_url":                s.cfg.TOSURL,
		"is_email_verify":        boolToInt(s.cfg.EmailVerify),
		"is_invite_force":        boolToInt(s.cfg.InviteForce),
		"email_whitelist_suffix": whitelistValue(s.cfg.EmailWhitelist),
		"is_recaptcha":           boolToInt(s.cfg.Recaptcha),
		"recaptcha_site_key":     s.cfg.RecaptchaSiteKey,
		"app_description":        s.cfg.AppDescription,
		"app_url":                s.cfg.AppURL,
		"logo":                   s.cfg.Logo,
	}, nil
}

func (s DBService) Plans(ctx context.Context) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT * FROM v2_plan WHERE "show" = 1 ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query guest plans: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("plan columns: %w", err)
	}

	var plans []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		scanArgs := make([]any, len(columns))
		for i := range values {
			scanArgs[i] = &values[i]
		}

		if err := rows.Scan(scanArgs...); err != nil {
			return nil, fmt.Errorf("scan guest plan: %w", err)
		}

		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeValue(values[i])
		}
		plans = append(plans, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate guest plans: %w", err)
	}

	return plans, nil
}

func (s DBService) InvitePreview(ctx context.Context, code string) (map[string]any, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return buildPreviewPayload(code, "invalid", 0, 0, nil), nil
	}

	if s.db == nil {
		return nil, ErrUnavailable
	}

	inviteCode, err := s.lookupInviteCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if inviteCode == nil || inviteCode.userID <= 0 {
		return buildPreviewPayload(code, "invalid", 0, 0, nil), nil
	}

	campaign, err := s.lookupCampaign(ctx, inviteCode.userID, inviteCode.code)
	if err != nil {
		return nil, err
	}
	if campaign != nil && campaign.status == 0 && campaign.expiredAt > 0 && campaign.expiredAt > time.Now().Unix() {
		giftTransferGB, giftHours := s.campaignTryOutProfile(ctx)
		return buildPreviewPayload(inviteCode.code, "campaign", giftTransferGB, giftHours, &campaign.expiredAt), nil
	}

	giftTransferGB, giftHours := s.defaultTryOutProfile(ctx)
	return buildPreviewPayload(inviteCode.code, "normal", giftTransferGB, giftHours, nil), nil
}

type inviteCodeRow struct {
	code   string
	userID int64
}

type campaignRow struct {
	status    int64
	expiredAt int64
}

func (s DBService) lookupInviteCode(ctx context.Context, code string) (*inviteCodeRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT code, user_id FROM v2_invite_code WHERE code = $1 AND status = 0 LIMIT 1`, code)
	result := &inviteCodeRow{}
	if err := row.Scan(&result.code, &result.userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query invite code: %w", err)
	}
	result.code = strings.TrimSpace(result.code)
	return result, nil
}

func (s DBService) lookupCampaign(ctx context.Context, userID int64, inviteCode string) (*campaignRow, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT status, expired_at FROM v2_invite_campaign WHERE user_id = $1 AND invite_code = $2 AND status IN (0, 1) ORDER BY id DESC LIMIT 1`,
		userID,
		inviteCode,
	)
	result := &campaignRow{}
	if err := row.Scan(&result.status, &result.expiredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query invite campaign: %w", err)
	}
	return result, nil
}

func (s DBService) defaultTryOutProfile(ctx context.Context) (float64, float64) {
	if s.cfg.TryOutPlanID <= 0 {
		return 0, 0
	}

	transferGB := s.lookupPlanTransferGB(ctx, s.cfg.TryOutPlanID)
	return transferGB, s.cfg.TryOutHour
}

func (s DBService) campaignTryOutProfile(ctx context.Context) (float64, float64) {
	if s.cfg.InviteTryOutPlanID <= 0 {
		return 0, 0
	}

	transferGB := s.cfg.InviteTryOutTransferGB
	if transferGB <= 0 {
		transferGB = s.lookupPlanTransferGB(ctx, s.cfg.InviteTryOutPlanID)
	}

	hours := s.cfg.InviteTryOutHours
	if hours <= 0 {
		hours = 24
	}

	return transferGB, hours
}

func (s DBService) lookupPlanTransferGB(ctx context.Context, planID int64) float64 {
	if planID <= 0 || s.db == nil {
		return 0
	}

	var transferEnable sql.NullFloat64
	row := s.db.QueryRowContext(ctx, `SELECT transfer_enable FROM v2_plan WHERE id = $1 LIMIT 1`, planID)
	if err := row.Scan(&transferEnable); err != nil {
		return 0
	}
	if !transferEnable.Valid {
		return 0
	}
	return transferEnable.Float64
}

func normalizeValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func whitelistValue(items []string) any {
	if len(items) == 0 {
		return 0
	}
	return items
}

func buildPreviewPayload(code, previewType string, giftTransferGB, giftHours float64, countdownExpiredAt *int64) map[string]any {
	return map[string]any{
		"code":                 strings.TrimSpace(code),
		"type":                 previewType,
		"gift_transfer_gb":     normalizeFloat(giftTransferGB),
		"gift_hours":           normalizeFloat(giftHours),
		"countdown_expired_at": countdownExpiredAt,
	}
}

func normalizeFloat(value float64) any {
	if value == float64(int64(value)) {
		return int64(value)
	}
	return value
}
