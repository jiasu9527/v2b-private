package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var serverGroupReferenceTables = []string{
	"v2_server_vmess",
	"v2_server_vless",
	"v2_server_trojan",
	"v2_server_shadowsocks",
	"v2_server_tuic",
	"v2_server_hysteria",
	"v2_server_anytls",
	"v2_server_v2node",
}

var allowedServerRouteActions = map[string]struct{}{
	"block":       {},
	"block_ip":    {},
	"block_port":  {},
	"protocol":    {},
	"dns":         {},
	"route":       {},
	"route_ip":    {},
	"default_out": {},
}

type serverGroupRow struct {
	ID        int64
	Name      string
	CreatedAt int64
	UpdatedAt int64
}

type serverRouteRow struct {
	ID          int64
	Remarks     string
	Match       string
	Action      string
	ActionValue sql.NullString
	CreatedAt   int64
	UpdatedAt   int64
}

func (s *DBService) ListServerGroups(ctx context.Context, groupID *int64) ([]ServerGroupRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	if groupID != nil {
		row, err := scanServerGroupRow(s.db.QueryRowContext(ctx, `SELECT id, name, created_at, updated_at
FROM v2_server_group
WHERE id = $1
LIMIT 1`, *groupID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []ServerGroupRecord{}, nil
			}
			return nil, fmt.Errorf("query server group: %w", err)
		}
		return []ServerGroupRecord{serverGroupRecord(row, 0, 0)}, nil
	}

	userCounts, err := s.serverGroupUserCounts(ctx)
	if err != nil {
		return nil, err
	}
	serverCounts, err := s.serverGroupServerCounts(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, updated_at
FROM v2_server_group
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query server groups: %w", err)
	}
	defer rows.Close()

	result := make([]ServerGroupRecord, 0)
	for rows.Next() {
		row, err := scanServerGroupRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, serverGroupRecord(row, userCounts[row.ID], serverCounts[row.ID]))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate server groups: %w", err)
	}
	return result, nil
}

func (s *DBService) SaveServerGroup(ctx context.Context, req ServerGroupSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return false, errors.New("组名不能为空")
	}

	now := time.Now().Unix()
	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_server_group (name, created_at, updated_at)
VALUES ($1, $2, $3)`, req.Name, now, now); err != nil {
			return false, errors.New("创建失败")
		}
		return true, nil
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_server_group
SET name = $2, updated_at = $3
WHERE id = $1`, *req.ID, req.Name, now)
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("保存失败")
	}
	if affected == 0 {
		return false, errors.New("组不存在")
	}
	return true, nil
}

func (s *DBService) DeleteServerGroup(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	ok, err := s.serverGroupExists(ctx, id)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("组不存在")
	}

	inUse, err := s.serverGroupInUse(ctx, id)
	if err != nil {
		return false, err
	}
	if inUse {
		return false, errors.New("该组已被节点所使用，无法删除")
	}

	planUsed, err := s.serverGroupReferencedByPlan(ctx, id)
	if err != nil {
		return false, err
	}
	if planUsed {
		return false, errors.New("该组已被订阅所使用，无法删除")
	}

	userUsed, err := s.serverGroupReferencedByUser(ctx, id)
	if err != nil {
		return false, err
	}
	if userUsed {
		return false, errors.New("该组已被用户所使用，无法删除")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_server_group WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	if affected == 0 {
		return false, errors.New("组不存在")
	}
	return true, nil
}

func (s *DBService) ListServerRoutes(ctx context.Context) ([]ServerRouteRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, remarks, "match", action, action_value, created_at, updated_at
FROM v2_server_route
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query server routes: %w", err)
	}
	defer rows.Close()

	result := make([]ServerRouteRecord, 0)
	for rows.Next() {
		row, err := scanServerRouteRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, serverRouteRecord(row))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate server routes: %w", err)
	}
	return result, nil
}

