package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

// EntryMonitorInventoryService is separate from the staff homepage: that page
// intentionally excludes rules which have never been configured for monitoring.
type EntryMonitorInventoryService interface {
	EntryMonitorInventory(context.Context) (EntryMonitorInventoryOverview, error)
}

type EntryMonitorReference struct {
	Type     string `json:"type"`
	ID       int64  `json:"id"`
	Protocol string `json:"protocol,omitempty"`
	Name     string `json:"name,omitempty"`
}

type EntryMonitorInventorySource struct {
	Key               string                  `json:"key"`
	AddressKey        string                  `json:"address_key"`
	Host              string                  `json:"host"`
	Port              int64                   `json:"port"`
	SuggestedPort     int64                   `json:"suggested_port"`
	PortBasis         string                  `json:"port_basis"`
	PolicyID          int64                   `json:"policy_id"`
	SourceKey         string                  `json:"source_key"`
	Name              string                  `json:"name"`
	PolicyEnabled     bool                    `json:"policy_enabled"`
	MonitorConfigured bool                    `json:"monitor_configured"`
	MonitorEnabled    bool                    `json:"monitor_enabled"`
	TargetCurrent     bool                    `json:"target_current"`
	AddressCovered    bool                    `json:"address_covered"`
	AddressObserved   bool                    `json:"address_observed"`
	Status            string                  `json:"status"`
	CoverageStatus    string                  `json:"coverage_status"`
	Issues            []string                `json:"issues"`
	References        []EntryMonitorReference `json:"references"`
	Probes            []SupportEntryProbe     `json:"probes"`
	LastReportedAt    *int64                  `json:"last_reported_at"`
}

type EntryMonitorInventorySummary struct {
	TotalSources      int64 `json:"total_sources"`
	ActiveSources     int64 `json:"active_sources"`
	ConfiguredSources int64 `json:"configured_sources"`
	SourceGaps        int64 `json:"source_gaps"`
	TotalAddresses    int64 `json:"total_addresses"`
	ActiveAddresses   int64 `json:"active_addresses"`
	CoveredAddresses  int64 `json:"covered_addresses"`
	AddressGaps       int64 `json:"address_gaps"`
	UnknownSources    int64 `json:"unknown_sources"`
}

type EntryMonitorInventoryOverview struct {
	Coverage     string                        `json:"coverage"`
	CoverageNote string                        `json:"coverage_note"`
	GeneratedAt  int64                         `json:"generated_at"`
	Summary      EntryMonitorInventorySummary  `json:"summary"`
	Sources      []EntryMonitorInventorySource `json:"sources"`
}

// EntryMonitorInventory performs SELECTs only, including on the first request.
// Startup owns schema initialization. Unlike ListClientEntryMonitors this must
// never refresh targets or accidentally turn a coverage check into a write.
func (s *DBService) EntryMonitorInventory(ctx context.Context) (EntryMonitorInventoryOverview, error) {
	if s == nil || s.db == nil {
		return EntryMonitorInventoryOverview{}, ErrUnavailable
	}
	policies, err := s.listClientEntryUserPolicies(ctx, false)
	if err != nil {
		return EntryMonitorInventoryOverview{}, err
	}
	nodes, err := s.loadClientEntryMonitorNodes(ctx, policies)
	if err != nil {
		return EntryMonitorInventoryOverview{}, err
	}
	monitors, err := s.loadClientEntryMonitorRecords(ctx)
	if err != nil {
		return EntryMonitorInventoryOverview{}, err
	}
	probes, err := s.loadSupportMonitorProbes(ctx)
	if err != nil {
		return EntryMonitorInventoryOverview{}, err
	}
	collections, err := s.loadEntryMonitorCollectionReferences(ctx)
	if err != nil {
		return EntryMonitorInventoryOverview{}, err
	}
	return buildEntryMonitorInventory(buildClientEntryMonitorPolicies(policies, nodes), policies, monitors, probes, collections, time.Now().Unix()), nil
}

// NormalizeEntryMonitorHost normalizes an exact address search, not a DNS lookup.
// A domain must not match an IP merely because a previous probe resolved it there.
func NormalizeEntryMonitorHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if net.ParseIP(host) == nil || !strings.Contains(host, ":") {
			return "", fmt.Errorf("invalid entry host")
		}
	}
	host, err := cliententry.NormalizeHost(host)
	if err != nil || host == "" {
		return "", fmt.Errorf("invalid entry host")
	}
	return host, nil
}

