package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"forest/go-api/internal/platform/addrutil"
)

const fixedClientEntryGroupStrategy = "ordered-fallback"
const defaultClientEntryRemoteSSHPort int64 = 0
const defaultClientEntryRemoteRefreshSec int64 = 300

type clientEntryGroupRow struct {
	ID                 int64
	Code               string
	Name               string
	DisplayName        string
	Strategy           string
	HideMemberNodes    int64
	Show               int64
	RemoteEnabled      int64
	RemoteHost         string
	RemoteSSHPort      int64
	RemoteSSHUser      string
	RemoteSSHPassword  string
	RemoteGroupRef     string
	RemoteExcludeNames string
	RemoteRefreshSec   int64
	CreatedAt          int64
	UpdatedAt          int64
}

func (s *DBService) ListClientEntryGroups(ctx context.Context, id *int64) ([]ClientEntryGroupRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	query := `SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", remote_enabled, remote_host, remote_ssh_port, remote_ssh_user, remote_ssh_password, remote_group_ref, remote_exclude_names, remote_refresh_sec, created_at, updated_at
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
			&row.RemoteEnabled,
			&row.RemoteHost,
			&row.RemoteSSHPort,
			&row.RemoteSSHUser,
			&row.RemoteSSHPassword,
			&row.RemoteGroupRef,
			&row.RemoteExcludeNames,
			&row.RemoteRefreshSec,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan client entry group: %w", err)
		}
		excludes := decodeClientEntryRemoteExcludeNames(row.RemoteExcludeNames)
		result = append(result, ClientEntryGroupRecord{
			ID:                 row.ID,
			Code:               strings.TrimSpace(row.Code),
			Name:               strings.TrimSpace(row.Name),
			DisplayName:        strings.TrimSpace(row.DisplayName),
			Strategy:           normalizeClientEntryGroupStrategy(row.Strategy),
			HideMemberNodes:    row.HideMemberNodes != 0,
			Show:               row.Show,
			RemoteEnabled:      row.RemoteEnabled != 0,
			RemoteHost:         strings.TrimSpace(row.RemoteHost),
			RemoteSSHPort:      normalizeClientEntryRemoteSSHPort(row.RemoteSSHPort),
			RemoteSSHUser:      strings.TrimSpace(row.RemoteSSHUser),
			RemoteSSHPassword:  strings.TrimSpace(row.RemoteSSHPassword),
			RemoteGroupRef:     strings.TrimSpace(row.RemoteGroupRef),
			RemoteExcludeNames: excludes,
			RemoteRefreshSec:   normalizeClientEntryRemoteRefreshSec(row.RemoteRefreshSec),
			CreatedAt:          row.CreatedAt,
			UpdatedAt:          row.UpdatedAt,
			Members:            []ClientEntryGroupMemberRecord{},
			IPs:                []ClientEntryGroupIPRecord{},
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
	ips, err := s.loadClientEntryGroupIPs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Members = members[result[index].ID]
		result[index].IPs = ips[result[index].ID]
	}
	return result, nil
}

func (s *DBService) SaveClientEntryGroup(ctx context.Context, req ClientEntryGroupSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}

	req.Code = strings.TrimSpace(req.Code)
	req.Name = strings.TrimSpace(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Strategy = normalizeClientEntryGroupStrategy(req.Strategy)
	if err := normalizeClientEntryRemoteSaveRequest(&req); err != nil {
		return false, err
	}
	if req.Code == "" {
		return false, errors.New("入口组标识不能为空")
	}
	if req.Name == "" {
		return false, errors.New("入口组名称不能为空")
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}
	if len(req.IPs) == 0 && !req.RemoteEnabled {
		return false, errors.New("入口IP不能为空")
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
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_group (code, name, display_name, strategy, hide_member_nodes, "show", remote_enabled, remote_host, remote_ssh_port, remote_ssh_user, remote_ssh_password, remote_group_ref, remote_exclude_names, remote_refresh_sec, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id`,
			req.Code,
			req.Name,
			req.DisplayName,
			req.Strategy,
			boolToInt64(req.HideMemberNodes),
			show,
			boolToInt64(req.RemoteEnabled),
			req.RemoteHost,
			req.RemoteSSHPort,
			req.RemoteSSHUser,
			req.RemoteSSHPassword,
			req.RemoteGroupRef,
			encodeClientEntryRemoteExcludeNames(req.RemoteExcludeNames),
			req.RemoteRefreshSec,
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
	remote_enabled = $8,
	remote_host = $9,
	remote_ssh_port = $10,
	remote_ssh_user = $11,
	remote_ssh_password = $12,
	remote_group_ref = $13,
	remote_exclude_names = $14,
	remote_refresh_sec = $15,
	updated_at = $16
WHERE id = $1`,
			groupID,
			req.Code,
			req.Name,
			req.DisplayName,
			req.Strategy,
			boolToInt64(req.HideMemberNodes),
			show,
			boolToInt64(req.RemoteEnabled),
			req.RemoteHost,
			req.RemoteSSHPort,
			req.RemoteSSHUser,
			req.RemoteSSHPassword,
			req.RemoteGroupRef,
			encodeClientEntryRemoteExcludeNames(req.RemoteExcludeNames),
			req.RemoteRefreshSec,
			now,
		)
		if err != nil {
			return false, errors.New("保存失败")
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return false, errors.New("保存失败")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_ip WHERE entry_group_id = $1`, groupID); err != nil {
			return false, errors.New("保存失败")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_member WHERE entry_group_id = $1`, groupID); err != nil {
			return false, errors.New("保存失败")
		}
	}

	seenMembers := make(map[string]struct{}, len(req.Members))
	for _, item := range req.Members {
		serverType := strings.TrimSpace(item.ServerType)
		if serverType == "" || item.ServerID <= 0 {
			continue
		}
		key := serverType + ":" + fmt.Sprint(item.ServerID)
		if _, exists := seenMembers[key]; exists {
			continue
		}
		seenMembers[key] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_group_member (entry_group_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
			groupID,
			serverType,
			item.ServerID,
			clientEntryNullableInt64(item.Sort),
			now,
			now,
		); err != nil {
			return false, errors.New("保存失败")
		}
	}

	seenIPs := make(map[string]struct{}, len(req.IPs))
	for _, item := range req.IPs {
		ip := strings.TrimSpace(item.IP)
		if !addrutil.IsIPOrHostname(ip) {
			return false, errors.New("入口地址格式不正确")
		}
		if _, exists := seenIPs[ip]; exists {
			continue
		}
		seenIPs[ip] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_group_ip (entry_group_id, ip, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)`,
			groupID,
			ip,
			clientEntryNullableInt64(item.Sort),
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

func normalizeClientEntryGroupStrategy(_ string) string {
	return fixedClientEntryGroupStrategy
}

func normalizeClientEntryRemoteSaveRequest(req *ClientEntryGroupSaveRequest) error {
	req.RemoteHost = strings.TrimSpace(req.RemoteHost)
	req.RemoteSSHUser = strings.TrimSpace(req.RemoteSSHUser)
	req.RemoteSSHPassword = strings.TrimSpace(req.RemoteSSHPassword)
	req.RemoteGroupRef = strings.TrimSpace(req.RemoteGroupRef)
	req.RemoteSSHPort = normalizeClientEntryRemoteSSHPort(req.RemoteSSHPort)
	req.RemoteRefreshSec = normalizeClientEntryRemoteRefreshSec(req.RemoteRefreshSec)
	req.RemoteExcludeNames = normalizeClientEntryRemoteExcludeList(req.RemoteExcludeNames)
	if !req.RemoteEnabled {
		req.RemoteHost = ""
		req.RemoteSSHPort = 0
		req.RemoteSSHUser = ""
		req.RemoteSSHPassword = ""
		req.RemoteGroupRef = ""
		req.RemoteExcludeNames = nil
		return nil
	}
	switch {
	case req.RemoteHost == "":
		return errors.New("远程站点地址不能为空")
	case req.RemoteSSHUser == "":
		return errors.New("远程站点账号不能为空")
	case req.RemoteSSHPassword == "":
		return errors.New("远程站点密码不能为空")
	case req.RemoteGroupRef == "":
		return errors.New("远程设备组不能为空")
	default:
		return nil
	}
}

func normalizeClientEntryRemoteSSHPort(value int64) int64 {
	if value <= 0 {
		return defaultClientEntryRemoteSSHPort
	}
	return value
}

func normalizeClientEntryRemoteRefreshSec(value int64) int64 {
	if value <= 0 {
		return defaultClientEntryRemoteRefreshSec
	}
	return value
}

func normalizeClientEntryRemoteExcludeList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func decodeClientEntryRemoteExcludeNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var result []string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return []string{}
	}
	return normalizeClientEntryRemoteExcludeList(result)
}

