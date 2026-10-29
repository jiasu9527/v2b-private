package aiops

import (
	"encoding/json"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
)

// These DTOs are intentionally separate from admin DTOs. Adding a password,
// token or config field to an admin response must not expand AI model exposure.
type pageDTO[T any] struct {
	Items       []T   `json:"items"`
	Total       int64 `json:"total"`
	Limit       int   `json:"limit"`
	Offset      int   `json:"offset"`
	NextOffset  *int  `json:"next_offset,omitempty"`
	Truncated   bool  `json:"truncated"`
	SizeLimited bool  `json:"size_limited,omitempty"`
}

// Keep every tool result below the chat persistence/context guard. Items are
// projected and individually bounded before this page budget is applied.
const maxToolOutputBytes = 30000
const maxToolItemBytes = 16000

func makePage[T any](items []T, total int64, limit, offset int) pageDTO[T] {
	p := pageDTO[T]{Items: items, Total: total, Limit: limit, Offset: offset}
	next := offset + len(items)
	p.Truncated = int64(next) < total
	if p.Truncated && len(items) > 0 {
		p.NextOffset = &next
	}
	return p
}

func boundedPage[T any](items []T, total int64, limit, offset, budget int) pageDTO[T] {
	if items == nil {
		items = []T{}
	}
	build := func(count int) pageDTO[T] {
		p := makePage(items[:count], total, limit, offset)
		p.SizeLimited = count < len(items)
		return p
	}
	full := build(len(items))
	if jsonSize(full) <= budget || len(items) <= 1 {
		return full
	}
	// Find the largest fitting prefix without O(n²) JSON work for long pages.
	// A single item is pre-bounded below maxToolItemBytes by its projection.
	low, high := 1, len(items)-1
	for low < high {
		middle := (low + high + 1) / 2
		if jsonSize(build(middle)) <= budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return build(low)
}

func jsonSize(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return maxToolOutputBytes + 1
	}
	return len(raw)
}
func window[T any](items []T, limit, offset int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func text(m map[string]any, key string, n int) string { v, _ := m[key].(string); return clip(v, n) }
func integer(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}
func optionalInt(m map[string]any, key string) *int64 {
	if v, ok := m[key]; !ok || v == nil {
		return nil
	}
	v := integer(m, key)
	return &v
}
func scalarList(v any) []string {
	out := []string{}
	switch list := v.(type) {
	case []string:
		for _, s := range window(list, 50, 0) {
			out = append(out, clip(s, 128))
		}
	case []any:
		for _, x := range window(list, 50, 0) {
			switch s := x.(type) {
			case string:
				out = append(out, clip(s, 128))
			case float64:
				out = append(out, strconv.FormatInt(int64(s), 10))
			case int64:
				out = append(out, strconv.FormatInt(s, 10))
			}
		}
	}
	return out
}

// A single rule may contain many long UA values and hundreds of split leaves.
// Return a useful rule summary rather than losing the entire result. All
// omitted collections retain their original count and explicit truncation flag.
func boundRule(out ruleDTO) ruleDTO {
	for jsonSize(out) > maxToolItemBytes {
		switch {
		case len(out.Groups) > 4:
			out.Groups = out.Groups[:len(out.Groups)/2]
			out.GroupsTruncated = true
		case len(out.Members) > 4:
			out.Members = out.Members[:len(out.Members)/2]
			out.MembersTruncated = true
		default:
			reduced := false
			for i := range out.Conditions {
				c := &out.Conditions[i]
				if len(c.Values) > 0 {
					c.Values = c.Values[:len(c.Values)/2]
					c.ValuesTruncated = true
					reduced = true
				}
			}
			if reduced {
				continue
			} else if len(out.Conditions) > 0 {
				out.Conditions = out.Conditions[:len(out.Conditions)/2]
				out.ConditionsTruncated = true
			} else if len(out.Groups) > 0 {
				out.Groups = out.Groups[:len(out.Groups)/2]
				out.GroupsTruncated = true
			} else if len(out.Members) > 0 {
				out.Members = out.Members[:len(out.Members)/2]
				out.MembersTruncated = true
			} else {
				return out
			}
		}
	}
	return out
}

type panelDTO struct {
	Schedule            bool   `json:"schedule"`
	Horizon             bool   `json:"horizon"`
	ScheduleLastRuntime *int64 `json:"schedule_last_runtime"`
	QueueRunning        bool   `json:"queue_running"`
	FailedJobs          int64  `json:"failed_jobs"`
	RecentJobs          int64  `json:"recent_jobs"`
	JobsPerMinute       int64  `json:"jobs_per_minute"`
	Workers             int64  `json:"workers"`
	PausedMasters       int64  `json:"paused_masters"`
}
type userDTO struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	PlanID         *int64 `json:"plan_id"`
	PlanName       string `json:"plan_name"`
	GroupID        *int64 `json:"group_id"`
	Banned         bool   `json:"banned"`
	TransferEnable int64  `json:"transfer_enable_bytes"`
	Upload         int64  `json:"upload_bytes"`
	Download       int64  `json:"download_bytes"`
	ExpiredAt      *int64 `json:"expired_at"`
	DeviceLimit    *int64 `json:"device_limit"`
	SpeedLimit     *int64 `json:"speed_limit"`
	LastSeenAt     int64  `json:"last_seen_at"`
	CreatedAt      int64  `json:"created_at"`
}
type lookupDTO struct {
	Found bool     `json:"found"`
	User  *userDTO `json:"user,omitempty"`
}

