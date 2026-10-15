package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func TestLegacyStaffRoutesRejectRestrictedEmployeesForAllMethods(t *testing.T) {
	for _, path := range []string{"plan/fetch", "notice/fetch", "notice/save", "notice/update", "notice/drop", "ticket/fetch", "ticket/reply", "ticket/close", "user/getUserInfoById", "user/update", "user/sendMail", "user/ban", "user/unban"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			t.Run(method+"/"+path, func(t *testing.T) {
				router := NewRouter(config.Config{AdminPath: "test-admin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 2, IsStaff: 1}}), WithAdminService(&fakeAdminService{}))
				req := httptest.NewRequest(method, "/api/v1/staff/"+path+"?auth_data=staff&id=7", strings.NewReader("auth_data=staff&id=7&subject=notice&content=message"))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestLegacyStaffRoutesUseFreshAdministratorIdentity(t *testing.T) {
	sessions := &freshOnlySupportSession{strictAppleIDSession: strictAppleIDSession{identity: &session.Identity{ID: 7, IsAdmin: 1}}, err: session.ErrUnauthorized}
	router := NewRouter(config.Config{}, WithSessionService(sessions), WithAdminService(&fakeAdminService{}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/user/update", strings.NewReader(`{"auth_data":"stale-admin","id":9}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 403 || sessions.freshCalls != 1 {
		t.Fatalf("stale role accepted: %d calls=%d", rec.Code, sessions.freshCalls)
	}
}
