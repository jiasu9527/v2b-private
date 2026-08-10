package user

import (
	"context"
	"fmt"
	"math"
	"strings"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/subscribelink"
)

type clientEntryUserPolicy struct {
	ID                 int64
	Mode               string
	Action             string
	EntryHost          string
	AssignedEntryHost  string
	ResolveEntryHost   bool
	ExtraNodes         []string
	ExtraNodesPosition string
	Conditions         []cliententry.Condition
	Members            []ClientEntryGroupMember
}

func (s *DBService) loadClientEntryUserPolicies(ctx context.Context, userID int64) ([]clientEntryUserPolicy, error) {
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.queryRowsAsMaps(ctx, `SELECT p.id, p.mode, p.action, p.conditions, p.entry_host,
p.resolve_entry_host, p.extra_nodes, p.extra_nodes_position,
COALESCE(split_group.entry_host, '') AS assigned_entry_host,
m.server_type, m.server_id, m.sort AS member_sort
FROM v2_client_entry_user_policy p
JOIN v2_client_entry_user_policy_member m ON m.policy_id = p.id
LEFT JOIN v2_client_entry_user_policy_split_assignment split_assignment
  ON split_assignment.policy_id = p.id AND split_assignment.user_id = $1
LEFT JOIN v2_client_entry_user_policy_split_group split_group
  ON split_group.id = split_assignment.group_id AND split_group.policy_id = p.id
WHERE p.enabled = 1
  AND (p.mode <> 'split' OR split_group.id IS NOT NULL)
ORDER BY CASE WHEN p.mode = 'split' THEN COALESCE(split_group.global_sort, p.sort) ELSE p.sort END ASC NULLS LAST,
         p.id ASC, m.sort ASC NULLS LAST, m.id ASC`, userID)
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
			mode := strings.ToLower(strings.TrimSpace(fmt.Sprint(row["mode"])))
			if mode == "" {
				mode = "standard"
			}
			if mode != "standard" && mode != "split" {
				return nil, fmt.Errorf("decode client entry rule %d mode: unsupported mode %q", id, mode)
			}
			action, err := cliententry.NormalizeAction(fmt.Sprint(row["action"]))
			if err != nil {
				return nil, fmt.Errorf("decode client entry rule %d action: %w", id, err)
			}
			conditions, err := cliententry.DecodeConditions(fmt.Sprint(row["conditions"]))
			if err != nil {
				return nil, fmt.Errorf("decode client entry rule %d conditions: %w", id, err)
			}
			extraNodes, err := subscribelink.DecodeList(fmt.Sprint(row["extra_nodes"]))
			if err != nil {
				return nil, fmt.Errorf("decode client entry rule %d extra nodes: %w", id, err)
			}
			extraNodesPosition, err := subscribelink.NormalizePosition(fmt.Sprint(row["extra_nodes_position"]))
			if err != nil {
				return nil, fmt.Errorf("decode client entry rule %d extra node position: %w", id, err)
			}
			policy = &clientEntryUserPolicy{
				ID:                 id,
				Mode:               mode,
				Action:             action,
				EntryHost:          strings.TrimSpace(fmt.Sprint(row["entry_host"])),
				AssignedEntryHost:  strings.TrimSpace(fmt.Sprint(row["assigned_entry_host"])),
				ResolveEntryHost:   mapInt64(row["resolve_entry_host"]) != 0,
				ExtraNodes:         extraNodes,
				ExtraNodesPosition: extraNodesPosition,
				Conditions:         conditions,
				Members:            []ClientEntryGroupMember{},
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

func applyClientEntryUserPolicies(servers []map[string]any, subject cliententry.Subject, policies []clientEntryUserPolicy) []map[string]any {
	return applyClientEntryUserPoliciesWithResolver(context.Background(), servers, subject, policies, nil)
}

func applyClientEntryUserPoliciesWithResolver(ctx context.Context, servers []map[string]any, subject cliententry.Subject, policies []clientEntryUserPolicy, resolver *clientEntryHostResolver) []map[string]any {
	result := make([]map[string]any, 0, len(servers))
	matchedPolicyIDs := make(map[int64]struct{})
	for _, server := range servers {
		serverType := strings.ToLower(strings.TrimSpace(fmt.Sprint(server["type"])))
		serverID := mapInt64(server["id"])
		entryOnly := mapInt64(server["client_entry_only"]) != 0
		delete(server, "client_entry_only")
		hide := false
		granted := !entryOnly
		for _, policy := range policies {
			if !clientEntryPolicyHasMember(policy.Members, ClientEntryGroupMember{ServerType: serverType, ServerID: serverID}) {
				continue
			}
			matched := cliententry.MatchAll(policy.Conditions, subject)
			if policy.Mode == "split" {
				matched = policy.AssignedEntryHost != ""
			}
			if !matched {
				continue
			}
			if policy.Action == cliententry.ActionHide {
				hide = true
				break
			}
			matchedPolicyIDs[policy.ID] = struct{}{}
			granted = true
			if policy.Action == cliententry.ActionOverride {
				entryHost := policy.EntryHost
				if policy.Mode == "split" {
					entryHost = policy.AssignedEntryHost
				}
				if policy.ResolveEntryHost && resolver != nil {
					selectionKey := fmt.Sprintf("%d:%d", subject.UserID, policy.ID)
					entryHost = resolver.Resolve(ctx, entryHost, selectionKey)
				}
				server["host"] = entryHost
			}
			server["client_entry_user_policy"] = 1
			server["client_entry_user_policy_id"] = policy.ID
			break
		}
		if hide || !granted {
			continue
		}
		result = append(result, server)
	}
	for _, policy := range policies {
		if len(policy.ExtraNodes) == 0 {
			continue
		}
		if _, matched := matchedPolicyIDs[policy.ID]; !matched {
			continue
		}
		extraServers := make([]map[string]any, 0, len(policy.ExtraNodes))
		for index, raw := range policy.ExtraNodes {
			extra, err := subscribelink.Parse(raw)
			if err != nil {
				continue
			}
			extra["id"] = -((policy.ID * 1000) + int64(index+1))
			extra["client_entry_user_policy"] = 1
			extra["client_entry_user_policy_id"] = policy.ID
			extraServers = append(extraServers, extra)
		}
		if policy.ExtraNodesPosition == subscribelink.PositionBefore {
			sortStart := clientEntryExtraNodeSortStart(result, subscribelink.PositionBefore, len(extraServers))
			for index, extra := range extraServers {
				extra["sort"] = sortStart + int64(index)
			}
			result = append(extraServers, result...)
		} else {
			sortStart := clientEntryExtraNodeSortStart(result, subscribelink.PositionAfter, len(extraServers))
			for index, extra := range extraServers {
				extra["sort"] = sortStart + int64(index)
			}
			result = append(result, extraServers...)
		}
		break
	}
	return result
}

func clientEntryExtraNodeSortStart(servers []map[string]any, position string, count int) int64 {
	if count <= 0 {
		return 0
	}

	var bound int64
	found := false
	for _, server := range servers {
		value, ok := serverSortValue(server["sort"])
		if !ok {
			continue
		}
		if !found {
			bound = value
			found = true
			continue
		}
		if position == subscribelink.PositionBefore && value < bound {
			bound = value
		}
		if position != subscribelink.PositionBefore && value > bound {
			bound = value
		}
	}

	delta := int64(count)
	if position == subscribelink.PositionBefore {
		if !found {
			return -delta
		}
		if bound < math.MinInt64+delta {
			return math.MinInt64
		}
		return bound - delta
	}
	if !found {
		return 1
	}
	if bound > math.MaxInt64-delta {
		return math.MaxInt64 - delta + 1
	}
	return bound + 1
}
