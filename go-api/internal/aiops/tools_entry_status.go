package aiops

import (
	"context"
	"sort"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
)

const entryPolicyStatusBudget = 8192

const entryPolicyStatusScope = "当前规则的候选监测来源及已有 TCP 观测，不是历史实际下发或用户全部节点的健康结论。监测端口不保证等于节点连接端口；未配置、过期、离线均不能视为正常。相同地址其他来源的结果仅供参考，不代表本来源已配置监测。查询不会刷新目标、发起检测或修改配置。"

// This compact projection is shared by simulation and query correction. Keep
// observations beside their real target identity instead of asking the model
// to invent a target ID from a policy/node ID.
type entryPolicyStatusDTO struct {
	Status           string                 `json:"status"`
	PolicyID         int64                  `json:"policy_id"`
	GeneratedAt      int64                  `json:"generated_at,omitempty"`
	ScopeNote        string                 `json:"scope_note"`
	SelectionNote    string                 `json:"selection_note,omitempty"`
	Sources          []entryStatusSourceDTO `json:"sources"`
	SourcesTotal     int                    `json:"sources_total"`
	SourcesTruncated bool                   `json:"sources_truncated"`
	ReadMore         string                 `json:"read_more,omitempty"`
}

type entryStatusSourceDTO struct {
	PolicyID              int64                      `json:"policy_id"`
	SourceKey             string                     `json:"source_key"`
	Host                  string                     `json:"host"`
	Port                  int64                      `json:"port"`
	PortBasis             string                     `json:"port_basis"`
	TargetID              *int64                     `json:"target_id,omitempty"`
	TargetCurrent         bool                       `json:"target_current"`
	PolicyEnabled         bool                       `json:"policy_enabled"`
	MonitorConfigured     bool                       `json:"monitor_configured"`
	MonitorEnabled        bool                       `json:"monitor_enabled"`
	CoverageStatus        string                     `json:"coverage_status"`
	Status                string                     `json:"status"`
	Issues                []string                   `json:"issues"`
	IssuesTruncated       bool                       `json:"issues_truncated,omitempty"`
	References            []entryStatusReferenceDTO  `json:"references"`
	ReferencesTotal       int                        `json:"references_total"`
	ReferencesTruncated   bool                       `json:"references_truncated"`
	Probes                []probeDTO                 `json:"probes"`
	ProbesTotal           int                        `json:"probes_total"`
	ProbesTruncated       bool                       `json:"probes_truncated"`
	AddressReferences     []entryAddressReferenceDTO `json:"address_references"`
	AddressReferenceTotal int                        `json:"address_references_total"`
	AddressRefsTruncated  bool                       `json:"address_references_truncated"`
}

type entryStatusReferenceDTO struct {
	Type     string `json:"type"`
	ID       int64  `json:"id"`
	Protocol string `json:"protocol,omitempty"`
}

type entryAddressReferenceDTO struct {
	ReferenceOnly   bool       `json:"reference_only"`
	PolicyID        int64      `json:"policy_id"`
	SourceKey       string     `json:"source_key"`
	TargetID        *int64     `json:"target_id,omitempty"`
	TargetCurrent   bool       `json:"target_current"`
	PolicyEnabled   bool       `json:"policy_enabled"`
	MonitorEnabled  bool       `json:"monitor_enabled"`
	CoverageStatus  string     `json:"coverage_status"`
	Status          string     `json:"status"`
	Probes          []probeDTO `json:"probes"`
	ProbesTotal     int        `json:"probes_total"`
	ProbesTruncated bool       `json:"probes_truncated"`
}

func (s *Service) entryStatusForPolicy(ctx context.Context, policyID int64) *entryPolicyStatusDTO {
	out := entryPolicyStatusDTO{Status: "unavailable", PolicyID: policyID, ScopeNote: entryPolicyStatusScope, Sources: []entryStatusSourceDTO{}}
	reader, ok := s.panel.(admin.EntryMonitorInventoryService)
	if !ok {
		return &out
	}
	v, err := reader.EntryMonitorInventory(ctx)
	if err != nil {
		return &out
	}
	out = projectEntryPolicyStatus(v, policyID)
	return &out
}

