package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	platformpostgres "forest/go-api/internal/platform/postgres"
)

var ErrSupportForbidden = errors.New("当前账号没有员工权限或已被封禁")

// SupportStaffRecord is a projection of the existing user identity, not a
// separate membership or independently configurable authorization record.
type SupportStaffRecord struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
}

func (s *DBService) ensureSupportSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	s.supportEnsureOnce.Do(func() { s.supportEnsureErr = platformpostgres.EnsureSupportSchema(ctx, s.db) })
	return s.supportEnsureErr
}

func (s *DBService) requireSupportStaff(ctx context.Context, actorID int64) (SupportStaffRecord, error) {
	if err := s.ensureSupportSchema(ctx); err != nil {
		return SupportStaffRecord{}, err
	}
	var staff SupportStaffRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,email FROM v2_user WHERE id = $1 AND banned = 0 AND is_admin = 0 AND is_staff = 1`, actorID).Scan(&staff.UserID, &staff.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return staff, ErrSupportForbidden
	}
	if err != nil {
		return staff, fmt.Errorf("read support authorization: %w", err)
	}
	return staff, nil
}

// IsSupportStaff always reads the existing user role, so revocation/ban takes
// effect without waiting for a cached session identity to expire.
func (s *DBService) IsSupportStaff(ctx context.Context, userID int64) (bool, error) {
	_, err := s.requireSupportStaff(ctx, userID)
	if errors.Is(err, ErrSupportForbidden) {
		return false, nil
	}
	return err == nil, err
}

// One bounded counter per employee/action; no search terms or viewing logs.
func (s *DBService) ConsumeSupportLimit(ctx context.Context, actorID int64, action string) error {
	if action != "lookup" && action != "change" {
		return ErrSupportEntryInvalid
	}
	if _, err := s.requireSupportStaff(ctx, actorID); err != nil {
		return err
	}
	max := int64(30)
	if action == "change" {
		max = 20
	}
	var count int64
	now := time.Now().Unix()
	err := s.db.QueryRowContext(ctx, `INSERT INTO v2_support_request_limit (actor_id, action, window_start, request_count)
VALUES ($1, $2, $3, 1)
ON CONFLICT (actor_id, action) DO UPDATE SET
request_count = CASE WHEN v2_support_request_limit.window_start = EXCLUDED.window_start THEN LEAST(v2_support_request_limit.request_count + 1, 1000) ELSE 1 END,
window_start = EXCLUDED.window_start RETURNING request_count`, actorID, action, now/60*60).Scan(&count)
	if err != nil {
		return err
	}
	if count > max {
		return ErrSupportRateLimited
	}
	return nil
}

var ErrSupportRateLimited = errors.New("操作过于频繁，请稍后重试")
