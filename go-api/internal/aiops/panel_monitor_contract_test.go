package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func panelParametersWithSize(size int) json.RawMessage {
	prefix := `{"api_key":"PRIVATE_SENTINEL","value":"`
	suffix := `"}`
	return json.RawMessage(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
}

func TestPanelParametersByteLimitSafeDiagnostic(t *testing.T) {
	for _, size := range []int{23999, 24000, 24001, 26369} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			out, err := normalizedPanelParameters(panelParametersWithSize(size))
			if size <= maxPanelParametersBytes {
				if err != nil || !json.Valid(out) {
					t.Fatal("valid boundary rejected", err)
				}
				return
			}
			var tooLarge *panelParametersTooLargeError
			err = safeAIError(safeAIError(fmt.Errorf("PRIVATE_WRAPPER: %w", err)))
			if len(out) != 0 || !errors.As(err, &tooLarge) || tooLarge.Bytes != size || tooLarge.Limit != 24000 || !errors.Is(err, ErrValidation) {
				t.Fatal("lost safe byte limit diagnosis", err)
			}
			raw := toolErrorContent(err)
			var content map[string]any
			if json.Unmarshal([]byte(raw), &content) != nil || content["code"] != "panel_parameters_too_large" || content["execution_state"] != "not_executed" || content["bytes"] != float64(size) || content["limit"] != float64(24000) {
				t.Fatal("missing structured rejection", raw)
			}
			for _, part := range []string{"entry_monitors.upsert", "entry_monitors.save", "全量替换", "不能分批提交", "不要原样重试"} {
				if !strings.Contains(raw, part) {
					t.Fatal("unsafe or incomplete correction guidance", raw)
				}
			}
			if strings.Contains(raw, "PRIVATE") || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(raw, strings.Repeat("x", 20)) {
				t.Fatal("raw parameter or wrapper leaked")
			}
		})
	}
	// Count UTF-8 bytes, not characters: this value is below 24k runes.
	raw := json.RawMessage(`{"value":"` + strings.Repeat("中", 8000) + `"}`)
	_, err := normalizedPanelParameters(raw)
	var tooLarge *panelParametersTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Bytes != len(raw) {
		t.Fatal("unicode size was not counted in bytes", err)
	}
}

func TestPanelMonitorSafeErrorAndRecoveryRequireTrustedMarker(t *testing.T) {
	for _, code := range []string{"entry_monitor_invalid_arguments", "entry_monitor_conflict"} {
		for _, trusted := range []bool{false, true} {
			err := safeAIError(safeAIError(fmt.Errorf("PRIVATE_WRAPPER: %w", &PanelOperationError{Status: 409, Code: code, Message: "PRIVATE_SENTINEL", NotExecuted: trusted, Retryable: true})))
			var panelError *PanelOperationError
			if !errors.As(err, &panelError) || panelError.NotExecuted != trusted || panelError.Retryable {
				t.Fatal("unsafe retry or lost trusted marker", err)
			}
			raw := toolErrorContent(err)
			if strings.Contains(raw, "PRIVATE") || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(raw, "未执行监测配置写入") != trusted {
				t.Fatal("unsafe message", raw)
			}
			if trusted && (!strings.Contains(raw, code) || !strings.Contains(raw, "不要原样重试") || !strings.Contains(raw, "entry_monitors.upsert")) {
				t.Fatal("missing monitor correction guidance", raw)
			}
		}
		a := Action{Kind: "panel_operation", Status: "failed", Output: panelPreflightOutput(code)}
		if panelActionRecovery(a) != panelRecoveryPreflight {
			t.Fatal("trusted monitor failure cannot recover", code)
		}
		a.Output = `{"code":"` + code + `"}`
		if panelActionRecovery(a) != panelRecoveryNone {
			t.Fatal("untrusted marker allowed recovery", code)
		}
		a.Status = "unknown"
		if panelActionRecovery(a) != panelRecoveryReadOnly {
			t.Fatal("uncertain operation upgraded to preflight", code)
		}
	}
}

func TestPanelProposalSchemaExplainsParameterLimitAndIncrementalWrites(t *testing.T) {
	for _, definition := range panelOperationToolDefinitions() {
		if definition.Function.Name != "propose_panel_operation" {
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(definition.Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		description := schema.Properties["parameters"].Description
		for _, part := range []string{"24000字节", "entry_monitors.upsert", "entry_monitors.save", "全量替换", "不能分批提交"} {
			if !strings.Contains(description, part) {
				t.Fatal("missing parameter contract", description)
			}
		}
		return
	}
	t.Fatal("proposal definition missing")
}

func TestPanelOversizedParametersNeverCreateOrExecuteActionPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	runtime := &fixturePanelOperations{ops: []PanelOperation{fixtureOperation("entry_monitors.save", "/dns-failover/entry-monitors", true, false)}}
	f.s.SetPanelOperations(runtime)
	session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	call := panelRecoveryCall("oversize-monitor", "propose_panel_operation", "entry_monitors.save", string(panelParametersWithSize(26369)))
	_, err = f.s.callTool(ctx, f.staff, session.ID, call)
	var tooLarge *panelParametersTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Bytes != 26369 {
		t.Fatal("opaque parameter rejection", err)
	}
	actions, err := f.s.ListActions(ctx, f.staff, session.ID)
	if err != nil || len(actions) != 0 || runtime.count() != 0 || runtime.validations != 0 {
		t.Fatal("oversize request reached validation, action storage or execution", len(actions), runtime.count(), runtime.validations, err)
	}
}

func TestPanelMonitorExecutionFailureClassificationPostgres(t *testing.T) {
	for _, code := range []string{"entry_monitor_invalid_arguments", "entry_monitor_conflict"} {
		for _, trusted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trusted=%v", code, trusted), func(t *testing.T) {
				f := setupStaffAIFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				runtime := &fixturePanelOperations{
					ops: []PanelOperation{fixtureOperation("entry_monitors.upsert", "/dns-failover/entry-monitors/upsert", true, false)},
					onExecute: func(context.Context) error {
						return &PanelOperationError{Status: 409, Code: code, Message: "PRIVATE_SENTINEL", NotExecuted: trusted, Retryable: true}
					},
				}
				f.s.SetPanelOperations(runtime)
				session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
				if err != nil {
					t.Fatal(err)
				}
				out, err := f.s.callTool(ctx, f.staff, session.ID, panelRecoveryCall("monitor-save", "propose_panel_operation", "entry_monitors.upsert", `{"value":"fixture"}`))
				if err != nil || !out.Pending {
					t.Fatal(out, err)
				}
				if _, err = f.s.ApproveAction(ctx, f.admin, session.ID, out.ActionID); err != nil {
					t.Fatal(err)
				}
				var action Action
				for {
					action, err = scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, out.ActionID))
					if err != nil {
						t.Fatal(err)
					}
					if action.Status != "running" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
				wantStatus, wantRecovery := "unknown", panelRecoveryReadOnly
				if trusted {
					wantStatus, wantRecovery = "failed", panelRecoveryPreflight
				}
				if action.Status != wantStatus || panelActionRecovery(action) != wantRecovery || runtime.count() != 1 || strings.Contains(action.Error+action.Output, "PRIVATE") {
					t.Fatal("monitor error retried or misclassified", action.Status, action.Output, action.Error, runtime.count())
				}
			})
		}
	}
}
