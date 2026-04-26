package user

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

func (s *DBService) ClientEntryGroups(ctx context.Context, userID int64) ([]ClientEntryGroup, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", created_at, updated_at
FROM v2_client_entry_group
WHERE "show" = 1
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query client entry groups: %w", err)
	}
	defer rows.Close()

	result := make([]ClientEntryGroup, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var (
			group           ClientEntryGroup
			hideMemberNodes int64
		)
		if err := rows.Scan(
			&group.ID,
			&group.Code,
			&group.Name,
			&group.DisplayName,
			&group.Strategy,
			&hideMemberNodes,
			&group.Show,
			&group.CreatedAt,
			&group.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan client entry group: %w", err)
		}
		group.Code = strings.TrimSpace(group.Code)
		group.Name = strings.TrimSpace(group.Name)
		group.DisplayName = strings.TrimSpace(group.DisplayName)
		group.Strategy = strings.TrimSpace(group.Strategy)
		group.HideMemberNodes = hideMemberNodes != 0
		group.Members = []ClientEntryGroupMember{}
		result = append(result, group)
		ids = append(ids, group.ID)
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

func (s *DBService) loadClientEntryGroupMembers(ctx context.Context, groupIDs []int64) (map[int64][]ClientEntryGroupMember, error) {
	result := make(map[int64][]ClientEntryGroupMember, len(groupIDs))
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
		record := ClientEntryGroupMember{
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

func buildInt64Placeholders(startAt int, values []int64) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}

	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for index, value := range values {
		parts = append(parts, "$"+strconv.Itoa(startAt+index))
		args = append(args, value)
	}
	return strings.Join(parts, ", "), args
}
