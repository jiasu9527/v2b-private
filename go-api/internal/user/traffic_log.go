package user

import (
	"context"
	"fmt"
	"time"
)

func (s *DBService) TrafficLogs(ctx context.Context, userID int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	rows, err := s.queryRowsAsMaps(ctx, `SELECT u, d, record_at, user_id, server_rate
FROM v2_stat_user
WHERE user_id = $1 AND record_at >= $2
ORDER BY record_at DESC`, userID, monthStart)
	if err != nil {
		return nil, fmt.Errorf("query traffic logs: %w", err)
	}
	return rows, nil
}
