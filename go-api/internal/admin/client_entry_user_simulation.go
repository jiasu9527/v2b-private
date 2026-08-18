package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

type clientEntrySimulationCandidate struct {
	policy      ClientEntryUserPolicyRecord
	visibleSort int64
}

// SimulateClientEntryUserPolicy evaluates the same persisted assignment data
// used by subscriptions. In particular, split policies are resolved from the
// user's assignment row instead of trying to infer a group in the browser.
func (s *DBService) SimulateClientEntryUserPolicy(ctx context.Context, req ClientEntryUserPolicySimulationRequest) (ClientEntryUserPolicySimulationResult, error) {
	if s.db == nil {
		return ClientEntryUserPolicySimulationResult{}, ErrUnavailable
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return ClientEntryUserPolicySimulationResult{}, errors.New("用户邮箱不能为空")
	}
	if (req.MemberType == "") != (req.MemberID == 0) || req.MemberID < 0 {
		return ClientEntryUserPolicySimulationResult{}, errors.New("生效节点无效")
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryUserPolicySimulationResult{}, err
	}

	var user ClientEntryUserPolicySimulationUser
	var planID, createdAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, plan_id, created_at
FROM v2_user
WHERE LOWER(email) = LOWER($1)
LIMIT 1`, email).Scan(&user.ID, &user.Email, &planID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientEntryUserPolicySimulationResult{}, nil
	}
	if err != nil {
		return ClientEntryUserPolicySimulationResult{}, fmt.Errorf("查询模拟用户失败: %w", err)
	}
	if planID.Valid {
		user.PlanID = planID.Int64
	}
	user.RegistrationDays = clientEntrySimulationRegistrationDays(createdAt, time.Now().Unix())
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))

	policies, err := s.ListClientEntryUserPolicies(ctx)
	if err != nil {
		return ClientEntryUserPolicySimulationResult{}, err
	}
	assignments := make(map[int64]int64)
	hasSplitPolicy := false
	for _, policy := range policies {
		if policy.Mode == ClientEntryUserPolicyModeSplit {
			hasSplitPolicy = true
			break
		}
	}
	if hasSplitPolicy {
		rows, queryErr := s.db.QueryContext(ctx, `SELECT policy_id, group_id
FROM v2_client_entry_user_policy_split_assignment
WHERE user_id = $1`, user.ID)
		if queryErr != nil {
			return ClientEntryUserPolicySimulationResult{}, fmt.Errorf("查询固定分组失败: %w", queryErr)
		}
		for rows.Next() {
			var policyID, groupID int64
			if scanErr := rows.Scan(&policyID, &groupID); scanErr != nil {
				rows.Close()
				return ClientEntryUserPolicySimulationResult{}, fmt.Errorf("读取固定分组失败: %w", scanErr)
			}
			assignments[policyID] = groupID
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			return ClientEntryUserPolicySimulationResult{}, fmt.Errorf("遍历固定分组失败: %w", rowsErr)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return ClientEntryUserPolicySimulationResult{}, fmt.Errorf("关闭固定分组查询失败: %w", closeErr)
		}
	}

	subject := cliententry.Subject{
		UserID:           user.ID,
		Email:            user.Email,
		RegistrationDays: user.RegistrationDays,
		PlanID:           user.PlanID,
		UA:               req.UA,
	}
	matched := selectClientEntryUserPolicySimulation(policies, assignments, subject, req.MemberType, req.MemberID)
	result := ClientEntryUserPolicySimulationResult{Found: true, User: &user}
	if matched != nil {
		result.Matched = matched
	}
	return result, nil
}

// MatchClientEntryUserPolicies evaluates a page of existing users together.
// Policies and fixed split assignments are loaded once, so an administrator
// can inspect a search result without issuing a full simulation query set for
// every individual row.
func (s *DBService) MatchClientEntryUserPolicies(ctx context.Context, requests []ClientEntryUserPolicyMatchRequest) ([]ClientEntryUserPolicyMatchResult, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if len(requests) == 0 {
		return []ClientEntryUserPolicyMatchResult{}, nil
	}
	if len(requests) > 100 {
		return nil, errors.New("批量匹配用户数量不能超过 100")
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	userIDs := make([]int64, 0, len(requests))
	seenUserIDs := make(map[int64]struct{}, len(requests))
	for _, request := range requests {
		if request.UserID <= 0 {
			return nil, errors.New("用户不存在")
		}
		if _, exists := seenUserIDs[request.UserID]; exists {
			continue
		}
		seenUserIDs[request.UserID] = struct{}{}
		userIDs = append(userIDs, request.UserID)
	}

	placeholders := make([]string, len(userIDs))
	args := make([]any, len(userIDs))
	for index, userID := range userIDs {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
		args[index] = userID
	}
	type currentUser struct {
		ID        int64
		Email     string
		PlanID    sql.NullInt64
		CreatedAt sql.NullInt64
	}
	users := make(map[int64]currentUser, len(userIDs))
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, plan_id, created_at
FROM v2_user
WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询批量匹配用户失败: %w", err)
	}
	for rows.Next() {
		var user currentUser
		if err := rows.Scan(&user.ID, &user.Email, &user.PlanID, &user.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取批量匹配用户失败: %w", err)
		}
		user.Email = strings.ToLower(strings.TrimSpace(user.Email))
		users[user.ID] = user
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历批量匹配用户失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("关闭批量匹配用户查询失败: %w", err)
	}

	policies, err := s.ListClientEntryUserPolicies(ctx)
	if err != nil {
		return nil, err
	}
	assignments := make(map[int64]map[int64]int64, len(users))
	if len(users) > 0 && clientEntrySimulationHasSplitPolicy(policies) {
		rows, err := s.db.QueryContext(ctx, `SELECT user_id, policy_id, group_id
FROM v2_client_entry_user_policy_split_assignment
WHERE user_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("查询批量固定分组失败: %w", err)
		}
		for rows.Next() {
			var userID, policyID, groupID int64
			if err := rows.Scan(&userID, &policyID, &groupID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("读取批量固定分组失败: %w", err)
			}
			if assignments[userID] == nil {
				assignments[userID] = make(map[int64]int64)
			}
			assignments[userID][policyID] = groupID
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("遍历批量固定分组失败: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("关闭批量固定分组查询失败: %w", err)
		}
	}

	now := time.Now().Unix()
	results := make([]ClientEntryUserPolicyMatchResult, 0, len(requests))
	for _, request := range requests {
		result := ClientEntryUserPolicyMatchResult{UserID: request.UserID}
		user, found := users[request.UserID]
		if !found {
			results = append(results, result)
			continue
		}
		result.Found = true
		subject := cliententry.Subject{
			UserID:           user.ID,
			Email:            user.Email,
			PlanID:           user.PlanID.Int64,
			RegistrationDays: clientEntrySimulationRegistrationDays(user.CreatedAt, now),
			UA:               request.UA,
		}
		result.Matched = selectClientEntryUserPolicySimulation(policies, assignments[user.ID], subject, "", 0)
		results = append(results, result)
	}
	return results, nil
}

