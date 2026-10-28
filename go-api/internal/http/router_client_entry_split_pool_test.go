package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func TestRouterAdminClientEntrySplitConvertBackupPoolOption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
		pool   bool
	}{
		{"pool without preview", `{"policy_id":9,"use_backup_ip_pool":true}`, http.StatusOK, true},
		{"manual compatibility", `{"policy_id":9,"entry_host_a":"a.example.test","entry_host_b":"b.example.test"}`, http.StatusOK, false},
		{"manual explicit", `{"policy_id":9,"entry_host_a":"a.example.test","entry_host_b":"b.example.test","use_backup_ip_pool":false}`, http.StatusOK, false},
		{"reject string flag", `{"policy_id":9,"use_backup_ip_pool":"true"}`, http.StatusBadRequest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			service := &fakeAdminService{clientEntrySplitPolicy: admin.ClientEntryUserPolicyRecord{ID: 9, Mode: admin.ClientEntryUserPolicyModeSplit}}
			router := NewRouter(config.Config{AppName: "forest-go", AdminPath: "localadmin"}, WithSessionService(sessions), WithAdminService(service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/client-entry-user-policy/split-convert?auth_data=jwt-admin", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusOK && (service.lastClientEntrySplitConvert.PolicyID != 9 || service.lastClientEntrySplitConvert.UseBackupIPPool != tc.pool) {
				t.Fatalf("request = %#v", service.lastClientEntrySplitConvert)
			}
			if tc.status != http.StatusOK && service.lastClientEntrySplitConvert.PolicyID != 0 {
				t.Fatal("invalid flag reached conversion service")
			}
		})
	}
}
