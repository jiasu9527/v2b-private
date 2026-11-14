package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

func serverToolDefinitions() []ToolDefinition {
	stageItem := map[string]any{"type": "object", "properties": map[string]any{
		"host":        map[string]any{"type": "string", "description": "从操作人本轮原文识别出的 SSH 地址；不要猜测"},
		"port":        map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "可选；原文未给出时省略，后台默认 22"},
		"username":    map[string]any{"type": "string", "description": "从原文识别出的登录用户名"},
		"password":    map[string]any{"type": "string", "description": "原文中的完整密码；使用私钥时省略"},
		"private_key": map[string]any{"type": "string", "description": "原文中的完整私钥；使用密码时省略"},
		"passphrase":  map[string]any{"type": "string", "description": "可选私钥口令"},
		"name":        map[string]any{"type": "string", "description": "可选显示名称"},
		"group":       map[string]any{"type": "string", "description": "可选分组"},
		"fingerprint": map[string]any{"type": "string", "description": "可选已知 SSH SHA256 主机指纹；不确定就省略"},
	}, "required": []string{"host", "username"}, "additionalProperties": false}
	stageRaw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"items": map[string]any{"type": "array", "items": stageItem, "minItems": 1, "maxItems": maxSSHIntakeBatchSize, "description": "从操作人本轮任意格式的原文整理出的服务器；可分批提交，整轮最多 50 台、单次参数最多 32768 字节。每台必须有密码或完整私钥"},
	}, "required": []string{"items"}, "additionalProperties": false})
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"intake_id": map[string]any{"type": "integer", "minimum": 1, "description": "当前管理员在本会话提交SSH资料后由后台生成的intake_id；不可猜测或使用其他会话资料"},
		"name":      map[string]any{"type": "string", "maxLength": 128, "description": "可选服务器名称，未填沿用提交名称或主机地址"},
		"group":     map[string]any{"type": "string", "maxLength": 100, "description": "可选服务器分组名称，未填沿用提交资料中的分组；不包含凭据"},
		"reason":    map[string]any{"type": "string", "minLength": 1, "maxLength": 500, "description": "登记原因及后续用途"},
	}, "required": []string{"intake_id", "reason"}, "additionalProperties": false})
	groupRaw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"server_id": map[string]any{"type": "integer", "minimum": 1, "description": "通过servers_list取得的已登记SSH服务器ID；不是面板节点ID或intake_id"},
		"version":   map[string]any{"type": "integer", "minimum": 1, "description": "servers_list中该服务器当前version；修改前必须重新读取，过期版本会拒绝"},
		"group":     map[string]any{"type": "string", "maxLength": 100, "description": "目标分组名称；空字符串表示移出分组，不能省略"},
		"reason":    map[string]any{"type": "string", "minLength": 1, "maxLength": 500, "description": "修改原因"},
	}, "required": []string{"server_id", "version", "group", "reason"}, "additionalProperties": false})
	return []ToolDefinition{
		sshIntakesToolDefinition(),
		{Type: "function", Function: ToolFunctionDefinition{Name: "ssh_server_stage", Description: "从本轮操作人原文整理 SSH 服务器资料并加密暂存，不连接或登记服务器；原文格式不限。每台须有地址、用户名和完整密码或私钥；不要猜测或跨台借凭据。返回不含凭据的 intake_id，随后逐台用 ssh_server_register 登记。若资料确实不明，向操作人说明缺哪台，不要伪造。", Parameters: stageRaw}},
		{Type: "function", Function: ToolFunctionDefinition{Name: "ssh_server_register", Description: "将本会话已安全收取的SSH资料提议登记到服务器列表，标准模式待审批、完全访问按当前授权执行。批量资料逐个intake_id调用，可用ssh_intakes_list核对进度。只接受intake_id，不接受密码私钥。先无认证读取主机指纹；未提供指纹时固定首次观测值（first_use），提供指纹必须匹配。仅新建、不覆盖已有服务器或凭据。登记成功不等于已登录，返回server_id后可用ssh_status检查。", Parameters: raw}},
		{Type: "function", Function: ToolFunctionDefinition{Name: "propose_server_group", Description: "仅修改已登记SSH服务器的分组标签，不重新登记、不连接服务器、不修改地址、指纹或凭据。先用servers_list核对server_id和version；逐次确认生成审核卡，完全访问按本轮授权执行；成功后重新读取列表核对。", Parameters: groupRaw}},
	}
}

func (s *Service) callServerGroupTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var args struct {
		ServerID int64  `json:"server_id"`
		Version  int64  `json:"version"`
		Group    string `json:"group"`
		Reason   string `json:"reason"`
	}
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if call.Function.Name != "propose_server_group" || d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || args.ServerID <= 0 || args.Version <= 0 || strings.TrimSpace(args.Reason) == "" || len([]rune(args.Reason)) > 500 {
		return ToolOutcome{}, ErrValidation
	}
	group, err := normalizeServerGroup(args.Group)
	if err != nil {
		return ToolOutcome{}, err
	}
	payload, _ := json.Marshal(serverGroupPayload{ServerID: args.ServerID, Version: args.Version, Group: group})
	action, err := s.proposeAction(ctx, actorID, sessionID, call.ID, actionSpec{Kind: "server_group_set", TargetID: args.ServerID, TargetType: "server", Payload: payload, Reason: strings.TrimSpace(args.Reason)})
	if err != nil {
		return ToolOutcome{}, err
	}
	result, _ := json.Marshal(actionToolSummary(action))
	return ToolOutcome{Content: string(result), Pending: true, ActionID: action.ID}, nil
}

