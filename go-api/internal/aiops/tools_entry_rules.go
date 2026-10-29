package aiops

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
)

func entryRuleToolDefinitions() []ToolDefinition {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string, min, max int64) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max, "description": desc}
	}
	kind := map[string]any{"type": "string", "enum": []string{"policy", "split_group"}, "description": "policy=标准规则；split_group=二分叶子，不接受二分父规则"}
	condition := map[string]any{"type": "object", "properties": map[string]any{
		"field":    map[string]any{"type": "string", "enum": []string{"user_id", "email", "plan_id", "registration_days", "ua"}},
		"operator": str("数值eq/gte/lte/gt/lt/in/between；邮箱in；UA contains_any/excludes_any/empty/not_empty"),
		"value":    map[string]any{"type": []string{"integer", "string"}}, "values": map[string]any{"type": "array", "items": map[string]any{"type": []string{"integer", "string"}}}, "min": map[string]any{"type": "integer"}, "max": map[string]any{"type": "integer"},
	}, "required": []string{"field", "operator"}, "additionalProperties": false}
	member := map[string]any{"type": "object", "properties": map[string]any{"server_type": str("节点协议，例如vless/vmess/trojan"), "server_id": integer("节点ID", 1, 2147483647)}, "required": []string{"server_type", "server_id"}, "additionalProperties": false}
	changes := map[string]any{
		"name": str("规则名称"), "entry_host": str("单个域名或IP"), "resolve_entry_host": integer("0下发域名，1解析后下发IP", 0, 1),
		"enabled": integer("0停用、1启用；二分叶子影响整个父规则", 0, 1), "sort": integer("优先级排序值，越小越先匹配；相同排序值按现有ID顺序", 0, 2147483647), "remarks": str("备注；二分叶子影响整个父规则"),
		"action":     map[string]any{"type": "string", "enum": []string{"override", "original", "hide"}, "description": "仅标准规则；original/hide会清空入口host和解析设置，在审批卡显示"},
		"conditions": map[string]any{"type": "array", "maxItems": 100, "items": condition, "description": "仅标准规则；完整替换所有AND条件，必须先读取全部条件和值；[]表示匹配所有用户。不要把摘要当完整条件"},
		"members":    map[string]any{"type": "array", "minItems": 1, "maxItems": 500, "items": member, "description": "完整替换生效节点列表，数组顺序即节点顺序；二分叶子影响同父规则全部叶子"},
	}
	out := []ToolDefinition{}
	add := func(name, desc string, p map[string]any, required ...string) {
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false})
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: desc, Parameters: raw}})
	}
	add("entry_rule_detail", "读取单个标准入口规则或二分叶子可编辑配置与revision。条件值、节点分页，不返回额外订阅链接凭据。修改前读取；每页revision必须相同；条件values_truncated时用condition_values读取全部值。", map[string]any{"kind": kind, "target_id": integer("规则或叶子ID", 1, 2147483647), "section": map[string]any{"type": "string", "enum": []string{"summary", "conditions", "condition_values", "members"}, "description": "默认summary返回标量与计数；conditions按条件分页；condition_values按一个条件的values分页；members按节点分页"}, "condition_index": integer("条件序号，从0开始，仅condition_values使用", 0, 100000), "limit": integer("分页条数，默认20最多50", 1, 50), "offset": integer("第一页0，后续按next_offset", 0, 100000)}, "kind", "target_id")
	add("propose_entry_rule_update", "创建入口规则编辑待确认卡。未提供字段保留；conditions/members为完整替换。二分叶子可改名称/入口/解析/排序以及共享状态/备注/生效节点，不能改变固定名单来源条件；合集入口保持统一。使用entry_rule_detail最新revision，不执行修改。", map[string]any{"kind": kind, "target_id": integer("规则或叶子ID", 1, 2147483647), "revision": str("entry_rule_detail返回的64位revision"), "changes": map[string]any{"type": "object", "properties": changes, "additionalProperties": false}, "reason": str("修改原因及用户影响")}, "kind", "target_id", "revision", "changes", "reason")
	return out
}

