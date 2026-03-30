package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *DBService) ListSystemLogs(ctx context.Context, current, pageSize int64, level string) ([]map[string]any, int64, error) {
	if s.db == nil {
		return nil, 0, ErrUnavailable
	}

	if current <= 0 {
		current = 1
	}
	if pageSize < 10 {
		pageSize = 10
	}
	level = strings.TrimSpace(level)
	offset := (current - 1) * pageSize

	var (
		total int64
		rows  *sql.Rows
		err   error
	)
	if level == "" {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_log`).Scan(&total)
		if err == nil {
			rows, err = s.db.QueryContext(ctx, `SELECT id, title, level, host, uri, method, data, ip, context, created_at, updated_at
FROM v2_log
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2`, pageSize, offset)
		}
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_log WHERE level = $1`, level).Scan(&total)
		if err == nil {
			rows, err = s.db.QueryContext(ctx, `SELECT id, title, level, host, uri, method, data, ip, context, created_at, updated_at
FROM v2_log
WHERE level = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3`, level, pageSize, offset)
		}
	}
	if err != nil {
		return nil, 0, fmt.Errorf("query system logs: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id        int64
			title     string
			rowLevel  sql.NullString
			host      sql.NullString
			uri       string
			method    string
			data      sql.NullString
			ip        sql.NullString
			contextV  sql.NullString
			createdAt int64
			updatedAt int64
		)
		if err := rows.Scan(&id, &title, &rowLevel, &host, &uri, &method, &data, &ip, &contextV, &createdAt, &updatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan system log: %w", err)
		}

		result = append(result, map[string]any{
			"id":         id,
			"title":      title,
			"level":      nullStringValue(rowLevel),
			"host":       nullStringValue(host),
			"uri":        uri,
			"method":     method,
			"data":       nullStringValue(data),
			"ip":         nullStringValue(ip),
			"context":    nullStringValue(contextV),
			"created_at": createdAt,
			"updated_at": updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate system logs: %w", err)
	}
	return result, total, nil
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
