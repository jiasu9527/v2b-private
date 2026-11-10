package passport

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// VerifyCurrentPassword performs a sensitive-operation check without creating
// another login session or returning subscription/login credentials.
func (s *DBService) VerifyCurrentPassword(ctx context.Context, userID int64, password string) error {
	if userID <= 0 || len(password) > 1024 || len(strings.TrimSpace(password)) < 8 {
		return NewHTTPError(http.StatusForbidden, "请验证当前本站登录密码")
	}
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return err
	}
	db, ok := s.db.(rower)
	if !ok {
		return ErrUnavailable
	}
	now := time.Now().Unix()
	var attempts int
	// Atomic upsert also covers the first concurrent attempts on an absent key.
	err := db.QueryRowContext(ctx, `INSERT INTO v2_runtime_kv(k,v,expire_at,created_at,updated_at)
 VALUES($1,'1',$2,$3,$3) ON CONFLICT(k) DO UPDATE SET
 v=CASE WHEN v2_runtime_kv.expire_at <= $3 THEN '1' ELSE ((v2_runtime_kv.v::int)+1)::text END,
 expire_at=CASE WHEN v2_runtime_kv.expire_at <= $3 THEN $2 ELSE v2_runtime_kv.expire_at END,
 updated_at=$3 RETURNING v::int`, fmt.Sprintf("NEWAPI_REAUTH:%d", userID), now+300, now).Scan(&attempts)
	if err != nil {
		return ErrUnavailable
	}
	if attempts > 10 {
		return NewHTTPError(http.StatusTooManyRequests, "验证过于频繁，请稍后再试")
	}
	user, err := s.findUserByID(ctx, userID)
	if err != nil {
		return ErrUnavailable
	}
	if user == nil || user.Banned != 0 || !verifyPassword(user.PasswordAlgo, user.PasswordSalt, strings.TrimSpace(password), user.Password) {
		return NewHTTPError(http.StatusForbidden, "本站密码验证未通过")
	}
	return nil
}
