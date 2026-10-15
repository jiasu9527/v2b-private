package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/subscribelink"
)

// SupportEntryQuerier permits preview/validation within the mutation transaction.
// These helpers never ensure schemas or write subscription activity records.
type SupportEntryQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type SupportEntryTarget struct {
	Key              string                   `json:"key"`
	Name             string                   `json:"name"`
	PolicyID         int64                    `json:"policy_id"`
	GroupID          int64                    `json:"group_id,omitempty"`
	EntryHost        string                   `json:"entry_host"`
	ResolveEntryHost bool                     `json:"resolve_entry_host"`
	Members          []ClientEntryGroupMember `json:"-"`
}
type SupportEntryOverride struct {
	SourcePolicyID int64  `json:"source_policy_id"`
	TargetPolicyID int64  `json:"target_policy_id"`
	TargetGroupID  int64  `json:"target_group_id,omitempty"`
	TargetKey      string `json:"target_key"`
	TargetName     string `json:"target_name"`
	EntryHost      string `json:"entry_host"`
	Active         bool   `json:"active"`
	Status         string `json:"status"`
	ExpiresAt      int64  `json:"expires_at"`
	Version        int64  `json:"version"`
}
type SupportEntryNode struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type SupportEntryScope struct {
	SourcePolicyID     int64                    `json:"source_policy_id"`
	Name               string                   `json:"name"`
	AutomaticEntryHost string                   `json:"automatic_entry_host"`
	Version            int64                    `json:"version"`
	Override           *SupportEntryOverride    `json:"override"`
	Targets            []SupportEntryTarget     `json:"targets"`
	Nodes              []SupportEntryNode       `json:"nodes"`
	Members            []ClientEntryGroupMember `json:"-"`
	Applicable         bool                     `json:"applicable"`
}
type SupportEntryUser struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	PlanName       string `json:"plan_name"`
	ExpiredAt      *int64 `json:"expired_at"`
	RemainingBytes int64  `json:"remaining_bytes"`
	Banned         bool   `json:"banned"`
}
type SupportEntryPreview struct {
	User   SupportEntryUser    `json:"user"`
	Scopes []SupportEntryScope `json:"scopes"`
	UA     string              `json:"ua"`
}

