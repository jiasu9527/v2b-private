package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/subscribelink"
)

const clientEntryRuleSortStep int64 = 10
const clientEntryVisibleOrderLockKey int64 = 2026081001

func (s *DBService) ListClientEntryUserPolicies(ctx context.Context) ([]ClientEntryUserPolicyRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to, p.action, p.conditions, p.entry_host, p.resolve_entry_host, p.extra_nodes, p.extra_nodes_position, p.enabled, p.remarks, p.created_at, p.updated_at
FROM v2_client_entry_user_policy p
ORDER BY p.sort ASC NULLS LAST, p.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query client entry rules: %w", err)
	}
	defer rows.Close()

	result := make([]ClientEntryUserPolicyRecord, 0)
	ids := make([]int64, 0)
	splitIDs := make([]int64, 0)
	for rows.Next() {
		var (
			record        ClientEntryUserPolicyRecord
			snapshotFrom  sql.NullInt64
			snapshotTo    sql.NullInt64
			conditionsRaw string
			extraNodesRaw string
		)
		if err := rows.Scan(
			&record.ID,
			&record.Name,
			&record.Sort,
			&record.Mode,
			&snapshotFrom,
			&snapshotTo,
			&record.Action,
			&conditionsRaw,
			&record.EntryHost,
			&record.ResolveEntryHost,
			&extraNodesRaw,
			&record.ExtraNodesPosition,
			&record.Enabled,
			&record.Remarks,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan client entry rule: %w", err)
		}
		mode, err := normalizeClientEntryUserPolicyMode(record.Mode)
		if err != nil {
			return nil, fmt.Errorf("decode client entry rule %d mode: %w", record.ID, err)
		}
		record.Mode = mode
		if snapshotFrom.Valid {
			value := snapshotFrom.Int64
			record.SnapshotFrom = &value
		}
		if snapshotTo.Valid {
			value := snapshotTo.Int64
			record.SnapshotTo = &value
		}
		conditions, err := cliententry.DecodeConditions(conditionsRaw)
		if err != nil {
			return nil, fmt.Errorf("decode client entry rule %d conditions: %w", record.ID, err)
		}
		action, err := cliententry.NormalizeAction(record.Action)
		if err != nil {
			return nil, fmt.Errorf("decode client entry rule %d action: %w", record.ID, err)
		}
		record.Name = strings.TrimSpace(record.Name)
		record.Action = action
		record.EntryHost = strings.TrimSpace(record.EntryHost)
		extraNodes, err := subscribelink.DecodeList(extraNodesRaw)
		if err != nil {
			return nil, fmt.Errorf("decode client entry rule %d extra nodes: %w", record.ID, err)
		}
		record.ExtraNodes = extraNodes
		extraNodesPosition, err := subscribelink.NormalizePosition(record.ExtraNodesPosition)
		if err != nil {
			return nil, fmt.Errorf("decode client entry rule %d extra node position: %w", record.ID, err)
		}
		record.ExtraNodesPosition = extraNodesPosition
		record.Remarks = strings.TrimSpace(record.Remarks)
		record.Conditions = conditions
		record.Members = []ClientEntryGroupMemberRecord{}
		if record.Mode == ClientEntryUserPolicyModeSplit {
			record.SplitGroups = []ClientEntryUserPolicySplitGroupRecord{}
			splitIDs = append(splitIDs, record.ID)
		}
		result = append(result, record)
		ids = append(ids, record.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry rules: %w", err)
	}
	if len(ids) == 0 {
		return result, nil
	}
	members, err := s.loadClientEntryUserPolicyMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Members = members[result[index].ID]
	}
	groups, counts, err := s.loadClientEntryUserPolicySplitGroups(ctx, splitIDs)
	if err != nil {
		return nil, err
	}
	for index := range result {
		if result[index].Mode != ClientEntryUserPolicyModeSplit {
			continue
		}
		result[index].SplitGroups = groups[result[index].ID]
		result[index].SnapshotUserCount = counts[result[index].ID]
	}
	if err := s.attachClientEntryUserPolicyIDRangeCounts(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

type clientEntryUserIDRange struct {
	PolicyID int64
	Minimum  int64
	Maximum  int64
}

// attachClientEntryUserPolicyIDRangeCounts reports actual user rows inside the
// combined user_id/between bounds. It intentionally does not call this a full
// rule match count: UA and registration-day conditions are evaluated at
// subscription time and cannot be represented by a static database count.
func (s *DBService) attachClientEntryUserPolicyIDRangeCounts(ctx context.Context, policies []ClientEntryUserPolicyRecord) error {
	ranges := make([]clientEntryUserIDRange, 0, len(policies))
	policyIndexes := make(map[int64]int, len(policies))
	for index := range policies {
		minimum, maximum, ok := combinedClientEntryUserIDRange(policies[index].Conditions)
		if !ok {
			continue
		}
		ranges = append(ranges, clientEntryUserIDRange{PolicyID: policies[index].ID, Minimum: minimum, Maximum: maximum})
		policyIndexes[policies[index].ID] = index
		count := int64(0)
		policies[index].IDRangeUserCount = &count
	}
	if len(ranges) == 0 {
		return nil
	}

	values := make([]string, 0, len(ranges))
	args := make([]any, 0, len(ranges)*3)
	for _, item := range ranges {
		start := len(args) + 1
		values = append(values, fmt.Sprintf("($%d::BIGINT, $%d::BIGINT, $%d::BIGINT)", start, start+1, start+2))
		args = append(args, item.PolicyID, item.Minimum, item.Maximum)
	}
	rows, err := s.db.QueryContext(ctx, `WITH ranges(policy_id, min_id, max_id) AS (VALUES `+strings.Join(values, ",")+`)
SELECT ranges.policy_id, COUNT(users.id)::BIGINT
FROM ranges
LEFT JOIN v2_user users ON users.id BETWEEN ranges.min_id AND ranges.max_id
GROUP BY ranges.policy_id`, args...)
	if err != nil {
		return fmt.Errorf("count users in client entry ID ranges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var policyID, count int64
		if err := rows.Scan(&policyID, &count); err != nil {
			return fmt.Errorf("scan client entry ID range user count: %w", err)
		}
		if index, ok := policyIndexes[policyID]; ok {
			value := count
			policies[index].IDRangeUserCount = &value
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate client entry ID range user counts: %w", err)
	}
	return nil
}

func combinedClientEntryUserIDRange(conditions []cliententry.Condition) (int64, int64, bool) {
	var minimum, maximum int64
	found := false
	for _, condition := range conditions {
		if condition.Field != "user_id" || condition.Operator != "between" {
			continue
		}
		nextMinimum, minErr := strconv.ParseInt(strings.TrimSpace(string(condition.Min)), 10, 64)
		nextMaximum, maxErr := strconv.ParseInt(strings.TrimSpace(string(condition.Max)), 10, 64)
		if minErr != nil || maxErr != nil {
			continue
		}
		if !found {
			minimum, maximum, found = nextMinimum, nextMaximum, true
			continue
		}
		if nextMinimum > minimum {
			minimum = nextMinimum
		}
		if nextMaximum < maximum {
			maximum = nextMaximum
		}
	}
	return minimum, maximum, found
}

func (s *DBService) loadClientEntryUserPolicyMembers(ctx context.Context, ids []int64) (map[int64][]ClientEntryGroupMemberRecord, error) {
	result := make(map[int64][]ClientEntryGroupMemberRecord, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT policy_id, server_type, server_id, sort
FROM v2_client_entry_user_policy_member
WHERE policy_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY policy_id ASC, sort ASC NULLS LAST, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry rule members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			policyID int64
			record   ClientEntryGroupMemberRecord
		)
		if err := rows.Scan(&policyID, &record.ServerType, &record.ServerID, &record.Sort); err != nil {
			return nil, fmt.Errorf("scan client entry rule member: %w", err)
		}
		record.ServerType = strings.ToLower(strings.TrimSpace(record.ServerType))
		if record.ServerType != "" && record.ServerID > 0 {
			result[policyID] = append(result[policyID], record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry rule members: %w", err)
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

	prepared, err := normalizeClientEntryUserPolicySaveRequest(req)
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()

	if err := validateClientEntryRuleUsers(ctx, tx, prepared.Conditions); err != nil {
		return false, err
	}
	if err := validateClientEntryRuleMembers(ctx, tx, prepared.Members); err != nil {
		return false, err
	}

	conditions, err := cliententry.EncodeConditions(prepared.Conditions)
	if err != nil {
		return false, err
	}
	extraNodes, err := subscribelink.EncodeList(prepared.ExtraNodes)
	if err != nil {
		return false, err
	}
	policyID := int64(0)
	if prepared.ID == nil {
		nextSort, err := nextClientEntryRuleSort(ctx, tx)
		if err != nil {
			return false, errors.New("保存失败")
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy
(name, sort, action, conditions, entry_host, resolve_entry_host, extra_nodes, extra_nodes_position, enabled, remarks, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
RETURNING id`, prepared.Name, nextSort, prepared.Action, conditions, prepared.EntryHost, prepared.ResolveEntryHost, extraNodes, prepared.ExtraNodesPosition, prepared.Enabled, prepared.Remarks, now).Scan(&policyID); err != nil {
			return false, errors.New("保存失败")
		}
	} else {
		policyID = *prepared.ID
		if policyID <= 0 {
			return false, errors.New("规则不存在")
		}
		result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET name = $2, action = $3, conditions = $4, entry_host = $5, resolve_entry_host = $6, extra_nodes = $7, extra_nodes_position = $8, enabled = $9, remarks = $10, updated_at = $11
WHERE id = $1 AND mode = 'standard'`, policyID, prepared.Name, prepared.Action, conditions, prepared.EntryHost, prepared.ResolveEntryHost, extraNodes, prepared.ExtraNodesPosition, prepared.Enabled, prepared.Remarks, now)
		if err != nil {
			return false, errors.New("保存失败")
		}
		if err := requireClientEntryRuleAffected(result, "规则"); err != nil {
			return false, errors.New("规则不存在")
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = $1`, policyID); err != nil {
		return false, errors.New("保存失败")
	}
	for index, member := range prepared.Members {
		memberSort := int64(index+1) * clientEntryRuleSortStep
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member (policy_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)`, policyID, member.ServerType, member.ServerID, memberSort, now); err != nil {
			return false, errors.New("保存失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("保存失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return true, nil
}

func (s *DBService) SortClientEntryUserPolicies(ctx context.Context, ids []int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}

	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return false, errors.New("规则顺序无效")
		}
		if _, exists := seen[id]; exists {
			return false, errors.New("规则顺序包含重复项")
		}
		seen[id] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存排序失败")
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM v2_client_entry_user_policy FOR UPDATE`)
	if err != nil {
		return false, errors.New("保存排序失败")
	}
	actual := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, errors.New("保存排序失败")
		}
		actual[id] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return false, errors.New("保存排序失败")
	}
	if len(actual) != len(seen) {
		return false, errors.New("规则列表已变化，请刷新后重试")
	}
	for id := range seen {
		if _, exists := actual[id]; !exists {
			return false, errors.New("规则列表已变化，请刷新后重试")
		}
	}

	now := time.Now().Unix()
	for index, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET sort = $2, updated_at = $3 WHERE id = $1`, id, int64(index+1)*clientEntryRuleSortStep, now)
		if err != nil || requireClientEntryRuleAffected(result, "规则") != nil {
			return false, errors.New("保存排序失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("保存排序失败")
	}
	return true, nil
}

// SortClientEntryUserPolicyRows persists the exact flattened rows rendered by
// the admin table: standard policies and current split leaves share one global
// ordering namespace. Split policy containers and non-leaf history rows are
// deliberately excluded.
func (s *DBService) SortClientEntryUserPolicyRows(ctx context.Context, items []ClientEntryUserPolicySortItem) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}
	type rowKey struct {
		kind string
		id   int64
	}
	seen := make(map[rowKey]struct{}, len(items))
	for index := range items {
		items[index].Kind = strings.ToLower(strings.TrimSpace(items[index].Kind))
		if (items[index].Kind != "policy" && items[index].Kind != "split_group") || items[index].ID <= 0 {
			return false, errors.New("规则顺序无效")
		}
		key := rowKey{kind: items[index].Kind, id: items[index].ID}
		if _, exists := seen[key]; exists {
			return false, errors.New("规则顺序包含重复项")
		}
		seen[key] = struct{}{}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存排序失败")
	}
	defer tx.Rollback()
	if err := lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return false, errors.New("保存排序失败")
	}

	actual := make(map[rowKey]struct{}, len(items))
	policyRows, err := tx.QueryContext(ctx, `SELECT id, mode FROM v2_client_entry_user_policy ORDER BY id ASC FOR UPDATE`)
	if err != nil {
		return false, errors.New("保存排序失败")
	}
	for policyRows.Next() {
		var id int64
		var mode string
		if err := policyRows.Scan(&id, &mode); err != nil {
			policyRows.Close()
			return false, errors.New("保存排序失败")
		}
		if !strings.EqualFold(strings.TrimSpace(mode), ClientEntryUserPolicyModeSplit) {
			actual[rowKey{kind: "policy", id: id}] = struct{}{}
		}
	}
	if err := policyRows.Err(); err != nil {
		policyRows.Close()
		return false, errors.New("保存排序失败")
	}
	if err := policyRows.Close(); err != nil {
		return false, errors.New("保存排序失败")
	}

	groupRows, err := tx.QueryContext(ctx, `SELECT split_group.id
FROM v2_client_entry_user_policy_split_group split_group
JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
WHERE policy.mode = 'split'
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
  )
ORDER BY split_group.id ASC
FOR UPDATE OF split_group`)
	if err != nil {
		return false, errors.New("保存排序失败")
	}
	for groupRows.Next() {
		var id int64
		if err := groupRows.Scan(&id); err != nil {
			groupRows.Close()
			return false, errors.New("保存排序失败")
		}
		actual[rowKey{kind: "split_group", id: id}] = struct{}{}
	}
	if err := groupRows.Err(); err != nil {
		groupRows.Close()
		return false, errors.New("保存排序失败")
	}
	if err := groupRows.Close(); err != nil {
		return false, errors.New("保存排序失败")
	}
	if len(actual) != len(seen) {
		return false, errors.New("规则列表已变化，请刷新后重试")
	}
	for key := range seen {
		if _, exists := actual[key]; !exists {
			return false, errors.New("规则列表已变化，请刷新后重试")
		}
	}

	now := time.Now().Unix()
	for index, item := range items {
		nextSort := int64(index+1) * clientEntryRuleSortStep
		var result sql.Result
		if item.Kind == "policy" {
			result, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET sort = $2, updated_at = $3
WHERE id = $1 AND mode <> 'split'`, item.ID, nextSort, now)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group split_group
SET global_sort = $2, updated_at = $3
WHERE split_group.id = $1
  AND EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy policy
	WHERE policy.id = split_group.policy_id AND policy.mode = 'split'
  )
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
  )`, item.ID, nextSort, now)
		}
		if err != nil || requireClientEntryRuleAffected(result, "规则") != nil {
			return false, errors.New("保存排序失败")
		}
	}
	if err := syncClientEntrySplitPolicySorts(ctx, tx); err != nil {
		return false, errors.New("保存排序失败")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("保存排序失败")
	}
	s.markClientEntryMonitorTargetsDirty()
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
		return false, errors.New("规则不存在")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("删除失败")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = $1`, id); err != nil {
		return false, errors.New("删除失败")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	if err := requireClientEntryRuleAffected(result, "规则"); err != nil {
		return false, errors.New("规则不存在")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("删除失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return true, nil
}

type preparedClientEntryRuleSaveRequest struct {
	ID                 *int64
	Name               string
	Action             string
	Conditions         []cliententry.Condition
	Members            []ClientEntryGroupMemberSaveRequest
	EntryHost          string
	ResolveEntryHost   int64
	ExtraNodes         []string
	ExtraNodesPosition string
	Enabled            int64
	Remarks            string
}

func normalizeClientEntryUserPolicySaveRequest(req ClientEntryUserPolicySaveRequest) (preparedClientEntryRuleSaveRequest, error) {
	result := preparedClientEntryRuleSaveRequest{
		ID:      req.ID,
		Name:    strings.TrimSpace(req.Name),
		Remarks: strings.TrimSpace(req.Remarks),
	}
	if result.Name == "" {
		return result, errors.New("规则名称不能为空")
	}
	if len([]rune(result.Name)) > 255 {
		return result, errors.New("规则名称不能超过 255 个字符")
	}
	if len([]rune(result.Remarks)) > 255 {
		return result, errors.New("备注不能超过 255 个字符")
	}
	action, err := cliententry.NormalizeAction(req.Action)
	if err != nil {
		return result, err
	}
	result.Action = action
	if action != cliententry.ActionOverride {
		if strings.TrimSpace(req.EntryHost) != "" {
			if action == cliententry.ActionHide {
				return result, errors.New("隐藏节点规则不能填写独立入口地址")
			}
			return result, errors.New("下发原入口地址规则不能填写独立入口地址")
		}
	} else {
		host, err := cliententry.NormalizeHost(req.EntryHost)
		if err != nil {
			return result, err
		}
		result.EntryHost = host
		if req.ResolveEntryHost != nil {
			if *req.ResolveEntryHost != 0 && *req.ResolveEntryHost != 1 {
				return result, errors.New("解析域名下发 IP 设置无效")
			}
			result.ResolveEntryHost = *req.ResolveEntryHost
		}
	}
	conditions, err := cliententry.NormalizeConditions(req.Conditions)
	if err != nil {
		return result, err
	}
	result.Conditions = conditions
	extraNodes, err := subscribelink.NormalizeList(req.ExtraNodes)
	if err != nil {
		return result, err
	}
	result.ExtraNodes = extraNodes
	extraNodesPosition, err := subscribelink.NormalizePosition(req.ExtraNodesPosition)
	if err != nil {
		return result, err
	}
	result.ExtraNodesPosition = extraNodesPosition
	members, err := normalizePolicyMembers(req.Members)
	if err != nil {
		return result, err
	}
	result.Members = members
	if len(result.Members) == 0 {
		return result, errors.New("生效节点不能为空")
	}
	result.Enabled = 1
	if req.Enabled != nil {
		if *req.Enabled != 0 && *req.Enabled != 1 {
			return result, errors.New("规则状态无效")
		}
		result.Enabled = *req.Enabled
	}
	return result, nil
}

func normalizePolicyMembers(values []ClientEntryGroupMemberSaveRequest) ([]ClientEntryGroupMemberSaveRequest, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]ClientEntryGroupMemberSaveRequest, 0, len(values))
	for _, value := range values {
		serverType := strings.ToLower(strings.TrimSpace(value.ServerType))
		if _, exists := managedServerDefinitions[serverType]; !exists || value.ServerID <= 0 {
			return nil, errors.New("生效节点无效")
		}
		key := serverType + ":" + fmt.Sprint(value.ServerID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ClientEntryGroupMemberSaveRequest{ServerType: serverType, ServerID: value.ServerID, Sort: value.Sort})
	}
	return result, nil
}

func validateClientEntryRuleUsers(ctx context.Context, tx *sql.Tx, conditions []cliententry.Condition) error {
	ids := make(map[int64]struct{})
	for _, condition := range conditions {
		if condition.Field != "user_id" || condition.Operator != "in" {
			continue
		}
		for _, raw := range condition.Values {
			id, err := clientEntryRuleRawInt64(raw)
			if err != nil || id <= 0 {
				return errors.New("指定用户无效")
			}
			ids[id] = struct{}{}
		}
	}
	for id := range ids {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM v2_user WHERE id = $1)`, id).Scan(&exists); err != nil {
			return errors.New("校验指定用户失败")
		}
		if !exists {
			return fmt.Errorf("指定用户 #%d 不存在", id)
		}
	}
	return nil
}

func validateClientEntryRuleMembers(ctx context.Context, tx *sql.Tx, members []ClientEntryGroupMemberSaveRequest) error {
	for _, member := range members {
		definition, exists := managedServerDefinitions[member.ServerType]
		if !exists || member.ServerID <= 0 {
			return errors.New("生效节点无效")
		}
		var found bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+quoteIdentifier(definition.table)+` WHERE id = $1)`, member.ServerID).Scan(&found); err != nil {
			return errors.New("校验生效节点失败")
		}
		if !found {
			return fmt.Errorf("生效节点 %s #%d 不存在", member.ServerType, member.ServerID)
		}
	}
	return nil
}

