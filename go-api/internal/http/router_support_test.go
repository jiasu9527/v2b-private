package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeSupportService struct {
	enabled                  bool
	limitErr                 error
	reads, writes, roleReads int
	actor                    int64
	req                      admin.SupportEntryChangeRequest
}

func (f *fakeSupportService) IsSupportStaff(context.Context, int64) (bool, error) {
	f.roleReads++
	return f.enabled, nil
}
func (f *fakeSupportService) ConsumeSupportLimit(context.Context, int64, string) error {
	return f.limitErr
}
func (f *fakeSupportService) SupportEntryOverview(context.Context) (admin.SupportEntryOverview, error) {
	f.reads++
	return admin.SupportEntryOverview{}, nil
}
func (f *fakeSupportService) LookupSupportUser(_ context.Context, id int64, query, ua string) (admin.SupportUserResult, error) {
	f.actor = id
	f.reads++
	return admin.SupportUserResult{}, nil
}
func (f *fakeSupportService) ChangeSupportEntry(_ context.Context, id int64, req admin.SupportEntryChangeRequest) (admin.SupportUserResult, error) {
	f.actor = id
	f.req = req
	f.writes++
	return admin.SupportUserResult{}, nil
}
func (f *fakeSupportService) ListSupportOperations(_ context.Context, id, page int64) (admin.SupportOperationResult, error) {
	f.actor = id
	f.reads++
	return admin.SupportOperationResult{}, nil
}
func supportTestRouter(f *fakeSupportService, identity *session.Identity) http.Handler {
	return NewRouter(config.Config{AdminPath: "admin-test"}, WithSessionService(strictAppleIDSession{identity: identity}), WithSupportService(f))
}
func TestSupportRoutesDenyOrdinaryAdminAndRevokedStaff(t *testing.T) {
	for _, identity := range []*session.Identity{nil, {ID: 7}, {ID: 7, IsStaff: 1}, {ID: 7, IsAdmin: 1}} {
		f := &fakeSupportService{}
		router := supportTestRouter(f, identity)
		req := httptest.NewRequest("GET", "/api/v1/support/overview", nil)
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 403 || f.reads != 0 {
			t.Fatalf("identity %+v status=%d reads=%d", identity, rec.Code, f.reads)
		}
	}
}
func TestSupportIdentityCannotAccessAdminOrLegacyStaffRoutes(t *testing.T) {
	for _, path := range []string{"/api/v1/admin-test/support/staff", "/api/v1/admin-test/support/operations", "/api/v1/admin-test/user/fetch", "/api/v1/staff/user/getUserInfoById?id=5", "/api/v1/staff/user/update", "/api/v1/staff/ticket/fetch", "/api/v1/staff/user/ban", "/api/v1/admin-test/dns-failover/entry-monitors"} {
		f := &fakeSupportService{enabled: true}
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1}).ServeHTTP(rec, req)
		if rec.Code != 403 || f.reads != 0 || f.writes != 0 {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}
func TestSupportOperationsScopeComesFromSessionNotRequest(t *testing.T) {
	for _, asAdmin := range []bool{false, true} {
		f := &fakeSupportService{enabled: true}
		identity := &session.Identity{ID: 7, IsStaff: 1}
		path := "/api/v1/support/operations?actor_id=99&page=2"
		want := int64(7)
		if asAdmin {
			identity.IsAdmin = 1
			path = "/api/v1/admin-test/support/operations?actor_id=99&page=2"
			want = 0
		}
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		supportTestRouter(f, identity).ServeHTTP(rec, req)
		if rec.Code != 200 || f.actor != want {
			t.Fatalf("status=%d actor=%d want=%d", rec.Code, f.actor, want)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("sensitive response was cacheable")
		}
	}
}
func TestSupportMutationRejectsUnknownFieldsOversizeAndTrailingData(t *testing.T) {
	valid := `{"user_id":5,"source_policy_id":10,"target_key":"policy:20","reason":"repair","version":0,"request_id":"r-1"}`
	for _, body := range []string{`{"user_id":5,"plan_id":999}`, `{"user_id":5,"auth_data":"body-token"}`, valid + ` {}`, `{"reason":"` + strings.Repeat("x", 33<<10) + `"}`} {
		f := &fakeSupportService{enabled: true}
		req := httptest.NewRequest("POST", "/api/v1/support/entry", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1}).ServeHTTP(rec, req)
		if rec.Code != 400 || f.writes != 0 {
			t.Fatalf("status=%d writes=%d", rec.Code, f.writes)
		}
	}
	f := &fakeSupportService{enabled: true}
	req := httptest.NewRequest("POST", "/api/v1/support/entry", strings.NewReader(valid))
	req.Header.Set("Authorization", "Bearer fixture")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1}).ServeHTTP(rec, req)
	if rec.Code != 200 || f.actor != 7 || f.req.UserID != 5 {
		t.Fatalf("valid request: %d %+v", rec.Code, f)
	}
}
func TestSupportRouteRechecksRoleAndEnforcesRateLimit(t *testing.T) {
	f := &fakeSupportService{enabled: true}
	router := supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1})
	for _, want := range []int{200, 429, 403} {
		if want == 429 {
			f.limitErr = admin.ErrSupportRateLimited
		}
		if want == 403 {
			f.enabled = false
		}
		req := httptest.NewRequest("GET", "/api/v1/support/user?query=alice@example.test", nil)
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("got %d want %d: %s", rec.Code, want, rec.Body.String())
		}
		if want == 429 && rec.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry-after")
		}
	}
	if f.reads != 1 || f.roleReads != 3 {
		t.Fatalf("calls %+v", f)
	}
}
func TestSupportMethodAndPageValidation(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "overview", 405}, {"GET", "entry", 405}, {"GET", "operations?page=0", 400}, {"GET", "operations?page=abc", 400}, {"GET", "not-an-endpoint", 404}} {
		f := &fakeSupportService{enabled: true}
		req := httptest.NewRequest(tc.method, "/api/v1/support/"+tc.path, nil)
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1}).ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%+v got %d", tc, rec.Code)
		}
	}
}

