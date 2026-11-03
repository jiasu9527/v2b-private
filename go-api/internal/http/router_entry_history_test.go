package httpapi

import (
	"forest/go-api/internal/admin"
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
