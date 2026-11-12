package aiops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"forest/go-api/internal/admin"
)

var errEntryInspectionRevision = fmt.Errorf("%w: invalid entry inspection revision", ErrValidation)

const entryInspectionRevisionHint = "入口拓扑revision格式不正确：首次查询请省略revision并从offset=0开始；翻页只能原样使用本接口在同一查询范围返回的64位小写十六进制revision，不能使用entry_probe_targets的数字revision、规则ID或其他接口的版本号。请修正参数，不要原样重试。"

func entryInspectionToolDefinitions() []ToolDefinition {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	page := func() map[string]any {
		return map[string]any{
			"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "默认20，最多50；按实际next_offset继续"},
			"offset":   map[string]any{"type": "integer", "minimum": 0, "maximum": 100000},
			"revision": str("首读省略；offset>0必填本接口在同一查询范围返回的64位小写十六进制拓扑revision，不能使用entry_probe_targets的数字revision或规则版本。配置变化返回conflict时从offset=0重读。不是探针结果时间快照"),
		}
	}
	out := []ToolDefinition{}
	add := func(name, desc string, props map[string]any, required ...string) {
		if required == nil {
			required = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
		strict := false
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: desc, Parameters: raw, Strict: &strict}})
	}
	p := page()
	p["key"] = str("entry_health返回的完整key，不是节点ID")
	p["section"] = map[string]any{"type": "string", "enum": []string{"sources", "probes"}, "description": "默认sources来源；probes为地址去重探针观测"}
	add("entry_health_detail", "只读分页读取一个已监测入口的全部sources或probes，不刷新目标/发起检测。主列表仅计数；探针结果为各次查询时观测，不是延迟历史。未监测来源用host_references或entry_coverage。", p, "key")
	p = page()
	p["host"] = str("精确域名或IP；IPv6支持方括号。不接受URL，不按探针解析IP反推域名")
	p["port"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "可选端口；省略返回该host全部端口，各行仍区分端口"}
	add("host_references", "按当前入口分配候选地址反查规则、固定叶子、合集和节点引用，包括停用/未监测来源。一行一个有type的引用，不返回凭据；不是整个站点所有配置或历史实际下发全集。", p, "host")
	p = page()
	p["gaps_only"] = map[string]any{"type": "boolean", "description": "默认true列覆盖缺口、观测缺失/过期及TCP异常，false列所有来源；summary始终为全范围统计"}
	add("entry_coverage", "独立检查当前入口分配候选配置的监测覆盖：区分来源未配置和同地址已有其他来源覆盖，区分停用、目标未同步、无启用探针及观测缺失。地址covered只指结构覆盖，不等于TCP健康；不改变员工首页过滤，不发探测。", p)
	return out
}