func clientEntrySimulationRegistrationDays(createdAt sql.NullInt64, now int64) int64 {
	if !createdAt.Valid || createdAt.Int64 <= 0 || now < createdAt.Int64 {
		return -1
	}
	return (now - createdAt.Int64) / 86400
}

func clientEntrySimulationHasSplitPolicy(policies []ClientEntryUserPolicyRecord) bool {
	for _, policy := range policies {
		if policy.Mode == ClientEntryUserPolicyModeSplit {
			return true
		}
	}
	return false
}

func selectClientEntryUserPolicySimulation(policies []ClientEntryUserPolicyRecord, assignments map[int64]int64, subject cliententry.Subject, memberType string, memberID int64) *ClientEntryUserPolicyRecord {
	candidates := make([]clientEntrySimulationCandidate, 0, len(policies))
	for _, policy := range policies {
		if policy.Enabled == 0 || !clientEntrySimulationHasMember(policy.Members, memberType, memberID) {
			continue
		}
		candidate := policy
		visibleSort := policy.Sort
		if policy.Mode == ClientEntryUserPolicyModeSplit {
			groupID, assigned := assignments[policy.ID]
			if !assigned {
				continue
			}
			var group *ClientEntryUserPolicySplitGroupRecord
			for index := range policy.SplitGroups {
				item := &policy.SplitGroups[index]
				if item.ID == groupID && item.IsLeaf {
					group = item
					break
				}
			}
			if group == nil {
				continue
			}
			candidate.Name = strings.TrimSpace(group.Name)
			candidate.EntryHost = strings.TrimSpace(group.EntryHost)
			candidate.SnapshotUserCount = group.UserCount
			candidate.SplitGroups = nil
			if group.GlobalSort != nil {
				visibleSort = *group.GlobalSort
			}
		} else if !cliententry.MatchAll(policy.Conditions, subject) {
			continue
		}
		candidates = append(candidates, clientEntrySimulationCandidate{policy: candidate, visibleSort: visibleSort})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].visibleSort != candidates[right].visibleSort {
			return candidates[left].visibleSort < candidates[right].visibleSort
		}
		return candidates[left].policy.ID < candidates[right].policy.ID
	})
	return &candidates[0].policy
}

func clientEntrySimulationHasMember(members []ClientEntryGroupMemberRecord, memberType string, memberID int64) bool {
	if memberType == "" {
		return len(members) > 0
	}
	for _, member := range members {
		if strings.EqualFold(strings.TrimSpace(member.ServerType), memberType) && member.ServerID == memberID {
			return true
		}
	}
	return false
}