func encodeClientEntryRemoteExcludeNames(values []string) string {
	raw, err := json.Marshal(normalizeClientEntryRemoteExcludeList(values))
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func (s *DBService) DeleteClientEntryGroup(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_ip WHERE entry_group_id = $1`, id); err != nil {
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

func (s *DBService) loadClientEntryGroupIPs(ctx context.Context, groupIDs []int64) (map[int64][]ClientEntryGroupIPRecord, error) {
	result := make(map[int64][]ClientEntryGroupIPRecord, len(groupIDs))
	if len(groupIDs) == 0 {
		return result, nil
	}

	inClause, args := buildInt64Placeholders(1, groupIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT entry_group_id, ip, sort
FROM v2_client_entry_group_ip
WHERE entry_group_id IN (`+inClause+`)
ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry group ips: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			groupID int64
			ip      string
			sort    sql.NullInt64
		)
		if err := rows.Scan(&groupID, &ip, &sort); err != nil {
			return nil, fmt.Errorf("scan client entry group ip: %w", err)
		}
		record := ClientEntryGroupIPRecord{
			IP: strings.TrimSpace(ip),
		}
		if sort.Valid {
			record.Sort = &sort.Int64
		}
		result[groupID] = append(result[groupID], record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry group ips: %w", err)
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
