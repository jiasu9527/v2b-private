package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"forest/go-api/internal/admin"
)

func probeToolDefinitions() []ToolDefinition {
	integer := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "description": description}
	}
	ids := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": integer("正整数 ID"), "minItems": 1, "maxItems": 50, "uniqueItems": true, "description": description}
	}
	page := func() map[string]any {
		return map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000, "default": 0}}
	}
	out := []ToolDefinition{}
	add := func(name, description string, props map[string]any, required ...string) {
		if required == nil {
			required = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
		// Optional IDs must stay optional through relays that convert tools
		// to strict schemas; filling an unknown target with 1 is not a default.
		strict := false
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: description, Parameters: raw, Strict: &strict}})
	}
	p := page()
	p["policy_id"] = integer("可选，只看指定规则；可以使用entry_simulate返回的matched.id")
	p["target_id"] = integer("可选，仅使用本工具已返回的target_id；未知时省略，不能填1或复制policy_id、节点ID、二分组ID。与policy_id同时传入时取交集")
	add("entry_probe_targets", "查找现有入口检测目标、规则及已登记探针 ID，不发探测。仅知规则ID时只传policy_id，省略target_id；空列表不是故障也不是正常，不可原样循环查询或猜ID。按 targets.next_offset 翻页；未配置监测的入口不会出现。返回的数字revision仅属监测配置，不能用于host_references/entry_coverage的字符串revision。", p)
	add("entry_probe_run", "提议对指定入口通过探针发起一次新 TCP 检测，完全访问自动执行，逐次确认模式等待批准。policy_ids 与 target_ids 至少填一项；都填时取交集。target_ids 精确到入口，未填时检查选中规则全部入口；probe_ids 未填时使用全部启用探针，最多50目标×50探针。任务已受理不等于检测通过，需用 entry_probe_result 查结果；上报仍可能触发现有自动切换/二分。", map[string]any{"policy_ids": ids("规则 ID，可省略"), "target_ids": ids("entry_probe_targets 返回的入口目标 ID，可省略"), "probe_ids": ids("指定探针 ID，可省略"), "reason": map[string]any{"type": "string", "description": "检测目的与预期影响"}}, "reason")
	p = page()
	p["run_id"] = integer("检测任务 run_id；审批完成后从动作输出取得")
	add("entry_probe_result", "读取指定检测任务及目标×探针的逐项结果，包含待上报项。按 results.next_offset 翻页；pending不代表正常、TCP正常不代表业务全链路正常，不重复发起任务。", p, "run_id")
	return out
}

type probeToolArgs struct {
	Limit     int     `json:"limit"`
	Offset    int     `json:"offset"`
	PolicyID  int64   `json:"policy_id"`
	TargetID  int64   `json:"target_id"`
	RunID     int64   `json:"run_id"`
	PolicyIDs []int64 `json:"policy_ids"`
	TargetIDs []int64 `json:"target_ids"`
	ProbeIDs  []int64 `json:"probe_ids"`
	Reason    string  `json:"reason"`
}

func decodeProbeToolArgs(call ToolCall) (probeToolArgs, error) {
	var args probeToolArgs
	var fields map[string]json.RawMessage
	if len(call.Function.Arguments) > 32768 || json.Unmarshal([]byte(call.Function.Arguments), &fields) != nil || fields == nil {
		return args, ErrValidation
	}
	found := false
	for _, def := range probeToolDefinitions() {
		if def.Function.Name != call.Function.Name {
			continue
		}
		found = true
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if json.Unmarshal(def.Function.Parameters, &schema) != nil {
			return args, ErrValidation
		}
		for k, v := range fields {
			if _, ok := schema.Properties[k]; !ok || strings.TrimSpace(string(v)) == "null" {
				return args, ErrValidation
			}
		}
		for _, k := range schema.Required {
			if _, ok := fields[k]; !ok {
				return args, ErrValidation
			}
		}
	}
	if !found {
		return args, ErrValidation
	}
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF {
		return args, ErrValidation
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	if args.Limit < 1 || args.Offset < 0 || args.Offset > 10000 || args.PolicyID < 0 || args.TargetID < 0 {
		return args, ErrValidation
	}
	return args, nil
}

type probeTargetDTO struct {
	TargetID         int64  `json:"target_id"`
	PolicyID         int64  `json:"policy_id"`
	PolicyName       string `json:"policy_name"`
	Name             string `json:"name"`
	SourceKey        string `json:"source_key"`
	Host             string `json:"host"`
	Port             int64  `json:"port"`
	Enabled          bool   `json:"enabled"`
	AutoSplitEnabled bool   `json:"auto_split_enabled"`
}
type probeAgentDTO struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	LastHeartbeatAt *int64 `json:"last_heartbeat_at"`
}

