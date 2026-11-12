package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"forest/go-api/internal/aiops"
)

func TestPanelRetirementPreflightCaptureNeverRetries(t *testing.T) {
	for _, code := range []string{"entry_retirement_invalid_arguments", "entry_retirement_conflict", "entry_retirement_blocked", "entry_retirement_not_found"} {
		capture := &panelOperationPreflightCapture{}
		ctx := context.WithValue(context.Background(), panelOperationPreflightKey{}, capture)
		capturePanelOperationPreflight(ctx, code, true)
		if capture.code != code || capture.retryable {
			t.Fatalf("lost typed preflight or enabled destructive retry: %#v", capture)
		}
	}
	for _, code := range []string{"entry_retirement_unknown", "entry_retirement_committed", "invalid_arguments"} {
		capture := &panelOperationPreflightCapture{}
		ctx := context.WithValue(context.Background(), panelOperationPreflightKey{}, capture)
		capturePanelOperationPreflight(ctx, code, true)
		if capture.code != "" || capture.retryable {
			t.Fatalf("unreviewed execution classification accepted: %#v", capture)
		}
	}
}

func TestPanelRetirementSchemaRejectionIsBeforeDispatch(t *testing.T) {
	for _, name := range []string{"entry_groups.retire_preview", "entry_groups.retire"} {
		p := testPanelOperations(&fakeAdminService{}, aiops.PanelOperation{
			Name: name, Path: "/server/client-entry-user-policy/retirement/execute", Method: "POST", Mutating: true, AdminOnly: true,
			Parameters: json.RawMessage(`{"type":"object","properties":{"preview_id":{"type":"integer","minimum":1}},"required":["preview_id"],"additionalProperties":false}`),
		})
		for _, raw := range []string{`{}`, `{"preview_id":0}`, `{"preview_id":"PRIVATE_SENTINEL"}`, `{"preview_id":1,"user_ids":[1,2]}`} {
			_, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, name, json.RawMessage(raw))
			var classified *aiops.PanelOperationError
			if !errors.As(err, &classified) || !classified.NotExecuted || classified.Retryable || classified.Code != "entry_retirement_invalid_arguments" {
				t.Fatalf("schema rejection lost trusted classification: %v", err)
			}
		}
	}
}