type entryInspectionArgs struct {
	Key      string `json:"key"`
	Section  string `json:"section"`
	Host     string `json:"host"`
	Port     *int64 `json:"port"`
	GapsOnly *bool  `json:"gaps_only"`
	Revision string `json:"revision"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

func (s *Service) callEntryInspectionTool(ctx context.Context, call ToolCall) (ToolOutcome, error) {
	var a entryInspectionArgs
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF {
		return ToolOutcome{}, ErrValidation
	}
	if a.Limit == 0 {
		a.Limit = 20
	}
	if a.Limit > 50 {
		a.Limit = 50
	}
	if a.Limit < 1 || a.Offset < 0 || a.Offset > 100000 {
		return ToolOutcome{}, ErrValidation
	}
	if !validReadRevision(a.Revision, a.Offset) {
		return ToolOutcome{}, errEntryInspectionRevision
	}
	var result any
	if call.Function.Name == "entry_health_detail" {
		if a.Key == "" || len(a.Key) > 512 || (a.Section != "" && !oneOf(a.Section, "sources", "probes")) {
			return ToolOutcome{}, ErrValidation
		}
		reader, ok := s.panel.(interface {
			SupportEntryOverview(context.Context) (admin.SupportEntryOverview, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		v, err := reader.SupportEntryOverview(ctx)
		if err != nil {
			return ToolOutcome{}, err
		}
		result, err = projectHealthDetail(v, a)
		if err != nil {
			return ToolOutcome{}, err
		}
	} else {
		if !oneOf(call.Function.Name, "entry_coverage", "host_references") {
			return ToolOutcome{}, ErrValidation
		}
		if call.Function.Name == "host_references" {
			var err error
			a.Host, err = admin.NormalizeEntryMonitorHost(a.Host)
			if err != nil || (a.Port != nil && (*a.Port < 1 || *a.Port > 65535)) {
				return ToolOutcome{}, ErrValidation
			}
		}
		reader, ok := s.panel.(interface {
			EntryMonitorInventory(context.Context) (admin.EntryMonitorInventoryOverview, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		v, err := reader.EntryMonitorInventory(ctx)
		if err != nil {
			return ToolOutcome{}, err
		}
		result, err = projectEntryInventory(v, a, call.Function.Name)
		if err != nil {
			return ToolOutcome{}, err
		}
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > maxToolOutputBytes {
		return ToolOutcome{}, ErrUnavailable
	}
	return ToolOutcome{Content: string(raw)}, nil
}

func inspectionRevision(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func healthTopologyRevision(item admin.SupportEntryItem) string {
	type source struct {
		PolicyID                      int64
		SourceKey, Name               string
		PolicyEnabled, MonitorEnabled bool
	}
	type probe struct {
		ID   int64
		Name string
	}
	sources := []source{}
	probes := []probe{}
	for _, s := range item.Sources {
		sources = append(sources, source{s.PolicyID, s.SourceKey, s.PolicyName, s.PolicyEnabled, s.MonitorEnabled})
	}
	for _, p := range item.Probes {
		probes = append(probes, probe{p.ProbeID, p.ProbeName})
	}
	return inspectionRevision(struct {
		Key     string
		Sources []source
		Probes  []probe
	}{item.Key, sources, probes})
}

func projectHealthDetail(v admin.SupportEntryOverview, a entryInspectionArgs) (any, error) {
	for _, item := range v.Items {
		if item.Key != a.Key {
			continue
		}
		rev := healthTopologyRevision(item)
		if a.Revision != "" && a.Revision != rev {
			return nil, ErrConflict
		}
		if a.Section == "" {
			a.Section = "sources"
		}
		out := map[string]any{"entry": projectHealthSummary(item), "revision": rev, "section": a.Section, "generated_at": v.GeneratedAt, "coverage_note": clip(v.CoverageNote, 1000)}
		budget := maxToolOutputBytes - jsonSize(out) - 128
		if a.Section == "sources" {
			items := []healthSourceDTO{}
			for _, src := range window(item.Sources, a.Limit, a.Offset) {
				items = append(items, healthSourceDTO{PolicyID: src.PolicyID, Name: clip(src.PolicyName, 128), SourceKey: src.SourceKey, Status: clip(src.Status, 64), Enabled: src.PolicyEnabled, MonitorEnabled: src.MonitorEnabled, Monitored: src.Monitored, ProbeCount: len(src.Probes), StatusReason: clip(src.StatusReason, 256)})
			}
			out["page"] = boundedPage(items, int64(len(item.Sources)), a.Limit, a.Offset, budget)
		} else {
			items := []probeDTO{}
			for _, p := range window(item.Probes, a.Limit, a.Offset) {
				items = append(items, probeDTO{ID: p.ProbeID, Name: clip(p.ProbeName, 128), Online: p.Online, Status: clip(p.Status, 64), Success: p.LastSuccess, LatencyMS: p.LastLatencyMS, ReportedAt: p.LastReportedAt})
			}
			out["page"] = boundedPage(items, int64(len(item.Probes)), a.Limit, a.Offset, budget)
		}
		return out, nil
	}
	return nil, ErrNotFound
}
