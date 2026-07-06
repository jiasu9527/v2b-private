package user

import (
	"context"
	"fmt"
	"strings"
)

type clientEntryUserPolicy struct {
	ID           int64
	EntryGroupID int64
	ServerType   string
	ServerID     int64
	Enabled      int64
	Remarks      string
	Entries      []string
}

func (s *DBService) loadClientEntryUserPolicies(ctx context.Context, email string) ([]clientEntryUserPolicy, error) {
	if strings.TrimSpace(email) == "" {
		return nil, nil
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.queryRowsAsMaps(ctx, `SELECT p.id, p.entry_group_id, p.server_type, p.server_id, p.enabled, p.remarks, e.entry AS ip
FROM v2_client_entry_user_policy p
JOIN v2_client_entry_user_policy_user u ON u.policy_id = p.id
JOIN v2_client_entry_user_policy_entry e ON e.policy_id = p.id
WHERE p.enabled = 1 AND lower(u.email) = lower($1)
ORDER BY p.id ASC, e.sort ASC NULLS LAST, e.id ASC`, strings.TrimSpace(email))
	if err != nil {
		return nil, fmt.Errorf("query client entry user policies: %w", err)
	}

	byKey := make(map[string]*clientEntryUserPolicy)
	order := make([]string, 0)
	for _, row := range rows {
		id := mapInt64(row["id"])
		key := fmt.Sprintf("%d:%s:%d", id, strings.TrimSpace(fmt.Sprint(row["server_type"])), mapInt64(row["server_id"]))
		policy := byKey[key]
		if policy == nil {
			policy = &clientEntryUserPolicy{
				ID:           id,
				EntryGroupID: mapInt64(row["entry_group_id"]),
				ServerType:   strings.TrimSpace(fmt.Sprint(row["server_type"])),
				ServerID:     mapInt64(row["server_id"]),
				Enabled:      mapInt64(row["enabled"]),
				Remarks:      strings.TrimSpace(fmt.Sprint(row["remarks"])),
			}
			byKey[key] = policy
			order = append(order, key)
		}
		entry := strings.TrimSpace(fmt.Sprint(row["ip"]))
		if entry != "" {
			policy.Entries = append(policy.Entries, entry)
		}
	}

	result := make([]clientEntryUserPolicy, 0, len(order))
	for _, key := range order {
		result = append(result, *byKey[key])
	}
	return result, nil
}

func applyClientEntryUserPolicies(servers []map[string]any, email string, policies []clientEntryUserPolicy) {
	if len(servers) == 0 || len(policies) == 0 || strings.TrimSpace(email) == "" {
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	byServer := make(map[string]clientEntryUserPolicy, len(policies))
	for _, policy := range policies {
		if len(policy.Entries) == 0 || policy.ServerID <= 0 {
			continue
		}
		serverType := strings.TrimSpace(policy.ServerType)
		if serverType == "" {
			continue
		}
		key := serverType + ":" + fmt.Sprint(policy.ServerID)
		if _, exists := byServer[key]; !exists {
			byServer[key] = policy
		}
	}
	if len(byServer) == 0 {
		return
	}

	for _, server := range servers {
		serverType := strings.TrimSpace(fmt.Sprint(server["type"]))
		serverID := mapInt64(server["id"])
		policy, ok := byServer[serverType+":"+fmt.Sprint(serverID)]
		if !ok || len(policy.Entries) == 0 {
			continue
		}
		server["host"] = policy.Entries[stableClientEntryIndex(email, serverType, serverID, len(policy.Entries))]
		server["client_entry_user_policy_id"] = policy.ID
		server["client_entry_group_id"] = policy.EntryGroupID
	}
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
