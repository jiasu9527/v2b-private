package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func userToolDefinitions() []ToolDefinition {
	fieldInt := func(description string, nullable bool, max int64) map[string]any {
		var typ any = "integer"
		if nullable {
			typ = []string{"integer", "null"}
		}
		return map[string]any{"type": typ, "minimum": 0, "maximum": max, "description": description}
	}
	page := map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000, "default": 0}}
	changes := map[string]any{
		"plan_id":            fieldInt("目标套餐ID；null移除套餐。选择套餐默认同步权限组、流量配额、设备及限速，可同时显式覆盖这些字段。不会重置已用流量或修改有效期。", true, 2147483647),
		"group_id":           fieldInt("节点权限组ID（不是邮箱指定入口组），null清空", true, 2147483647),
		"transfer_enable":    fieldInt("总流量配额，单位字节，1 GiB=1073741824", false, aiMaxSafeInteger),
		"expired_at":         fieldInt("到期Unix秒时间戳；null永久", true, 253402300799),
		"device_limit":       fieldInt("设备数上限；null或0不限", true, 2147483647),
		"speed_limit":        fieldInt("限速Mbps；null或0不限", true, 2147483647),
		"banned":             fieldInt("1封禁，0解封", false, 1),
		"balance":            fieldInt("余额绝对值，单位分；不是增量", false, 2147483647),
		"commission_balance": fieldInt("佣金余额绝对值，单位分；不是增量", false, 2147483647),
		"discount":           fieldInt("专属折扣百分比，null使用默认", true, 100),
		"commission_rate":    fieldInt("返佣百分比，null使用默认", true, 100),
		"commission_type":    fieldInt("返佣类型0系统默认/1循环/2首次", false, 2),
		"remarks":            map[string]any{"type": []string{"string", "null"}, "maxLength": 4000, "description": "管理员备注，最多4000字节；null清空"},
	}
	for _, key := range []string{"plan_id", "group_id", "expired_at"} {
		changes[key].(map[string]any)["minimum"] = 1
	}
	out := []ToolDefinition{}
	add := func(name, description string, properties map[string]any, required []string) {
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: description, Parameters: raw}})
	}
	add("plans_list", "查询可分配套餐及对应权限组、流量bytes、设备/限速；含隐藏套餐。按next_offset分页。", page, []string{})
	add("groups_list", "查询节点权限组ID和名称，供修改用户权限组使用；不同于邮箱指定入口规则。按next_offset分页。", page, []string{})
	add("user_detail", "按准确用户ID查询普通用户可修改设置、余额分、流量字节和备注；不含角色或任何密码/订阅凭据。", map[string]any{"user_id": map[string]any{"type": "integer", "minimum": 1}}, []string{"user_id"})
	add("propose_user_update", "为指定普通用户创建修改审批卡；支持套餐、权限组、配额、有效期、限速、设备、封禁、余额、折扣返佣和备注。先查user_detail核对。金额/配额为绝对值；reset_usage=true明确清零上下行流量。完全访问自动执行，逐次确认模式等待批准。", map[string]any{"user_id": map[string]any{"type": "integer", "minimum": 1}, "changes": map[string]any{"type": "object", "properties": changes, "additionalProperties": false}, "reset_usage": map[string]any{"type": "boolean", "description": "只在操作人要求重置已用流量时传true"}, "reason": map[string]any{"type": "string", "maxLength": 500}}, []string{"user_id", "changes", "reason"})
	return out
}

type aiUserToolArgs struct {
	Limit      int                        `json:"limit"`
	Offset     int                        `json:"offset"`
	UserID     int64                      `json:"user_id"`
	Changes    map[string]json.RawMessage `json:"changes"`
	ResetUsage bool                       `json:"reset_usage"`
	Reason     string                     `json:"reason"`
}

func (s *Service) callUserTool(ctx context.Context, actor, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var a aiUserToolArgs
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
	if a.Limit < 1 || a.Offset < 0 || a.Offset > 10000 {
		return ToolOutcome{}, ErrValidation
	}
	var out any
	switch call.Function.Name {
	case "plans_list", "groups_list":
		table := "v2_server_group"
		projection := `jsonb_build_object('id',id,'name',LEFT(name,256))`
		if call.Function.Name == "plans_list" {
			table = "v2_plan"
			projection = `jsonb_build_object('id',id,'name',LEFT(name,256),'group_id',group_id,'transfer_enable',CASE WHEN transfer_enable<=0 THEN 0 WHEN transfer_enable>=1073741824 THEN transfer_enable ELSE transfer_enable*1073741824 END,'device_limit',device_limit,'speed_limit',speed_limit,'visible',"show")`
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return ToolOutcome{}, err
		}
		defer tx.Rollback()
		var count int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return ToolOutcome{}, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+projection+` FROM `+table+` ORDER BY id LIMIT $1 OFFSET $2`, a.Limit, a.Offset)
		if err != nil {
			return ToolOutcome{}, err
		}
		items := []json.RawMessage{}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return ToolOutcome{}, err
			}
			items = append(items, json.RawMessage(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return ToolOutcome{}, err
		}
		if err = tx.Commit(); err != nil {
			return ToolOutcome{}, err
		}
		out = boundedPage(items, count, a.Limit, a.Offset, maxToolOutputBytes)
	case "user_detail":
		if a.UserID <= 0 {
			return ToolOutcome{}, ErrValidation
		}
		var raw []byte
		err := s.db.QueryRowContext(ctx, `SELECT (`+userSettingsProjection+` || jsonb_build_object('u',u,'d',d,'last_seen_at',t,'remarks',LEFT(remarks,4000),'remarks_truncated',COALESCE(char_length(remarks)>4000,false))) FROM v2_user WHERE id=$1 AND is_admin=0 AND is_staff=0`, a.UserID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ToolOutcome{}, ErrNotFound
		}
		if err != nil {
			return ToolOutcome{}, err
		}
		out = json.RawMessage(raw)
	case "propose_user_update":
		if a.UserID <= 0 {
			return ToolOutcome{}, ErrValidation
		}
		changes, err := normalizeUserChanges(a.Changes)
		if err != nil || (len(changes) == 0 && !a.ResetUsage) {
			return ToolOutcome{}, ErrValidation
		}
		payload, _ := json.Marshal(userUpdatePayload{UserID: a.UserID, Changes: changes, ResetUsage: a.ResetUsage})
		action, err := s.proposeAction(ctx, actor, sessionID, call.ID, actionSpec{Kind: "user_update", TargetID: a.UserID, TargetType: "user", Payload: payload, Reason: a.Reason})
		if err != nil {
			return ToolOutcome{}, err
		}
		raw, _ := json.Marshal(actionToolSummary(action))
		return ToolOutcome{Content: string(raw), Pending: true, ActionID: action.ID}, nil
	default:
		return ToolOutcome{}, ErrValidation
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > maxToolOutputBytes {
		return ToolOutcome{}, ErrUnavailable
	}
	return ToolOutcome{Content: string(raw)}, nil
}
