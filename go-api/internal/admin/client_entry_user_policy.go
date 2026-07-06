package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *DBService) ListClientEntryUserPolicies(ctx context.Context) ([]ClientEntryUserPolicyRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.email, p.entry_group_id, g.display_name AS entry_group_name, p.server_type, p.server_id, COALESCE(s.name, '') AS server_name, p.enabled, p.remarks, p.created_at, p.updated_at
FROM v2_client_entry_user_policy p
LEFT JOIN v2_client_entry_group g ON g.id = p.entry_group_id
LEFT JOIN LATERAL (
	SELECT name FROM v2_server_vmess WHERE p.server_type = 'vmess' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_trojan WHERE p.server_type = 'trojan' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_shadowsocks WHERE p.server_type = 'shadowsocks' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_vless WHERE p.server_type = 'vless' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_tuic WHERE p.server_type = 'tuic' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_hysteria WHERE p.server_type = 'hysteria' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_anytls WHERE p.server_type = 'anytls' AND id = p.server_id
	UNION ALL SELECT name FROM v2_server_v2node WHERE p.server_type = 'v2node' AND id = p.server_id
	LIMIT 1
) s ON true
ORDER BY p.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query client entry user policies: %w", err)
	}
	defer rows.Close()

	result := make([]ClientEntryUserPolicyRecord, 0)
	for rows.Next() {
		var record ClientEntryUserPolicyRecord
		if err := rows.Scan(&record.ID, &record.Email, &record.EntryGroupID, &record.EntryGroupName, &record.ServerType, &record.ServerID, &record.ServerName, &record.Enabled, &record.Remarks, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan client entry user policy: %w", err)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry user policies: %w", err)
	}
	return result, nil
}

func (s *DBService) SaveClientEntryUserPolicy(ctx context.Context, req ClientEntryUserPolicySaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.ServerType = strings.TrimSpace(req.ServerType)
	req.Remarks = strings.TrimSpace(req.Remarks)
	if req.Email == "" {
		return false, errors.New("用户邮箱不能为空")
	}
	if req.EntryGroupID <= 0 {
		return false, errors.New("入口组不能为空")
	}
	if req.ServerType == "" || req.ServerID <= 0 {
		return false, errors.New("生效节点不能为空")
	}
	enabled := int64(1)
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	now := time.Now().Unix()
	if req.ID == nil {
		_, err := s.db.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy (email, entry_group_id, server_type, server_id, enabled, remarks, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (email, server_type, server_id) DO UPDATE SET entry_group_id = EXCLUDED.entry_group_id, enabled = EXCLUDED.enabled, remarks = EXCLUDED.remarks, updated_at = EXCLUDED.updated_at`, req.Email, req.EntryGroupID, req.ServerType, req.ServerID, enabled, req.Remarks, now, now)
		if err != nil {
			return false, errors.New("保存失败")
		}
		return true, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET email = $2, entry_group_id = $3, server_type = $4, server_id = $5, enabled = $6, remarks = $7, updated_at = $8
WHERE id = $1`, *req.ID, req.Email, req.EntryGroupID, req.ServerType, req.ServerID, enabled, req.Remarks, now)
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeleteClientEntryUserPolicy(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, errors.New("ID不能为空")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("删除失败")
	}
	return true, nil
}