// References follow administrator edits. Split parent groups are not destinations.
func LoadSupportEntryTargets(ctx context.Context, q SupportEntryQuerier) ([]SupportEntryTarget, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.name,COALESCE(g.id,0),COALESCE(g.name,''),
 CASE WHEN p.mode = 'split' THEN g.entry_host ELSE p.entry_host END,
 COALESCE(g.resolve_entry_host,p.resolve_entry_host),m.server_type,m.server_id
FROM v2_client_entry_user_policy p
LEFT JOIN v2_client_entry_user_policy_split_group g ON p.mode = 'split' AND g.policy_id = p.id
 AND NOT EXISTS (SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = g.id)
JOIN v2_client_entry_user_policy_member m ON m.policy_id = p.id
WHERE p.enabled = 1 AND p.action = 'override'
 AND (p.mode = 'standard' OR (p.mode = 'split' AND g.id IS NOT NULL))
 AND TRIM(CASE WHEN p.mode = 'split' THEN g.entry_host ELSE p.entry_host END) <> ''
ORDER BY p.sort,p.id,g.sort,g.id,m.sort,m.id`)
	if err != nil {
		return nil, fmt.Errorf("load support entry targets: %w", err)
	}
	defer rows.Close()
	result := []SupportEntryTarget{}
	indices := map[string]int{}
	for rows.Next() {
		var target SupportEntryTarget
		var groupName, serverType string
		var resolve, serverID int64
		if err := rows.Scan(&target.PolicyID, &target.Name, &target.GroupID, &groupName, &target.EntryHost, &resolve, &serverType, &serverID); err != nil {
			return nil, err
		}
		target.Key = fmt.Sprintf("policy:%d", target.PolicyID)
		if target.GroupID > 0 {
			target.Key += fmt.Sprintf(":split-group:%d", target.GroupID)
			target.Name += " / " + groupName
		}
		target.ResolveEntryHost = resolve != 0
		host, err := cliententry.NormalizeHost(target.EntryHost)
		if err != nil {
			continue
		}
		target.EntryHost = host
		index, ok := indices[target.Key]
		if !ok {
			index = len(result)
			indices[target.Key] = index
			result = append(result, target)
		}
		member := ClientEntryGroupMember{ServerType: strings.ToLower(strings.TrimSpace(serverType)), ServerID: serverID}
		if member.ServerID > 0 && member.ServerType != "" && !clientEntryPolicyHasMember(result[index].Members, member) {
			result[index].Members = append(result[index].Members, member)
		}
	}
	return result, rows.Err()
}

// Equal full member sets are required, not merely intersecting visible nodes.
func SupportEntryCompatible(left, right []ClientEntryGroupMember) bool {
	keys := func(values []ClientEntryGroupMember) map[string]bool {
		result := map[string]bool{}
		for _, m := range values {
			if m.ServerID > 0 && strings.TrimSpace(m.ServerType) != "" {
				result[fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(m.ServerType)), m.ServerID)] = true
			}
		}
		return result
	}
	a, b := keys(left), keys(right)
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}
func LoadSupportEntryOverrides(ctx context.Context, q SupportEntryQuerier, userID int64) ([]SupportEntryOverride, error) {
	rows, err := q.QueryContext(ctx, `SELECT source_policy_id,COALESCE(target_policy_id,0),COALESCE(target_group_id,0),target_key,active,COALESCE(expires_at,0),version
FROM v2_support_entry_override WHERE user_id = $1 ORDER BY source_policy_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("load support entry overrides: %w", err)
	}
	defer rows.Close()
	result := []SupportEntryOverride{}
	for rows.Next() {
		var record SupportEntryOverride
		var active int64
		if err := rows.Scan(&record.SourcePolicyID, &record.TargetPolicyID, &record.TargetGroupID, &record.TargetKey, &active, &record.ExpiresAt, &record.Version); err != nil {
			return nil, err
		}
		record.Active = active != 0
		result = append(result, record)
	}
	return result, rows.Err()
}
func supportEntryOverrideTarget(record SupportEntryOverride, members []ClientEntryGroupMember, targets []SupportEntryTarget, now int64) (SupportEntryTarget, string) {
	if !record.Active {
		return SupportEntryTarget{}, "restored"
	}
	if record.ExpiresAt > 0 && record.ExpiresAt <= now {
		return SupportEntryTarget{}, "expired"
	}
	for _, target := range targets {
		if target.Key != record.TargetKey || target.PolicyID != record.TargetPolicyID || target.GroupID != record.TargetGroupID {
			continue
		}
		if !SupportEntryCompatible(members, target.Members) {
			return SupportEntryTarget{}, "incompatible"
		}
		return target, "active"
	}
	return SupportEntryTarget{}, "target_unavailable"
}
func applySupportEntryOverrides(ctx context.Context, servers []map[string]any, policies []clientEntryUserPolicy, records []SupportEntryOverride, targets []SupportEntryTarget, userID, now int64, resolver *clientEntryHostResolver) []map[string]any {
	byPolicy := map[int64]clientEntryUserPolicy{}
	for _, p := range policies {
		byPolicy[p.ID] = p
	}
	selected := map[int64]SupportEntryTarget{}
	for _, record := range records {
		source, ok := byPolicy[record.SourcePolicyID]
		if !ok || source.Action == cliententry.ActionHide {
			continue
		}
		if target, status := supportEntryOverrideTarget(record, source.Members, targets, now); status == "active" {
			selected[record.SourcePolicyID] = target
		}
	}
	for _, server := range servers {
		if subscribelink.IsExtra(server) || mapInt64(server["id"]) <= 0 {
			continue
		}
		policyID := mapInt64(server["client_entry_user_policy_id"])
		target, ok := selected[policyID]
		if !ok {
			continue
		}
		host := target.EntryHost
		if target.ResolveEntryHost && resolver != nil {
			host = resolver.Resolve(ctx, host, fmt.Sprintf("%d:%d", userID, policyID))
		}
		server["host"] = host
	}
	return servers
}
func (s *DBService) applySupportEntries(ctx context.Context, servers []map[string]any, policies []clientEntryUserPolicy, userID, now int64) ([]map[string]any, error) {
	if !s.supportEntryEnabled {
		return servers, nil
	}
	records, err := LoadSupportEntryOverrides(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	active := false
	for _, record := range records {
		if record.Active && (record.ExpiresAt == 0 || record.ExpiresAt > now) {
			active = true
			break
		}
	}
	if !active {
		return servers, nil
	}
	targets, err := LoadSupportEntryTargets(ctx, s.db)
	if err != nil {
		return nil, err
	}
	return applySupportEntryOverrides(ctx, servers, policies, records, targets, userID, now, s.clientEntryHostResolver), nil
}

// Preview reuses the subscription first-match/hide evaluator, without DNS,
// random ports, cached subscription responses or activity tracking.
func PreviewSupportEntries(ctx context.Context, q SupportEntryQuerier, userID int64, ua string, now int64) (SupportEntryPreview, error) {
	result := SupportEntryPreview{Scopes: []SupportEntryScope{}, UA: ua}
	var groupID, planID, banned, transfer, uploaded, downloaded, createdAt int64
	var expiry sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT u.id,u.email,COALESCE(p.name,''),u.expired_at,COALESCE(u.group_id,0),COALESCE(u.plan_id,0),COALESCE(u.banned,0),COALESCE(u.transfer_enable,0),COALESCE(u.u,0),COALESCE(u.d,0),COALESCE(u.created_at,0)
FROM v2_user u LEFT JOIN v2_plan p ON p.id = u.plan_id WHERE u.id = $1`, userID).Scan(&result.User.ID, &result.User.Email, &result.User.PlanName, &expiry, &groupID, &planID, &banned, &transfer, &uploaded, &downloaded, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	result.User.Banned = banned != 0
	result.User.RemainingBytes = transfer - uploaded - downloaded
	if result.User.RemainingBytes < 0 {
		result.User.RemainingBytes = 0
	}
	expiry = normalizeNullableExpiry(expiry)
	if expiry.Valid {
		value := expiry.Int64
		result.User.ExpiredAt = &value
	}
	policies, names, err := loadSupportPreviewPolicies(ctx, q, userID)
	if err != nil {
		return result, err
	}
	targets, err := LoadSupportEntryTargets(ctx, q)
	if err != nil {
		return result, err
	}
	records, err := LoadSupportEntryOverrides(ctx, q, userID)
	if err != nil {
		return result, err
	}
	servers := []map[string]any{}
	if knowledgeUserAvailable(banned, transfer, expiry, now) {
		for _, table := range serverFetchTables {
			rows, err := q.QueryContext(ctx, `SELECT id,COALESCE(name,''),COALESCE(group_id,'[]'),COALESCE(client_entry_only,0),COALESCE(host,'') FROM `+table.table+` WHERE "show" = 1 ORDER BY sort ASC NULLS LAST,id ASC`)
			if err != nil {
				return result, err
			}
			for rows.Next() {
				var id, entryOnly int64
				var name, groups, host string
				if err := rows.Scan(&id, &name, &groups, &entryOnly, &host); err != nil {
					rows.Close()
					return result, err
				}
				if serverGroupAllowed(parseIDString(groups), groupID) {
					servers = append(servers, map[string]any{"id": id, "type": table.serverType, "name": name, "host": host, "client_entry_only": entryOnly})
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return result, err
			}
		}
	}
	servers = applyClientEntryUserPoliciesWithResolver(ctx, servers, cliententry.Subject{UserID: userID, Email: result.User.Email, PlanID: planID, RegistrationDays: serverFetchRegistrationDays(createdAt, now), UA: ua}, policies, nil)
	byPolicy := map[int64]clientEntryUserPolicy{}
	for _, p := range policies {
		byPolicy[p.ID] = p
	}
	indices := map[int64]int{}
	for _, server := range servers {
		policyID := mapInt64(server["client_entry_user_policy_id"])
		if policyID <= 0 || mapInt64(server["id"]) <= 0 || subscribelink.IsExtra(server) {
			continue
		}
		policy := byPolicy[policyID]
		index, ok := indices[policyID]
		if !ok {
			host := policy.EntryHost
			if policy.Mode == "split" {
				host = policy.AssignedEntryHost
			}
			if policy.Action == cliententry.ActionOriginal {
				host = "原节点地址"
			}
			index = len(result.Scopes)
			indices[policyID] = index
			result.Scopes = append(result.Scopes, SupportEntryScope{SourcePolicyID: policyID, Name: names[policyID], AutomaticEntryHost: host, Members: policy.Members, Targets: []SupportEntryTarget{}, Nodes: []SupportEntryNode{}, Applicable: true})
		}
		result.Scopes[index].Nodes = append(result.Scopes[index].Nodes, SupportEntryNode{ID: mapInt64(server["id"]), Type: fmt.Sprint(server["type"]), Name: fmt.Sprint(server["name"])})
	}
	for _, record := range records {
		index, ok := indices[record.SourcePolicyID]
		if !ok {
			index = len(result.Scopes)
			indices[record.SourcePolicyID] = index
			name := names[record.SourcePolicyID]
			if name == "" {
				name = fmt.Sprintf("已失效规则 #%d", record.SourcePolicyID)
			}
			result.Scopes = append(result.Scopes, SupportEntryScope{SourcePolicyID: record.SourcePolicyID, Name: name, Targets: []SupportEntryTarget{}, Nodes: []SupportEntryNode{}})
		}
		scope := &result.Scopes[index]
		scope.Version = record.Version
		target, status := supportEntryOverrideTarget(record, scope.Members, targets, now)
		record.Status = status
		if record.Active && (record.ExpiresAt == 0 || record.ExpiresAt > now) && !scope.Applicable {
			record.Status = "source_not_applicable"
		}
		if target.Key != "" {
			record.TargetName = target.Name
			record.EntryHost = target.EntryHost
		}
		scope.Override = &record
	}
	for index := range result.Scopes {
		scope := &result.Scopes[index]
		if !scope.Applicable {
			continue
		}
		for _, target := range targets {
			if SupportEntryCompatible(scope.Members, target.Members) {
				scope.Targets = append(scope.Targets, target)
			}
		}
	}
	sort.SliceStable(result.Scopes, func(i, j int) bool { return result.Scopes[i].SourcePolicyID < result.Scopes[j].SourcePolicyID })
	return result, nil
}
func loadSupportPreviewPolicies(ctx context.Context, q SupportEntryQuerier, userID int64) ([]clientEntryUserPolicy, map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.name,p.mode,p.action,p.conditions,p.entry_host,
 COALESCE(g.entry_host,''),COALESCE(g.resolve_entry_host,p.resolve_entry_host),m.server_type,m.server_id
FROM v2_client_entry_user_policy p
JOIN v2_client_entry_user_policy_member m ON m.policy_id = p.id
LEFT JOIN v2_client_entry_user_policy_split_assignment a ON a.policy_id = p.id AND a.user_id = $1
LEFT JOIN v2_client_entry_user_policy_split_group g ON g.id = a.group_id AND g.policy_id = p.id
WHERE p.enabled = 1 AND (p.mode <> 'split' OR g.id IS NOT NULL)
ORDER BY CASE WHEN p.mode = 'split' THEN COALESCE(g.global_sort,p.sort) ELSE p.sort END ASC NULLS LAST,p.id,m.sort ASC NULLS LAST,m.id`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	result := []clientEntryUserPolicy{}
	names := map[int64]string{}
	indices := map[int64]int{}
	for rows.Next() {
		var p clientEntryUserPolicy
		var name, raw, serverType string
		var resolve, serverID int64
		if err := rows.Scan(&p.ID, &name, &p.Mode, &p.Action, &raw, &p.EntryHost, &p.AssignedEntryHost, &resolve, &serverType, &serverID); err != nil {
			return nil, nil, err
		}
		index, ok := indices[p.ID]
		if !ok {
			var err error
			p.Action, err = cliententry.NormalizeAction(p.Action)
			if err != nil {
				return nil, nil, err
			}
			p.Conditions, err = cliententry.DecodeConditions(raw)
			if err != nil {
				return nil, nil, err
			}
			p.ResolveEntryHost = resolve != 0
			index = len(result)
			indices[p.ID] = index
			names[p.ID] = name
			result = append(result, p)
		}
		member := ClientEntryGroupMember{ServerType: strings.ToLower(strings.TrimSpace(serverType)), ServerID: serverID}
		if !clientEntryPolicyHasMember(result[index].Members, member) {
			result[index].Members = append(result[index].Members, member)
		}
	}
	return result, names, rows.Err()
}

// WithSupportEntries opts subscription generation into a separately ensured schema.
func (s *DBService) WithSupportEntries() *DBService { s.supportEntryEnabled = true; return s }
