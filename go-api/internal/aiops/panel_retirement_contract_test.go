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

func TestPanelRetirementSafeErrorsRequireTrustedMarker(t *testing.T) {
	for _, code := range []string{"entry_retirement_invalid_arguments", "entry_retirement_conflict", "entry_retirement_blocked", "entry_retirement_not_found"} {
		t.Run(code, func(t *testing.T) {
			for _, trusted := range []bool{false, true} {
				err := safeAIError(safeAIError(fmt.Errorf("PRIVATE_WRAPPER: %w", &PanelOperationError{Status: 409, Code: code, Message: "PRIVATE_SQL", NotExecuted: trusted, Retryable: true})))
				var panelError *PanelOperationError
				if !errors.As(err, &panelError) || panelError.NotExecuted != trusted || panelError.Retryable {
					t.Fatal("unsafe retry or lost trusted marker", err)
				}
				raw := toolErrorContent(err)
				if strings.Contains(raw, "PRIVATE") || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(raw, "未执行清理") != trusted {
					t.Fatal("raw error or false non-execution assurance", raw)
				}
				if trusted && (!strings.Contains(raw, code) || !strings.Contains(raw, "不要原样重试")) {
					t.Fatal("trusted correction guidance lost", raw)
				}
			}
			action := Action{Kind: "panel_operation", Status: "failed", Output: panelPreflightOutput(code)}
			if panelActionRecovery(action) != panelRecoveryPreflight {
				t.Fatal("trusted preflight cannot recover", code)
			}
			action.Output = `{"code":"` + code + `","message":"not_executed"}`
			if panelActionRecovery(action) != panelRecoveryNone {
				t.Fatal("body text granted preflight recovery", code)
			}
			action.Status = "unknown"
			action.Output = panelPreflightOutput(code)
			if panelActionRecovery(action) != panelRecoveryReadOnly {
				t.Fatal("unknown execution upgraded to safe preflight", code)
			}
		})
	}
}

func TestPanelRetirementExecutionFailureClassificationPostgres(t *testing.T) {
	for _, code := range []string{"entry_retirement_invalid_arguments", "entry_retirement_conflict", "entry_retirement_blocked", "entry_retirement_not_found"} {
		for _, trusted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trusted=%v", code, trusted), func(t *testing.T) {
				f := setupStaffAIFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				runtime := &fixturePanelOperations{
					ops: []PanelOperation{fixtureOperation("entry_groups.retire", "/server/client-entry-user-policy/retirement/execute", true, false)},
					onExecute: func(context.Context) error {
						return &PanelOperationError{Status: 409, Code: code, Message: "PRIVATE_SENTINEL", NotExecuted: trusted, Retryable: true}
					},
				}
				f.s.SetPanelOperations(runtime)
				session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
				if err != nil {
					t.Fatal(err)
				}
				out, err := f.s.callTool(ctx, f.staff, session.ID, panelRecoveryCall("retirement", "propose_panel_operation", "entry_groups.retire", `{"value":"fixture"}`))
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
					t.Fatal("retirement failure retried or misclassified", action.Status, action.Output, action.Error, runtime.count())
				}
			})
		}
	}
}

func TestPanelRetirementRecoveryAllowsOnlyAuditAndReceipt(t *testing.T) {
	s := &Service{}
	runtime := &fixturePanelOperations{}
	s.SetPanelOperations(runtime)
	for _, name := range []string{"entry_groups.audit", "entry_groups.retirement", "entry_groups.retire_preview", "entry_groups.retire"} {
		for _, mutating := range []bool{false, true} {
			runtime.ops = []PanelOperation{fixtureOperation(name, "/fixture/retirement", mutating, true)}
			args, _ := json.Marshal(map[string]any{"operation": name, "parameters": map[string]any{}})
			got := s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: "panel_query", Arguments: string(args)}})
			want := !mutating && (name == "entry_groups.audit" || name == "entry_groups.retirement")
			if got != want {
				t.Fatalf("operation=%s mutating=%v allowed=%v", name, mutating, got)
			}
		}
	}
}
