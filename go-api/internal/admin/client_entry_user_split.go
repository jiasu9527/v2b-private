package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/subscribelink"
)

const (
	defaultClientEntryUserPolicySplitMinutes int64 = 60
	maxClientEntryUserPolicySplitMinutes     int64 = 30 * 24 * 60
)

type preparedClientEntryUserPolicySplitCreateRequest struct {
	Name             string
	Minutes          int64
	Members          []ClientEntryGroupMemberSaveRequest
	EntryHostA       string
	EntryHostB       string
	ResolveEntryHost int64
	Enabled          int64
	Remarks          string
}

func (s *DBService) PreviewClientEntryUserPolicySplit(ctx context.Context, req ClientEntryUserPolicySplitPreviewRequest) (ClientEntryUserPolicySplitPreviewResult, error) {
	if s.db == nil {
		return ClientEntryUserPolicySplitPreviewResult{}, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryUserPolicySplitPreviewResult{}, err
	}
	minutes, err := normalizeClientEntryUserPolicySplitMinutes(req.Minutes)
	if err != nil {
		return ClientEntryUserPolicySplitPreviewResult{}, err
	}
	to := time.Now().Unix()
	from := to - minutes*60
	result := ClientEntryUserPolicySplitPreviewResult{Minutes: minutes, From: from, To: to}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)::BIGINT
