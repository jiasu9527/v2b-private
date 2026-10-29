package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"forest/go-api/internal/admin"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

type aiToolArgs struct {
	Query          string     `json:"query,omitempty"`
	Limit          int        `json:"limit,omitempty"`
	Offset         int        `json:"offset,omitempty"`
	UserID         int64      `json:"user_id,omitempty"`
	TradeNo        string     `json:"trade_no,omitempty"`
	Status         *int64     `json:"status,omitempty"`
	PolicyID       int64      `json:"policy_id,omitempty"`
	Email          string     `json:"email,omitempty"`
	UA             string     `json:"ua,omitempty"`
	ServerID       int64      `json:"server_id,omitempty"`
	Command        string     `json:"command,omitempty"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	TargetID       int64      `json:"target_id,omitempty"`
	Host           string     `json:"host,omitempty"`
	PolicyIDs      []int64    `json:"policy_ids,omitempty"`
	NodeType       string     `json:"node_type,omitempty"`
	NodeID         int64      `json:"node_id,omitempty"`
	Changes        *nodePatch `json:"changes,omitempty"`
}

func (s *Service) toolDefinitions() []ToolDefinition {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	num := func(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }
	out := []ToolDefinition{}
	add := func(n, d string, p map[string]any, required ...string) {
		if p == nil {
			p = map[string]any{}
		}
		if required == nil {
			required = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false})
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: n, Description: d, Parameters: raw}})
	}
	page := func() map[string]any {
		return map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20, "description": "每页默认20条，最多50条，不要传100。输出较大时会返回更少记录，按 next_offset 继续，不要使用 offset+limit"}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000, "default": 0, "description": "第一页为0，后续使用结果的next_offset；truncated=true表示尚未读完"}}
	}
	add("panel_status", "读取调度器与队列健康，不读取凭据或原始日志。", nil)
	add("user_lookup", "完整邮箱或ID精确查询普通用户套餐和用量，不含凭据。", map[string]any{"query": str("完整邮箱或正整数ID")}, "query")
	p := page()
	p["user_id"] = num("精确用户ID")
	p["trade_no"] = str("完整订单号")
	p["status"] = num("状态0-4")
	add("orders_list", "查询套餐订单，金额为分，不含支付凭据与Apple ID交付。", p)
	add("nodes_list", "查询节点基础配置和上报状态，不含节点凭据或安装命令。", page())
	p = page()
	p["policy_id"] = num("可选精确规则ID")
	add("entry_rules_list", "查询入口规则/UA/固定分组，指定邮箱仅返回数量；大规则嵌套列表会摘要裁剪并返回总数与truncated，不可将省略的条件视为不存在。按next_offset翻页。", p)
	add("entry_simulate", "按实际UA和当前规则模拟入口；不改配置。", map[string]any{"email": str("完整邮箱"), "ua": str("实际UA最多1024字节，空表示真实为空")}, "email", "ua")
	add("entry_health", "读取已有入口检测结果，不发新探测，TCP正常不代表业务全链路正常。", page())
	add("backup_ips_list", "读取备用IP健康及占用，不清检测状态不领取IP。", page())
	add("servers_list", "分页读取管理员登记SSH服务器，不返回密码/私钥；按next_offset继续。", page())
	add("ssh_status", "创建固定只读系统诊断命令的待审批卡片；不立即执行，必须管理员逐次确认。", map[string]any{"server_id": num("已登记SSH服务器ID"), "reason": str("检查原因")}, "server_id", "reason")
	add("propose_ssh_command", "提议通过SSH在已登记落地机或中转机执行完整命令，可安装节点、运行非交互安装脚本、修改配置、重启服务；须逐次人工审批。先检查目标系统并确认脚本来源和参数，不猜测安装地址；不可在命令中放SSH密码私钥。执行输出实时更新，失败或断线不自动重试。", map[string]any{"server_id": num("已登记服务器ID"), "command": str("完整非交互命令，最多16000字节；没有终端和键盘输入，安装脚本须明确非交互参数"), "timeout_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 1800, "default": 120, "description": "最长执行秒数，默认120；安装可选较长时间，最长1800秒"}, "reason": str("原因、风险与可回滚性，最多500字")}, "server_id", "command", "reason")
	add("propose_entry_host", "创建入口host变更待审批卡，不执行；保留UA与固定名单，合集成员不支持单独改。", map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"policy", "split_group"}}, "target_id": num("规则/叶子ID"), "host": str("单个域名或IP"), "reason": str("原因与影响")}, "kind", "target_id", "host", "reason")
	add("propose_node_change", "仅提议指定节点host/port/show改变，必须后台确认，不支持其他配置或任意API透传。", map[string]any{"node_type": str("协议"), "node_id": num("节点ID"), "changes": map[string]any{"type": "object", "properties": map[string]any{"host": str("域名或IP"), "port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}, "show": map[string]any{"type": "integer", "enum": []int{0, 1}}}, "additionalProperties": false}, "reason": str("原因与影响")}, "node_type", "node_id", "changes", "reason")
	add("monitor_run", "创建指定1-10条规则检测审批卡。检测不直接修改入口，结果仍遵循后台既有自动化配置。", map[string]any{"policy_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}, "minItems": 1, "maxItems": 10, "uniqueItems": true}, "reason": str("检测原因")}, "policy_ids", "reason")
	out = append(out, userToolDefinitions()...)
	out = append(out, ticketToolDefinitions()...)
	out = append(out, probeToolDefinitions()...)
	out = append(out, entryRuleToolDefinitions()...)
	out = append(out, nodeToolDefinitions()...)
	return out
}
func (s *Service) callTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return ToolOutcome{}, err
	}
	if s.panel == nil {
		return ToolOutcome{}, ErrUnavailable
	}
	if len(call.Function.Arguments) > 32768 {
		return ToolOutcome{}, ErrValidation
	}
	if !s.validToolArgumentKeys(call.Function.Name, call.Function.Arguments) {
		return ToolOutcome{}, ErrValidation
	}
	switch call.Function.Name {
	case "plans_list", "groups_list", "user_detail", "propose_user_update":
		return s.callUserTool(ctx, actorID, sessionID, call)
	case "tickets_list", "ticket_get", "ticket_settings", "propose_ticket_update", "propose_ticket_settings":
		return s.callTicketTool(ctx, actorID, sessionID, call)
	case "entry_probe_targets", "entry_probe_run", "entry_probe_result":
		return s.callProbeTool(ctx, actorID, sessionID, call)
	case "entry_rule_detail", "propose_entry_rule_update":
		return s.callEntryRuleTool(ctx, actorID, sessionID, call)
	case "node_detail", "propose_node_update":
		return s.callNodeTool(ctx, actorID, sessionID, call)
	}
	var a aiToolArgs
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF {
		return ToolOutcome{}, ErrValidation
	}
	if a.Limit == 0 {
		a.Limit = 20
	}
	// Only read-only list tools declare limit. Keep the server cap even when a
	// model ignores maximum in the schema, without wasting a tool round. The
	// returned page reports the effective limit and actual continuation offset.
	if a.Limit > 50 {
		a.Limit = 50
	}
	if a.Limit < 1 || a.Offset < 0 || a.Offset > 10000 {
		return ToolOutcome{}, ErrValidation
	}
	var result any
	var err error
	pending := false
	switch call.Function.Name {
	case "panel_status":
		var status admin.SystemStatus
		status, err = s.panel.GetSystemStatus(ctx)
		if err == nil {
			var q admin.QueueStats
			q, err = s.panel.GetQueueStats(ctx)
			result = panelDTO{Schedule: status.Schedule, Horizon: status.Horizon, ScheduleLastRuntime: status.ScheduleLastRuntime, QueueRunning: q.Status, FailedJobs: q.FailedJobs, RecentJobs: q.RecentJobs, JobsPerMinute: q.JobsPerMinute, Workers: q.Processes, PausedMasters: q.PausedMasters}
		}
	case "user_lookup":
		q := strings.TrimSpace(a.Query)
		field := "id"
		if id, e := strconv.ParseInt(q, 10, 64); e != nil || id <= 0 {
			if !validAIEmail(q) {
				return ToolOutcome{}, ErrValidation
			}
			field = "email"
			q = strings.ToLower(q)
		}
		var v admin.UserListResult
		v, err = s.panel.ListUsers(ctx, admin.UserFetchRequest{Current: 1, PageSize: 10, Sort: "id", SortType: "ASC", Filters: []admin.UserFilter{{Key: field, Condition: "=", Value: q}, {Key: "is_admin", Condition: "=", Value: "0"}, {Key: "is_staff", Condition: "=", Value: "0"}}})
		if err == nil {
			if len(v.Data) > 1 || v.Total > 1 {
				return ToolOutcome{}, ErrValidation
			}
			r := lookupDTO{}
			if len(v.Data) == 1 {
				u := projectUser(v.Data[0])
				r.Found = true
				r.User = &u
			}
			result = r
		}
	case "orders_list":
		if a.UserID < 0 || len(a.TradeNo) > 128 || (a.Status != nil && (*a.Status < 0 || *a.Status > 4)) {
			return ToolOutcome{}, ErrValidation
		}
		f := []admin.OrderFilter{}
		if a.UserID > 0 {
			f = append(f, admin.OrderFilter{Key: "user_id", Condition: "=", Value: strconv.FormatInt(a.UserID, 10)})
		}
		if a.TradeNo != "" {
			f = append(f, admin.OrderFilter{Key: "trade_no", Condition: "=", Value: a.TradeNo})
		}
		if a.Status != nil {
			f = append(f, admin.OrderFilter{Key: "status", Condition: "=", Value: strconv.FormatInt(*a.Status, 10)})
		}
		var v admin.OrderListResult
		v, err = s.panel.FetchOrders(ctx, admin.OrderFetchRequest{Current: int64(a.Offset/50 + 1), PageSize: 50, Filters: f})
		if err == nil {
			rows := v.Data
			start := a.Offset % 50
			if start+a.Limit > 50 && v.Total > int64((a.Offset/50+1)*50) {
				next, e := s.panel.FetchOrders(ctx, admin.OrderFetchRequest{Current: int64(a.Offset/50 + 2), PageSize: 50, Filters: f})
				if e != nil {
					err = e
				} else {
					rows = append(rows, next.Data...)
				}
			}
			items := []orderDTO{}
			for _, r := range window(rows, a.Limit, start) {
				items = append(items, projectOrder(r))
			}
			result = boundedPage(items, v.Total, a.Limit, a.Offset, maxToolOutputBytes)
		}
	case "nodes_list":
		var v []map[string]any
		v, err = s.panel.ListManagedServers(ctx)
		if err == nil {
			items := []nodeDTO{}
			for _, r := range window(v, a.Limit, a.Offset) {
				items = append(items, projectNode(r))
			}
			result = boundedPage(items, int64(len(v)), a.Limit, a.Offset, maxToolOutputBytes)
		}
	case "entry_rules_list":
		if a.PolicyID < 0 {
			return ToolOutcome{}, ErrValidation
		}
		var v []admin.ClientEntryUserPolicyRecord
		v, err = s.panel.ListClientEntryUserPolicies(ctx)
		if err == nil {
			rows := []admin.ClientEntryUserPolicyRecord{}
			for _, r := range v {
				if a.PolicyID == 0 || r.ID == a.PolicyID {
					rows = append(rows, r)
				}
			}
			items := []ruleDTO{}
			for _, r := range window(rows, a.Limit, a.Offset) {
				items = append(items, projectRule(r))
			}
			result = boundedPage(items, int64(len(rows)), a.Limit, a.Offset, maxToolOutputBytes)
		}
	case "entry_simulate":
		if !validAIEmail(a.Email) || len(a.UA) > 1024 {
			return ToolOutcome{}, ErrValidation
		}
		var v admin.ClientEntryUserPolicySimulationResult
		v, err = s.panel.SimulateClientEntryUserPolicy(ctx, admin.ClientEntryUserPolicySimulationRequest{Email: a.Email, UA: a.UA})
		if err == nil {
			r := simulationDTO{Found: v.Found}
			if v.User != nil {
				r.User = &simulationUserDTO{ID: v.User.ID, Email: clip(v.User.Email, 254), PlanID: v.User.PlanID, RegistrationDays: v.User.RegistrationDays}
			}
			if v.Matched != nil {
				x := projectRule(*v.Matched)
				r.Matched = &x
			}
			result = r
		}
	case "entry_health":
		r, ok := s.panel.(interface {
			SupportEntryOverview(context.Context) (admin.SupportEntryOverview, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		var v admin.SupportEntryOverview
		v, err = r.SupportEntryOverview(ctx)
		if err == nil {
			result = projectHealth(v, "", a.Limit, a.Offset)
		}
	case "backup_ips_list":
		r, ok := s.panel.(interface {
			ListClientEntryBackupIPs(context.Context) (admin.ClientEntryBackupIPList, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		var v admin.ClientEntryBackupIPList
		v, err = r.ListClientEntryBackupIPs(ctx)
		if err == nil {
			items := []backupDTO{}
			for _, r := range window(v.Items, a.Limit, a.Offset) {
				items = append(items, projectBackup(r))
			}
			result = boundedPage(items, int64(len(v.Items)), a.Limit, a.Offset, maxToolOutputBytes)
		}
	case "servers_list":
		var v []Server
		v, err = s.ListServers(ctx, actorID)
		if err == nil {
			result = boundedPage(window(v, a.Limit, a.Offset), int64(len(v)), a.Limit, a.Offset, maxToolOutputBytes)
		}
	case "ssh_status", "propose_ssh_command", "propose_entry_host", "propose_node_change", "monitor_run":
		spec := actionSpec{Reason: a.Reason}
		switch call.Function.Name {
		case "ssh_status", "propose_ssh_command":
			command := a.Command
			if call.Function.Name == "ssh_status" {
				command = diagnosticCommand
			}
			spec.Kind = "ssh_command"
			spec.TargetID = a.ServerID
			spec.Payload, _ = json.Marshal(sshPayload{ServerID: a.ServerID, Command: command, TimeoutSeconds: a.TimeoutSeconds})
		case "propose_entry_host":
			spec.Kind = "entry_host"
			spec.TargetID = a.TargetID
			spec.TargetType = a.Kind
			spec.Payload, _ = json.Marshal(entryPayload{Kind: a.Kind, TargetID: a.TargetID, Host: a.Host})
		case "propose_node_change":
			if a.Changes == nil {
				return ToolOutcome{}, ErrValidation
			}
			spec.Kind = "node_change"
			spec.TargetID = a.NodeID
			spec.TargetType = a.NodeType
			spec.Payload, _ = json.Marshal(nodePayload{NodeType: a.NodeType, NodeID: a.NodeID, Changes: *a.Changes})
		case "monitor_run":
			spec.Kind = "monitor_run"
			spec.Payload, _ = json.Marshal(monitorPayload{PolicyIDs: a.PolicyIDs})
		}
		var action Action
		action, err = s.proposeAction(ctx, actorID, sessionID, call.ID, spec)
		if err == nil {
			// The full immutable payload is available in the human-review card.
			// Do not duplicate a long command or 100 monitor targets in the model
			// result and accidentally lose Pending when a byte limit is reached.
			result = actionToolSummary(action)
		}
		pending = err == nil
	default:
		return ToolOutcome{}, ErrValidation
	}
	if err != nil {
		return ToolOutcome{}, safeAIError(err)
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return ToolOutcome{}, ErrUnavailable
	}
	if len(raw) > maxToolOutputBytes {
		return ToolOutcome{Content: `{"status":"error","code":"output_too_large","message":"单项结果仍过大，请按精确ID缩小范围或在后台查看详情"}`, Pending: pending}, nil
	}
	return ToolOutcome{Content: string(raw), Pending: pending}, nil
}

func actionToolSummary(action Action) map[string]any {
	return map[string]any{"action_id": action.ID, "session_id": action.SessionID, "kind": action.Kind, "status": action.Status, "target_id": action.TargetID, "target_type": action.TargetType, "expires_at": action.ExpiresAt, "message": "待人工确认，尚未执行；完整目标与命令请在操作审核卡查看"}
}

// Enforce the per-tool allowlist server-side, not just in the provider's JSON
// schema. The shared decoding struct must not accept another tool's arguments.
func (s *Service) validToolArgumentKeys(name, arguments string) bool {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &args) != nil || args == nil {
		return false
	}
	for _, tool := range s.toolDefinitions() {
		if tool.Function.Name != name {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if json.Unmarshal(tool.Function.Parameters, &schema) != nil {
			return false
		}
		for key, value := range args {
			if _, ok := schema.Properties[key]; !ok || string(value) == "null" {
				return false
			}
		}
		for _, key := range schema.Required {
			if _, ok := args[key]; !ok {
				return false
			}
		}
		return true
	}
	return false
}
func validAIEmail(s string) bool {
	if len(s) > 254 || s == "" {
		return false
	}
	a, e := mail.ParseAddress(s)
	return e == nil && a.Address == s && strings.Contains(s, "@")
}
func oneOf(s string, v ...string) bool {
	for _, x := range v {
		if s == x {
			return true
		}
	}
	return false
}
func safeAIError(e error) error {
	for _, v := range []error{ErrForbidden, ErrValidation, ErrConflict, ErrNotFound, ErrBusy, ErrUnavailable} {
		if errors.Is(e, v) {
			return v
		}
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(e, context.Canceled) {
		return context.Canceled
	}
	return ErrUnavailable
}

// Only fixed error categories reach the model. SQL, network error bodies and
// administrator credentials must never be copied into a tool result.
func toolErrorContent(err error) string {
	code, message := "unavailable", "当前查询暂不可用，请核对后台服务状态；不要连续重复请求"
	switch {
	case errors.Is(err, ErrValidation):
		code, message = "invalid_arguments", "工具参数无效，请按声明字段检查参数和分页范围"
	case errors.Is(err, ErrForbidden):
		code, message = "forbidden", "管理员权限已变化，请停止操作"
	case errors.Is(err, ErrNotFound):
		code, message = "not_found", "目标记录不存在，请核对准确ID"
	case errors.Is(err, ErrConflict):
		code, message = "conflict", "目标配置或操作状态已变化，请先刷新核对，不要重复写操作"
	case errors.Is(err, ErrBusy):
		code, message = "busy", "另一个请求正在处理，请稍后核对结果，不要重复写操作"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code, message = "timeout", "查询超时或取消，请缩小查询范围；禁止自动重试写操作"
	}
	raw, _ := json.Marshal(map[string]string{"status": "error", "code": code, "message": message})
	return string(raw)
}
