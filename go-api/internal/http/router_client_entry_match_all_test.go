package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func TestRouterEntryRuleMatchAllConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		confirmed               bool
		status                  int
	}{
		{"json_true", "application/json", `{"auth_data":"jwt-admin","name":"all","conditions":[],"confirm_match_all":true}`, true, http.StatusOK},
		{"json_false", "application/json", `{"auth_data":"jwt-admin","name":"all","conditions":[],"confirm_match_all":false}`, false, http.StatusOK},
		{"json_absent", "application/json", `{"auth_data":"jwt-admin","name":"all","conditions":[]}`, false, http.StatusOK},
		{"json_invalid", "application/json", `{"auth_data":"jwt-admin","name":"all","conditions":[],"confirm_match_all":"true"}`, false, http.StatusBadRequest},
		{"form_true", "application/x-www-form-urlencoded", "auth_data=jwt-admin&name=all&conditions=%5B%5D&confirm_match_all=true", true, http.StatusOK},
		{"form_false", "application/x-www-form-urlencoded", "auth_data=jwt-admin&name=all&conditions=%5B%5D&confirm_match_all=false", false, http.StatusOK},
		{"form_invalid", "application/x-www-form-urlencoded", "auth_data=jwt-admin&name=all&conditions=%5B%5D&confirm_match_all=yes", false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			adminService := &fakeAdminService{}
			router := NewRouter(config.Config{AppName: "forest-go", AdminPath: "localadmin"}, WithSessionService(sessionService), WithAdminService(adminService))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/client-entry-user-policy/save", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if adminService.lastClientEntryUserPolicySave.ConfirmMatchAll != tc.confirmed {
				t.Fatalf("confirmation=%v", adminService.lastClientEntryUserPolicySave.ConfirmMatchAll)
			}
		})
	}
}
