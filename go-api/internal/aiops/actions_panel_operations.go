package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type panelOperationPayload struct {
	Operation         string          `json:"operation"`
	Parameters        json.RawMessage `json:"parameters,omitempty"`
	ParametersCipher  string          `json:"parameters_cipher,omitempty"`
	ParametersPreview json.RawMessage `json:"parameters_preview,omitempty"`
}

// Ciphertext is internal storage, never a browser/model display field.
func (a Action) MarshalJSON() ([]byte, error) {
	type publicAction Action
	view := publicAction(a)
	if a.Kind == "panel_operation" {
		var payload panelOperationPayload
		if json.Unmarshal(a.Payload, &payload) != nil {
			view.Payload = json.RawMessage(`{}`)
		} else {
			payload.Parameters = nil
			payload.ParametersCipher = ""
			view.Payload, _ = json.Marshal(payload)
		}
	}
	return json.Marshal(view)
}

func (s *Service) decodePanelOperationPayload(raw json.RawMessage) (panelOperationPayload, json.RawMessage, error) {
	var payload panelOperationPayload
	if json.Unmarshal(raw, &payload) != nil {
		return payload, nil, ErrValidation
	}
	parameters := payload.Parameters
	if payload.ParametersCipher != "" {
		if len(parameters) > 0 {
			return payload, nil, ErrValidation
		}
		plain, err := s.decryptSecret(payload.ParametersCipher)
		if err != nil {
			return payload, nil, ErrUnavailable
		}
		parameters = json.RawMessage(plain)
	}
	parameters, err := normalizedPanelParameters(parameters)
	return payload, parameters, err
}

func panelOperationSnapshot(operation PanelOperation, parameters json.RawMessage) string {
	raw, _ := json.Marshal(struct {
		Operation  PanelOperation
		Parameters json.RawMessage
	}{operation, parameters})
	return hashOperation(string(raw))
}

func (s *Service) capturePanelOperation(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	payload, parameters, err := s.decodePanelOperationPayload(spec.Payload)
	if err != nil {
		return nil, "", "", err
	}
	if spec.TargetType != payload.Operation {
		return nil, "", "", ErrValidation
	}
	runtime := s.getPanelOperations()
	operation, err := findPanelOperation(runtime, payload.Operation)
	if err != nil {
		return nil, "", "", err
	}
	if !operation.Mutating {
		return nil, "", "", ErrForbidden
	}
	actor, err := panelActor(ctx, tx, spec.ActorID, true)
	if err != nil {
		return nil, "", "", err
	}
	if operation.AdminOnly && !actor.IsAdmin {
		return nil, "", "", ErrForbidden
	}
	if operation.AdminOnly {
		if err = panelAdminSession(ctx, tx, spec.SessionID, true); err != nil {
			return nil, "", "", err
		}
	}
	if err = runtime.Validate(ctx, actor, operation.Name, parameters); err != nil {
		return nil, "", "", safeAIError(err)
	}
	if payload.ParametersCipher == "" {
		payload.ParametersCipher, err = s.encryptSecret(string(parameters))
		if err != nil {
			return nil, "", "", err
		}
	}
	payload.Parameters = nil
	payload.ParametersPreview, _ = previewPanelParameters(parameters)
	raw, _ := json.Marshal(payload)
	// Binds the descriptor and requested parameters, not a business-row version.
	return raw, panelOperationSnapshot(operation, parameters), operation.Description, nil
}

func (s *Service) validatePanelOperationApprover(ctx context.Context, tx *sql.Tx, actorID int64, a Action) error {
	payload, parameters, err := s.decodePanelOperationPayload(a.Payload)
	if err != nil {
		return err
	}
	runtime := s.getPanelOperations()
	operation, err := findPanelOperation(runtime, payload.Operation)
	if err != nil {
		return err
	}
	actor, err := panelActor(ctx, tx, actorID, true)
	if err != nil {
		return err
	}
	if operation.AdminOnly && !actor.IsAdmin {
		return ErrForbidden
	}
	if operation.AdminOnly {
		if err = panelAdminSession(ctx, tx, a.SessionID, true); err != nil {
			return err
		}
	}
	return safeAIErrorOrNil(runtime.Validate(ctx, actor, operation.Name, parameters))
}

func safeAIErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return safeAIError(err)
}

func (s *Service) executePanelOperation(ctx context.Context, a Action, snapshot string) (string, string, string) {
	fail := func() (string, string, string) {
		return "failed", "", "后台操作权限、参数或目录已变化，未执行"
	}
	payload, parameters, err := s.decodePanelOperationPayload(a.Payload)
	if err != nil {
		return fail()
	}
	runtime := s.getPanelOperations()
	operation, err := findPanelOperation(runtime, payload.Operation)
	if err != nil || !operation.Mutating || panelOperationSnapshot(operation, parameters) != snapshot {
		return fail()
	}
	origin, err := panelActor(ctx, s.db, a.ActorID, false)
	if err != nil {
		return fail()
	}
	actor, err := panelActor(ctx, s.db, a.ApprovedBy, false)
	if err != nil || (operation.AdminOnly && (!origin.IsAdmin || !actor.IsAdmin)) {
		return fail()
	}
	if operation.AdminOnly {
		if panelAdminSession(ctx, s.db, a.SessionID, false) != nil {
			return fail()
		}
	}
	if s.requireSessionAccess(ctx, origin.ID, a.SessionID) != nil || s.requireSessionAccess(ctx, actor.ID, a.SessionID) != nil {
		return fail()
	}
	if runtime.Validate(ctx, origin, operation.Name, parameters) != nil || runtime.Validate(ctx, actor, operation.Name, parameters) != nil {
		return fail()
	}
	origin, err = panelActor(ctx, s.db, a.ActorID, false)
	if err != nil {
		return fail()
	}
	actor, err = panelActor(ctx, s.db, a.ApprovedBy, false)
	if err != nil || (operation.AdminOnly && (!origin.IsAdmin || !actor.IsAdmin)) {
		return fail()
	}
	if operation.AdminOnly {
		if panelAdminSession(ctx, s.db, a.SessionID, false) != nil {
			return fail()
		}
	}
	// Approval preserves the approver's identity for audit, but must not grant
	// an employee proposal broader database scope through an admin approver.
	effectiveActor := PanelOperationActor{ID: actor.ID, IsAdmin: origin.IsAdmin && actor.IsAdmin}
	if runtime.Validate(ctx, effectiveActor, operation.Name, parameters) != nil {
		return fail()
	}
	var active bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_action a JOIN v2_ai_config c ON c.id=1 WHERE a.id=$1 AND a.status='running' AND NOT a.stop_requested AND c.enabled)`, a.ID).Scan(&active); err != nil || !active || ctx.Err() != nil {
		return fail()
	}
	if a.ApprovalMode == "full_access" && s.requireFullAccess(ctx, actor.ID, a.SessionID, a.AuthorizationTurnID) != nil {
		return fail()
	}
	data, err := runtime.Execute(ctx, effectiveActor, operation.Name, parameters)
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrValidation) || errors.Is(err, ErrNotFound) {
			return "failed", "", "后台操作被权限或参数校验拒绝，未完成"
		}
		return "unknown", "", "后台操作返回异常，可能已部分执行；请核对结果，禁止自动重试"
	}
	// Preserve an explicit successful mutation result even if its output cannot
	// be saved; never invite the model to repeat a write to recover its output.
	resultCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := s.savePanelResult(resultCtx, actor.ID, a.SessionID, operation.Name, data, true)
	if err != nil {
		return "succeeded", "后台操作已完成，但结果保存失败；请查询业务现状，不要重复执行", ""
	}
	return "succeeded", output, ""
}