func (s *Service) callProbeTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	a, err := decodeProbeToolArgs(call)
	if err != nil {
		return ToolOutcome{}, err
	}
	var value any
	switch call.Function.Name {
	case "entry_probe_targets":
		reader, ok := s.panel.(interface {
			ListClientEntryMonitors(context.Context) (admin.ClientEntryMonitorOverview, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		v, e := reader.ListClientEntryMonitors(ctx)
		if e != nil {
			return ToolOutcome{}, safeAIError(e)
		}
		policies := map[int64]bool{}
		for _, p := range v.Policies {
			policies[p.ID] = p.Enabled
		}
		items := []probeTargetDTO{}
		for _, m := range v.Items {
			if a.PolicyID > 0 && m.PolicyID != a.PolicyID {
				continue
			}
			for _, t := range m.Targets {
				if a.TargetID > 0 && t.ID != a.TargetID {
					continue
				}
				items = append(items, probeTargetDTO{TargetID: t.ID, PolicyID: m.PolicyID, PolicyName: clip(m.PolicyName, 128), Name: clip(t.Name, 128), SourceKey: clip(t.SourceKey, 128), Host: clip(t.Host, 255), Port: t.Port, Enabled: m.Enabled && policies[m.PolicyID], AutoSplitEnabled: t.AutoSplitEnabled})
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].TargetID < items[j].TargetID })
		probes := []probeAgentDTO{}
		for _, p := range v.Probes {
			if p.Enabled {
				probes = append(probes, probeAgentDTO{ID: p.ID, Name: clip(p.Name, 128), Enabled: p.Enabled, LastHeartbeatAt: p.LastHeartbeatAt})
			}
		}
		sort.Slice(probes, func(i, j int) bool { return probes[i].ID < probes[j].ID })
		// Include all selectable probes within the same cap as run selection.
		if len(probes) > 50 {
			return ToolOutcome{}, ErrValidation
		}
		envelope := struct {
			Revision         int64                   `json:"revision"`
			Targets          pageDTO[probeTargetDTO] `json:"targets"`
			Probes           []probeAgentDTO         `json:"enabled_probes"`
			AutomaticEffects string                  `json:"automatic_effects"`
			QueryHint        string                  `json:"query_hint,omitempty"`
		}{Revision: v.Revision, Probes: probes, AutomaticEffects: "检测上报仍按原配置触发自动切换或二分；禁用目标不可发起", QueryHint: probeTargetQueryHint(a, len(items))}
		budget := maxToolOutputBytes - jsonSize(envelope) - 64
		envelope.Targets = boundedPage(window(items, a.Limit, a.Offset), int64(len(items)), a.Limit, a.Offset, budget)
		value = envelope
	case "entry_probe_run":
		if a.Reason == "" || len([]rune(a.Reason)) > 500 || len(a.PolicyIDs)+len(a.TargetIDs) == 0 {
			return ToolOutcome{}, ErrValidation
		}
		for _, ids := range [][]int64{a.PolicyIDs, a.TargetIDs, a.ProbeIDs} {
			if len(ids) > 50 {
				return ToolOutcome{}, ErrValidation
			}
			seen := map[int64]bool{}
			for _, id := range ids {
				if id <= 0 || seen[id] {
					return ToolOutcome{}, ErrValidation
				}
				seen[id] = true
			}
		}
		reader, ok := s.panel.(interface {
			ListClientEntryMonitors(context.Context) (admin.ClientEntryMonitorOverview, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		// Initialize/refresh the normal monitored target table before capturing it.
		if _, err = reader.ListClientEntryMonitors(ctx); err != nil {
			return ToolOutcome{}, safeAIError(err)
		}
		p := probeActionPayload{Selection: admin.AIEntryProbeSelection{PolicyIDs: a.PolicyIDs, TargetIDs: a.TargetIDs, ProbeIDs: a.ProbeIDs}}
		raw, _ := json.Marshal(p)
		action, e := s.proposeAction(ctx, actorID, sessionID, call.ID, actionSpec{Kind: "entry_probe_run", Payload: raw, Reason: a.Reason})
		if e != nil {
			return ToolOutcome{}, e
		}
		raw, _ = json.Marshal(actionToolSummary(action))
		return ToolOutcome{Content: string(raw), Pending: true, ActionID: action.ID}, nil
	case "entry_probe_result":
		if a.RunID <= 0 {
			return ToolOutcome{}, ErrValidation
		}
		value, err = s.readProbeRun(ctx, a.RunID, a.Limit, a.Offset)
		if err != nil {
			return ToolOutcome{}, err
		}
	default:
		return ToolOutcome{}, ErrValidation
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxToolOutputBytes {
		return ToolOutcome{}, ErrUnavailable
	}
	return ToolOutcome{Content: string(raw)}, nil
}

// Do not silently broaden a filter or fabricate target IDs. An empty lookup
// must explain how to discover the real ID, not invite an unchanged poll.
func probeTargetQueryHint(a probeToolArgs, total int) string {
	if total != 0 {
		return ""
	}
	if a.TargetID > 0 {
		return "当前筛选未命中检测目标；target_id不是policy_id、节点或二分组ID，不可猜测。请省略target_id、保留已核实的policy_id重新查询，从返回项取得真实target_id；仍为空则核对entry_coverage，不要原样重复或据此判定入口正常/故障。"
	}
	return "当前范围没有匹配的已配置监测目标；不要原样重复查询。用entry_coverage核对覆盖缺口，或host_references按已核实的地址查询来源；其首次查询省略revision。缺少监测不代表入口正常或故障。"
}

type probeRunResultDTO struct {
	TargetID      int64  `json:"target_id"`
	TargetVersion int64  `json:"target_version"`
	PolicyID      int64  `json:"policy_id"`
	PolicyName    string `json:"policy_name"`
	TargetName    string `json:"target_name"`
	Host          string `json:"host"`
	Port          int64  `json:"port"`
	ProbeID       int64  `json:"probe_id"`
	ProbeName     string `json:"probe_name"`
	Status        string `json:"status"`
	LatencyMS     *int64 `json:"latency_ms"`
	Error         string `json:"error,omitempty"`
	ReportedAt    *int64 `json:"reported_at"`
}
type probeRunDTO struct {
	RunID       int64                      `json:"run_id"`
	Status      string                     `json:"status"`
	StartedAt   int64                      `json:"started_at"`
	CompletedAt *int64                     `json:"completed_at"`
	Expected    int64                      `json:"expected"`
	Received    int64                      `json:"received"`
	Succeeded   int64                      `json:"succeeded"`
	Failed      int64                      `json:"failed"`
	Pending     int64                      `json:"pending"`
	Results     pageDTO[probeRunResultDTO] `json:"results"`
	Message     string                     `json:"message"`
}

// Read only the immutable expected pairs for this run. Unrelated periodic
// reports or results from another target generation cannot enter the summary.
func (s *Service) readProbeRun(ctx context.Context, id int64, limit, offset int) (probeRunDTO, error) {
	var out probeRunDTO
	if s.db == nil {
		return out, ErrUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var completed sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id,status,started_at,completed_at FROM v2_client_entry_monitor_run WHERE id=$1`, id).Scan(&out.RunID, &out.Status, &out.StartedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if completed.Valid {
		out.CompletedAt = &completed.Int64
	}
	const pairs = ` FROM v2_client_entry_monitor_run run CROSS JOIN LATERAL jsonb_to_recordset(run.expected_pairs::jsonb) AS pair(target_id bigint,probe_id bigint,target_version bigint,policy_id bigint,policy_name text,target_name text,host text,port bigint,probe_name text)
LEFT JOIN v2_client_entry_monitor_run_result result ON result.run_id=run.id AND result.target_id=pair.target_id AND result.probe_id=pair.probe_id WHERE run.id=$1`
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(result.id),COUNT(*) FILTER(WHERE result.success=1),COUNT(*) FILTER(WHERE result.success=0)`+pairs, id).Scan(&out.Expected, &out.Received, &out.Succeeded, &out.Failed)
	if err != nil {
		return out, err
	}
	out.Pending = out.Expected - out.Received
	rows, err := tx.QueryContext(ctx, `SELECT pair.target_id,pair.target_version,pair.policy_id,pair.policy_name,pair.target_name,pair.host,pair.port,pair.probe_id,pair.probe_name,result.success,result.latency_ms,result.error,result.reported_at`+pairs+` ORDER BY pair.target_id,pair.probe_id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return out, err
	}
	items := []probeRunResultDTO{}
	for rows.Next() {
		var item probeRunResultDTO
		var success, latency, reported sql.NullInt64
		var problem sql.NullString
		if err = rows.Scan(&item.TargetID, &item.TargetVersion, &item.PolicyID, &item.PolicyName, &item.TargetName, &item.Host, &item.Port, &item.ProbeID, &item.ProbeName, &success, &latency, &problem, &reported); err != nil {
			rows.Close()
			return out, err
		}
		item.Status = "pending"
		if success.Valid {
			if success.Int64 == 1 {
				item.Status = "healthy"
			} else {
				item.Status = "failed"
			}
		}
		if latency.Valid {
			item.LatencyMS = &latency.Int64
		}
		if reported.Valid {
			item.ReportedAt = &reported.Int64
		}
		item.PolicyName = clip(item.PolicyName, 128)
		item.TargetName = clip(item.TargetName, 128)
		item.ProbeName = clip(item.ProbeName, 128)
		item.Host = clip(item.Host, 255)
		item.Error = clip(problem.String, 256)
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Message = "只代表本次指定入口与探针的 TCP 结果；pending为尚未上报，不能视为正常。结果上报继续遵循后台既有自动化配置。"
	out.Results = boundedPage(items, out.Expected, limit, offset, maxToolOutputBytes-jsonSize(out)-64)
	return out, tx.Commit()
}