func registrationOnlyBatch(calls []ToolCall) bool {
	if len(calls) == 0 || len(calls) > maxSSHIntakeBatchSize {
		return false
	}
	for _, call := range calls {
		if call.Function.Name != "ssh_server_register" {
			return false
		}
	}
	return true
}

func (s *Service) callServerTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var args struct {
		IntakeID int64  `json:"intake_id"`
		Name     string `json:"name"`
		Group    string `json:"group"`
		Reason   string `json:"reason"`
	}
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if call.Function.Name != "ssh_server_register" || d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || args.IntakeID <= 0 || strings.TrimSpace(args.Reason) == "" || len([]rune(args.Reason)) > 500 || len([]rune(strings.TrimSpace(args.Name))) > 128 {
		return ToolOutcome{}, ErrValidation
	}
	group, err := normalizeServerGroup(args.Group)
	if err != nil {
		return ToolOutcome{}, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return ToolOutcome{}, err
	}
	intake, err := loadRegistrationIntake(ctx, s.db, args.IntakeID, sessionID, actorID, false)
	if err != nil {
		return ToolOutcome{}, err
	}
	if intake.RegisteredServerID > 0 {
		server, err := s.registeredIntakeServer(ctx, s.db, intake)
		if err != nil {
			return ToolOutcome{}, err
		}
		return ToolOutcome{Content: serverRegistrationOutput(server.ID, true)}, nil
	}
	if intake.ExpiresAt <= time.Now().Unix() {
		return ToolOutcome{}, ErrConflict
	}
	// Reuse a pending card before probing again. A card from an earlier turn
	// is never silently promoted to the current turn's full-access grant.
	if existing, err := pendingServerRegistration(ctx, s.db, sessionID, actorID, intake.ID); err == nil {
		return serverRegistrationProposalOutcome(existing, call.ID), nil
	} else if !errors.Is(err, ErrNotFound) {
		return ToolOutcome{}, err
	}
	if exists, err := registrationTargetExists(ctx, s.db, intake.Host, intake.Port, intake.Username); err != nil {
		return ToolOutcome{}, err
	} else if exists {
		return serverRegistrationConflict(), nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server`).Scan(&count); err != nil {
		return ToolOutcome{}, err
	}
	if count >= 200 {
		return ToolOutcome{Content: `{"status":"error","code":"server_capacity","message":"服务器列表已达到200台容量，本条未登记；请先整理现有服务器，不要重复提交同一登记"}`}, nil
	}
	observed, err := s.ProbeHostKey(ctx, actorID, HostKeyRequest{Host: intake.Host, Port: intake.Port})
	if err != nil {
		return ToolOutcome{}, safeAIError(err)
	}
	trustSource := "first_use"
	if intake.HostKeySHA256 != "" {
		trustSource = "provided"
		if intake.HostKeySHA256 != observed.Fingerprint {
			return ToolOutcome{Content: `{"status":"error","code":"host_key_mismatch","message":"SSH主机指纹与提交值不匹配，未登记服务器或使用凭据"}`}, nil
		}
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		name = strings.TrimSpace(intake.Name)
	}
	if name == "" {
		name = intake.Host
	}
	if group == "" {
		group = intake.Group
	}
	payload, _ := json.Marshal(serverRegisterPayload{IntakeID: intake.ID, Name: name, Host: intake.Host, Port: intake.Port, Username: intake.Username, HostKeySHA256: observed.Fingerprint, TrustSource: trustSource, HasPassword: intake.PasswordCipher != "", HasPrivateKey: intake.PrivateKeyCipher != "", Group: group})
	action, err := s.proposeAction(ctx, actorID, sessionID, call.ID, actionSpec{Kind: "server_register", TargetID: intake.ID, TargetType: "server_intake", Payload: payload, Reason: args.Reason})
	if err != nil {
		return ToolOutcome{}, err
	}
	return serverRegistrationProposalOutcome(action, call.ID), nil
}

func serverRegistrationProposalOutcome(action Action, callID string) ToolOutcome {
	pending := action.ToolCallID == callID || (registrationCallTurn(callID) > 0 && registrationCallTurn(callID) == registrationCallTurn(action.ToolCallID))
	summary := actionToolSummary(action)
	if !pending {
		summary["message"] = "该资料已有较早轮次的待审核登记操作，请在现有操作卡审核；未扩大旧操作的授权，也未重复登记"
	}
	raw, _ := json.Marshal(summary)
	return ToolOutcome{Content: string(raw), Pending: pending, ActionID: action.ID}
}

func registrationCallTurn(callID string) int64 {
	parts := strings.SplitN(callID, ":", 3)
	if len(parts) != 3 || parts[0] != "turn" {
		return 0
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	return id
}

func pendingServerRegistration(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sessionID, actorID, intakeID int64) (Action, error) {
	return scanAction(q.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE session_id=$1 AND actor_id=$2 AND kind='server_register' AND target_id=$3 AND status='pending' AND expires_at>$4 ORDER BY id LIMIT 1`, sessionID, actorID, intakeID, time.Now().Unix()))
}

func serverRegistrationConflict() ToolOutcome {
	return ToolOutcome{Content: `{"status":"error","code":"conflict","message":"相同主机、端口和用户名的服务器已登记；未覆盖原服务器或凭据，请使用servers_list确认现有server_id"}`}
}

func serverRegistrationOutput(serverID int64, existing bool) string {
	raw, _ := json.Marshal(map[string]any{"status": "succeeded", "server_id": serverID, "already_registered": existing, "connection_verified": false, "message": "SSH服务器已登记；尚未认证登录或执行命令，可使用返回的server_id继续检查"})
	return string(raw)
}
