package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const bytesPerGiB = 1073741824.0

func (s *DBService) GetStat(ctx context.Context, startAt, endAt int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if startAt <= 0 || endAt <= 0 || endAt <= startAt {
		return nil, errors.New("invalid stat range")
	}

	orderCount, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_order WHERE created_at >= $1 AND created_at < $2`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	orderTotal, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE created_at >= $1 AND created_at < $2`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	paidCount, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	paidTotal, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	commissionCount, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	commissionTotal, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	registerCount, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	inviteCount, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2 AND invite_user_id IS NOT NULL`, startAt, endAt)
	if err != nil {
		return nil, err
	}
	transferUsedTotal, err := s.sumStringQuery(ctx, `SELECT CAST(COALESCE(SUM(u) + SUM(d), 0) AS text) FROM v2_stat_server WHERE created_at >= $1 AND created_at < $2`, startAt, endAt)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"order_count":         orderCount,
		"order_total":         orderTotal,
		"paid_count":          paidCount,
		"paid_total":          paidTotal,
		"commission_count":    commissionCount,
		"commission_total":    commissionTotal,
		"register_count":      registerCount,
		"invite_count":        inviteCount,
		"transfer_used_total": transferUsedTotal,
	}, nil
}

func (s *DBService) GetStatOverride(ctx context.Context) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	now := time.Now()
	nowUnix := now.Unix()
	currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	lastMonthTime := now.AddDate(0, -1, 0)
	lastMonthStart := time.Date(lastMonthTime.Year(), lastMonthTime.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()

	onlineUser, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_user WHERE t >= $1`, nowUnix-600)
	if err != nil {
		return nil, err
	}
	monthIncome, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, currentMonthStart, nowUnix)
	if err != nil {
		return nil, err
	}
	monthRegisterTotal, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, currentMonthStart, nowUnix)
	if err != nil {
		return nil, err
	}
	lastMonthRegisterTotal, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, lastMonthStart, currentMonthStart)
	if err != nil {
		return nil, err
	}
	monthPaidUserTotal, err := s.countRegisteredPaidUsers(ctx, currentMonthStart, nowUnix)
	if err != nil {
		return nil, err
	}
	lastMonthPaidUserTotal, err := s.countRegisteredPaidUsers(ctx, lastMonthStart, currentMonthStart)
	if err != nil {
		return nil, err
	}
	dayRegisterTotal, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, dayStart, nowUnix)
	if err != nil {
		return nil, err
	}
	ticketPendingTotal, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_ticket WHERE status = 0 AND reply_status = 0`)
	if err != nil {
		return nil, err
	}
	commissionPendingTotal, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_order WHERE commission_status = 0 AND invite_user_id IS NOT NULL AND status NOT IN (0, 2) AND commission_balance > 0`)
	if err != nil {
		return nil, err
	}
	dayIncome, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, dayStart, nowUnix)
	if err != nil {
		return nil, err
	}
	lastMonthIncome, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, lastMonthStart, currentMonthStart)
	if err != nil {
		return nil, err
	}
	commissionMonthPayout, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, currentMonthStart, nowUnix)
	if err != nil {
		return nil, err
	}
	commissionLastMonthPayout, err := s.sumQuery(ctx, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, lastMonthStart, currentMonthStart)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"online_user":                  onlineUser,
		"month_income":                 monthIncome,
		"month_register_total":         monthRegisterTotal,
		"last_month_register_total":    lastMonthRegisterTotal,
		"month_paid_user_total":        monthPaidUserTotal,
		"last_month_paid_user_total":   lastMonthPaidUserTotal,
		"day_register_total":           dayRegisterTotal,
		"ticket_pending_total":         ticketPendingTotal,
		"commission_pending_total":     commissionPendingTotal,
		"day_income":                   dayIncome,
		"last_month_income":            lastMonthIncome,
		"commission_month_payout":      commissionMonthPayout,
		"commission_last_month_payout": commissionLastMonthPayout,
	}, nil
}

