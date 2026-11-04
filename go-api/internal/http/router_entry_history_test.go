package httpapi

import (
	"context"
	"encoding/json"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHistoricalEntrySplitHTTPParameters(t *testing.T) {
	service := &fakeAdminService{clientEntrySplitPreview: admin.ClientEntryUserPolicySplitPreviewResult{HistoryHost: "192.0.2.7", SnapshotHash: "hash", From: 100, To: 200, UserCount: 4}}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}), WithAdminService(service))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/localadmin/server/client-entry-user-policy/split-preview?auth_data=jwt-admin&history_host=192.0.2.7&history_from=100&history_to=200", nil))
	if w.Code != 200 || service.lastClientEntrySplitPreview.HistoryHost != "192.0.2.7" || service.lastClientEntrySplitPreview.HistoryFrom != 100 || service.lastClientEntrySplitPreview.HistoryTo != 200 || !strings.Contains(w.Body.String(), `"snapshot_hash":"hash"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"name":"history","history_single_group":true,"history_host":"192.0.2.7","history_from":100,"history_to":200,"snapshot_hash":"hash","members":[{"server_type":"vmess","server_id":11}],"entry_host_a":"192.0.2.8","entry_host_b":"192.0.2.9"}`
	req := httptest.NewRequest("POST", "/api/v1/localadmin/server/client-entry-user-policy/split-create?auth_data=jwt-admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	got := service.lastClientEntrySplitCreate
	if w.Code != 200 || got.HistoryHost != "192.0.2.7" || got.HistoryFrom != 100 || got.HistoryTo != 200 || got.SnapshotHash != "hash" || !got.HistorySingleGroup {
		t.Fatal(w.Code, w.Body.String(), got)
	}
	for _, raw := range []string{"not-a-time", "1.5", "-"} {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/client-entry-user-policy/split-preview?auth_data=jwt-admin&history_host=192.0.2.7&history_from="+raw, nil))
		if w.Code != 400 {
			t.Fatal("bad history time accepted", w.Code)
		}
	}
}

func TestAIEntryHistorySplitCatalogSupportsPageParameters(t *testing.T) {
	service := &fakeAdminService{clientEntrySplitPreview: admin.ClientEntryUserPolicySplitPreviewResult{HistoryHost: "45.192.207.49", SnapshotHash: "hash-from-preview", From: 1793177880, To: 1793782680, UserCount: 1271}}
	p := newPanelOperations(config.Config{}, &routerState{admin: service})
	actor := aiops.PanelOperationActor{ID: 1, IsAdmin: true}
	for operation, raw := range map[string]string{
		"entry_split.preview": `{"history_host":"45.192.207.49","history_from":1793177880,"history_to":1793782680}`,
		"entry_split.create":  `{"name":"historical","history_host":"45.192.207.49","history_from":1793177880,"history_to":1793782680,"snapshot_hash":"hash-from-preview","history_single_group":false,"members":[{"server_type":"vmess","server_id":14}],"entry_host_a":"45.192.207.202","entry_host_b":"45.192.196.62"}`,
	} {
		if err := p.Validate(context.Background(), actor, operation, json.RawMessage(raw)); err != nil {
			t.Fatal(operation, err)
		}
		result, err := p.Execute(context.Background(), actor, operation, json.RawMessage(raw))
		if err != nil {
			t.Fatal(operation, err)
		}
		if operation == "entry_split.preview" && !strings.Contains(string(result), "hash-from-preview") {
			t.Fatal("preview hash lost before reaching model", string(result))
		}
	}
	got := service.lastClientEntrySplitCreate
	if got.HistoryHost != "45.192.207.49" || got.SnapshotHash != "hash-from-preview" || got.HistoryFrom != 1793177880 || got.HistoryTo != 1793782680 || got.Minutes != 0 || len(got.Members) != 1 || got.Members[0].ServerID != 14 || got.HistorySingleGroup {
		t.Fatal("AI adapter lost historical page parameters", got)
	}
}
