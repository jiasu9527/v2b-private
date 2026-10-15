package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// Support monitor DTOs are intentionally independent of administrator probe
// records: those records include the probe authentication secret.
type SupportEntryProbe struct {
	ProbeID        int64  `json:"probe_id"`
	ProbeName      string `json:"probe_name"`
	Online         bool   `json:"online"`
	Status         string `json:"status"`
	StatusReason   string `json:"status_reason"`
	LastSuccess    *bool  `json:"last_success"`
	LastLatencyMS  *int64 `json:"last_latency_ms"`
	LastReportedAt *int64 `json:"last_reported_at"`
	LastError      string `json:"last_error"`
	LastResolvedIP string `json:"last_resolved_ip"`
	SourceCount    int64  `json:"source_count"`
}

type SupportEntrySource struct {
	PolicyID         int64               `json:"policy_id"`
	PolicyName       string              `json:"policy_name"`
	SourceKey        string              `json:"source_key"`
	Name             string              `json:"name"`
	PolicyEnabled    bool                `json:"policy_enabled"`
	MonitorEnabled   bool                `json:"monitor_enabled"`
	Monitored        bool                `json:"monitored"`
	CheckIntervalSec int64               `json:"check_interval_sec"`
	TCPTimeoutMS     int64               `json:"tcp_timeout_ms"`
	FailureThreshold int64               `json:"failure_threshold"`
	SuccessThreshold int64               `json:"success_threshold"`
	Status           string              `json:"status"`
	StatusReason     string              `json:"status_reason"`
	Probes           []SupportEntryProbe `json:"probes"`
}

type SupportEntryItem struct {
	Key            string               `json:"key"`
	Host           string               `json:"host"`
	Port           int64                `json:"port"`
	Protocol       string               `json:"protocol"`
	Status         string               `json:"status"`
	StatusReason   string               `json:"status_reason"`
	Sources        []SupportEntrySource `json:"sources"`
	Probes         []SupportEntryProbe  `json:"probes"`
	LastReportedAt *int64               `json:"last_reported_at"`
}

type SupportEntrySummary struct {
	Total       int64 `json:"total"`
	Healthy     int64 `json:"healthy"`
	Partial     int64 `json:"partial"`
	Unhealthy   int64 `json:"unhealthy"`
	Stale       int64 `json:"stale"`
	Unmonitored int64 `json:"unmonitored"` // Compatibility field; unconfigured sources are excluded.
	Disabled    int64 `json:"disabled"`
}

type SupportEntryOverview struct {
	Coverage     string              `json:"coverage"`
	CoverageNote string              `json:"coverage_note"`
	GeneratedAt  int64               `json:"generated_at"`
	Summary      SupportEntrySummary `json:"summary"`
	Items        []SupportEntryItem  `json:"items"`
}

type supportMonitorProbe struct {
	ID              int64
	Name            string
	LastHeartbeatAt sql.NullInt64
}

// SupportEntryOverview only reads existing candidates and probe observations.
// It never refreshes monitor targets, queues a run, or changes routing state.
func (s *DBService) SupportEntryOverview(ctx context.Context) (SupportEntryOverview, error) {
	if s == nil || s.db == nil {
		return SupportEntryOverview{}, ErrUnavailable
	}
	policies, err := s.resolveClientEntryMonitorPolicies(ctx)
	if err != nil {
		return SupportEntryOverview{}, err
	}
	monitors, err := s.loadClientEntryMonitorRecords(ctx)
	if err != nil {
		return SupportEntryOverview{}, err
	}
	probes, err := s.loadSupportMonitorProbes(ctx)
	if err != nil {
		return SupportEntryOverview{}, err
	}
	return buildSupportEntryOverview(policies, monitors, probes, time.Now().Unix()), nil
}

func (s *DBService) loadSupportMonitorProbes(ctx context.Context) ([]supportMonitorProbe, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, last_heartbeat_at
FROM v2_dns_probe WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load support monitor probes: %w", err)
	}
	defer rows.Close()
	probes := make([]supportMonitorProbe, 0)
	for rows.Next() {
		var probe supportMonitorProbe
		if err := rows.Scan(&probe.ID, &probe.Name, &probe.LastHeartbeatAt); err != nil {
			return nil, fmt.Errorf("scan support monitor probe: %w", err)
		}
		probes = append(probes, probe)
	}
	return probes, rows.Err()
}

