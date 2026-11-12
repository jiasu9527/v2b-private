package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"forest/go-api/internal/admin"
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
	for attempt := 1; ; attempt++ {
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
		origin, err := s.retryPanelActor(ctx, a.ActorID)
		if err != nil {
			return fail()
		}
		actor, err := s.retryPanelActor(ctx, a.ApprovedBy)
		if err != nil || (operation.AdminOnly && (!origin.IsAdmin || !actor.IsAdmin)) {
			return fail()
		}
		if operation.AdminOnly {
			if s.retryPanelAdminSession(ctx, a.SessionID) != nil {
				return fail()
			}
		}
		if retryStateRead(ctx, func(check context.Context) error {
			if err := s.requireSessionAccess(check, origin.ID, a.SessionID); err != nil {
				return err
			}
			return s.requireSessionAccess(check, actor.ID, a.SessionID)
		}) != nil {
			return fail()
		}
		if runtime.Validate(ctx, origin, operation.Name, parameters) != nil || runtime.Validate(ctx, actor, operation.Name, parameters) != nil {
			return fail()
		}
		origin, err = s.retryPanelActor(ctx, a.ActorID)
		if err != nil {
			return fail()
		}
		actor, err = s.retryPanelActor(ctx, a.ApprovedBy)
		if err != nil || (operation.AdminOnly && (!origin.IsAdmin || !actor.IsAdmin)) {
			return fail()
		}
		if operation.AdminOnly {
			if s.retryPanelAdminSession(ctx, a.SessionID) != nil {
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
		err = retryStateRead(ctx, func(check context.Context) error {
			return s.db.QueryRowContext(check, `SELECT EXISTS(SELECT 1 FROM v2_ai_action a JOIN v2_ai_config c ON c.id=1 WHERE a.id=$1 AND a.status='running' AND NOT a.stop_requested AND c.enabled)`, a.ID).Scan(&active)
		})
		if err != nil || !active || ctx.Err() != nil {
			return fail()
		}
		if a.ApprovalMode == "full_access" && s.requireFullAccess(ctx, actor.ID, a.SessionID, a.AuthorizationTurnID) != nil {
			return fail()
		}
		data, err := runtime.Execute(ctx, effectiveActor, operation.Name, parameters)
		if err != nil {
			var nodeValidation *admin.ManagedServerValidationError
			if errors.As(err, &nodeValidation) {
				return "failed", panelPreflightOutput("node_invalid_arguments"), "节点参数校验未通过，未执行节点保存：" + nodeValidation.Error()
			}
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrValidation) || errors.Is(err, ErrNotFound) {
				return "failed", "", "后台操作被权限或参数校验拒绝，未完成"
			}
			var panelError *PanelOperationError
			if errors.As(err, &panelError) {
				code, message := panelOperationErrorText(panelError)
				// This marker originates in the private HTTP adapter, never from an
				// upstream response body or a model-supplied status. Failures after
				// dispatch remain unknown regardless of their HTTP status.
				if panelError.NotExecuted && oneOf(code, "ip_pool_invalid_arguments", "entry_order_invalid_arguments", "entry_order_conflict", "entry_monitor_invalid_arguments", "entry_monitor_conflict", "entry_retirement_invalid_arguments", "entry_retirement_conflict", "entry_retirement_blocked", "entry_retirement_not_found") {
					return "failed", panelPreflightOutput(code), message
				}
				if panelError.NotExecuted && strings.HasPrefix(code, "dns_") {
					if panelError.Retryable && oneOf(code, "dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed") && attempt < operationMaxAttempts {
						if err := readToolRetryDelay(ctx, attempt); err != nil {
							return "failed", panelPreflightOutput(code), "写前查询重试已取消，未发送 DNS 修改"
						}
						// Re-enter all checks with the same encrypted payload and
						// action ID. Never retry a response without proof of no write.
						continue
					}
					if panelError.Retryable && attempt == operationMaxAttempts {
						message += "（写前查询已自动尝试 3 次，未发送 DNS 修改）"
					}
					return "failed", panelPreflightOutput(code), message
				}
				return "unknown", "", fmt.Sprintf("后台操作返回 HTTP %d（%s）：%s；可能已部分执行，请核对结果，禁止自动重试", panelError.Status, code, message)
			}
			return "unknown", "", "后台操作返回异常，可能已部分执行；请核对结果，禁止自动重试"
		}
		// Preserve an explicit successful mutation result even if its output cannot
		// be saved; never invite the model to repeat a write to recover its output.
		resultCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Keep protected results behind the result reader's fresh role check.
		// Ordinary compact receipts can stay inline in the existing action;
		// large payloads still use chunked storage automatically.
		output, err := s.savePanelResult(resultCtx, actor.ID, a.SessionID, operation.Name, data, panelMutationNeedsChunk(operation, data))
		if err != nil {
			return "succeeded", "后台操作已完成，但结果保存失败；请查询业务现状，不要重复执行", ""
		}
		return "succeeded", output, ""
	}
}

func panelMutationNeedsChunk(operation PanelOperation, data json.RawMessage) bool {
	// automaticActionSummary includes only 6000 characters. Keep ample room for
	// the inline envelope; medium results must remain fully recoverable by ID.
	return operation.AdminOnly || jsonSize(struct {
		Operation string          `json:"operation"`
		Data      json.RawMessage `json:"data"`
	}{operation.Name, data}) > 5000
}

func panelPreflightOutput(code string) string {
	data, _ := json.Marshal(map[string]string{"execution_state": "not_executed", "code": code, "recovery": "panel_preflight"})
	return string(data)
}
