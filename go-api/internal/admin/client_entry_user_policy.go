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

	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.entry_group_id, g.display_name AS entry_group_name, p.server_type, p.server_id, COALESCE(s.name, '') AS server_name, p.enabled, p.remarks, p.created_at, p.updated_at
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
	ids := make([]int64, 0)
	for rows.Next() {
		var record ClientEntryUserPolicyRecord
		if err := rows.Scan(&record.ID, &record.EntryGroupID, &record.EntryGroupName, &record.ServerType, &record.ServerID, &record.ServerName, &record.Enabled, &record.Remarks, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan client entry user policy: %w", err)
		}
		result = append(result, record)
		ids = append(ids, record.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry user policies: %w", err)
	}
	if len(ids) == 0 {
		return result, nil
	}
	emails, err := s.loadClientEntryUserPolicyEmails(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Emails = emails[result[index].ID]
		if len(result[index].Emails) > 0 {
			result[index].Email = result[index].Emails[0]
		}
	}
	return result, nil
}

func (s *DBService) loadClientEntryUserPolicyEmails(ctx context.Context, ids []int64) (map[int64][]string, error) {
	result := make(map[int64][]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT policy_id, email
FROM v2_client_entry_user_policy_user
WHERE policy_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY policy_id ASC, email ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry user policy emails: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var policyID int64
		var email string
		if err := rows.Scan(&policyID, &email); err != nil {
			return nil, fmt.Errorf("scan client entry user policy email: %w", err)
		}
		result[policyID] = append(result[policyID], strings.TrimSpace(email))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry user policy emails: %w", err)
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
	emails := normalizePolicyEmails(append(req.Emails, req.Email))
	req.ServerType = strings.TrimSpace(req.ServerType)
	req.Remarks = strings.TrimSpace(req.Remarks)
	if len(emails) == 0 {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()
	policyID := int64(0)
	if req.ID == nil {
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy (entry_group_id, server_type, server_id, enabled, remarks, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id`, req.EntryGroupID, req.ServerType, req.ServerID, enabled, req.Remarks, now, now).Scan(&policyID); err != nil {
			return false, errors.New("保存失败")
		}
	} else {
		policyID = *req.ID
		result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET entry_group_id = $2, server_type = $3, server_id = $4, enabled = $5, remarks = $6, updated_at = $7
WHERE id = $1`, policyID, req.EntryGroupID, req.ServerType, req.ServerID, enabled, req.Remarks, now)
		if err != nil {
			return false, errors.New("保存失败")
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return false, errors.New("保存失败")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_user WHERE policy_id = $1`, policyID); err != nil {
		return false, errors.New("保存失败")
	}
	for _, email := range emails {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_user (policy_id, email, created_at, updated_at)
VALUES ($1, $2, $3, $4)`, policyID, email, now, now); err != nil {
			return false, errors.New("保存失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func normalizePolicyEmails(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, email := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' ' }) {
			email = strings.ToLower(strings.TrimSpace(email))
			if email == "" {
				continue
			}
			if _, ok := seen[email]; ok {
				continue
			}
			seen[email] = struct{}{}
			result = append(result, email)
		}
	}
	return result
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("删除失败")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_user WHERE policy_id = $1`, id); err != nil {
		return false, errors.New("删除失败")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id = $1`, id)
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
