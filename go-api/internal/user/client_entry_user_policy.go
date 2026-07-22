package user

import (
	"context"
	"fmt"
	"strings"

	"forest/go-api/internal/cliententry"
)

// clientEntryUserPolicy is a rule evaluated independently for every visible
// node.  Its list order is the matching order; there is deliberately no
// numeric "priority" exposed to users.
type clientEntryUserPolicy struct {
	ID         int64
	Action     string
	EntryHost  string
	Conditions []cliententry.Condition
	Members    []ClientEntryGroupMember
}

func (s *DBService) loadClientEntryUserPolicies(ctx context.Context) ([]clientEntryUserPolicy, error) {
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.queryRowsAsMaps(ctx, `SELECT p.id, p.action, p.conditions, p.entry_host, m.server_type, m.server_id, m.sort AS member_sort
FROM v2_client_entry_user_policy p
JOIN v2_client_entry_user_policy_member m ON m.policy_id = p.id
WHERE p.enabled = 1
ORDER BY p.sort ASC NULLS LAST, p.id ASC, m.sort ASC NULLS LAST, m.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query client entry rules: %w", err)
	}

	byID := make(map[int64]*clientEntryUserPolicy)
	order := make([]int64, 0)
	for _, row := range rows {
		id := mapInt64(row["id"])
		if id <= 0 {
			continue
		}
		policy := byID[id]
		if policy == nil {
			action, err := cliententry.NormalizeAction(fmt.Sprint(row["action"]))
			if err != nil {
				return nil, fmt.Errorf("decode client entry rule %d action: %w", id, err)
			}
			conditions, err := cliententry.DecodeConditions(fmt.Sprint(row["conditions"]))
			if err != nil {
				return nil, fmt.Errorf("decode client entry rule %d conditions: %w", id, err)
			}
			policy = &clientEntryUserPolicy{
				ID:         id,
				Action:     action,
				EntryHost:  strings.TrimSpace(fmt.Sprint(row["entry_host"])),
				Conditions: conditions,
				Members:    []ClientEntryGroupMember{},
			}
			byID[id] = policy
			order = append(order, id)
		}
		member := ClientEntryGroupMember{
			ServerType: strings.ToLower(strings.TrimSpace(fmt.Sprint(row["server_type"]))),
			ServerID:   mapInt64(row["server_id"]),
		}
		if member.ServerType != "" && member.ServerID > 0 && !clientEntryPolicyHasMember(policy.Members, member) {
			policy.Members = append(policy.Members, member)
		}
	}

	result := make([]clientEntryUserPolicy, 0, len(order))
	for _, id := range order {
		policy := byID[id]
		if policy != nil && len(policy.Members) > 0 {
			result = append(result, *policy)
		}
	}
	return result, nil
}

func clientEntryPolicyHasMember(values []ClientEntryGroupMember, target ClientEntryGroupMember) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value.ServerType), strings.TrimSpace(target.ServerType)) && value.ServerID == target.ServerID {
			return true
		}
	}
	return false
}

// applyClientEntryUserPolicies preserves the permission-filtered server set,
// then resolves each remaining node against the first matching rule.  A hide
// rule removes only the selected node; a rule never grants access to a node
// that was filtered by its group_id earlier in Servers.
func applyClientEntryUserPolicies(servers []map[string]any, subject cliententry.Subject, policies []clientEntryUserPolicy) []map[string]any {
	if len(servers) == 0 || len(policies) == 0 {
		return servers
	}
	result := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		serverType := strings.ToLower(strings.TrimSpace(fmt.Sprint(server["type"])))
		serverID := mapInt64(server["id"])
		hide := false
		for _, policy := range policies {
			if !clientEntryPolicyHasMember(policy.Members, ClientEntryGroupMember{ServerType: serverType, ServerID: serverID}) {
				continue
			}
			if !cliententry.MatchAll(policy.Conditions, subject) {
				continue
			}
			if policy.Action == cliententry.ActionHide {
				hide = true
				break
			}
			server["host"] = policy.EntryHost
			server["client_entry_user_policy"] = 1
			server["client_entry_user_policy_id"] = policy.ID
			break
		}
		if hide {
			continue
		}
		result = append(result, server)
	}
	return result
}
