package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	platformpostgres "forest/go-api/internal/platform/postgres"
)

var (
	ErrSupportForbidden     = errors.New("员工权限未开通或已停用")
	ErrSupportStaffInvalid  = errors.New("员工配置无效")
	ErrSupportStaffConflict = errors.New("员工配置已变化，请刷新后重试")
)

type SupportStaffRecord struct {
	UserID     int64    `json:"user_id"`
	Email      string   `json:"email"`
	Enabled    bool     `json:"enabled"`
	TargetKeys []string `json:"target_keys"`
	Version    int64    `json:"version"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at"`
}

type SupportStaffSaveRequest struct {
	Email      string   `json:"email"`
	Enabled    bool     `json:"enabled"`
	TargetKeys []string `json:"target_keys"`
	Version    int64    `json:"version"`
}

type SupportStaffListResult struct {
	Items   []SupportStaffRecord `json:"items"`
	Targets []SupportEntryTarget `json:"targets"`
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
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT staff.user_id, users.email, staff.enabled, staff.target_keys::TEXT, staff.version, staff.created_at, staff.updated_at
FROM v2_support_staff staff JOIN v2_user users ON users.id = staff.user_id
WHERE staff.user_id = $1 AND staff.enabled = 1 AND users.banned = 0 AND users.is_admin = 0 AND users.is_staff = 0`, actorID).
		Scan(&staff.UserID, &staff.Email, &staff.Enabled, &raw, &staff.Version, &staff.CreatedAt, &staff.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return staff, ErrSupportForbidden
	}
	if err != nil {
		return staff, fmt.Errorf("read support authorization: %w", err)
	}
	if err = json.Unmarshal([]byte(raw), &staff.TargetKeys); err != nil {
		return staff, fmt.Errorf("decode support authorization: %w", err)
	}
	if staff.TargetKeys == nil {
		staff.TargetKeys = []string{}
	}
	return staff, nil
}

// IsSupportStaff is intentionally a live check, not an identity-cache flag.
func (s *DBService) IsSupportStaff(ctx context.Context, userID int64) (bool, error) {
	_, err := s.requireSupportStaff(ctx, userID)
	if errors.Is(err, ErrSupportForbidden) {
		return false, nil
	}
	return err == nil, err
}

func (s *DBService) ListSupportStaff(ctx context.Context) (SupportStaffListResult, error) {
	result := SupportStaffListResult{Items: []SupportStaffRecord{}, Targets: []SupportEntryTarget{}}
	if err := s.ensureSupportSchema(ctx); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT staff.user_id, users.email, staff.enabled, staff.target_keys::TEXT, staff.version, staff.created_at, staff.updated_at
FROM v2_support_staff staff JOIN v2_user users ON users.id = staff.user_id ORDER BY staff.user_id`)
	if err != nil {
		return result, fmt.Errorf("list support staff: %w", err)
	}
	for rows.Next() {
		var item SupportStaffRecord
		var raw string
		if err = rows.Scan(&item.UserID, &item.Email, &item.Enabled, &raw, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return result, err
		}
		if err = json.Unmarshal([]byte(raw), &item.TargetKeys); err != nil {
			rows.Close()
			return result, err
		}
		if item.TargetKeys == nil {
			item.TargetKeys = []string{}
		}
		result.Items = append(result.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	result.Targets, err = s.ListSupportEntryTargets(ctx)
	return result, err
}

func (s *DBService) SaveSupportStaff(ctx context.Context, adminID int64, req SupportStaffSaveRequest) (SupportStaffRecord, error) {
	var result SupportStaffRecord
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !isValidEmail(req.Email) || req.Version < 0 || len(req.TargetKeys) > 500 {
		return result, fmt.Errorf("%w: 请填写完整注册邮箱，最多授权 500 个入口", ErrSupportStaffInvalid)
	}
	if err := s.ensureSupportSchema(ctx); err != nil {
		return result, err
	}
	targets, err := s.ListSupportEntryTargets(ctx)
	if err != nil {
		return result, err
	}
	available := make(map[string]bool, len(targets))
	for _, target := range targets {
		available[target.Key] = true
	}
	keys := make([]string, 0, len(req.TargetKeys))
	seen := make(map[string]bool)
	for _, key := range req.TargetKeys {
		key = strings.TrimSpace(key)
		// Disabling is always allowed even if old targets were removed.
		if key == "" || len(key) > 255 || (req.Enabled && !available[key]) {
			return result, fmt.Errorf("%w: 授权入口不存在、停用或不是可指定入口", ErrSupportStaffInvalid)
		}
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	sort.Strings(keys)
	raw, _ := json.Marshal(keys)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var liveAdminID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE id = $1 AND is_admin = 1 AND banned = 0 FOR SHARE`, adminID).Scan(&liveAdminID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrSupportForbidden
		}
		return result, err
	}
	var banned, isAdmin, isStaff int64
	err = tx.QueryRowContext(ctx, `SELECT id, email, banned, is_admin, is_staff FROM v2_user WHERE LOWER(email) = $1 FOR UPDATE`, req.Email).
		Scan(&result.UserID, &result.Email, &banned, &isAdmin, &isStaff)
	if errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("%w: 请先注册员工账号，再用完整邮箱添加", ErrSupportStaffInvalid)
	}
	if err != nil {
		return result, err
	}
	if req.Enabled && (banned != 0 || isAdmin != 0 || isStaff != 0) {
		return result, fmt.Errorf("%w: 只能授权未封禁的普通账号，不能使用管理员或旧员工账号", ErrSupportStaffInvalid)
	}
	var version, createdAt int64
	err = tx.QueryRowContext(ctx, `SELECT version, created_at FROM v2_support_staff WHERE user_id = $1 FOR UPDATE`, result.UserID).Scan(&version, &createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if version != req.Version {
		return result, ErrSupportStaffConflict
	}
	now := time.Now().Unix()
	result.Enabled, result.TargetKeys, result.Version, result.UpdatedAt = req.Enabled, keys, version+1, now
	if createdAt == 0 {
		createdAt = now
	}
	result.CreatedAt = createdAt
	enabled := int64(0)
	if req.Enabled {
		enabled = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_support_staff (user_id, enabled, target_keys, version, created_at, updated_at)
VALUES ($1, $2, $3::JSONB, $4, $5, $6)
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, target_keys = EXCLUDED.target_keys, version = EXCLUDED.version, updated_at = EXCLUDED.updated_at`, result.UserID, enabled, string(raw), result.Version, createdAt, now); err != nil {
		return result, err
	}
	// Granting, revoking, and changing allowed targets all require a fresh login.
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id = $1`, result.UserID); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if s.authCache != nil {
		s.authCache.InvalidateUser(result.UserID)
	}
	return result, nil
}

// One bounded counter per employee/action; no search terms or viewing logs.
func (s *DBService) ConsumeSupportLimit(ctx context.Context, actorID int64, action string) error {
	if action != "lookup" && action != "change" {
		return ErrSupportStaffInvalid
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
