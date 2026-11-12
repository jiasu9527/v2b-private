package aiops

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
)

const entryRuleDeliveryCorePrompt = entryInvestigationPrompt + `入口排序优先用entry_rules.order读取全局可见行和revision，再用entry_rules.move传items（要移动的A/B叶子）、after（例如用户指定的特殊置顶规则）及expected_revision。二分叶子用kind=split_group及叶子ID，不用所属policy_id；普通行用kind=policy，两种ID不能混用。move由后端保留未选行相对顺序；不要把内鬼假地址等特殊规则移动或覆盖。旧entry_rules.sort是全表替换，不能仅传A/B，必须含全部普通规则和全部当前叶子含停用项，不能传父容器或非叶子。参数无效且明确未执行时先查目录并修正，不需要用户手动排序；版本冲突则重读核对，禁止原样重试。成功后回读并按真实用户、节点、UA模拟，不能只凭受理就宣称生效。用户要求把拉取过某历史故障入口IP的人单独建组/二分时，使用目录entry_split.preview指定history_host及时间范围，再用entry_split.create原样传回history_host、history_from/from、history_to/to、snapshot_hash及明确的共同members、A/B入口；这是后台历史拉取IP页面能力，不是entry_evidence按单来源档案二分，也不是最近任意订阅拉取。先查目录schema，不能因旧知识宣称不存在。历史hash不是持久名单快照ID，不能伪造。创建独立组无需先删旧组或移出合集；新组不会自动继承原UA，必须如实核对共同节点、UA条件及排序，保留特殊置顶规则并模拟验证，无法表达差异时说明边界，不得声称原UA已继承。入口规则控制订阅下发：每个节点只检查members包含它的规则，按优先级取第一条命中规则；同一条规则的conditions是AND，不能把ID下界和上界拆成两条放行规则。用户ID支持eq/gt/gte/lt/lte/in/between；“ID十万以内”包含100000，用lte的value=100000或between的min=1,max=100000，一条条件即可；“小于十万”用lt，不要混淆边界。新建独立节点只给指定ID范围下发时，先保持show=0，检查其权限组和所有引用该节点的规则，用client_entry_only=1加一条范围白名单，仅把目标节点加入members；沿用节点原地址时action=original，确需替换入口才用override。规则准备并核对后才开启show=1；无需机械增加一条反向hide。已有节点需要保留现有入口分配时，可对目标节点用一条高优先级hide排除范围外用户（例如gt 100000），范围内继续原规则；不要把新节点白名单方案强加到原有入口并覆盖其地址。不要对其他节点增加隐藏规则，不要擅自清空条件、移出合集或删除现有规则。client_entry_only仍可能被其他original/override规则授权，必须核对目标的全部引用和先后次序；show=0或权限组不符时规则不会越权下发。保存后读取规则并用实际存在的边界内外用户及对应UA模拟验证，不能把用户ID当邮箱或编造用户。ID范围二分仍只支持一个between加可选UA，不要假定任意比较条件都可二分。入口规则不改变节点端鉴权名单，订阅不下发不代表持有旧连接资料的用户一定无法连接，不能宣称已完成节点连接权限隔离。`

const entryGroupRetirementPrompt = `清理已提取用户的旧固定分组、回收入口IP时，跨多个旧组提取实际拉取用户时，先用entry_split.preview的history_group_ids取历史下发用户并集；排除拉过故障IP的整个人用history_exclude_hosts，排除不局限来源组。预览和创建原样传递规范化筛选、时间及snapshot_hash，不用分配名单替代实际下发，不累加各组人数，不让模型复制用户ID。然后先查entry_groups.audit：group_ids是旧叶子ID，replacement_group_ids是已建新组叶子ID；分页读取并沿用expected_revision，使用明确from/to窗口。分配人数、完整成功响应的实际下发人数、被新组覆盖人数和剩余人数分开报告；总人数按user_id取并集，不能相加各组人数，不能把日志缺失说成从未下发/从未换IP。名单相交只是覆盖分析，不代表节点、UA和排序已经生效。使用entry_groups.retire_preview把指定旧叶子及新组冻结在服务端，不需要模型复制上千个用户ID；这是持久化预览，走propose_panel_operation，预览不删除入口。用户已明确“其余走保底”时，在当前会话权限内沿用该授权，传allow_fallback=true和已核实的fallback_policy_id，不要继续只按零剩余人数选组或反复要求相同确认；未明确允许回落则保持false。后台会核对替代组及保底的条件、节点覆盖和配置变化；保留的专线、VIP、假地址可能先命中，不可把所有剩余用户都称为直接命中保底。读取预览影响和阻塞原因后，用entry_groups.retire仅提交preview_id、preview_hash和唯一idempotency_key执行；同一次执行保留同一key，不用更换key绕过冲突。一次事务只清选中叶子及空祖先/空父规则，保留未选兄弟组、其他规则、原排序、IP库存与健康记录；不要用entry_rules.delete整条删除代替叶子清理，不要删除IP池条目当作释放占用。结果未知先用entry_groups.retirement的id=preview_id读回执（不是operation_id），再查询audit/IP池核对，不能重放未知操作；过期或状态变化需核对后重新预览。完成报告按回执区分解除占用IP、仍被引用IP、健康可用IP和故障IP，不能把故障资源说成健康备用。旧订阅客户端需更新订阅才取得新分配；这些回收IP可能已暴露过，不能称为从未下发的新IP。`

