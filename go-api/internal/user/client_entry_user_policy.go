package user

import (
	"context"
	"fmt"
	"strings"
)

type clientEntryUserPolicy struct {
	ID      int64
	Enabled int64
	Remarks string
	Members []ClientEntryGroupMember
}

func (s *DBService) loadClientEntryUserPolicies(ctx context.Context, email string) ([]clientEntryUserPolicy, error) {
	if strings.TrimSpace(email) == "" {
		return nil, nil
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.queryRowsAsMaps(ctx, `SELECT p.id, p.enabled, p.remarks, m.server_type, m.server_id
FROM v2_client_entry_user_policy p
JOIN v2_client_entry_user_policy_user u ON u.policy_id = p.id
JOIN v2_client_entry_user_policy_member m ON m.policy_id = p.id
WHERE p.enabled = 1 AND lower(u.email) = lower($1)
ORDER BY p.id ASC, m.sort ASC NULLS LAST, m.id ASC`, strings.TrimSpace(email))
	if err != nil {
		return nil, fmt.Errorf("query client entry user policies: %w", err)
	}

	byKey := make(map[string]*clientEntryUserPolicy)
	order := make([]string, 0)
	for _, row := range rows {
		id := mapInt64(row["id"])
		key := fmt.Sprint(id)
		policy := byKey[key]
		if policy == nil {
			policy = &clientEntryUserPolicy{
				ID:      id,
				Enabled: mapInt64(row["enabled"]),
				Remarks: strings.TrimSpace(fmt.Sprint(row["remarks"])),
			}
			byKey[key] = policy
			order = append(order, key)
		}
		member := ClientEntryGroupMember{ServerType: strings.TrimSpace(fmt.Sprint(row["server_type"])), ServerID: mapInt64(row["server_id"])}
		if member.ServerType != "" && member.ServerID > 0 && !clientEntryPolicyHasMember(policy.Members, member) {
			policy.Members = append(policy.Members, member)
		}
	}

	result := make([]clientEntryUserPolicy, 0, len(order))
	for _, key := range order {
		result = append(result, *byKey[key])
	}
	return result, nil
}

func clientEntryPolicyHasMember(values []ClientEntryGroupMember, target ClientEntryGroupMember) bool {
	for _, value := range values {
		if strings.TrimSpace(value.ServerType) == strings.TrimSpace(target.ServerType) && value.ServerID == target.ServerID {
			return true
		}
	}
	return false
}

func applyClientEntryUserPolicies(servers []map[string]any, email string, policies []clientEntryUserPolicy) []map[string]any {
	if len(servers) == 0 || len(policies) == 0 || strings.TrimSpace(email) == "" {
		return servers
	}
	allowed := make(map[string]struct{})
	for _, policy := range policies {
		for _, member := range policy.Members {
			serverType := strings.TrimSpace(member.ServerType)
			if serverType == "" || member.ServerID <= 0 {
				continue
			}
			allowed[serverType+":"+fmt.Sprint(member.ServerID)] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return servers
	}
	result := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		serverType := strings.TrimSpace(fmt.Sprint(server["type"]))
		serverID := mapInt64(server["id"])
		if _, ok := allowed[serverType+":"+fmt.Sprint(serverID)]; !ok {
			continue
		}
		server["client_entry_user_policy"] = 1
		result = append(result, server)
	}
	return result
}

func stableClientEntryIndex(email, serverType string, serverID int64, count int) int {
	if count <= 1 {
		return 0
	}
	sum := int64(0)
	for _, ch := range strings.ToLower(strings.TrimSpace(email)) + ":" + serverType + ":" + fmt.Sprint(serverID) {
		sum += int64(ch)
	}
	if sum < 0 {
		sum = -sum
	}
	return int(sum % int64(count))
}
