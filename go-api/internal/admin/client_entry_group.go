package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var allowedClientEntryStrategies = map[string]struct{}{
	"latency-first":      {},
	"sticky-low-latency": {},
	"ordered-fallback":   {},
}

type clientEntryGroupRow struct {
	ID              int64
	Code            string
	Name            string
	DisplayName     string
	Strategy        string
	HideMemberNodes int64
	Show            int64
	CreatedAt       int64
	UpdatedAt       int64
}

func (s *DBService) ListClientEntryGroups(ctx context.Context, id *int64) ([]ClientEntryGroupRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	query := `SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", created_at, updated_at
FROM v2_client_entry_group`
	args := make([]any, 0, 1)
	if id != nil {
		query += ` WHERE id = $1`
		args = append(args, *id)
	}
	query += ` ORDER BY id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry groups: %w", err)
	}
	defer rows.Close()

	result := make([]ClientEntryGroupRecord, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var row clientEntryGroupRow
		if err := rows.Scan(
			&row.ID,
			&row.Code,
			&row.Name,
			&row.DisplayName,
			&row.Strategy,
			&row.HideMemberNodes,
			&row.Show,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan client entry group: %w", err)
		}
		result = append(result, ClientEntryGroupRecord{
			ID:              row.ID,
			Code:            strings.TrimSpace(row.Code),
			Name:            strings.TrimSpace(row.Name),
			DisplayName:     strings.TrimSpace(row.DisplayName),
			Strategy:        strings.TrimSpace(row.Strategy),
			HideMemberNodes: row.HideMemberNodes != 0,
			Show:            row.Show,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
			Members:         []ClientEntryGroupMemberRecord{},
		})
		ids = append(ids, row.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry groups: %w", err)
	}
	if len(ids) == 0 {
		return result, nil
	}

	members, err := s.loadClientEntryGroupMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Members = members[result[index].ID]
	}
	return result, nil
}

func (s *DBService) SaveClientEntryGroup(ctx context.Context, req ClientEntryGroupSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Code = strings.TrimSpace(req.Code)
	req.Name = strings.TrimSpace(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Strategy = strings.TrimSpace(req.Strategy)
	if req.Code == "" {
		return false, errors.New("入口组标识不能为空")
	}
	if req.Name == "" {
		return false, errors.New("入口组名称不能为空")
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}
	if _, ok := allowedClientEntryStrategies[req.Strategy]; !ok {
		return false, errors.New("入口组策略不正确")
	}
	if len(req.Members) == 0 {
		return false, errors.New("入口组成员不能为空")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	show := int64(1)
	if req.Show != nil {
		show = *req.Show
	}

	groupID := int64(0)
	if req.ID == nil {
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_group (code, name, display_name, strategy, hide_member_nodes, "show", created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id`,
			req.Code,
			req.Name,
			req.DisplayName,
			req.Strategy,
			boolToInt64(req.HideMemberNodes),
			show,
			now,
			now,
		).Scan(&groupID); err != nil {
			return false, errors.New("创建失败")
		}
	} else {
		groupID = *req.ID
		result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_group
SET code = $2,
	name = $3,
	display_name = $4,
	strategy = $5,
	hide_member_nodes = $6,
	"show" = $7,
	updated_at = $8
WHERE id = $1`,
			groupID,
			req.Code,
			req.Name,
			req.DisplayName,
			req.Strategy,
			boolToInt64(req.HideMemberNodes),
			show,
			now,
		)
		if err != nil {
			return false, errors.New("保存失败")
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return false, errors.New("保存失败")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_member WHERE entry_group_id = $1`, groupID); err != nil {
			return false, errors.New("保存失败")
		}
	}

	for _, member := range req.Members {
		serverType := strings.TrimSpace(member.ServerType)
		if serverType == "" || member.ServerID <= 0 {
			return false, errors.New("入口组成员格式不正确")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_group_member (entry_group_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
			groupID,
			serverType,
			member.ServerID,
			clientEntryNullableInt64(member.Sort),
			now,
			now,
		); err != nil {
			return false, errors.New("保存失败")
		}
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeleteClientEntryGroup(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("入口组不存在")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("删除失败")
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_member WHERE entry_group_id = $1`, id); err != nil {
		return false, errors.New("删除失败")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("删除失败")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("删除失败")
	}
	return true, nil
}

func (s *DBService) loadClientEntryGroupMembers(ctx context.Context, groupIDs []int64) (map[int64][]ClientEntryGroupMemberRecord, error) {
	result := make(map[int64][]ClientEntryGroupMemberRecord, len(groupIDs))
	if len(groupIDs) == 0 {
		return result, nil
	}

	inClause, args := buildInt64Placeholders(1, groupIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT entry_group_id, server_type, server_id, sort
FROM v2_client_entry_group_member
WHERE entry_group_id IN (`+inClause+`)
ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry group members: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			groupID    int64
			serverType string
			serverID   int64
			sort       sql.NullInt64
		)
		if err := rows.Scan(&groupID, &serverType, &serverID, &sort); err != nil {
			return nil, fmt.Errorf("scan client entry group member: %w", err)
		}
		record := ClientEntryGroupMemberRecord{
			ServerType: strings.TrimSpace(serverType),
			ServerID:   serverID,
		}
		if sort.Valid {
			record.Sort = &sort.Int64
		}
		result[groupID] = append(result[groupID], record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry group members: %w", err)
	}
	return result, nil
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func clientEntryNullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