func projectUser(m map[string]any) userDTO {
	return userDTO{ID: integer(m, "id"), Email: text(m, "email", 254), PlanID: optionalInt(m, "plan_id"), PlanName: text(m, "plan_name", 128), GroupID: optionalInt(m, "group_id"), Banned: integer(m, "banned") != 0, TransferEnable: integer(m, "transfer_enable"), Upload: integer(m, "u"), Download: integer(m, "d"), ExpiredAt: optionalInt(m, "expired_at"), DeviceLimit: optionalInt(m, "device_limit"), SpeedLimit: optionalInt(m, "speed_limit"), LastSeenAt: integer(m, "t"), CreatedAt: integer(m, "created_at")}
}

type orderDTO struct {
	ID          int64  `json:"id"`
	TradeNo     string `json:"trade_no"`
	UserID      int64  `json:"user_id"`
	PlanID      int64  `json:"plan_id"`
	PlanName    string `json:"plan_name"`
	Period      string `json:"period"`
	TotalAmount int64  `json:"total_amount_cents"`
	Status      int64  `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	PaidAt      *int64 `json:"paid_at"`
}

func projectOrder(m map[string]any) orderDTO {
	return orderDTO{ID: integer(m, "id"), TradeNo: text(m, "trade_no", 128), UserID: integer(m, "user_id"), PlanID: integer(m, "plan_id"), PlanName: text(m, "plan_name", 128), Period: text(m, "period", 64), TotalAmount: integer(m, "total_amount"), Status: integer(m, "status"), CreatedAt: integer(m, "created_at"), PaidAt: optionalInt(m, "paid_at")}
}

type nodeDTO struct {
	ID                int64    `json:"id"`
	Type              string   `json:"type"`
	Name              string   `json:"name"`
	Host              string   `json:"host"`
	Port              string   `json:"port"`
	ServerPort        int64    `json:"server_port"`
	Visible           bool     `json:"visible"`
	GroupIDs          []string `json:"group_ids"`
	GroupIDsTruncated bool     `json:"group_ids_truncated"`
	Online            int64    `json:"online"`
	LastCheckAt       int64    `json:"last_check_at"`
	LastPushAt        int64    `json:"last_push_at"`
	AvailableStatus   int64    `json:"available_status"`
}

func projectNode(m map[string]any) nodeDTO {
	port := text(m, "port", 32)
	if port == "" {
		port = strconv.FormatInt(integer(m, "port"), 10)
	}
	out := nodeDTO{ID: integer(m, "id"), Type: text(m, "type", 20), Name: text(m, "name", 128), Host: text(m, "host", 255), Port: port, ServerPort: integer(m, "server_port"), Visible: integer(m, "show") != 0, GroupIDs: scalarList(m["group_id"]), Online: integer(m, "online"), LastCheckAt: integer(m, "last_check_at"), LastPushAt: integer(m, "last_push_at"), AvailableStatus: integer(m, "available_status")}
	switch ids := m["group_id"].(type) {
	case []string:
		out.GroupIDsTruncated = len(ids) > len(out.GroupIDs)
	case []any:
		out.GroupIDsTruncated = len(ids) > len(out.GroupIDs)
	}
	for jsonSize(out) > maxToolItemBytes && len(out.GroupIDs) > 0 {
		out.GroupIDs = out.GroupIDs[:len(out.GroupIDs)/2]
		out.GroupIDsTruncated = true
	}
	return out
}

type conditionDTO struct {
	Field           string   `json:"field"`
	Operator        string   `json:"operator"`
	Values          []string `json:"values,omitempty"`
	Minimum         string   `json:"minimum,omitempty"`
	Maximum         string   `json:"maximum,omitempty"`
	Value           string   `json:"value,omitempty"`
	ValueCount      int      `json:"value_count"`
	ValuesTruncated bool     `json:"values_truncated"`
	EmailCount      int      `json:"email_count,omitempty"`
}
type leafDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	EntryHost  string `json:"entry_host"`
	UserCount  int64  `json:"user_count"`
	IsLeaf     bool   `json:"is_leaf"`
	GlobalSort *int64 `json:"global_sort"`
}
type memberDTO struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}
type ruleDTO struct {
	ID                  int64          `json:"id"`
	Name                string         `json:"name"`
	Mode                string         `json:"mode"`
	Action              string         `json:"action"`
	Sort                int64          `json:"sort"`
	Enabled             bool           `json:"enabled"`
	EntryHost           string         `json:"entry_host"`
	ResolveHost         bool           `json:"resolve_host"`
	SnapshotUsers       int64          `json:"snapshot_users"`
	Conditions          []conditionDTO `json:"conditions"`
	ConditionsTotal     int            `json:"conditions_total"`
	ConditionsTruncated bool           `json:"conditions_truncated"`
	Members             []memberDTO    `json:"members"`
	MembersTotal        int            `json:"members_total"`
	MembersTruncated    bool           `json:"members_truncated"`
	Groups              []leafDTO      `json:"groups"`
	GroupsTotal         int            `json:"groups_total"`
	GroupsTruncated     bool           `json:"groups_truncated"`
}

func rawScalar(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return clip(x, 256)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
func projectRule(r admin.ClientEntryUserPolicyRecord) ruleDTO {
	out := ruleDTO{ID: r.ID, Name: clip(r.Name, 128), Mode: clip(r.Mode, 32), Action: clip(r.Action, 32), Sort: r.Sort, Enabled: r.Enabled != 0, EntryHost: clip(r.EntryHost, 255), ResolveHost: r.ResolveEntryHost != 0, SnapshotUsers: r.SnapshotUserCount, Conditions: []conditionDTO{}, Members: []memberDTO{}, Groups: []leafDTO{}, ConditionsTotal: len(r.Conditions), MembersTotal: len(r.Members), GroupsTotal: len(r.SplitGroups), ConditionsTruncated: len(r.Conditions) > 20, MembersTruncated: len(r.Members) > 50, GroupsTruncated: len(r.SplitGroups) > 50}
	for _, c := range window(r.Conditions, 20, 0) {
		item := conditionDTO{Field: clip(c.Field, 32), Operator: clip(c.Operator, 32), ValueCount: len(c.Values)}
		if strings.EqualFold(c.Field, "email") {
			item.EmailCount = len(c.Values)
			item.ValuesTruncated = len(c.Values) > 0
		} else if oneOf(c.Field, "ua", "user_id", "plan_id", "registration_days") {
			item.Minimum = rawScalar(c.Min)
			item.Maximum = rawScalar(c.Max)
			item.Value = rawScalar(c.Value)
			for _, v := range window(c.Values, 20, 0) {
				item.Values = append(item.Values, rawScalar(v))
			}
			item.ValuesTruncated = len(c.Values) > 20
		}
		out.Conditions = append(out.Conditions, item)
	}
	for _, m := range window(r.Members, 50, 0) {
		out.Members = append(out.Members, memberDTO{Type: clip(m.ServerType, 20), ID: m.ServerID})
	}
	for _, g := range window(r.SplitGroups, 50, 0) {
		out.Groups = append(out.Groups, leafDTO{ID: g.ID, Name: clip(g.Name, 128), Path: clip(g.Path, 128), EntryHost: clip(g.EntryHost, 255), UserCount: g.UserCount, IsLeaf: g.IsLeaf, GlobalSort: g.GlobalSort})
	}
	return boundRule(out)
}

type simulationUserDTO struct {
	ID               int64  `json:"id"`
	Email            string `json:"email"`
	PlanID           int64  `json:"plan_id"`
	RegistrationDays int64  `json:"registration_days"`
}
type simulationDTO struct {
	Found   bool               `json:"found"`
	User    *simulationUserDTO `json:"user,omitempty"`
	Matched *ruleDTO           `json:"matched,omitempty"`
}
type probeDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Online     bool   `json:"online"`
	Status     string `json:"status"`
	Success    *bool  `json:"last_success"`
	LatencyMS  *int64 `json:"latency_ms"`
	ReportedAt *int64 `json:"reported_at"`
}
type healthSourceDTO struct {
	PolicyID  int64  `json:"policy_id"`
	Name      string `json:"name"`
	SourceKey string `json:"source_key"`
	Status    string `json:"status"`
	Enabled   bool   `json:"enabled"`
}
type healthItemDTO struct {
	Key              string            `json:"key"`
	Host             string            `json:"host"`
	Port             int64             `json:"port"`
	Status           string            `json:"status"`
	StatusReason     string            `json:"status_reason"`
	LastReportedAt   *int64            `json:"last_reported_at"`
	Sources          []healthSourceDTO `json:"sources"`
	SourceCount      int               `json:"source_count"`
	SourcesTruncated bool              `json:"sources_truncated"`
	Probes           []probeDTO        `json:"probes"`
	ProbeCount       int               `json:"probe_count"`
	ProbesTruncated  bool              `json:"probes_truncated"`
}
type healthDTO struct {
	GeneratedAt int64                     `json:"generated_at"`
	Coverage    string                    `json:"coverage"`
	Summary     admin.SupportEntrySummary `json:"summary"`
	Entries     pageDTO[healthItemDTO]    `json:"entries"`
}

func projectHealth(v admin.SupportEntryOverview, status string, limit, offset int) healthDTO {
	filtered := []admin.SupportEntryItem{}
	for _, r := range v.Items {
		if status == "" || r.Status == status {
			filtered = append(filtered, r)
		}
	}
	items := []healthItemDTO{}
	for _, r := range window(filtered, limit, offset) {
		o := healthItemDTO{Key: clip(r.Key, 300), Host: clip(r.Host, 255), Port: r.Port, Status: clip(r.Status, 64), StatusReason: clip(r.StatusReason, 256), LastReportedAt: r.LastReportedAt, Sources: []healthSourceDTO{}, SourceCount: len(r.Sources), SourcesTruncated: len(r.Sources) > 20, Probes: []probeDTO{}, ProbeCount: len(r.Probes), ProbesTruncated: len(r.Probes) > 20}
		for _, x := range window(r.Sources, 20, 0) {
			o.Sources = append(o.Sources, healthSourceDTO{PolicyID: x.PolicyID, Name: clip(x.PolicyName, 128), SourceKey: clip(x.SourceKey, 128), Status: clip(x.Status, 64), Enabled: x.PolicyEnabled})
		}
		for _, p := range window(r.Probes, 20, 0) {
			o.Probes = append(o.Probes, probeDTO{ID: p.ProbeID, Name: clip(p.ProbeName, 128), Online: p.Online, Status: clip(p.Status, 64), Success: p.LastSuccess, LatencyMS: p.LastLatencyMS, ReportedAt: p.LastReportedAt})
		}
		for jsonSize(o) > maxToolItemBytes {
			if len(o.Sources) > 0 && (len(o.Probes) == 0 || jsonSize(o.Sources) >= jsonSize(o.Probes)) {
				o.Sources = o.Sources[:len(o.Sources)/2]
				o.SourcesTruncated = true
			} else if len(o.Probes) > 0 {
				o.Probes = o.Probes[:len(o.Probes)/2]
				o.ProbesTruncated = true
			} else {
				break
			}
		}
		items = append(items, o)
	}
	out := healthDTO{GeneratedAt: v.GeneratedAt, Coverage: clip(v.Coverage, 128), Summary: v.Summary}
	// Account for the health envelope and aggregate summary, not just items.
	budget := maxToolOutputBytes - jsonSize(out) - 64
	out.Entries = boundedPage(items, int64(len(filtered)), limit, offset, budget)
	return out
}

type usageDTO struct {
	Kind     string `json:"kind"`
	PolicyID int64  `json:"policy_id"`
	GroupID  int64  `json:"group_id,omitempty"`
	Name     string `json:"name"`
}
type backupDTO struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	IP              string     `json:"ip"`
	Port            int64      `json:"port"`
	Enabled         bool       `json:"enabled"`
	Available       bool       `json:"available"`
	Used            bool       `json:"used"`
	Status          string     `json:"status"`
	QuarantineUntil int64      `json:"quarantine_until"`
	OnlineProbes    int64      `json:"online_probes"`
	HealthyProbes   int64      `json:"healthy_probes"`
	Usages          []usageDTO `json:"usages"`
	UsageCount      int        `json:"usage_count"`
	UsagesTruncated bool       `json:"usages_truncated"`
}

func projectBackup(r admin.ClientEntryBackupIPRecord) backupDTO {
	o := backupDTO{ID: r.ID, Name: clip(r.Name, 128), IP: clip(r.IP, 64), Port: r.Port, Enabled: r.Enabled, Available: r.Available, Used: r.Used, Status: clip(r.Status, 64), QuarantineUntil: r.QuarantineUntil, OnlineProbes: r.OnlineProbeCount, HealthyProbes: r.HealthyProbeCount, Usages: []usageDTO{}, UsageCount: len(r.Usages), UsagesTruncated: len(r.Usages) > 20}
	for _, u := range window(r.Usages, 20, 0) {
		o.Usages = append(o.Usages, usageDTO{Kind: clip(u.Kind, 32), PolicyID: u.PolicyID, GroupID: u.SplitGroupID, Name: clip(u.PolicyName, 128)})
	}
	for jsonSize(o) > maxToolItemBytes && len(o.Usages) > 0 {
		o.Usages = o.Usages[:len(o.Usages)/2]
		o.UsagesTruncated = true
	}
	return o
}