const entryRuleDeliveryPrompt = entryRuleDeliveryCorePrompt + "\n" + entryGroupRetirementPrompt

func entryRuleToolDefinitions() []ToolDefinition {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string, min, max int64) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max, "description": desc}
	}
	kind := map[string]any{"type": "string", "enum": []string{"policy", "split_group"}, "description": "policy=标准规则；split_group=二分叶子，不接受二分父规则"}
	condition := map[string]any{"type": "object", "properties": map[string]any{
		"field":    map[string]any{"type": "string", "enum": []string{"user_id", "email", "plan_id", "registration_days", "ua"}},
		"operator": str("数值eq等于/gt大于/gte大于等于/lt小于/lte小于等于/in指定值/between含两端；user_id十万以内用lte value=100000或between min=1 max=100000；邮箱in；UA contains_any/excludes_any/empty/not_empty"),
		"value":    map[string]any{"type": []string{"integer", "string"}}, "values": map[string]any{"type": "array", "items": map[string]any{"type": []string{"integer", "string"}}}, "min": map[string]any{"type": "integer"}, "max": map[string]any{"type": "integer"},
	}, "required": []string{"field", "operator"}, "additionalProperties": false}
	member := map[string]any{"type": "object", "properties": map[string]any{"server_type": str("节点协议，例如vless/vmess/trojan"), "server_id": integer("节点ID", 1, 2147483647)}, "required": []string{"server_type", "server_id"}, "additionalProperties": false}
	changes := map[string]any{
		"name": str("规则名称"), "entry_host": str("单个域名或IP"), "resolve_entry_host": integer("0下发域名，1解析后下发IP", 0, 1),
		"enabled": integer("0停用、1启用；二分叶子影响整个父规则", 0, 1), "sort": integer("优先级排序值，越小越先匹配；相同排序值按现有ID顺序", 0, 2147483647), "remarks": str("备注；二分叶子影响整个父规则"),
		"action":            map[string]any{"type": "string", "enum": []string{"override", "original", "hide"}, "description": "仅标准规则；override替换目标节点入口，original按原节点地址下发，hide不下发；逐节点首条命中生效。original/hide会清空入口host和解析设置，在审批卡显示"},
		"conditions":        map[string]any{"type": "array", "maxItems": 100, "items": condition, "description": "仅标准规则；完整替换所有AND条件，必须先读取全部条件和值；清空为[]会匹配所选节点范围全部合资格用户，需同时confirm_match_all=true；不要把摘要当完整条件"},
		"confirm_match_all": map[string]any{"type": "boolean", "description": "仅标准规则且必须与conditions一起提交；把非空条件清空为[]时须为true，明确确认扩大匹配范围。已有空条件原样编辑不需确认"},
		"members":           map[string]any{"type": "array", "minItems": 1, "maxItems": 500, "items": member, "description": "完整替换生效节点列表，数组顺序即节点顺序；二分叶子影响同父规则全部叶子"},
	}
	out := []ToolDefinition{}
	add := func(name, desc string, p map[string]any, required ...string) {
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false})
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: desc, Parameters: raw}})
	}
	add("entry_rule_detail", "读取单个标准入口规则或二分叶子可编辑配置与revision。首读取得revision，展开条件/节点及翻页时携带它；offset>0必须携带revision。版本变化返回conflict，重新从summary读取，禁止拼接不同版本。条件values_truncated时用condition_values读取全部值，不返回订阅凭据。", map[string]any{"kind": kind, "target_id": integer("规则或叶子ID", 1, 2147483647), "revision": str("首读可省略；后续使用首次返回的64字符revision，过期拒绝返回混合分页"), "section": map[string]any{"type": "string", "enum": []string{"summary", "conditions", "condition_values", "members"}, "description": "默认summary返回标量与计数；conditions按条件分页；condition_values按一个条件的values分页；members按节点分页"}, "condition_index": integer("条件序号，从0开始，仅condition_values使用", 0, 100000), "limit": integer("分页条数，默认20最多50", 1, 50), "offset": integer("第一页0，后续按next_offset", 0, 100000)}, "kind", "target_id")
	add("propose_entry_rule_update", "编辑入口规则，按会话权限执行。未提供字段保留；conditions/members为完整替换。二分叶子可改名称/入口/解析/排序以及共享状态/备注/生效节点，不能改变固定名单来源条件；合集入口保持统一。使用entry_rule_detail最新revision；完全访问自动执行，逐次确认模式等待批准。", map[string]any{"kind": kind, "target_id": integer("规则或叶子ID", 1, 2147483647), "revision": str("entry_rule_detail返回的64字符revision"), "changes": map[string]any{"type": "object", "properties": changes, "additionalProperties": false}, "reason": str("修改原因及用户影响")}, "kind", "target_id", "revision", "changes", "reason")
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
		return ToolOutcome{Content: string(raw), Pending: true, ActionID: action.ID}, nil
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
	if !oneOf(a.Section, "summary", "conditions", "condition_values", "members") || !validReadRevision(a.Revision, a.Offset) {
		return ToolOutcome{}, ErrValidation
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
	// Compare while the rule/topology locks are still held. Never return a page
	// from a new configuration under a caller's previous revision.
	if a.Revision != "" && a.Revision != v.Revision {
		return ToolOutcome{}, ErrConflict
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

func validReadRevision(revision string, offset int) bool {
	if revision == "" {
		return offset == 0
	}
	decoded, err := hex.DecodeString(revision)
	return err == nil && len(decoded) == 32 && revision == strings.ToLower(revision)
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
