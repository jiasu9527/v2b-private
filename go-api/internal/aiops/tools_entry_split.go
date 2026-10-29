package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"forest/go-api/internal/admin"
)

func entrySplitToolDefinitions() []ToolDefinition {
	integer := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "description": description}
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	out := []ToolDefinition{}
	add := func(name, description string, props map[string]any, required ...string) {
		if required == nil {
			required = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: description, Parameters: raw}})
	}
	add("entry_split_options", "列出已被所有在线探针确认故障且可手动二分的固定名单叶子，返回target_id和revision；自动二分关闭也可手动。指定target_id时读取该叶子的当前配置及revision，无须已故障，用于开关自动二分。标准规则转换请先entry_rule_detail。空列表不代表规则不能明确指定二分。", map[string]any{"policy_id": integer("可选过滤规则ID"), "target_id": integer("可选精确入口监测目标ID"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000, "default": 0}})
	add("propose_entry_manual_split", "明确手动对故障固定名单叶子二分：忽略auto_split_enabled是否开启，不会顺带开启永久自动化。由原故障二分队列核实所有在线探针新鲜失败、至少2用户并原子领取2个健康备用IP。审批后返回operation_id，须查询最终结果；缺IP会等待，不能把排队当成功。若无需故障判定而直接指定规则二分，使用propose_entry_split。", map[string]any{"target_id": integer("entry_split_options返回的监测目标ID"), "revision": str("刚读取的64位revision"), "reason": str("明确操作原因与影响")}, "target_id", "revision", "reason")
	add("entry_split_operation", "查询故障二分持久化操作结果，包含pending原因、备用IP不足、子组ID及完成状态。pending表示仍等待，succeeded才表示已二分；不得重复提交。", map[string]any{"operation_id": integer("手动二分动作输出的operation_id")}, "operation_id")
	add("propose_entry_auto_split", "只开启或关闭指定固定叶子的自动二分开关，不立即二分。不应为了执行一次明确手动二分而开启此开关。沿用后台行为：切换会清空目标检测状态、取消旧pending事故，重新开启后重新累计失败。", map[string]any{"target_id": integer("入口监测目标ID"), "revision": str("entry_split_options指定target_id返回的revision"), "enabled": map[string]any{"type": "boolean"}, "reason": str("用户要求永久开关的原因")}, "target_id", "revision", "enabled", "reason")
	add("propose_entry_split", "按用户明确指令直接二分标准规则或当前固定名单叶子，无须自动二分开关开启或探针已失败。标准规则仅支持一个用户ID范围加可选UA且无额外下发节点，保留UA条件；叶子将原固定名单均分，至少2人。先entry_rule_detail获得kind,target_id,revision。指定use_backup_ip_pool=true原子领取2个健康备用IP，或false同时指定两个不同入口；不能用换IP代替二分。会保留原规则、建立两个子组，合集成员按后台原行为移出合集。", map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"policy", "split_group"}}, "target_id": integer("标准规则ID或叶子组ID，非监测target_id"), "revision": str("entry_rule_detail返回的revision"), "use_backup_ip_pool": map[string]any{"type": "boolean"}, "entry_host_a": str("不用备用池时指定第一个域名/IP"), "entry_host_b": str("不用备用池时指定第二个域名/IP"), "reason": str("明确二分原因与影响")}, "kind", "target_id", "revision", "use_backup_ip_pool", "reason")
	return out
}

type entrySplitToolArgs struct {
	PolicyID        int64  `json:"policy_id"`
	TargetID        int64  `json:"target_id"`
	OperationID     int64  `json:"operation_id"`
	Limit           int    `json:"limit"`
	Offset          int    `json:"offset"`
	Kind            string `json:"kind"`
	Revision        string `json:"revision"`
	Enabled         *bool  `json:"enabled"`
	UseBackupIPPool *bool  `json:"use_backup_ip_pool"`
	EntryHostA      string `json:"entry_host_a"`
	EntryHostB      string `json:"entry_host_b"`
	Reason          string `json:"reason"`
}