func (s *Service) entryStatusForSimulation(ctx context.Context, matched admin.ClientEntryUserPolicyRecord) *entryPolicyStatusDTO {
	out := entryPolicyStatusDTO{Status: "unavailable", PolicyID: matched.ID, ScopeNote: entryPolicyStatusScope, Sources: []entryStatusSourceDTO{}}
	reader, ok := s.panel.(admin.EntryMonitorInventoryService)
	if !ok {
		return &out
	}
	v, err := reader.EntryMonitorInventory(ctx)
	if err != nil {
		// The simulation itself is still valid. Do not replace it with an error
		// or leak database error text into the model context.
		return &out
	}
	out = projectEntrySimulationStatus(v, matched)
	return &out
}

func projectEntrySimulationStatus(v admin.EntryMonitorInventoryOverview, matched admin.ClientEntryUserPolicyRecord) entryPolicyStatusDTO {
	// A split simulation carries the selected leaf's host but not its leaf ID.
	// Never include sibling leaves with different hosts as this user's route.
	filterHost := matched.Action == cliententry.ActionOverride
	out := projectEntryPolicyStatusSelection(v, matched.ID, matched.EntryHost, filterHost)
	out.SelectionNote += "模拟未指定单个节点，以下为命中规则的候选来源；不保证该用户所有节点均命中此规则。"
	if matched.Mode == admin.ClientEntryUserPolicyModeSplit {
		out.SelectionNote += "固定分组已按模拟返回的入口地址筛选；模拟未返回叶子 ID，同地址的多个叶子仍为候选，不能据此认定用户属于每个叶子。"
	}
	return boundEntryPolicyStatus(out)
}

func projectEntryPolicyStatus(v admin.EntryMonitorInventoryOverview, policyID int64) entryPolicyStatusDTO {
	return projectEntryPolicyStatusSelection(v, policyID, "", false)
}

func projectEntryPolicyStatusSelection(v admin.EntryMonitorInventoryOverview, policyID int64, host string, filterHost bool) entryPolicyStatusDTO {
	out := entryPolicyStatusDTO{Status: "available", PolicyID: policyID, GeneratedAt: v.GeneratedAt, ScopeNote: entryPolicyStatusScope, Sources: []entryStatusSourceDTO{}}
	wantHost, hostErr := admin.NormalizeEntryMonitorHost(host)
	selected := make([]admin.EntryMonitorInventorySource, 0)
	for _, src := range v.Sources {
		if src.PolicyID != policyID {
			continue
		}
		if filterHost {
			gotHost, err := admin.NormalizeEntryMonitorHost(src.Host)
			if hostErr != nil || err != nil || gotHost != wantHost {
				continue
			}
		}
		selected = append(selected, src)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].SourceKey < selected[j].SourceKey })
	out.SourcesTotal = len(selected)
	if len(selected) == 0 {
		out.Status = "no_candidates"
		out.SelectionNote = "没有可关联的当前候选来源；规则可能无下发入口或已变化，不能据此判定健康，也不要猜测检测目标 ID。"
	}
	for _, src := range window(selected, 6, 0) {
		out.Sources = append(out.Sources, projectEntryStatusSource(v, src))
	}
	return boundEntryPolicyStatus(out)
}

func entryStatusTargetID(src admin.EntryMonitorInventorySource) *int64 {
	for _, ref := range src.References {
		if ref.Type == "monitor_target" && ref.ID > 0 {
			id := ref.ID
			return &id
		}
	}
	return nil
}

func projectEntryStatusProbes(src admin.EntryMonitorInventorySource, limit int) []probeDTO {
	probes := append([]admin.SupportEntryProbe(nil), src.Probes...)
	sort.Slice(probes, func(i, j int) bool { return probes[i].ProbeID < probes[j].ProbeID })
	out := make([]probeDTO, 0)
	for _, p := range window(probes, limit, 0) {
		out = append(out, probeDTO{ID: p.ProbeID, Name: clip(p.ProbeName, 64), Online: p.Online, Status: clip(p.Status, 32), Success: p.LastSuccess, LatencyMS: p.LastLatencyMS, ReportedAt: p.LastReportedAt})
	}
	return out
}