func buildSupportEntryOverview(policies []ClientEntryMonitorPolicy, monitors []ClientEntryMonitorRecord, probes []supportMonitorProbe, now int64) SupportEntryOverview {
	result := SupportEntryOverview{
		Coverage: "entry_assignment", GeneratedAt: now,
		CoverageNote: "仅显示节点管理中的入口分配里已纳入监测的入口；包含已配置检测的规则入口、固定分组叶子入口及保留原入口规则选中的节点。未配置检测的规则及其来源不显示，不包含独立客户端入口或未关联规则的节点。检测仅代表 TCP 连通性，不代表业务全链路可用。",
		Items:        make([]SupportEntryItem, 0),
	}
	monitorsByPolicy := make(map[int64]ClientEntryMonitorRecord, len(monitors))
	for _, monitor := range monitors {
		monitorsByPolicy[monitor.PolicyID] = monitor
	}
	itemByKey := make(map[string]int)
	for _, policy := range policies {
		monitor, configured := monitorsByPolicy[policy.ID]
		// Exclude unconfigured sources before endpoint deduplication and status
		// aggregation: a separate unmonitored rule must not create a phantom
		// endpoint or downgrade another rule's monitored healthy endpoint.
		if !configured {
			continue
		}
		targetBySource := make(map[string]ClientEntryMonitorTarget, len(monitor.Targets))
		for _, target := range monitor.Targets {
			targetBySource[target.SourceKey] = target
		}
		for _, candidate := range policy.Targets {
			target, hasTarget := targetBySource[candidate.SourceKey]
			port := candidate.SuggestedPort
			if hasTarget && target.Port >= 1 && target.Port <= 65535 {
				port = target.Port
			}
			addressKey := clientEntryMonitorAddressKey(candidate.Host, port)
			key := "tcp:" + addressKey
			host := strings.TrimSpace(candidate.Host)
			if addressKey != "" {
				host, _, _ = net.SplitHostPort(addressKey)
			} else {
				// Invalid legacy values must not collapse into one empty endpoint.
				key = fmt.Sprintf("invalid:%d:%s", policy.ID, candidate.SourceKey)
			}
			index, exists := itemByKey[key]
			if !exists {
				index = len(result.Items)
				itemByKey[key] = index
				result.Items = append(result.Items, SupportEntryItem{
					Key: key, Host: host, Port: port, Protocol: "tcp",
					Sources: make([]SupportEntrySource, 0), Probes: make([]SupportEntryProbe, 0),
				})
			}
			source := supportMonitorSource(policy, candidate, monitor, configured, target, hasTarget, probes, addressKey, now)
			result.Items[index].Sources = append(result.Items[index].Sources, source)
		}
	}
	for index := range result.Items {
		item := &result.Items[index]
		statuses := make([]string, 0, len(item.Sources))
		byProbe := make(map[int64][]SupportEntryProbe)
		for _, source := range item.Sources {
			statuses = append(statuses, source.Status)
			for _, probe := range source.Probes {
				byProbe[probe.ProbeID] = append(byProbe[probe.ProbeID], probe)
				item.LastReportedAt = supportLatestReport(item.LastReportedAt, probe.LastReportedAt, now)
			}
		}
		item.Status = supportAggregateStatus(statuses)
		item.StatusReason = supportStatusReason(item.Status)
		for _, states := range byProbe {
			item.Probes = append(item.Probes, aggregateSupportProbe(states, now))
		}
		sort.Slice(item.Probes, func(i, j int) bool { return item.Probes[i].ProbeID < item.Probes[j].ProbeID })
		result.Summary.Total++
		switch item.Status {
		case "healthy":
			result.Summary.Healthy++
		case "partial":
			result.Summary.Partial++
		case "unhealthy":
			result.Summary.Unhealthy++
		case "stale":
			result.Summary.Stale++
		case "disabled":
			result.Summary.Disabled++
		}
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if supportStatusPriority(a.Status) != supportStatusPriority(b.Status) {
			return supportStatusPriority(a.Status) < supportStatusPriority(b.Status)
		}
		return a.Key < b.Key
	})
	return result
}