func (s *DBService) GetStatOrder(ctx context.Context) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT record_at, register_count, paid_total, paid_count, commission_total, commission_count
FROM v2_stat
ORDER BY record_at DESC
LIMIT 31`)
	if err != nil {
		return nil, fmt.Errorf("query stat order: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var (
			recordAt        int64
			registerCount   int64
			paidTotal       int64
			paidCount       int64
			commissionTotal int64
			commissionCount int64
		)
		if err := rows.Scan(&recordAt, &registerCount, &paidTotal, &paidCount, &commissionTotal, &commissionCount); err != nil {
			return nil, fmt.Errorf("scan stat order: %w", err)
		}

		date := time.Unix(recordAt, 0).Format("01-02")
		result = append(result,
			map[string]any{"type": "注册人数", "date": date, "value": registerCount},
			map[string]any{"type": "收款金额", "date": date, "value": float64(paidTotal) / 100},
			map[string]any{"type": "收款笔数", "date": date, "value": paidCount},
			map[string]any{"type": "佣金金额(已发放)", "date": date, "value": float64(commissionTotal) / 100},
			map[string]any{"type": "佣金笔数(已发放)", "date": date, "value": commissionCount},
		)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stat order: %w", err)
	}

	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

func (s *DBService) GetRanking(ctx context.Context, rankingType string, startAt, endAt, limit int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if startAt <= 0 || endAt <= 0 || endAt <= startAt {
		return nil, errors.New("invalid ranking range")
	}
	if limit <= 0 {
		limit = 20
	}

	switch strings.TrimSpace(rankingType) {
	case "server_traffic_rank":
		return s.getLegacyServerTrafficRank(ctx, startAt, endAt, limit)
	case "user_consumption_rank":
		return s.getLegacyUserConsumptionRank(ctx, startAt, endAt, limit)
	case "invite_rank":
		return s.getLegacyInviteRank(ctx, startAt, endAt, limit)
	default:
		return nil, errors.New("invalid ranking type")
	}
}

func (s *DBService) GetStatRecord(ctx context.Context, statType string, startAt, endAt int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if startAt <= 0 || endAt <= 0 || endAt <= startAt {
		return nil, errors.New("invalid stat record range")
	}

	var (
		query string
		field string
	)
	switch strings.TrimSpace(statType) {
	case "paid_total":
		field = "paid_total"
		query = `SELECT id, record_at, record_type, order_count, order_total, commission_count, commission_total, paid_count, paid_total / 100.0 AS field_value, register_count, invite_count, transfer_used_total, created_at, updated_at
FROM v2_stat
WHERE record_at >= $1 AND record_at < $2
ORDER BY record_at ASC`
	case "commission_total":
		field = "commission_total"
		query = `SELECT id, record_at, record_type, order_count, order_total, commission_count, commission_total / 100.0 AS field_value, paid_count, paid_total, register_count, invite_count, transfer_used_total, created_at, updated_at
FROM v2_stat
WHERE record_at >= $1 AND record_at < $2
ORDER BY record_at ASC`
	case "register_count":
		field = "register_count"
		query = `SELECT id, record_at, record_type, order_count, order_total, commission_count, commission_total, paid_count, paid_total, register_count, invite_count, transfer_used_total, created_at, updated_at
