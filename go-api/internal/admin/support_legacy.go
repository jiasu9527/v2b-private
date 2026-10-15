package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type legacyStaffScopeKey struct{}

// WithLegacyStaffScope is set by authenticated legacy staff handlers, never by
// a request parameter. Administrator calls retain their existing behavior.
func WithLegacyStaffScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, legacyStaffScopeKey{}, true)
}

func isLegacyStaffScope(ctx context.Context) bool {
	value, _ := ctx.Value(legacyStaffScopeKey{}).(bool)
	return value
}

const legacyStaffUserPredicate = `u.is_admin = 0 AND u.is_staff = 0
AND NOT EXISTS (SELECT 1 FROM v2_support_staff support WHERE support.user_id = u.id)`

// This projection must remain separate from administrator user details. In
// particular, neither authentication material nor nested invite-user records
// may cross the legacy staff boundary.
func legacyStaffUserDetail(user map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{
		"id", "email", "banned", "is_admin", "is_staff", "group_id", "plan_id",
		"transfer_enable", "u", "d", "device_limit", "speed_limit", "expired_at",
		"balance", "commission_balance", "commission_type", "commission_rate", "discount",
		"invite_user_id", "remarks", "created_at", "updated_at", "last_login_at",
	} {
		if value, ok := user[key]; ok {
			result[key] = value
		}
	}
	return result
}

func (s *DBService) getLegacyStaffUserInfo(ctx context.Context, id int64) (map[string]any, error) {
	if err := s.ensureSupportSchema(ctx); err != nil {
		return nil, err
	}
	user, err := s.queryJSONMap(ctx, adminUserJSONMapQuery(`SELECT u.* FROM v2_user u WHERE u.id = $1 AND `+legacyStaffUserPredicate+` LIMIT 1`), id)
	if err != nil {
		return nil, fmt.Errorf("query legacy staff user detail: %w", err)
	}
	if user == nil {
		return nil, errors.New("用户不存在")
	}
	return legacyStaffUserDetail(user), nil
}

// Lock before checking membership: an administrator grants support membership
// under the same user-row lock. A second statement sees a grant which committed
// while we were waiting for the lock, including previously disabled memberships.
func lockLegacyStaffUser(ctx context.Context, tx *sql.Tx, id int64) error {
	var admin, staff int64
	if err := tx.QueryRowContext(ctx, `SELECT is_admin, is_staff FROM v2_user WHERE id = $1 FOR UPDATE`, id).Scan(&admin, &staff); err != nil {
		return err
	}
	var support bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM v2_support_staff WHERE user_id = $1)`, id).Scan(&support); err != nil {
		return err
	}
	if admin != 0 || staff != 0 || support {
		return ErrSupportForbidden
	}
	return nil
}

func lockLegacyStaffUsers(ctx context.Context, tx *sql.Tx, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	in, args := buildInt64Placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM v2_user WHERE id IN (`+in+`) ORDER BY id FOR UPDATE`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// A fresh statement after all locks also observes any role grant we waited on.
	rows, err = tx.QueryContext(ctx, `SELECT u.id FROM v2_user u WHERE u.id IN (`+in+`) AND `+legacyStaffUserPredicate+` ORDER BY u.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := make([]int64, 0, len(ids))
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed = append(allowed, id)
	}
	return allowed, rows.Err()
}
