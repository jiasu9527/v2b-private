package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

func TestIPPoolBulkDeleteSchemaRequiresScope(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	for _, raw := range []string{`{"ids":[180,181]}`, `{"scope":"bad","ids":[180]}`, `{"scope":null}`, `{"scope":"selected","ids":[0]}`} {
		if err := p.Validate(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "ip_pool.delete_many", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid schema accepted", raw)
		}
	}
	if err := p.Validate(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "ip_pool.delete_many", json.RawMessage(`{"scope":"selected","ids":[180,181]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestIPPoolBulkDeleteOnlyPreDispatchValidationIsRecoverable(t *testing.T) {
	for _, body := range []string{`{"ids":[180,181]}`, `{"scope":"selected"}`, `{"scope":"selected","ids":[]}`, `{"scope":"selected","ids":[0]}`, `{"scope":"faulty","ids":[180]}`, `{"scope":"all","ids":[180]}`, `{"scope":"selected","ids":[180],"unexpected":true}`} {
		service := &fakeClientEntryBackupIPService{fakeAdminService: &fakeAdminService{}}
		capture := &panelOperationPreflightCapture{}
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(context.WithValue(r.Context(), panelOperationPreflightKey{}, capture))
		w := httptest.NewRecorder()
		handleClientEntryBackupIPBulkDelete(w, r, service)
		if w.Code != 400 || capture.code != "ip_pool_invalid_arguments" || capture.retryable || service.bulkRequest.Scope != "" {
			t.Fatalf("invalid request reached backend or lost preflight: %s %d %+v", body, w.Code, capture)
		}
	}
	for _, cause := range []error{errors.New("PRIVATE_SENTINEL failure after dispatch"), errors.New(`{"execution_state":"not_executed","code":"ip_pool_invalid_arguments"}`)} {
		service := &fakeClientEntryBackupIPService{fakeAdminService: &fakeAdminService{}, err: cause}
		p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: service})
		_, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "ip_pool.delete_many", json.RawMessage(`{"scope":"selected","ids":[180,181]}`))
		var got *aiops.PanelOperationError
		if !errors.As(err, &got) || got.NotExecuted || got.Code != "" || service.bulkRequest.Scope != "selected" {
			t.Fatalf("post dispatch became safe preflight: %+v %v", got, err)
		}
		if strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatal("backend cause leaked")
		}
	}
	// Valid deletion still preserves the backend's skipped-in-use result.
	service := &fakeClientEntryBackupIPService{fakeAdminService: &fakeAdminService{}, bulkDelete: admin.ClientEntryBackupIPBulkDeleteResult{Requested: 2, Deleted: 1, SkippedInUse: 1}}
	p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: service})
	data, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "ip_pool.delete_many", json.RawMessage(`{"scope":"selected","ids":[180,181]}`))
	if err != nil || !strings.Contains(string(data), `"skipped_in_use":1`) {
		t.Fatal(string(data), err)
	}
}