func (s *DBService) SaveServerRoute(ctx context.Context, req ServerRouteSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Remarks = strings.TrimSpace(req.Remarks)
	req.Action = strings.TrimSpace(req.Action)
	req.ActionValue = trimmedStringPtr(req.ActionValue)
	req.Match = normalizeStringSlice(req.Match)

	if req.Remarks == "" {
		return false, errors.New("备注不能为空")
	}
	if req.Action == "" {
		return false, errors.New("动作类型不能为空")
	}
	if _, ok := allowedServerRouteActions[req.Action]; !ok {
		return false, errors.New("动作类型参数有误")
	}
	if req.Action == "default_out" {
		req.Match = []string{}
	} else if len(req.Match) == 0 {
		return false, errors.New("匹配值不能为空")
	}

	rawMatch, err := json.Marshal(req.Match)
	if err != nil {
		return false, errors.New("保存失败")
	}

	now := time.Now().Unix()
	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_server_route (
remarks, "match", action, action_value, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6
)`, req.Remarks, string(rawMatch), req.Action, nullableString(req.ActionValue), now, now); err != nil {
			return false, errors.New("创建失败")
		}
		return true, nil
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_server_route
SET remarks = $2,
	"match" = $3,
	action = $4,
	action_value = $5,
	updated_at = $6
WHERE id = $1`,
		*req.ID,
		req.Remarks,
		string(rawMatch),
		req.Action,
		nullableString(req.ActionValue),
		now,
	)
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("保存失败")
	}
	if affected == 0 {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeleteServerRoute(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	ok, err := s.serverRouteExists(ctx, id)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("路由不存在")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_server_route WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	if affected == 0 {
		return false, errors.New("删除失败")
	}
	return true, nil
}

func (s *DBService) serverGroupUserCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, COUNT(*) AS count
FROM v2_user
WHERE group_id IS NOT NULL
GROUP BY group_id`)
	if err != nil {
		return nil, fmt.Errorf("query server group user counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[int64]int64)
	for rows.Next() {
		var groupID int64
		var count int64
		if err := rows.Scan(&groupID, &count); err != nil {
			return nil, fmt.Errorf("scan server group user count: %w", err)
		}
		counts[groupID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate server group user counts: %w", err)
	}
	return counts, nil
}

func (s *DBService) serverGroupServerCounts(ctx context.Context) (map[int64]int64, error) {
	counts := make(map[int64]int64)
	for _, table := range serverGroupReferenceTables {
		rows, err := s.db.QueryContext(ctx, `SELECT group_id FROM `+table)
		if err != nil {
			return nil, fmt.Errorf("query %s group ids: %w", table, err)
		}

		for rows.Next() {
			var raw sql.NullString
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan %s group ids: %w", table, err)
			}
			for _, id := range parseServerIDList(raw.String) {
				counts[id]++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate %s group ids: %w", table, err)
		}
		rows.Close()
	}
	return counts, nil
}

func (s *DBService) serverGroupExists(ctx context.Context, id int64) (bool, error) {
	var exists int64
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_server_group WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query server group existence: %w", err)
}

func (s *DBService) serverRouteExists(ctx context.Context, id int64) (bool, error) {
	var exists int64
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_server_route WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query server route existence: %w", err)
}

func (s *DBService) serverGroupInUse(ctx context.Context, id int64) (bool, error) {
	for _, table := range serverGroupReferenceTables {
		rows, err := s.db.QueryContext(ctx, `SELECT group_id FROM `+table)
		if err != nil {
			return false, fmt.Errorf("query %s group references: %w", table, err)
		}

		for rows.Next() {
			var raw sql.NullString
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return false, fmt.Errorf("scan %s group references: %w", table, err)
			}
			for _, groupID := range parseServerIDList(raw.String) {
				if groupID == id {
					rows.Close()
					return true, nil
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, fmt.Errorf("iterate %s group references: %w", table, err)
		}
		rows.Close()
	}
	return false, nil
}

func (s *DBService) serverGroupReferencedByPlan(ctx context.Context, id int64) (bool, error) {
	var exists int64
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_plan WHERE group_id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query server group plan reference: %w", err)
}

func (s *DBService) serverGroupReferencedByUser(ctx context.Context, id int64) (bool, error) {
	var exists int64
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_user WHERE group_id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query server group user reference: %w", err)
}

func scanServerGroupRow(scanner interface{ Scan(...any) error }) (serverGroupRow, error) {
	var row serverGroupRow
	if err := scanner.Scan(&row.ID, &row.Name, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return serverGroupRow{}, err
	}
	return row, nil
}

func scanServerRouteRow(scanner interface{ Scan(...any) error }) (serverRouteRow, error) {
	var row serverRouteRow
	if err := scanner.Scan(
		&row.ID,
		&row.Remarks,
		&row.Match,
		&row.Action,
		&row.ActionValue,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return serverRouteRow{}, err
	}
	return row, nil
}

func serverGroupRecord(row serverGroupRow, userCount, serverCount int64) ServerGroupRecord {
	return ServerGroupRecord{
		ID:          row.ID,
		Name:        row.Name,
		UserCount:   userCount,
		ServerCount: serverCount,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func serverRouteRecord(row serverRouteRow) ServerRouteRecord {
	return ServerRouteRecord{
		ID:          row.ID,
		Remarks:     row.Remarks,
		Match:       parseServerStringList(row.Match),
		Action:      row.Action,
		ActionValue: nullStringPtr(row.ActionValue),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func parseServerIDList(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return nil
	}

	var values []any
	if err := json.Unmarshal([]byte(raw), &values); err == nil {
		result := make([]int64, 0, len(values))
		seen := make(map[int64]struct{})
		for _, value := range values {
			next, ok := anyToInt64(value)
			if !ok {
				continue
			}
			if _, exists := seen[next]; exists {
				continue
			}
			seen[next] = struct{}{}
			result = append(result, next)
		}
		return result
	}

	raw = strings.Trim(raw, "[]")
	parts := strings.Split(raw, ",")
	result := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{})
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part == "" {
			continue
		}
		next, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		if _, exists := seen[next]; exists {
			continue
		}
		seen[next] = struct{}{}
		result = append(result, next)
	}
	return result
}

func parseServerStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return []string{}
	}

	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err == nil {
		return normalizeStringSlice(values)
	}

	var generic []any
	if err := json.Unmarshal([]byte(raw), &generic); err == nil {
		return normalizeStringSlice(stringsFromAny(generic))
	}

	raw = strings.Trim(raw, "[]")
	parts := strings.Split(raw, ",")
	values = make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func stringsFromAny(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		next := strings.TrimSpace(fmt.Sprint(value))
		if next != "" {
			result = append(result, next)
		}
	}
	return result
}

func normalizeStringSlice(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		next := strings.TrimSpace(value)
		if next != "" {
			result = append(result, next)
		}
	}
	return result
}

func anyToInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), float64(int64(typed)) == typed
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case json.Number:
		next, err := typed.Int64()
		return next, err == nil
	case string:
		next, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return next, err == nil
	default:
		next, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return next, err == nil
	}
}