func (s *Service) callEntrySplitTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var a entrySplitToolArgs
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.PolicyID < 0 || a.TargetID < 0 {
		return ToolOutcome{}, ErrValidation
	}
	if a.Limit == 0 {
		a.Limit = 20
	}
	if a.Limit > 50 {
		a.Limit = 50
	}
	if a.Limit < 1 || a.Offset < 0 || a.Offset > 10000 {
		return ToolOutcome{}, ErrValidation
	}
	var value any
	switch call.Function.Name {
	case "entry_split_options":
		service, ok := s.panel.(interface {
			ListClientEntryManualAutoSplitOptions(context.Context) ([]admin.ClientEntryManualAutoSplitOption, error)
			ReadAIEntrySplitTarget(context.Context, int64) (admin.AIEntrySplitTarget, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		if a.TargetID > 0 {
			target, err := service.ReadAIEntrySplitTarget(ctx, a.TargetID)
			if err != nil {
				return entrySplitToolError(err)
			}
			if a.PolicyID > 0 && target.PolicyID != a.PolicyID {
				return ToolOutcome{}, ErrNotFound
			}
			value = target
		} else {
			options, err := service.ListClientEntryManualAutoSplitOptions(ctx)
			if err != nil {
				return entrySplitToolError(err)
			}
			filtered := []admin.ClientEntryManualAutoSplitOption{}
			for _, option := range options {
				if a.PolicyID == 0 || option.PolicyID == a.PolicyID {
					filtered = append(filtered, option)
				}
			}
			items := []map[string]any{}
			for _, option := range window(filtered, a.Limit, a.Offset) {
				target, err := service.ReadAIEntrySplitTarget(ctx, option.TargetID)
				if err != nil {
					return entrySplitToolError(err)
				}
				items = append(items, map[string]any{"target": target, "confirmed_failed_probes": option.FailedProbeCount, "online_probes": option.OnlineProbeCount, "manual_split_available": true})
			}
			value = boundedPage(items, int64(len(filtered)), a.Limit, a.Offset, maxToolOutputBytes)
		}
	case "entry_split_operation":
		if a.OperationID <= 0 {
			return ToolOutcome{}, ErrValidation
		}
		service, ok := s.panel.(interface {
			GetAIEntrySplitOperation(context.Context, int64) (admin.AIEntrySplitOperation, error)
		})
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		operation, err := service.GetAIEntrySplitOperation(ctx, a.OperationID)
		if err != nil {
			return entrySplitToolError(err)
		}
		value = operation
	case "propose_entry_manual_split", "propose_entry_auto_split", "propose_entry_split":
		if a.TargetID <= 0 || len(a.Revision) != 64 || strings.TrimSpace(a.Reason) == "" || len([]rune(a.Reason)) > 500 {
			return ToolOutcome{}, ErrValidation
		}
		preparer, ok := s.panel.(interface{ PrepareAIEntrySplit(context.Context) error })
		if !ok {
			return ToolOutcome{}, ErrUnavailable
		}
		if err := preparer.PrepareAIEntrySplit(ctx); err != nil {
			return ToolOutcome{}, safeAIError(err)
		}
		p := entrySplitActionPayload{TargetID: a.TargetID, Revision: a.Revision, Enabled: a.Enabled}
		kind, targetType := "entry_manual_split", "monitor_target"
		if call.Function.Name == "propose_entry_auto_split" {
			kind = "entry_auto_split"
			if a.Enabled == nil {
				return ToolOutcome{}, ErrValidation
			}
		}
		if call.Function.Name == "propose_entry_split" {
			if a.UseBackupIPPool == nil || (a.Kind != "policy" && a.Kind != "split_group") {
				return ToolOutcome{}, ErrValidation
			}
			kind, targetType = "entry_split", a.Kind
			p.Selection = &admin.AIEntryDirectSplit{Kind: a.Kind, TargetID: a.TargetID, Revision: a.Revision, UseBackupIPPool: *a.UseBackupIPPool, EntryHostA: a.EntryHostA, EntryHostB: a.EntryHostB}
		}
		raw, _ := json.Marshal(p)
		action, err := s.proposeAction(ctx, actorID, sessionID, call.ID, actionSpec{Kind: kind, TargetID: a.TargetID, TargetType: targetType, Payload: raw, Reason: a.Reason})
		if err != nil {
			return entrySplitToolError(err)
		}
		content, _ := json.Marshal(actionToolSummary(action))
		return ToolOutcome{Content: string(content), Pending: true, ActionID: action.ID}, nil
	default:
		return ToolOutcome{}, ErrValidation
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxToolOutputBytes {
		return ToolOutcome{}, ErrUnavailable
	}
	return ToolOutcome{Content: string(raw)}, nil
}

func entrySplitToolError(err error) (ToolOutcome, error) {
	if errors.Is(err, admin.ErrAIEntrySplitInvalid) {
		raw, _ := json.Marshal(map[string]string{"status": "error", "code": "split_unavailable", "message": err.Error()})
		return ToolOutcome{Content: string(raw)}, nil
	}
	if errors.Is(err, admin.ErrAIEntrySplitConflict) || errors.Is(err, admin.ErrAIEntryRuleConflict) {
		return ToolOutcome{}, ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ToolOutcome{}, ErrNotFound
	}
	return ToolOutcome{}, safeAIError(err)
}