func nextClientEntryRuleSort(ctx context.Context, tx *sql.Tx) (int64, error) {
	if err := lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return 0, err
	}
	var last int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(visible_sort), 0)::BIGINT
FROM (
	SELECT policy.sort::BIGINT AS visible_sort
	FROM v2_client_entry_user_policy policy
	WHERE policy.mode <> 'split'
	UNION ALL
	SELECT split_group.global_sort AS visible_sort
	FROM v2_client_entry_user_policy_split_group split_group
	JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
	WHERE policy.mode = 'split' AND split_group.global_sort IS NOT NULL
	  AND NOT EXISTS (
		SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
	  )
) visible`).Scan(&last)
	if err != nil {
		return 0, err
	}
	if last < 0 || last > (1<<62) {
		return clientEntryRuleSortStep, nil
	}
	return last + clientEntryRuleSortStep, nil
}

func lockClientEntryVisibleOrder(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return errors.New("排序事务不存在")
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, clientEntryVisibleOrderLockKey)
	return err
}

func shiftClientEntryVisibleSortsAfter(ctx context.Context, tx *sql.Tx, position, delta int64) error {
	if delta <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET sort = sort + $2
WHERE mode <> 'split' AND sort > $1`, position, delta); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group
SET global_sort = global_sort + $2
WHERE global_sort IS NOT NULL AND global_sort > $1`, position, delta); err != nil {
		return err
	}
	return syncClientEntrySplitPolicySorts(ctx, tx)
}

func syncClientEntrySplitPolicySorts(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy policy
SET sort = positions.global_sort::INTEGER
FROM (
	SELECT split_group.policy_id, MIN(split_group.global_sort) AS global_sort
	FROM v2_client_entry_user_policy_split_group split_group
	WHERE split_group.global_sort IS NOT NULL
	  AND NOT EXISTS (
		SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
	  )
	GROUP BY split_group.policy_id
) positions
WHERE policy.id = positions.policy_id AND policy.mode = 'split'`)
	return err
}

func requireClientEntryRuleAffected(result sql.Result, label string) error {
	if result == nil {
		return errors.New(label + "不存在")
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New(label + "不存在")
	}
	return nil
}

func clientEntryRuleRawInt64(raw []byte) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}
