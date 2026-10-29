package admin

import (
	"context"
	"database/sql"
	"errors"
)

type aiStaffScopeKey struct{}

// WithAIStaffScope marks the internal AI execution path, not user-supplied
// HTTP input. Its restrictions must survive the handler's delegated identity.
func WithAIStaffScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, aiStaffScopeKey{}, true)
}

func aiStaffScoped(ctx context.Context) bool {
	scoped, _ := ctx.Value(aiStaffScopeKey{}).(bool)
	return scoped
}
func aiStaffUserPredicate(ctx context.Context) string {
	if aiStaffScoped(ctx) {
		return " AND is_admin=0 AND is_staff=0"
	}
	return ""
}

// Lock before deleting related rows or sessions, so a promotion that races a
// prior HTTP check cannot cause employees to modify a privileged account.
func lockAIStaffUserTargets(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if !aiStaffScoped(ctx) || len(ids) == 0 {
		return nil
	}
	in, args := buildInt64Placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, `SELECT id,is_admin,is_staff FROM v2_user WHERE id IN (`+in+`) ORDER BY id FOR UPDATE`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, isAdmin, isStaff int64
		if err = rows.Scan(&id, &isAdmin, &isStaff); err != nil {
			return err
		}
		if isAdmin != 0 || isStaff != 0 {
			return errors.New("员工不能修改管理员或员工账号")
		}
	}
	return rows.Err()
}