FROM v2_stat
WHERE record_at >= $1 AND record_at < $2
ORDER BY record_at ASC`
	default:
		return nil, errors.New("invalid stat record type")
	}

	rows, err := s.db.QueryContext(ctx, query, startAt, endAt)
	if err != nil {
		return nil, fmt.Errorf("query stat record: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id                int64
			recordAt          int64
			recordType        string
			orderCount        int64
			orderTotal        int64
			commissionCount   int64
			commissionTotal   any
			paidCount         int64
			paidTotal         any
			registerCount     int64
			inviteCount       int64
			transferUsedTotal string
			createdAt         int64
			updatedAt         int64
		)
		if err := rows.Scan(&id, &recordAt, &recordType, &orderCount, &orderTotal, &commissionCount, &commissionTotal, &paidCount, &paidTotal, &registerCount, &inviteCount, &transferUsedTotal, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan stat record: %w", err)
		}

		item := map[string]any{
			"id":                  id,
			"record_at":           recordAt,
			"record_type":         recordType,
			"order_count":         orderCount,
			"order_total":         orderTotal,
			"commission_count":    commissionCount,
			"paid_count":          paidCount,
			"register_count":      registerCount,
			"invite_count":        inviteCount,
			"transfer_used_total": transferUsedTotal,
			"created_at":          createdAt,
			"updated_at":          updatedAt,
		}
		switch field {
		case "paid_total":
			item["commission_total"] = int64ValueAny(commissionTotal)
			item["paid_total"] = float64ValueAny(paidTotal)
		case "commission_total":
			item["commission_total"] = float64ValueAny(commissionTotal)
			item["paid_total"] = int64ValueAny(paidTotal)
		default:
			item["commission_total"] = int64ValueAny(commissionTotal)
			item["paid_total"] = int64ValueAny(paidTotal)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stat record: %w", err)
	}

	return result, nil
}

func (s *DBService) GetServerLastRank(ctx context.Context) ([]map[string]any, error) {
	startAt, endAt := dayRange(-1)
	return s.getServerRank(ctx, startAt, endAt)
}

func (s *DBService) GetServerTodayRank(ctx context.Context) ([]map[string]any, error) {
	startAt, endAt := dayRange(0)
	return s.getServerRank(ctx, startAt, endAt)
}

func (s *DBService) GetUserLastRank(ctx context.Context) ([]map[string]any, error) {
	startAt, endAt := dayRange(-1)
	return s.getUserRank(ctx, startAt, endAt)
}

func (s *DBService) GetUserTodayRank(ctx context.Context) ([]map[string]any, error) {
	startAt, endAt := dayRange(0)
	return s.getUserRank(ctx, startAt, endAt)
}

func (s *DBService) GetStatUser(ctx context.Context, userID, current, pageSize int64) ([]map[string]any, int64, error) {
	if s.db == nil {
		return nil, 0, ErrUnavailable
	}
	if userID <= 0 {
		return nil, 0, fmt.Errorf("invalid user id")
	}
	if current <= 0 {
		current = 1
	}
	if pageSize < 10 {
		pageSize = 10
	}

	total, err := s.countQuery(ctx, `SELECT COUNT(*) FROM v2_stat_user WHERE user_id = $1`, userID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, CAST(server_rate AS text), CAST(u AS bigint), CAST(d AS bigint), record_type, record_at, created_at, updated_at
FROM v2_stat_user
WHERE user_id = $1
ORDER BY record_at DESC
LIMIT $2 OFFSET $3`, userID, pageSize, (current-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("query stat user: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id         int64
			rowUserID  int64
			serverRate string
			u          int64
			d          int64
			recordType string
			recordAt   int64
			createdAt  int64
			updatedAt  int64
		)
		if err := rows.Scan(&id, &rowUserID, &serverRate, &u, &d, &recordType, &recordAt, &createdAt, &updatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan stat user: %w", err)
		}
		result = append(result, map[string]any{
			"id":          id,
			"user_id":     rowUserID,
			"server_rate": serverRate,
			"u":           u,
			"d":           d,
			"record_type": recordType,
			"record_at":   recordAt,
			"created_at":  createdAt,
			"updated_at":  updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate stat user: %w", err)
	}
	return result, total, nil
}

func (s *DBService) getServerRank(ctx context.Context, startAt, endAt int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT server_id, server_type, CAST(u AS bigint), CAST(d AS bigint), CAST(CAST(u AS numeric) + CAST(d AS numeric) AS double precision) AS total
FROM v2_stat_server
WHERE record_at >= $1 AND record_at < $2 AND record_type = 'd'
ORDER BY total DESC
LIMIT 15`, startAt, endAt)
	if err != nil {
		return nil, fmt.Errorf("query server rank: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var (
			serverID   int64
			serverType string
			u          int64
			d          int64
			total      float64
		)
		if err := rows.Scan(&serverID, &serverType, &u, &d, &total); err != nil {
			return nil, fmt.Errorf("scan server rank: %w", err)
		}
		entry := map[string]any{
			"server_id":   serverID,
			"server_type": serverType,
			"u":           u,
			"d":           d,
			"total":       total / bytesPerGiB,
		}
		if name, err := s.fetchServerName(ctx, serverType, serverID); err != nil {
			return nil, err
		} else if name != "" {
			entry["server_name"] = name
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate server rank: %w", err)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i]["total"].(float64) > result[j]["total"].(float64)
	})
	return result, nil
}

func (s *DBService) getUserRank(ctx context.Context, startAt, endAt int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT user_id, CAST(server_rate AS double precision), CAST(u AS bigint), CAST(d AS bigint), CAST(CAST(u AS numeric) + CAST(d AS numeric) AS double precision) AS total
FROM v2_stat_user
WHERE record_at >= $1 AND record_at < $2 AND record_type = 'd'
ORDER BY total DESC
LIMIT 30`, startAt, endAt)
	if err != nil {
		return nil, fmt.Errorf("query user rank: %w", err)
	}
	defer rows.Close()

	data := make([]map[string]any, 0)
	indexByUserID := make(map[int64]int)
	for rows.Next() {
		var (
			userID     int64
			serverRate float64
			u          int64
			d          int64
			total      float64
		)
		if err := rows.Scan(&userID, &serverRate, &u, &d, &total); err != nil {
			return nil, fmt.Errorf("scan user rank: %w", err)
		}

		email, err := s.fetchUserEmail(ctx, userID)
		if err != nil {
			return nil, err
		}
		if email == "" {
			email = "null"
		}
		total = total * serverRate / bytesPerGiB

		if index, ok := indexByUserID[userID]; ok {
			data[index]["total"] = data[index]["total"].(float64) + total
			continue
		}

		data = append(data, map[string]any{
			"user_id": userID,
			"u":       u,
			"d":       d,
			"total":   total,
			"email":   email,
		})
		indexByUserID[userID] = len(data) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user rank: %w", err)
	}

	sort.Slice(data, func(i, j int) bool {
		return data[i]["total"].(float64) > data[j]["total"].(float64)
	})
	if len(data) > 15 {
		data = data[:15]
	}
	return data, nil
}