func projectEntryStatusSource(v admin.EntryMonitorInventoryOverview, src admin.EntryMonitorInventorySource) entryStatusSourceDTO {
	out := entryStatusSourceDTO{
		PolicyID: src.PolicyID, SourceKey: clip(src.SourceKey, 128), Host: clip(src.Host, 255), Port: src.Port, PortBasis: clip(src.PortBasis, 40),
		TargetID: entryStatusTargetID(src), TargetCurrent: src.TargetCurrent, PolicyEnabled: src.PolicyEnabled,
		MonitorConfigured: src.MonitorConfigured, MonitorEnabled: src.MonitorEnabled,
		CoverageStatus: clip(src.CoverageStatus, 40), Status: clip(src.Status, 32),
		Issues: []string{}, References: []entryStatusReferenceDTO{}, Probes: projectEntryStatusProbes(src, 4),
		ReferencesTotal: len(src.References), ProbesTotal: len(src.Probes), AddressReferences: []entryAddressReferenceDTO{},
	}
	for _, issue := range window(src.Issues, 8, 0) {
		out.Issues = append(out.Issues, clip(issue, 40))
	}
	out.IssuesTruncated = len(src.Issues) > len(out.Issues)
	for _, ref := range window(src.References, 8, 0) {
		out.References = append(out.References, entryStatusReferenceDTO{Type: clip(ref.Type, 32), ID: ref.ID, Protocol: clip(ref.Protocol, 20)})
	}
	// An exact host+port reference is useful when this source is unconfigured,
	// but is not a replacement source. Do not use resolved IPs or host alone.
	peers := make([]admin.EntryMonitorInventorySource, 0)
	for _, peer := range v.Sources {
		if peer.PolicyID == src.PolicyID && peer.SourceKey == src.SourceKey {
			continue
		}
		if !sameEntryStatusAddress(src, peer) || !peer.MonitorConfigured {
			continue
		}
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].PolicyID != peers[j].PolicyID {
			return peers[i].PolicyID < peers[j].PolicyID
		}
		return peers[i].SourceKey < peers[j].SourceKey
	})
	out.AddressReferenceTotal = len(peers)
	for _, peer := range window(peers, 2, 0) {
		probes := projectEntryStatusProbes(peer, 2)
		out.AddressReferences = append(out.AddressReferences, entryAddressReferenceDTO{ReferenceOnly: true, PolicyID: peer.PolicyID, SourceKey: clip(peer.SourceKey, 128), TargetID: entryStatusTargetID(peer), TargetCurrent: peer.TargetCurrent, PolicyEnabled: peer.PolicyEnabled, MonitorEnabled: peer.MonitorEnabled, CoverageStatus: clip(peer.CoverageStatus, 40), Status: clip(peer.Status, 32), Probes: probes, ProbesTotal: len(peer.Probes), ProbesTruncated: len(probes) < len(peer.Probes)})
	}
	// Bound each nested row before applying the outer budget. Even malicious
	// Unicode/control-character strings must not turn a read into a huge result.
	for jsonSize(out) > 4096 {
		switch {
		case len(out.AddressReferences) > 0:
			out.AddressReferences = out.AddressReferences[:len(out.AddressReferences)-1]
		case len(out.References) > 0:
			out.References = out.References[:len(out.References)-1]
		case len(out.Probes) > 0:
			out.Probes = out.Probes[:len(out.Probes)-1]
		case len(out.Issues) > 0:
			out.Issues = out.Issues[:len(out.Issues)-1]
			out.IssuesTruncated = true
		default:
			return finalizeEntryStatusSource(out)
		}
	}
	return finalizeEntryStatusSource(out)
}

func finalizeEntryStatusSource(out entryStatusSourceDTO) entryStatusSourceDTO {
	out.ReferencesTruncated = len(out.References) < out.ReferencesTotal
	out.ProbesTruncated = len(out.Probes) < out.ProbesTotal
	out.AddressRefsTruncated = len(out.AddressReferences) < out.AddressReferenceTotal
	return out
}

func sameEntryStatusAddress(a, b admin.EntryMonitorInventorySource) bool {
	if a.Port < 1 || a.Port > 65535 || a.Port != b.Port {
		return false
	}
	ah, ae := admin.NormalizeEntryMonitorHost(a.Host)
	bh, be := admin.NormalizeEntryMonitorHost(b.Host)
	return ae == nil && be == nil && ah == bh
}

func boundEntryPolicyStatus(out entryPolicyStatusDTO) entryPolicyStatusDTO {
	for {
		out.SourcesTruncated = len(out.Sources) < out.SourcesTotal
		if out.SourcesTruncated {
			out.ReadMore = "需要完整来源时分页读取 entry_coverage(gaps_only=false)，包括健康来源；已知地址可用 host_references，首次省略 revision，按接口返回版本翻页。"
		}
		if jsonSize(out) <= entryPolicyStatusBudget || len(out.Sources) == 0 {
			return out
		}
		out.Sources = out.Sources[:len(out.Sources)-1]
	}
}