type freshOnlySupportSession struct {
	strictAppleIDSession
	freshCalls int
	err        error
}

func (f *freshOnlySupportSession) AuthenticateFresh(context.Context, string, bool) (*session.Identity, error) {
	f.freshCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.identity, nil
}
func TestSupportAndCheckLoginBypassCachedSession(t *testing.T) {
	for _, path := range []string{"/api/v1/support/overview", "/api/v1/user/checkLogin"} {
		f := &fakeSupportService{enabled: true}
		sessions := &freshOnlySupportSession{strictAppleIDSession: strictAppleIDSession{identity: &session.Identity{ID: 7, IsStaff: 1}}, err: session.ErrUnauthorized}
		router := NewRouter(config.Config{}, WithSessionService(sessions), WithSupportService(f))
		req := httptest.NewRequest("GET", path+"?auth_data=fixture", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 403 || sessions.freshCalls != 1 || f.reads != 0 {
			t.Fatalf("%s status %d calls %d", path, rec.Code, sessions.freshCalls)
		}
	}
}
func TestSupportErrorsHideDatabaseDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	handleSupportError(rec, errors.New("password=secret db connection failed"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatal(rec.Body.String())
	}
}

func TestCheckLoginPreservesLegacyJSONAndFormAuth(t *testing.T) {
	for _, tc := range []struct{ mime, body string }{{"application/json", `{"auth_data":"fixture"}`}, {"application/x-www-form-urlencoded", "auth_data=fixture"}} {
		f := &fakeSupportService{enabled: true}
		req := httptest.NewRequest("POST", "/api/v1/user/checkLogin", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.mime)
		rec := httptest.NewRecorder()
		supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1, Email: "staff@example.test"}).ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"is_staff":true`) || !strings.Contains(rec.Body.String(), `"is_support":true`) || f.roleReads != 0 {
			t.Fatalf("legacy %s: %d %s", tc.mime, rec.Code, rec.Body.String())
		}
	}
}

func TestExistingStaffCanUseWorkbenchWithoutSeparateGrant(t *testing.T) {
	f := &fakeSupportService{enabled: true}
	req := httptest.NewRequest("GET", "/api/v1/support/overview", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	rec := httptest.NewRecorder()
	supportTestRouter(f, &session.Identity{ID: 7, IsStaff: 1}).ServeHTTP(rec, req)
	if rec.Code != 200 || f.reads != 1 {
		t.Fatalf("existing employee denied: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCheckLoginReportsExistingStaffWithoutSupportService(t *testing.T) {
	router := NewRouter(config.Config{}, WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 7, IsStaff: 1}}))
	req := httptest.NewRequest("GET", "/api/v1/user/checkLogin?auth_data=fixture", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"is_staff":true`) || !strings.Contains(rec.Body.String(), `"is_support":true`) {
		t.Fatalf("checkLogin relies on separate grant: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRemovedEmployeeGrantEndpointsAreNotAvailableToAdministrator(t *testing.T) {
	for _, path := range []string{"staff", "staff/save"} {
		for _, method := range []string{"GET", "POST"} {
			f := &fakeSupportService{}
			req := httptest.NewRequest(method, "/api/v1/admin-test/support/"+path, nil)
			req.Header.Set("Authorization", "Bearer fixture")
			rec := httptest.NewRecorder()
			supportTestRouter(f, &session.Identity{ID: 1, IsAdmin: 1}).ServeHTTP(rec, req)
			if rec.Code != 404 || f.reads != 0 || f.writes != 0 {
				t.Fatalf("removed route %s %s remains: %d", method, path, rec.Code)
			}
		}
	}
}