func (s *DBService) loadEntryMonitorCollectionReferences(ctx context.Context) (map[string][]EntryMonitorReference, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, m.policy_id, m.split_group_id
FROM v2_client_entry_collection c
JOIN v2_client_entry_collection_member m ON m.collection_id = c.id
ORDER BY c.id, m.policy_id NULLS LAST, m.split_group_id NULLS LAST`)
	if err != nil {
		return nil, fmt.Errorf("load entry monitoring collection references: %w", err)
	}
	defer rows.Close()
	result := make(map[string][]EntryMonitorReference)
	for rows.Next() {
		ref := EntryMonitorReference{Type: "collection"}
		var policy, group sql.NullInt64
		if err := rows.Scan(&ref.ID, &ref.Name, &policy, &group); err != nil {
			return nil, fmt.Errorf("scan entry monitoring collection references: %w", err)
		}
		if policy.Valid {
			key := "policy:" + strconv.FormatInt(policy.Int64, 10)
			result[key] = append(result[key], ref)
		}
		if group.Valid {
			key := "split_group:" + strconv.FormatInt(group.Int64, 10)
			result[key] = append(result[key], ref)
		}
	}
	return result, rows.Err()
}

func buildEntryMonitorInventory(policies []ClientEntryMonitorPolicy, details []ClientEntryUserPolicyRecord, monitors []ClientEntryMonitorRecord, probes []supportMonitorProbe, collections map[string][]EntryMonitorReference, now int64) EntryMonitorInventoryOverview {
	result := EntryMonitorInventoryOverview{
		Coverage: "current_entry_assignment_candidates", GeneratedAt: now,
		CoverageNote: "当前入口分配配置的候选监测地址，包含未配置及停用来源；不是历史实际下发全集。端口取已保存的检测端口，否则取候选建议端口，不保证等于订阅连接端口。不包含独立客户端入口、额外节点链接或未关联规则的节点。source_gaps 按启用来源计算，address_gaps 按启用来源的地址去重计算；地址被其他来源监测不能消除本来源缺口。covered 仅代表配置覆盖，observed 代表至少一个在线探针有新鲜明确结果，均不等同于 TCP 健康。查询不会解析域名，也不会触发检测或同步目标。",
		Sources:      make([]EntryMonitorInventorySource, 0),
	}
	byPolicy := make(map[int64]ClientEntryMonitorRecord, len(monitors))
	for _, monitor := range monitors {
		byPolicy[monitor.PolicyID] = monitor
	}
	detailByPolicy := make(map[int64]ClientEntryUserPolicyRecord, len(details))
	for _, detail := range details {
		detailByPolicy[detail.ID] = detail
	}
	addresses, activeAddresses, coveredAddresses, observedAddresses := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, policy := range policies {
		monitor, configured := byPolicy[policy.ID]
		targets := make(map[string]ClientEntryMonitorTarget, len(monitor.Targets))
		for _, target := range monitor.Targets {
			targets[target.SourceKey] = target
		}
		for _, candidate := range policy.Targets {
			target, hasTarget := targets[candidate.SourceKey]
			port, basis := candidate.SuggestedPort, "suggested_monitor_port"
			if hasTarget && target.Port >= 1 && target.Port <= 65535 {
				port, basis = target.Port, "configured_monitor_port"
			}
			addressKey := clientEntryMonitorAddressKey(candidate.Host, port)
			host := strings.TrimSpace(candidate.Host)
			if addressKey != "" {
				host, _, _ = net.SplitHostPort(addressKey)
			}
			source := supportMonitorSource(policy, candidate, monitor, configured, target, hasTarget, probes, addressKey, now)
			view := EntryMonitorInventorySource{
				Key: candidate.SourceKey, AddressKey: addressKey, Host: host, Port: port,
				SuggestedPort: candidate.SuggestedPort, PortBasis: basis, PolicyID: policy.ID,
				SourceKey: candidate.SourceKey, Name: candidate.Name, PolicyEnabled: policy.Enabled,
				MonitorConfigured: configured, MonitorEnabled: configured && monitor.Enabled,
				TargetCurrent: hasTarget && addressKey != "" && addressKey == clientEntryMonitorAddressKey(target.Host, target.Port),
				Status:        source.Status, Probes: source.Probes, Issues: make([]string, 0),
				References: entryMonitorSourceReferences(policy, candidate, detailByPolicy[policy.ID], target, hasTarget, collections),
			}
			switch {
			case !policy.Enabled:
				view.CoverageStatus = "policy_disabled"
			case addressKey == "":
				view.CoverageStatus = "invalid_address"
			case !configured:
				view.CoverageStatus = "unconfigured"
			case !monitor.Enabled:
				view.CoverageStatus = "monitor_disabled"
			case !hasTarget:
				view.CoverageStatus = "target_missing"
			case !view.TargetCurrent:
				view.CoverageStatus = "target_address_mismatch"
			case len(probes) == 0:
				view.CoverageStatus = "no_enabled_probes"
			default:
				view.CoverageStatus = "ready"
			}
			if view.CoverageStatus != "ready" {
				view.Issues = append(view.Issues, view.CoverageStatus)
			} else {
				view.Issues = entryMonitorObservationIssues(target, probes, monitor, now)
			}
			observed := false
			for _, probe := range view.Probes {
				view.LastReportedAt = supportLatestReport(view.LastReportedAt, probe.LastReportedAt, now)
				if probe.Online && probe.LastSuccess != nil && (probe.Status == "healthy" || probe.Status == "unhealthy" || probe.Status == "partial") {
					observed = true
				}
			}
			result.Summary.TotalSources++
			if configured {
				result.Summary.ConfiguredSources++
			}
			if addressKey != "" {
				addresses[addressKey] = true
			}
			if policy.Enabled {
				result.Summary.ActiveSources++
				if addressKey != "" {
					activeAddresses[addressKey] = true
				}
				if view.CoverageStatus != "ready" {
					result.Summary.SourceGaps++
				} else {
					coveredAddresses[addressKey] = true
				}
				if observed && view.CoverageStatus == "ready" {
					observedAddresses[addressKey] = true
				} else {
					result.Summary.UnknownSources++
				}
			}
			result.Sources = append(result.Sources, view)
		}
	}
	for i := range result.Sources {
		source := &result.Sources[i]
		source.AddressCovered = source.AddressKey != "" && coveredAddresses[source.AddressKey]
		source.AddressObserved = source.AddressKey != "" && observedAddresses[source.AddressKey]
	}
	result.Summary.TotalAddresses = int64(len(addresses))
	result.Summary.ActiveAddresses = int64(len(activeAddresses))
	result.Summary.CoveredAddresses = int64(len(coveredAddresses))
	result.Summary.AddressGaps = result.Summary.ActiveAddresses - result.Summary.CoveredAddresses
	sort.Slice(result.Sources, func(i, j int) bool { return result.Sources[i].Key < result.Sources[j].Key })
	return result
}

func entryMonitorObservationIssues(target ClientEntryMonitorTarget, probes []supportMonitorProbe, monitor ClientEntryMonitorRecord, now int64) []string {
	states := make(map[int64]ClientEntryMonitorState, len(target.States))
	for _, state := range target.States {
		states[state.ProbeID] = state
	}
	issues := make(map[string]bool)
	for _, probe := range probes {
		state, exists := states[probe.ID]
		if !dnsProbeHeartbeatFresh(probe.LastHeartbeatAt, now, defaultProbeOfflineSec) {
			issues["probe_offline"] = true
		}
		if !exists || state.LastReportedAt == nil {
			issues["no_observation"] = true
		} else if !dnsFailoverProbeStateFresh(sql.NullInt64{Int64: *state.LastReportedAt, Valid: true}, now, monitor.CheckIntervalSec, monitor.TCPTimeoutMS, defaultProbeOfflineSec) {
			issues["observation_stale"] = true
		} else if state.LastSuccess == nil {
			issues["observation_unconfirmed"] = true
		}
	}
	result := make([]string, 0, len(issues))
	for issue := range issues {
		result = append(result, issue)
	}
	sort.Strings(result)
	return result
}

func entryMonitorSourceReferences(policy ClientEntryMonitorPolicy, candidate ClientEntryMonitorCandidateTarget, detail ClientEntryUserPolicyRecord, target ClientEntryMonitorTarget, hasTarget bool, collections map[string][]EntryMonitorReference) []EntryMonitorReference {
	refs := []EntryMonitorReference{{Type: "policy", ID: policy.ID, Name: policy.Name}}
	collectionKey := "policy:" + strconv.FormatInt(policy.ID, 10)
	parts := strings.Split(candidate.SourceKey, ":")
	if len(parts) == 4 && parts[0] == "policy" && parts[1] == strconv.FormatInt(policy.ID, 10) {
		if id, err := strconv.ParseInt(parts[3], 10, 64); err == nil && id > 0 {
			if parts[2] == "split-group" {
				refs = append(refs, EntryMonitorReference{Type: "split_group", ID: id, Name: candidate.Name})
				collectionKey = "split_group:" + parts[3]
			} else if _, valid := managedServerTypeTable[parts[2]]; valid {
				refs = append(refs, EntryMonitorReference{Type: "node", ID: id, Protocol: parts[2], Name: candidate.Name})
			}
		}
	}
	if policy.Action == cliententry.ActionOverride {
		for _, member := range detail.Members {
			refs = append(refs, EntryMonitorReference{Type: "node", ID: member.ServerID, Protocol: member.ServerType})
		}
	}
	refs = append(refs, collections[collectionKey]...)
	if hasTarget && target.ID > 0 {
		refs = append(refs, EntryMonitorReference{Type: "monitor_target", ID: target.ID})
	}
	return refs
}
