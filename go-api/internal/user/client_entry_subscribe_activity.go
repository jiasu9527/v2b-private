package user

import "context"

// recordClientEntrySubscribeActivity records every successful node-list fetch.
// Keeping the latest timestamp exact is important for short snapshot windows:
// throttling writes could otherwise omit a user who fetched near the boundary.
// Activity tracking is best-effort and must never make a subscription fail.
func (s *DBService) recordClientEntrySubscribeActivity(ctx context.Context, userID, subscribedAt int64) {
	if s == nil || s.db == nil || userID <= 0 || subscribedAt <= 0 {
		return
	}

	_, _ = s.db.ExecContext(ctx, `INSERT INTO v2_user_subscribe_activity
(user_id, last_subscribe_at, created_at, updated_at)
VALUES ($1, $2, $2, $2)
ON CONFLICT (user_id) DO UPDATE
SET last_subscribe_at = EXCLUDED.last_subscribe_at,
    updated_at = EXCLUDED.updated_at
WHERE v2_user_subscribe_activity.last_subscribe_at < EXCLUDED.last_subscribe_at`, userID, subscribedAt)
}
