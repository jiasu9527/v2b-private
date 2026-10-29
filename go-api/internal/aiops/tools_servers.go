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
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"intake_id": map[string]any{"type": "integer", "minimum": 1, "description": "当前管理员在本会话提交SSH资料后由后台生成的intake_id；不可猜测或使用其他会话资料"},
		"name":      map[string]any{"type": "string", "maxLength": 128, "description": "可选服务器名称，未填沿用提交名称或主机地址"},
		"reason":    map[string]any{"type": "string", "minLength": 1, "maxLength": 500, "description": "登记原因及后续用途"},
	}, "required": []string{"intake_id", "reason"}, "additionalProperties": false})
	return []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{Name: "ssh_server_register", Description: "将本会话已安全收取的SSH资料提议登记到服务器列表，标准模式待审批、完全访问按当前授权执行。只接受intake_id，不接受密码私钥。先无认证读取主机指纹；未提供指纹时固定首次观测值（first_use），提供指纹必须匹配。仅新建、不覆盖已有服务器或凭据。登记成功不等于已登录，返回server_id后可用ssh_status检查。", Parameters: raw}}}
}

func (s *Service) callServerTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var args struct {
		IntakeID int64  `json:"intake_id"`
		Name     string `json:"name"`
		Reason   string `json:"reason"`
	}
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if call.Function.Name != "ssh_server_register" || d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || args.IntakeID <= 0 || strings.TrimSpace(args.Reason) == "" || len([]rune(args.Reason)) > 500 || len([]rune(strings.TrimSpace(args.Name))) > 128 {
		return ToolOutcome{}, ErrValidation
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
	payload, _ := json.Marshal(serverRegisterPayload{IntakeID: intake.ID, Name: name, Host: intake.Host, Port: intake.Port, Username: intake.Username, HostKeySHA256: observed.Fingerprint, TrustSource: trustSource, HasPassword: intake.PasswordCipher != "", HasPrivateKey: intake.PrivateKeyCipher != ""})
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