func (s *DBService) fetchServerName(ctx context.Context, serverType string, serverID int64) (string, error) {
	table := statServerTable(serverType)
	if table == "" {
		return "", nil
	}

	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM `+table+` WHERE parent_id IS NULL AND id = $1 LIMIT 1`, serverID).Scan(&name)
	if err == nil {
		return name, nil
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	return "", fmt.Errorf("query server name: %w", err)
}

func statServerTable(serverType string) string {
	switch strings.TrimSpace(serverType) {
	case "shadowsocks":
		return "v2_server_shadowsocks"
	case "v2ray", "vmess":
		return "v2_server_vmess"
	case "trojan":
		return "v2_server_trojan"
	case "vless":
		return "v2_server_vless"
	case "tuic":
		return "v2_server_tuic"
	case "hysteria":
		return "v2_server_hysteria"
	case "anytls":
		return "v2_server_anytls"
	case "v2node":
		return "v2_server_v2node"
	default:
		return ""
	}
}

func (s *DBService) fetchUserEmail(ctx context.Context, userID int64) (string, error) {
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&email)
	if err == nil {
		return email, nil
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	return "", fmt.Errorf("query user email: %w", err)
}

func (s *DBService) countRegisteredPaidUsers(ctx context.Context, startAt, endAt int64) (int64, error) {
	return s.countQuery(ctx, `SELECT COUNT(*)
FROM v2_user
WHERE created_at >= $1 AND created_at < $2
AND EXISTS (
	SELECT 1
	FROM v2_order
	WHERE v2_order.user_id = v2_user.id
	AND v2_order.user_id IS NOT NULL
	AND v2_order.user_id > 0
	AND v2_order.status NOT IN (0, 2)
)`, startAt, endAt)
}

func (s *DBService) countQuery(ctx context.Context, query string, args ...any) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count query: %w", err)
	}
	return count, nil
}

func (s *DBService) sumQuery(ctx context.Context, query string, args ...any) (int64, error) {
	var sum int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&sum); err != nil {
		return 0, fmt.Errorf("sum query: %w", err)
	}
	return sum, nil
}

func dayRange(offsetDays int) (int64, int64) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, offsetDays)
	end := start.Add(24 * time.Hour)
	return start.Unix(), end.Unix()
}

func (s *DBService) getLegacyServerTrafficRank(ctx context.Context, startAt, endAt, limit int64) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, server_type, COALESCE(SUM(u), 0) AS u, COALESCE(SUM(d), 0) AS d, COALESCE(SUM(u) + SUM(d), 0) AS total
FROM v2_stat_server
WHERE record_at >= $1 AND record_at < $2
GROUP BY server_id, server_type
ORDER BY total DESC
LIMIT $3`, startAt, endAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query legacy server traffic rank: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var serverID int64
		var serverType string
		var u, d, total int64
		if err := rows.Scan(&serverID, &serverType, &u, &d, &total); err != nil {
			return nil, fmt.Errorf("scan legacy server traffic rank: %w", err)
		}
		result = append(result, map[string]any{
			"server_id":   serverID,
			"server_type": serverType,
			"u":           u,
			"d":           d,
			"total":       total,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy server traffic rank: %w", err)
	}
	return result, nil
}

func (s *DBService) getLegacyUserConsumptionRank(ctx context.Context, startAt, endAt, limit int64) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, COALESCE(SUM(u), 0) AS u, COALESCE(SUM(d), 0) AS d, COALESCE(SUM(u) + SUM(d), 0) AS total
FROM v2_stat_user
WHERE record_at >= $1 AND record_at < $2
GROUP BY user_id
ORDER BY total DESC
LIMIT $3`, startAt, endAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query legacy user consumption rank: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var userID int64
		var u, d, total int64
		if err := rows.Scan(&userID, &u, &d, &total); err != nil {
			return nil, fmt.Errorf("scan legacy user consumption rank: %w", err)
		}
		email, err := s.fetchUserEmail(ctx, userID)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"user_id": userID,
			"u":       u,
			"d":       d,
			"total":   total,
			"email":   email,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy user consumption rank: %w", err)
	}
	return result, nil
}

func (s *DBService) getLegacyInviteRank(ctx context.Context, startAt, endAt, limit int64) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT invite_user_id, COUNT(*) AS count
FROM v2_user
WHERE created_at >= $1 AND created_at < $2 AND invite_user_id IS NOT NULL
GROUP BY invite_user_id
ORDER BY count DESC
LIMIT $3`, startAt, endAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query legacy invite rank: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var inviteUserID int64
		var count int64
		if err := rows.Scan(&inviteUserID, &count); err != nil {
			return nil, fmt.Errorf("scan legacy invite rank: %w", err)
		}
		email, err := s.fetchUserEmail(ctx, inviteUserID)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"invite_user_id": inviteUserID,
			"count":          count,
			"email":          email,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy invite rank: %w", err)
	}
	return result, nil
}

func (s *DBService) sumStringQuery(ctx context.Context, query string, args ...any) (string, error) {
	var sum string
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&sum); err != nil {
		return "", fmt.Errorf("sum string query: %w", err)
	}
	return sum, nil
}

func int64ValueAny(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	case []byte:
		var next int64
		fmt.Sscan(string(typed), &next)
		return next
	case string:
		var next int64
		fmt.Sscan(typed, &next)
		return next
	default:
		var next int64
		fmt.Sscan(fmt.Sprint(value), &next)
		return next
	}
}

func float64ValueAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int64:
		return float64(typed)
	case []byte:
		var next float64
		fmt.Sscan(string(typed), &next)
		return next
	case string:
		var next float64
		fmt.Sscan(typed, &next)
		return next
	default:
		var next float64
		fmt.Sscan(fmt.Sprint(value), &next)
		return next
	}
}