type entryRuleToolArgs struct {
	Kind           string                     `json:"kind"`
	TargetID       int64                      `json:"target_id"`
	Revision       string                     `json:"revision"`
	Section        string                     `json:"section"`
	ConditionIndex int                        `json:"condition_index"`
	Limit          int                        `json:"limit"`
	Offset         int                        `json:"offset"`
	Changes        map[string]json.RawMessage `json:"changes"`
	Reason         string                     `json:"reason"`
}

func (s *Service) callEntryRuleTool(ctx context.Context, actor, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var a entryRuleToolArgs
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.TargetID <= 0 || (a.Kind != "policy" && a.Kind != "split_group") {
		return ToolOutcome{}, ErrValidation
	}
	if call.Function.Name == "propose_entry_rule_update" {
		payload, _ := json.Marshal(entryRuleUpdatePayload{Kind: a.Kind, TargetID: a.TargetID, Revision: a.Revision, Changes: a.Changes})
		action, err := s.proposeAction(ctx, actor, sessionID, call.ID, actionSpec{Kind: "entry_rule_update", TargetID: a.TargetID, TargetType: a.Kind, Payload: payload, Reason: a.Reason})
		if err != nil {
			return ToolOutcome{}, err
		}
		raw, _ := json.Marshal(actionToolSummary(action))
		return ToolOutcome{Content: string(raw), Pending: true}, nil
	}
	if call.Function.Name != "entry_rule_detail" {
		return ToolOutcome{}, ErrValidation
	}
	if a.Limit == 0 {
		a.Limit = 20
	}
	if a.Limit > 50 {
		a.Limit = 50
	}
	if a.Limit < 1 || a.Offset < 0 || a.Offset > 100000 || a.ConditionIndex < 0 {
		return ToolOutcome{}, ErrValidation
	}
	if a.Section == "" {
		a.Section = "summary"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolOutcome{}, err
	}
	defer tx.Rollback()
	v, err := admin.LoadAIEntryRule(ctx, tx, a.Kind, a.TargetID)
	if err != nil {
		return ToolOutcome{}, entryRuleError(err)
	}
	if err = tx.Commit(); err != nil {
		return ToolOutcome{}, err
	}
	raw, _ := json.Marshal(v)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "conditions")
	delete(fields, "members")
	fields["conditions_count"] = len(v.Conditions)
	fields["members_count"] = len(v.Members)
	result := map[string]any{"rule": fields, "section": a.Section}
	budget := maxToolOutputBytes - jsonSize(result) - 128
	switch a.Section {
	case "summary":
	case "conditions":
		items := []map[string]any{}
		for i, c := range v.Conditions {
			items = append(items, projectEntryEditCondition(c, i))
		}
		result["page"] = boundedPage(window(items, a.Limit, a.Offset), int64(len(items)), a.Limit, a.Offset, budget)
	case "condition_values":
		if a.ConditionIndex >= len(v.Conditions) {
			return ToolOutcome{}, ErrValidation
		}
		c := v.Conditions[a.ConditionIndex]
		result["condition_index"] = a.ConditionIndex
		result["page"] = boundedPage(window(c.Values, a.Limit, a.Offset), int64(len(c.Values)), a.Limit, a.Offset, budget)
	case "members":
		result["page"] = boundedPage(window(v.Members, a.Limit, a.Offset), int64(len(v.Members)), a.Limit, a.Offset, budget)
	default:
		return ToolOutcome{}, ErrValidation
	}
	raw, err = json.Marshal(result)
	if err != nil || len(raw) > maxToolOutputBytes {
		return ToolOutcome{}, ErrUnavailable
	}
	return ToolOutcome{Content: string(raw)}, nil
}

func projectEntryEditCondition(c cliententry.Condition, index int) map[string]any {
	raw, _ := json.Marshal(c)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["index"] = index
	if len(c.Values) > 0 || (c.Field == "email" && c.Operator == "in") {
		count := len(c.Values)
		out["values_count"] = count
		out["values_truncated"] = count > 20
		if count > 20 {
			out["values"] = c.Values[:20]
		}
	}
	return out
}
