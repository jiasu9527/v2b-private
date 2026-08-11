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
	if createdAt.Valid {
		now := time.Now().Unix()
		if createdAt.Int64 > 0 && now >= createdAt.Int64 {
			user.RegistrationDays = (now - createdAt.Int64) / 86400
		} else {
			user.RegistrationDays = -1
		}
	} else {
		user.RegistrationDays = -1
	}
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