func supportMonitorSource(policy ClientEntryMonitorPolicy, candidate ClientEntryMonitorCandidateTarget, monitor ClientEntryMonitorRecord, configured bool, target ClientEntryMonitorTarget, hasTarget bool, probes []supportMonitorProbe, addressKey string, now int64) SupportEntrySource {
	source := SupportEntrySource{
		PolicyID: policy.ID, PolicyName: policy.Name, SourceKey: candidate.SourceKey, Name: candidate.Name,
		PolicyEnabled: policy.Enabled, MonitorEnabled: configured && monitor.Enabled, Monitored: configured,
		CheckIntervalSec: monitor.CheckIntervalSec, TCPTimeoutMS: monitor.TCPTimeoutMS,
		FailureThreshold: monitor.FailureThreshold, SuccessThreshold: monitor.SuccessThreshold,
		Probes: make([]SupportEntryProbe, 0),
	}
	switch {
	case !policy.Enabled:
		source.Status, source.StatusReason = "disabled", "入口规则已停用"
		return source
	case configured && !monitor.Enabled:
		source.Status, source.StatusReason = "disabled", "入口检测已暂停"
		return source
	case !configured:
		source.Status, source.StatusReason = "unmonitored", "尚未纳入入口检测"
		return source
	}
	states := make(map[int64]ClientEntryMonitorState, len(target.States))
	for _, state := range target.States {
		states[state.ProbeID] = state
	}
	// Candidates reflect the current rule. A monitor refresh happens elsewhere;
	// never present an old host's successful observation as this host's health.
	targetCurrent := hasTarget && addressKey != "" && addressKey == clientEntryMonitorAddressKey(target.Host, target.Port)
	statuses := make([]string, 0, len(probes))
	for _, probe := range probes {
		state, hasState := states[probe.ID]
		view := SupportEntryProbe{ProbeID: probe.ID, ProbeName: probe.Name, SourceCount: 1,
			Online: dnsProbeHeartbeatFresh(probe.LastHeartbeatAt, now, defaultProbeOfflineSec)}
		if targetCurrent && hasState {
			view.LastSuccess, view.LastLatencyMS = state.LastSuccess, state.LastLatencyMS
			view.LastReportedAt = state.LastReportedAt
			view.LastError, view.LastResolvedIP = state.LastError, state.LastResolvedIP
		}
		switch {
		case !targetCurrent:
			view.Status, view.StatusReason = "stale", "入口地址或检测目标待同步"
		case !view.Online:
			view.Status, view.StatusReason = "stale", "探针离线，当前状态未知"
		case !hasState || state.LastReportedAt == nil:
			view.Status, view.StatusReason = "stale", "尚无检测结果"
		case !dnsFailoverProbeStateFresh(sql.NullInt64{Int64: *state.LastReportedAt, Valid: true}, now, monitor.CheckIntervalSec, monitor.TCPTimeoutMS, defaultProbeOfflineSec):
			view.Status, view.StatusReason = "stale", "检测结果已过期"
		case state.LastSuccess == nil:
			view.Status, view.StatusReason = "stale", "检测结果等待确认"
		case *state.LastSuccess && state.ConsecutiveFailure > 0:
			view.Status, view.StatusReason = "partial", "失败待复核"
		case !*state.LastSuccess && state.ConsecutiveSuccess > 0:
			view.Status, view.StatusReason = "partial", "恢复待复核"
		case *state.LastSuccess:
			view.Status, view.StatusReason = "healthy", "TCP 连通正常"
		default:
			view.Status, view.StatusReason = "unhealthy", "TCP 连通异常"
		}
		if view.Status != "healthy" {
			view.LastLatencyMS = nil
		}
		source.Probes = append(source.Probes, view)
		statuses = append(statuses, view.Status)
	}
	source.Status = supportAggregateStatus(statuses)
	source.StatusReason = supportStatusReason(source.Status)
	if len(probes) == 0 {
		source.Status, source.StatusReason = "stale", "无启用探针，当前状态未知"
	}
	return source
}

// Ignore disabled sources, but never discard an unknown or failing active
// source just because another rule/probe happens to have a healthy result.
func supportAggregateStatus(statuses []string) string {
	result := ""
	for _, status := range statuses {
		if status == "disabled" {
			continue
		}
		if result == "" {
			result = status
		} else if result != status {
			return "partial"
		}
	}
	if result != "" {
		return result
	}
	if len(statuses) > 0 {
		return "disabled"
	}
	return "stale"
}

func aggregateSupportProbe(states []SupportEntryProbe, now int64) SupportEntryProbe {
	result := states[0]
	statuses := make([]string, 0, len(states))
	for _, state := range states {
		statuses = append(statuses, state.Status)
		latest := supportLatestReport(result.LastReportedAt, state.LastReportedAt, now)
		if latest != nil && state.LastReportedAt != nil && *latest == *state.LastReportedAt {
			result = state
		}
	}
	result.SourceCount = int64(len(states))
	result.Status = supportAggregateStatus(statuses)
	result.StatusReason = supportStatusReason(result.Status)
	if result.Status != "healthy" && result.Status != "unhealthy" {
		result.LastSuccess = nil
		result.LastLatencyMS = nil
	}
	return result
}

func supportLatestReport(current, next *int64, now int64) *int64 {
	if current != nil && (*current < 0 || *current > now) {
		current = nil
	}
	if next != nil && *next >= 0 && *next <= now && (current == nil || *next > *current) {
		value := *next
		return &value
	}
	return current
}

func supportStatusPriority(status string) int {
	switch status {
	case "unhealthy":
		return 0
	case "partial":
		return 1
	case "stale":
		return 2
	case "unmonitored":
		return 3
	case "healthy":
		return 4
	default:
		return 5
	}
}

func supportStatusReason(status string) string {
	switch status {
	case "healthy":
		return "所有有效来源和探针的 TCP 检测正常"
	case "partial":
		return "存在异常、待复核或状态不一致，请展开查看"
	case "unhealthy":
		return "TCP 检测异常"
	case "stale":
		return "检测数据过期、缺失或探针离线，当前状态未知"
	case "unmonitored":
		return "尚未纳入入口检测"
	default:
		return "规则或检测已停用"
	}
}