FROM v2_user_subscribe_activity activity
JOIN v2_user users ON users.id = activity.user_id
WHERE activity.last_subscribe_at BETWEEN $1 AND $2`, from, to).Scan(&result.UserCount); err != nil {
		return ClientEntryUserPolicySplitPreviewResult{}, fmt.Errorf("预览近期订阅用户失败: %w", err)
	}
	return result, nil
}

func (s *DBService) CreateClientEntryUserPolicySplit(ctx context.Context, req ClientEntryUserPolicySplitCreateRequest) (ClientEntryUserPolicyRecord, error) {
	if s.db == nil {
		return ClientEntryUserPolicyRecord{}, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}
	prepared, err := normalizeClientEntryUserPolicySplitCreateRequest(req)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}

	now := time.Now().Unix()
	from := now - prepared.Minutes*60
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("开启数据库事务", err)
	}
	defer tx.Rollback()
	if err := validateClientEntryRuleMembers(ctx, tx, prepared.Members); err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}
	nextSort, err := nextClientEntryRuleSort(ctx, tx)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("读取规则顺序", err)
	}

	var policyID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy
(name, sort, mode, action, conditions, entry_host, resolve_entry_host, extra_nodes, extra_nodes_position, snapshot_from, snapshot_to, enabled, remarks, created_at, updated_at)
VALUES ($1, $2, 'split', 'override', '[]', '', $3, '[]', 'after', $4, $5, $6, $7, $8, $8)
RETURNING id`, prepared.Name, nextSort, prepared.ResolveEntryHost, from, now, prepared.Enabled, prepared.Remarks, now).Scan(&policyID); err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("写入规则", err)
	}
	for index, member := range prepared.Members {
		memberSort := int64(index+1) * clientEntryRuleSortStep
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member
(policy_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)`, policyID, member.ServerType, member.ServerID, memberSort, now); err != nil {
			return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("写入生效节点", err)
		}
	}

	groupA, err := insertClientEntryUserPolicySplitGroup(ctx, tx, policyID, nil, "A", "A", prepared.EntryHostA, clientEntryRuleSortStep, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("创建 A 组", err)
	}
	groupB, err := insertClientEntryUserPolicySplitGroup(ctx, tx, policyID, nil, "B", "B", prepared.EntryHostB, 2*clientEntryRuleSortStep, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("创建 B 组", err)
	}

	rows, err := tx.QueryContext(ctx, `WITH eligible AS (
	SELECT activity.user_id,
	       ROW_NUMBER() OVER (ORDER BY activity.user_id ASC) AS position,
	       COUNT(*) OVER () AS total
	FROM v2_user_subscribe_activity activity
	JOIN v2_user users ON users.id = activity.user_id
	WHERE activity.last_subscribe_at BETWEEN $1 AND $2
)
INSERT INTO v2_client_entry_user_policy_split_assignment
(policy_id, user_id, group_id, created_at, updated_at)
SELECT $3, user_id, CASE WHEN position <= (total + 1) / 2 THEN $4 ELSE $5 END, $2, $2
FROM eligible
ORDER BY user_id ASC
RETURNING group_id`, from, now, policyID, groupA, groupB)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("固定用户名单", err)
	}
	userCount := int64(0)
	groupCounts := map[int64]int64{groupA: 0, groupB: 0}
	for rows.Next() {
		var groupID int64
		if err := rows.Scan(&groupID); err != nil {
			rows.Close()
			return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("读取分组结果", err)
		}
		userCount++
		groupCounts[groupID]++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("读取分组结果", err)
	}
	if err := rows.Close(); err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("关闭分组结果", err)
	}
	if userCount < 2 || groupCounts[groupA] == 0 || groupCounts[groupB] == 0 {
		return ClientEntryUserPolicyRecord{}, errors.New("近期成功拉取订阅的用户不足 2 人，无法创建二分规则")
	}
	if difference := groupCounts[groupA] - groupCounts[groupB]; difference < -1 || difference > 1 {
		return ClientEntryUserPolicyRecord{}, errors.New("二分用户人数异常，请重试")
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryUserPolicyRecord{}, clientEntryUserPolicySplitCreateFailure("提交数据库事务", err)
	}
	s.markClientEntryMonitorTargetsDirty()
	members := make([]ClientEntryGroupMemberRecord, 0, len(prepared.Members))
	for index, member := range prepared.Members {
		sortValue := int64(index+1) * clientEntryRuleSortStep
		members = append(members, ClientEntryGroupMemberRecord{ServerType: member.ServerType, ServerID: member.ServerID, Sort: &sortValue})
	}
	snapshotFrom, snapshotTo := from, now
	return ClientEntryUserPolicyRecord{
		ID: policyID, Name: prepared.Name, Sort: nextSort, Mode: ClientEntryUserPolicyModeSplit,
		SnapshotFrom: &snapshotFrom, SnapshotTo: &snapshotTo, SnapshotUserCount: userCount,
		SplitGroups: []ClientEntryUserPolicySplitGroupRecord{
			{ID: groupA, PolicyID: policyID, Name: "A", Path: "A", EntryHost: prepared.EntryHostA, Sort: clientEntryRuleSortStep, UserCount: groupCounts[groupA], IsLeaf: true, CreatedAt: now, UpdatedAt: now},
			{ID: groupB, PolicyID: policyID, Name: "B", Path: "B", EntryHost: prepared.EntryHostB, Sort: 2 * clientEntryRuleSortStep, UserCount: groupCounts[groupB], IsLeaf: true, CreatedAt: now, UpdatedAt: now},
		},
		Action: cliententry.ActionOverride, Conditions: []cliententry.Condition{}, Members: members,
		EntryHost: "", ResolveEntryHost: prepared.ResolveEntryHost, ExtraNodes: []string{}, ExtraNodesPosition: "after",
		Enabled: prepared.Enabled, Remarks: prepared.Remarks, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func clientEntryUserPolicySplitCreateFailure(stage string, err error) error {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "未知阶段"
	}
	if err == nil {
		err = errors.New("unknown error")
	}
	log.Printf("client entry user policy split create failed stage=%q err=%q", stage, err)
	return fmt.Errorf("创建二分规则失败（%s）：%w", stage, err)
}

// ConvertClientEntryUserPolicyToSplit turns one existing, pure user-ID range
// rule into a fixed two-way snapshot without creating another policy row. The
// original condition is intentionally retained as conversion provenance; at
// subscription time split assignments, rather than conditions, decide matches.
func (s *DBService) ConvertClientEntryUserPolicyToSplit(ctx context.Context, req ClientEntryUserPolicySplitConvertRequest) (ClientEntryUserPolicyRecord, error) {
	if s.db == nil {
		return ClientEntryUserPolicyRecord{}, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}
	if req.PolicyID <= 0 {
		return ClientEntryUserPolicyRecord{}, errors.New("规则不存在")
	}
	hostA, hostB, err := normalizeClientEntryUserPolicySplitHosts(req.EntryHostA, req.EntryHostB)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	defer tx.Rollback()

	var action, conditionsRaw, extraNodesRaw string
	if err := tx.QueryRowContext(ctx, `SELECT action, conditions, extra_nodes
FROM v2_client_entry_user_policy
WHERE id = $1 AND mode = 'standard'
FOR UPDATE`, req.PolicyID).Scan(&action, &conditionsRaw, &extraNodesRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClientEntryUserPolicyRecord{}, errors.New("只能转换现有的标准规则")
		}
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	if !strings.EqualFold(strings.TrimSpace(action), cliententry.ActionOverride) {
		return ClientEntryUserPolicyRecord{}, errors.New("只有覆盖入口地址的规则可以转换为固定二分")
	}
	conditions, err := cliententry.DecodeConditions(conditionsRaw)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("规则匹配条件无效，无法转换")
	}
	if len(conditions) != 1 || conditions[0].Field != "user_id" || conditions[0].Operator != "between" {
		return ClientEntryUserPolicyRecord{}, errors.New("只能转换仅包含一个用户 ID 范围条件的规则")
	}
	minimum, minErr := clientEntryRuleRawInt64(conditions[0].Min)
	maximum, maxErr := clientEntryRuleRawInt64(conditions[0].Max)
	if minErr != nil || maxErr != nil || minimum <= 0 || maximum < minimum {
		return ClientEntryUserPolicyRecord{}, errors.New("用户 ID 范围无效，无法转换")
	}
	extraNodes, err := subscribelink.DecodeList(extraNodesRaw)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("规则额外节点无效，无法转换")
	}
	if len(extraNodes) != 0 {
		return ClientEntryUserPolicyRecord{}, errors.New("包含额外下发节点的规则不能转换为固定二分")
	}

	now := time.Now().Unix()
	groupA, err := insertClientEntryUserPolicySplitGroup(ctx, tx, req.PolicyID, nil, "A", "A", hostA, clientEntryRuleSortStep, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	groupB, err := insertClientEntryUserPolicySplitGroup(ctx, tx, req.PolicyID, nil, "B", "B", hostB, 2*clientEntryRuleSortStep, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}

	rows, err := tx.QueryContext(ctx, `WITH eligible AS (
	SELECT users.id AS user_id,
	       ROW_NUMBER() OVER (ORDER BY users.id ASC) AS position,
	       COUNT(*) OVER () AS total
	FROM v2_user users
	WHERE users.id BETWEEN $1 AND $2
)
INSERT INTO v2_client_entry_user_policy_split_assignment
(policy_id, user_id, group_id, created_at, updated_at)
SELECT $3, user_id, CASE WHEN position <= (total + 1) / 2 THEN $4 ELSE $5 END, $6, $6
FROM eligible
ORDER BY user_id ASC
RETURNING group_id`, minimum, maximum, req.PolicyID, groupA, groupB, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	userCount := int64(0)
	groupCounts := map[int64]int64{groupA: 0, groupB: 0}
	for rows.Next() {
		var groupID int64
		if err := rows.Scan(&groupID); err != nil {
			rows.Close()
			return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
		}
		userCount++
		groupCounts[groupID]++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	if err := rows.Close(); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	if userCount < 2 || groupCounts[groupA] == 0 || groupCounts[groupB] == 0 {
		return ClientEntryUserPolicyRecord{}, errors.New("用户 ID 范围内的现有用户不足 2 人，无法转换")
	}
	if difference := groupCounts[groupA] - groupCounts[groupB]; difference < -1 || difference > 1 {
		return ClientEntryUserPolicyRecord{}, errors.New("二分用户人数异常，请重试")
	}

	result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET mode = 'split', entry_host = '', extra_nodes = '[]', snapshot_from = NULL, snapshot_to = NULL, updated_at = $2
WHERE id = $1 AND mode = 'standard'`, req.PolicyID, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	if err := requireClientEntryRuleAffected(result, "规则"); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("规则已变化，请刷新后重试")
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("转换固定二分规则失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return ClientEntryUserPolicyRecord{
		ID: req.PolicyID, Mode: ClientEntryUserPolicyModeSplit, Action: cliententry.ActionOverride,
		Conditions: conditions, SnapshotUserCount: userCount,
		SplitGroups: []ClientEntryUserPolicySplitGroupRecord{
			{ID: groupA, PolicyID: req.PolicyID, Name: "A", Path: "A", EntryHost: hostA, Sort: clientEntryRuleSortStep, UserCount: groupCounts[groupA], IsLeaf: true, CreatedAt: now, UpdatedAt: now},
			{ID: groupB, PolicyID: req.PolicyID, Name: "B", Path: "B", EntryHost: hostB, Sort: 2 * clientEntryRuleSortStep, UserCount: groupCounts[groupB], IsLeaf: true, CreatedAt: now, UpdatedAt: now},
		},
	}, nil
}

