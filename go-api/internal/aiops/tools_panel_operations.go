package aiops

import (
	"context"
	"encoding/json"
	"strings"
)

func panelOperationToolDefinitions() []ToolDefinition {
	definitions := []ToolDefinition{}
	add := func(name, description string, properties map[string]any, required ...string) {
		if required == nil {
			required = []string{}
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
		definitions = append(definitions, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: description, Parameters: schema}})
	}
	text := func(description string) any { return map[string]any{"type": "string", "description": description} }
	integer := func(min, max int) any { return map[string]any{"type": "integer", "minimum": min, "maximum": max} }
	add("panel_operations_list", "查询当前可用的后台业务操作目录。按category或query寻找能力，不可猜测名称；读写操作均列出，支付配置永不开放。按next_offset分页。", map[string]any{"category": text("可选业务分类"), "query": text("可选名称或描述关键词"), "limit": integer(1, 50), "offset": integer(0, 10000)})
	add("panel_operation_describe", "读取一个后台操作的完整参数schema、读写属性与权限。调用前先查看schema，参数放入parameters对象；不能传任意URL或SQL。", map[string]any{"operation": text("目录中准确的operation名称")}, "operation")
	properties := func() map[string]any {
		return map[string]any{"operation": text("已查看schema的operation名称"), "parameters": map[string]any{"type": "object", "description": "严格按该operation的schema传参；无参数时传{}"}}
	}
	add("panel_query", "执行目录中只读的后台查询，不能执行写操作。返回数据经敏感字段处理；大结果返回result_id和正文片段，必须用panel_result_read继续读取。", properties(), "operation", "parameters")
	p := properties()
	p["reason"] = text("变更原因与影响；不能包含密码、密钥或其他凭据")
	add("propose_panel_operation", "提议目录中的后台写操作。逐次确认模式等待审核，完全访问模式自动执行；严格使用参数schema，不猜测参数。参数加密保存，执行结果可通过result_id续读。失败或结果未知不得自动重试。支付配置不允许，即使完全访问也不能调用。", p, "operation", "parameters", "reason")
	add("panel_result_read", "读取本会话后台操作的大结果。content_offset与content_limit按Unicode字符，返回next_content_offset作为下一次content_offset直到truncated=false；所有片段拼接后是完整JSON。结果保存30分钟，过期可重新执行只读查询，不能为取结果重复写操作。", map[string]any{"result_id": integer(1, 9007199254740991), "content_offset": integer(0, 9007199254740991), "content_limit": integer(1, 2000)}, "result_id")
	return definitions
}

func (s *Service) callPanelOperationTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var args struct {
		Operation     string          `json:"operation"`
		Parameters    json.RawMessage `json:"parameters"`
		Reason        string          `json:"reason"`
		Category      string          `json:"category"`
		Query         string          `json:"query"`
		Limit         int             `json:"limit"`
		Offset        int             `json:"offset"`
		ResultID      int64           `json:"result_id"`
		ContentOffset int64           `json:"content_offset"`
		ContentLimit  int             `json:"content_limit"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ToolOutcome{}, ErrValidation
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	if args.ContentLimit == 0 {
		args.ContentLimit = 2000
	}
	if args.Limit < 1 || args.Offset < 0 || args.Offset > 10000 || args.ContentLimit < 1 || args.ContentLimit > 2000 || len(args.Query) > 200 || len(args.Category) > 100 {
		return ToolOutcome{}, ErrValidation
	}
	if err := s.ensureSchema(ctx); err != nil {
		return ToolOutcome{}, err
	}
	actor, err := panelActor(ctx, s.db, actorID, false)
	if err != nil {
		return ToolOutcome{}, err
	}
	if call.Function.Name == "panel_result_read" {
		out, err := s.readPanelResult(ctx, actorID, sessionID, args.ResultID, args.ContentOffset, args.ContentLimit)
		return ToolOutcome{Content: out}, err
	}
	runtime := s.getPanelOperations()
	if runtime == nil {
		return ToolOutcome{}, ErrUnavailable
	}
	if call.Function.Name == "panel_operations_list" {
		operations := sortedPanelOperations(runtime, actor, args.Category, strings.TrimSpace(args.Query))
		summaries := []map[string]any{}
		for _, operation := range window(operations, args.Limit, args.Offset) {
			summaries = append(summaries, map[string]any{"name": operation.Name, "category": operation.Category, "description": operation.Description, "mutating": operation.Mutating, "admin_only": operation.AdminOnly})
		}
		raw, err := json.Marshal(boundedPage(summaries, int64(len(operations)), args.Limit, args.Offset, maxToolOutputBytes))
		return ToolOutcome{Content: string(raw)}, err
	}
	operation, err := findPanelOperation(runtime, args.Operation)
	if err != nil {
		return ToolOutcome{}, err
	}
	if operation.AdminOnly && !actor.IsAdmin {
		return ToolOutcome{}, ErrForbidden
	}
	if operation.AdminOnly {
		if err := panelAdminSession(ctx, s.db, sessionID, false); err != nil {
			return ToolOutcome{}, err
		}
	}
	if call.Function.Name == "panel_operation_describe" {
		raw, err := json.Marshal(operation)
		if len(raw) > maxToolOutputBytes {
			return ToolOutcome{}, ErrUnavailable
		}
		return ToolOutcome{Content: string(raw)}, err
	}
	parameters, err := normalizedPanelParameters(args.Parameters)
	if err != nil {
		return ToolOutcome{}, err
	}
	if (call.Function.Name == "panel_query" && operation.Mutating) || (call.Function.Name == "propose_panel_operation" && !operation.Mutating) {
		return ToolOutcome{}, ErrForbidden
	}
	if err = runtime.Validate(ctx, actor, operation.Name, parameters); err != nil {
		return ToolOutcome{}, safeAIError(err)
	}
	if call.Function.Name == "panel_query" {
		// Re-read immediately before invoking the internal authenticated adapter.
		actor, err = panelActor(ctx, s.db, actorID, false)
		if err != nil {
			return ToolOutcome{}, err
		}
		if operation.AdminOnly && !actor.IsAdmin {
			return ToolOutcome{}, ErrForbidden
		}
		if operation.AdminOnly {
			if err = panelAdminSession(ctx, s.db, sessionID, false); err != nil {
				return ToolOutcome{}, err
			}
		}
		data, err := runtime.Execute(ctx, actor, operation.Name, parameters)
		if err != nil {
			return ToolOutcome{}, safeAIError(err)
		}
		out, err := s.savePanelResult(ctx, actorID, sessionID, operation.Name, data, false)
		return ToolOutcome{Content: out}, err
	}
	if call.Function.Name != "propose_panel_operation" {
		return ToolOutcome{}, ErrValidation
	}
	_, secrets := previewPanelParameters(parameters)
	for _, secret := range secrets {
		args.Reason = strings.ReplaceAll(args.Reason, secret, "[REDACTED]")
	}
	payload, _ := json.Marshal(panelOperationPayload{Operation: operation.Name, Parameters: parameters})
	action, err := s.proposeAction(ctx, actorID, sessionID, call.ID, actionSpec{Kind: "panel_operation", TargetType: operation.Name, Payload: payload, Reason: args.Reason})
	if err != nil {
		return ToolOutcome{}, err
	}
	raw, _ := json.Marshal(actionToolSummary(action))
	return ToolOutcome{Content: string(raw), Pending: true, ActionID: action.ID}, nil
}
