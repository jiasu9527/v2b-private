package aiops

import (
	"forest/go-api/internal/admin"
)

// One row per source; unbounded references/probes never travel inside this DTO.
// host_references flattens reference relations into its own pageable rows.
type entryCoverageSourceDTO struct {
	Key               string   `json:"key"`
	AddressKey        string   `json:"address_key"`
	Host              string   `json:"host"`
	Port              int64    `json:"port"`
	SuggestedPort     int64    `json:"suggested_port"`
	PortBasis         string   `json:"port_basis"`
	PolicyID          int64    `json:"policy_id"`
	SourceKey         string   `json:"source_key"`
	Name              string   `json:"name"`
	PolicyEnabled     bool     `json:"policy_enabled"`
	MonitorConfigured bool     `json:"monitor_configured"`
	MonitorEnabled    bool     `json:"monitor_enabled"`
	TargetCurrent     bool     `json:"target_current"`
	AddressCovered    bool     `json:"address_covered"`
	AddressObserved   bool     `json:"address_observed"`
	Status            string   `json:"status"`
	CoverageStatus    string   `json:"coverage_status"`
	Issues            []string `json:"issues"`
	ReferenceCount    int      `json:"reference_count"`
	ProbeCount        int      `json:"probe_count"`
	LastReportedAt    *int64   `json:"last_reported_at"`
}

type hostReferenceDTO struct {
	SourceKey      string            `json:"source_key"`
	Host           string            `json:"host"`
	Port           int64             `json:"port"`
	PortBasis      string            `json:"port_basis"`
	PolicyID       int64             `json:"policy_id"`
	PolicyEnabled  bool              `json:"policy_enabled"`
	CoverageStatus string            `json:"coverage_status"`
	Reference      entryReferenceDTO `json:"reference"`
}

type entryReferenceDTO struct {
	Type     string `json:"type"`
	ID       int64  `json:"id"`
	Protocol string `json:"protocol,omitempty"`
	Name     string `json:"name,omitempty"`
}

func projectCoverageSource(s admin.EntryMonitorInventorySource) entryCoverageSourceDTO {
	return entryCoverageSourceDTO{Key: s.Key, AddressKey: s.AddressKey, Host: clip(s.Host, 255), Port: s.Port, SuggestedPort: s.SuggestedPort, PortBasis: s.PortBasis, PolicyID: s.PolicyID, SourceKey: s.SourceKey, Name: clip(s.Name, 128), PolicyEnabled: s.PolicyEnabled, MonitorConfigured: s.MonitorConfigured, MonitorEnabled: s.MonitorEnabled, TargetCurrent: s.TargetCurrent, AddressCovered: s.AddressCovered, AddressObserved: s.AddressObserved, Status: s.Status, CoverageStatus: s.CoverageStatus, Issues: s.Issues, ReferenceCount: len(s.References), ProbeCount: len(s.Probes), LastReportedAt: s.LastReportedAt}
}

func projectEntryInventory(v admin.EntryMonitorInventoryOverview, a entryInspectionArgs, tool string) (any, error) {
	out := map[string]any{"coverage": v.Coverage, "coverage_note": clip(v.CoverageNote, 1500), "generated_at": v.GeneratedAt}
	if tool == "host_references" {
		rows := []hostReferenceDTO{}
		for _, source := range v.Sources {
			if source.Host != a.Host || (a.Port != nil && source.Port != *a.Port) {
				continue
			}
			for _, ref := range source.References {
				rows = append(rows, hostReferenceDTO{SourceKey: source.SourceKey, Host: source.Host, Port: source.Port, PortBasis: source.PortBasis, PolicyID: source.PolicyID, PolicyEnabled: source.PolicyEnabled, CoverageStatus: source.CoverageStatus, Reference: entryReferenceDTO{Type: ref.Type, ID: ref.ID, Protocol: clip(ref.Protocol, 32), Name: clip(ref.Name, 128)}})
			}
		}
		rev := inspectionRevision(rows)
		if a.Revision != "" && a.Revision != rev {
			return nil, ErrConflict
		}
		out["revision"] = rev
		out["host"] = a.Host
		out["port"] = a.Port
		out["page"] = boundedPage(window(rows, a.Limit, a.Offset), int64(len(rows)), a.Limit, a.Offset, maxToolOutputBytes-jsonSize(out)-128)
		return out, nil
	}
	rows := []entryCoverageSourceDTO{}
	for _, source := range v.Sources {
		if (a.GapsOnly == nil || *a.GapsOnly) && source.CoverageStatus == "ready" && len(source.Issues) == 0 && source.Status == "healthy" {
			continue
		}
		rows = append(rows, projectCoverageSource(source))
	}
	// Pin ordering, membership, and structural coverage while allowing new probe
	// measurements. A gap appearing/disappearing changes this hash too, so a
	// filtered page cannot silently skip records as observations arrive.
	topology := make([]entryCoverageSourceDTO, len(rows))
	copy(topology, rows)
	for i := range topology {
		topology[i].LastReportedAt = nil
		topology[i].AddressObserved = false
	}
	rev := inspectionRevision(topology)
	if a.Revision != "" && a.Revision != rev {
		return nil, ErrConflict
	}
	out["revision"] = rev
	out["summary"] = v.Summary
	out["gaps_only"] = a.GapsOnly == nil || *a.GapsOnly
	out["page"] = boundedPage(window(rows, a.Limit, a.Offset), int64(len(rows)), a.Limit, a.Offset, maxToolOutputBytes-jsonSize(out)-128)
	return out, nil
}