func (s *DBService) SplitClientEntryUserPolicyGroup(ctx context.Context, req ClientEntryUserPolicyGroupSplitRequest) (ClientEntryUserPolicyRecord, error) {
	if s.db == nil {
		return ClientEntryUserPolicyRecord{}, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}
	if req.PolicyID <= 0 || req.GroupID <= 0 {
		return ClientEntryUserPolicyRecord{}, errors.New("二分组不存在")
	}
	hostA, hostB, err := normalizeClientEntryUserPolicySplitHosts(req.EntryHostA, req.EntryHostB)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	defer tx.Rollback()

	var parentName, parentPath string
	if err := tx.QueryRowContext(ctx, `SELECT split_group.name, split_group.path
FROM v2_client_entry_user_policy_split_group split_group
JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
WHERE split_group.id = $1 AND split_group.policy_id = $2 AND policy.mode = 'split'
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
  )
FOR UPDATE OF split_group`, req.GroupID, req.PolicyID).Scan(&parentName, &parentPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClientEntryUserPolicyRecord{}, errors.New("只能继续二分当前叶子组")
		}
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}

	assignmentRows, err := tx.QueryContext(ctx, `SELECT user_id
FROM v2_client_entry_user_policy_split_assignment
WHERE policy_id = $1 AND group_id = $2
ORDER BY user_id ASC
FOR UPDATE`, req.PolicyID, req.GroupID)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	userCount := int64(0)
	for assignmentRows.Next() {
		var userID int64
		if err := assignmentRows.Scan(&userID); err != nil {
			assignmentRows.Close()
			return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
		}
		userCount++
	}
	if err := assignmentRows.Err(); err != nil {
		assignmentRows.Close()
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	if err := assignmentRows.Close(); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	if userCount < 2 {
		return ClientEntryUserPolicyRecord{}, errors.New("该组用户不足 2 人，无法继续二分")
	}

	parentPath = strings.TrimSpace(parentPath)
	if parentPath == "" {
		parentPath = strings.TrimSpace(parentName)
	}
	pathA, pathB := parentPath+".1", parentPath+".2"
	if len([]rune(pathA)) > 255 || len([]rune(pathB)) > 255 {
		return ClientEntryUserPolicyRecord{}, errors.New("二分层级过深")
	}
	parentID := req.GroupID
	groupA, err := insertClientEntryUserPolicySplitGroup(ctx, tx, req.PolicyID, &parentID, pathA, pathA, hostA, clientEntryRuleSortStep, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	groupB, err := insertClientEntryUserPolicySplitGroup(ctx, tx, req.PolicyID, &parentID, pathB, pathB, hostB, 2*clientEntryRuleSortStep, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	half := (userCount + 1) / 2
	result, err := tx.ExecContext(ctx, `WITH ranked AS (
	SELECT user_id, ROW_NUMBER() OVER (ORDER BY user_id ASC) AS position
	FROM v2_client_entry_user_policy_split_assignment
	WHERE policy_id = $1 AND group_id = $2
)
UPDATE v2_client_entry_user_policy_split_assignment assignment
SET group_id = CASE WHEN ranked.position <= $3 THEN $4 ELSE $5 END,
    updated_at = $6
FROM ranked
WHERE assignment.policy_id = $1 AND assignment.user_id = ranked.user_id`, req.PolicyID, req.GroupID, half, groupA, groupB, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != userCount {
		return ClientEntryUserPolicyRecord{}, errors.New("分组用户已变化，请刷新后重试")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group
SET entry_host = '', updated_at = $2
WHERE id = $1`, req.GroupID, now); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET updated_at = $2 WHERE id = $1`, req.PolicyID, now); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("继续二分失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return ClientEntryUserPolicyRecord{ID: req.PolicyID, Mode: ClientEntryUserPolicyModeSplit}, nil
}

func (s *DBService) UpdateClientEntryUserPolicySplitGroupHost(ctx context.Context, req ClientEntryUserPolicyGroupHostUpdateRequest) (ClientEntryUserPolicyRecord, error) {
	if s.db == nil {
		return ClientEntryUserPolicyRecord{}, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryUserPolicyRecord{}, err
	}
	if req.PolicyID <= 0 || req.GroupID <= 0 {
		return ClientEntryUserPolicyRecord{}, errors.New("二分组不存在")
	}
	host, err := cliententry.NormalizeHost(req.EntryHost)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, fmt.Errorf("分组入口地址无效: %w", err)
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("更新分组入口失败")
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group split_group
SET entry_host = $3, updated_at = $4
WHERE split_group.id = $1 AND split_group.policy_id = $2
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
  )
  AND EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy policy WHERE policy.id = split_group.policy_id AND policy.mode = 'split'
  )`, req.GroupID, req.PolicyID, host, now)
	if err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("更新分组入口失败")
	}
	if err := requireClientEntryRuleAffected(result, "二分组"); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("只能修改当前叶子组的入口地址")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET updated_at = $2 WHERE id = $1 AND mode = 'split'`, req.PolicyID, now); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("更新分组入口失败")
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryUserPolicyRecord{}, errors.New("更新分组入口失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return ClientEntryUserPolicyRecord{ID: req.PolicyID, Mode: ClientEntryUserPolicyModeSplit}, nil
}

func (s *DBService) SetClientEntryUserPolicyEnabled(ctx context.Context, id, enabled int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, errors.New("规则不存在")
	}
	if enabled != 0 && enabled != 1 {
		return false, errors.New("规则状态无效")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET enabled = $2, updated_at = $3 WHERE id = $1`, id, enabled, time.Now().Unix())
	if err != nil {
		return false, errors.New("更新规则状态失败")
	}
	if err := requireClientEntryRuleAffected(result, "规则"); err != nil {
		return false, errors.New("规则不存在")
	}
	s.markClientEntryMonitorTargetsDirty()
	return true, nil
}

func (s *DBService) loadClientEntryUserPolicySplitGroups(ctx context.Context, policyIDs []int64) (map[int64][]ClientEntryUserPolicySplitGroupRecord, map[int64]int64, error) {
	groups := make(map[int64][]ClientEntryUserPolicySplitGroupRecord, len(policyIDs))
	counts := make(map[int64]int64, len(policyIDs))
	if len(policyIDs) == 0 {
		return groups, counts, nil
	}
	placeholders := make([]string, len(policyIDs))
	args := make([]any, len(policyIDs))
	for index, id := range policyIDs {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
		args[index] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT split_group.id, split_group.policy_id, split_group.parent_id,
       split_group.name, split_group.path, split_group.entry_host, split_group.sort,
       COUNT(users.id)::BIGINT AS user_count,
       NOT EXISTS (
	       SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
	       ) AS is_leaf,
       split_group.created_at, split_group.updated_at
FROM v2_client_entry_user_policy_split_group split_group
LEFT JOIN v2_client_entry_user_policy_split_assignment assignment
  ON assignment.policy_id = split_group.policy_id AND assignment.group_id = split_group.id
LEFT JOIN v2_user users ON users.id = assignment.user_id
WHERE split_group.policy_id IN (`+strings.Join(placeholders, ",")+`)
GROUP BY split_group.id
ORDER BY split_group.policy_id ASC, split_group.path ASC, split_group.sort ASC, split_group.id ASC`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query client entry split groups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			record   ClientEntryUserPolicySplitGroupRecord
			parentID sql.NullInt64
		)
		if err := rows.Scan(&record.ID, &record.PolicyID, &parentID, &record.Name, &record.Path, &record.EntryHost, &record.Sort, &record.UserCount, &record.IsLeaf, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("scan client entry split group: %w", err)
		}
		if parentID.Valid {
			value := parentID.Int64
			record.ParentID = &value
		}
		record.Name = strings.TrimSpace(record.Name)
		record.Path = strings.TrimSpace(record.Path)
		record.EntryHost = strings.TrimSpace(record.EntryHost)
		groups[record.PolicyID] = append(groups[record.PolicyID], record)
		counts[record.PolicyID] += record.UserCount
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate client entry split groups: %w", err)
	}
	return groups, counts, nil
}

func normalizeClientEntryUserPolicyMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = ClientEntryUserPolicyModeStandard
	}
	switch mode {
	case ClientEntryUserPolicyModeStandard, ClientEntryUserPolicyModeSplit:
		return mode, nil
	default:
		return "", errors.New("规则模式无效")
	}
}

func normalizeClientEntryUserPolicySplitMinutes(value int64) (int64, error) {
	if value == 0 {
		return defaultClientEntryUserPolicySplitMinutes, nil
	}
	if value < 1 || value > maxClientEntryUserPolicySplitMinutes {
		return 0, errors.New("订阅时间范围必须在 1 分钟到 30 天之间")
	}
	return value, nil
}

func normalizeClientEntryUserPolicySplitCreateRequest(req ClientEntryUserPolicySplitCreateRequest) (preparedClientEntryUserPolicySplitCreateRequest, error) {
	result := preparedClientEntryUserPolicySplitCreateRequest{
		Name:    strings.TrimSpace(req.Name),
		Remarks: strings.TrimSpace(req.Remarks),
		Enabled: 1,
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
	minutes, err := normalizeClientEntryUserPolicySplitMinutes(req.Minutes)
	if err != nil {
		return result, err
	}
	result.Minutes = minutes
	result.EntryHostA, result.EntryHostB, err = normalizeClientEntryUserPolicySplitHosts(req.EntryHostA, req.EntryHostB)
	if err != nil {
		return result, err
	}
	if req.ResolveEntryHost != nil {
		if *req.ResolveEntryHost != 0 && *req.ResolveEntryHost != 1 {
			return result, errors.New("解析域名下发 IP 设置无效")
		}
		result.ResolveEntryHost = *req.ResolveEntryHost
	}
	if req.Enabled != nil {
		if *req.Enabled != 0 && *req.Enabled != 1 {
			return result, errors.New("规则状态无效")
		}
		result.Enabled = *req.Enabled
	}
	result.Members, err = normalizePolicyMembers(req.Members)
	if err != nil {
		return result, err
	}
	if len(result.Members) == 0 {
		return result, errors.New("生效节点不能为空")
	}
	return result, nil
}

func normalizeClientEntryUserPolicySplitHosts(valueA, valueB string) (string, string, error) {
	hostA, err := cliententry.NormalizeHost(valueA)
	if err != nil {
		return "", "", fmt.Errorf("A 组入口地址无效: %w", err)
	}
	hostB, err := cliententry.NormalizeHost(valueB)
	if err != nil {
		return "", "", fmt.Errorf("B 组入口地址无效: %w", err)
	}
	if strings.EqualFold(hostA, hostB) {
		return "", "", errors.New("A、B 两组入口地址不能相同")
	}
	return hostA, hostB, nil
}

func insertClientEntryUserPolicySplitGroup(ctx context.Context, tx *sql.Tx, policyID int64, parentID *int64, name, path, entryHost string, sortValue, now int64) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy_split_group
(policy_id, parent_id, name, path, entry_host, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
RETURNING id`, policyID, parentID, name, path, entryHost, sortValue, now).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}
