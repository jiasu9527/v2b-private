package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeOnlineUsersService struct {
	*fakeAdminService
	result admin.OnlineUsers
	err    error
	calls  int
	last   admin.OnlineUsersRequest
}

func (s *fakeOnlineUsersService) GetOnlineUsers(_ context.Context, req admin.OnlineUsersRequest) (admin.OnlineUsers, error) {
	s.calls++
	s.last = req
	return s.result, s.err
}

func TestRouterOnlineUsersAdministratorResponseAndFilters(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  admin.OnlineUsersRequest
	}{
		{"scope=city&province_code=440000&city=深圳市&page=2&page_size=50&search=user@example.test", admin.OnlineUsersRequest{Scope: "city", ProvinceCode: "440000", City: "深圳", Page: 2, PageSize: 50, Search: "user@example.test"}},
		{"scope=city&province_code=440000&city=", admin.OnlineUsersRequest{Scope: "city", ProvinceCode: "440000", Page: 1, PageSize: 20}},
		{"scope=province&province_code=", admin.OnlineUsersRequest{Scope: "province", Page: 1, PageSize: 20}},
		{"scope=country&country_code=cn", admin.OnlineUsersRequest{Scope: "country", CountryCode: "CN", Page: 1, PageSize: 20}},
		{"scope=unknown&reason=location_stale", admin.OnlineUsersRequest{Scope: "unknown", Reason: "location_stale", Page: 1, PageSize: 20}},
		{"", admin.OnlineUsersRequest{Scope: "all", Page: 1, PageSize: 20}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			service := &fakeOnlineUsersService{fakeAdminService: &fakeAdminService{}, result: admin.OnlineUsers{
				Total: 22, Page: 2, PageSize: 20, OnlineWindowSeconds: 600,
				Users: []admin.OnlineUser{{ID: 7, Email: "account@example.test", LastSeenAt: 1792830000, LocationSeenAt: 1792829999, CountryCode: "CN", ProvinceCode: "440000", CityName: "深圳"}},
			}}
			sessions := &fakeSessionService{user: &session.Identity{ID: 8, IsAdmin: 1, IsStaff: 1}}
			router := NewRouter(config.Config{AdminPath: "online-admin"}, WithSessionService(sessions), WithAdminService(service))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/online-admin/stat/online/users?"+tc.query, nil)
			req.Header.Set("Authorization", "Bearer admin-token")
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || service.calls != 1 || service.last != tc.want || !sessions.lastRequireAdmin {
				t.Fatalf("status=%d calls=%d request=%+v want=%+v body=%s", rec.Code, service.calls, service.last, tc.want, rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("account response must not be cached")
			}
			var response struct {
				Data admin.OnlineUsers `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.Total != 22 || len(response.Data.Users) != 1 || response.Data.Users[0].Email != "account@example.test" {
				t.Fatalf("unexpected user response: %+v", response)
			}
		})
	}
}

func TestRouterOnlineUsersRejectsUnauthorizedRolesAndMethods(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		identity     *session.Identity
		err          error
		status       int
	}{
		{"anonymous", http.MethodGet, nil, session.ErrUnauthorized, http.StatusForbidden},
		{"ordinary user", http.MethodGet, &session.Identity{ID: 1}, nil, http.StatusForbidden},
		{"staff", http.MethodGet, &session.Identity{ID: 2, IsStaff: 1}, nil, http.StatusForbidden},
		{"nil authenticated identity", http.MethodGet, nil, nil, http.StatusForbidden},
		{"post", http.MethodPost, &session.Identity{ID: 7, IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"delete", http.MethodDelete, &session.Identity{ID: 7, IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeOnlineUsersService{fakeAdminService: &fakeAdminService{}}
			router := NewRouter(config.Config{AdminPath: "admin"}, WithSessionService(&fakeSessionService{user: tc.identity, authErr: tc.err}), WithAdminService(service))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, "/api/v1/admin/stat/online/users?auth_data=test", nil))
			if rec.Code != tc.status || service.calls != 0 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestRouterOnlineUsersRejectsInvalidQueriesBeforeService(t *testing.T) {
	for _, query := range []string{
		"scope=bad", "scope=country", "scope=country&country_code=ZZ", "scope=province", "scope=city&province_code=440000",
		"scope=city&city=深圳", "scope=city&province_code=bad&city=深圳", "scope=unknown&reason=bad",
		"page=-1", "page=0", "page=", "page=x", "page_size=0", "page_size=101", "page=9223372036854775807&page_size=20",
	} {
		t.Run(query, func(t *testing.T) {
			service := &fakeOnlineUsersService{fakeAdminService: &fakeAdminService{}}
			router := NewRouter(config.Config{AdminPath: "admin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 8, IsAdmin: 1}}), WithAdminService(service))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/stat/online/users?auth_data=test&"+query, nil))
			if rec.Code != http.StatusBadRequest || service.calls != 0 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestRouterOnlineUsersServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service admin.Service
		status  int
	}{
		{"nil", nil, http.StatusServiceUnavailable},
		{"older service", &fakeAdminService{}, http.StatusServiceUnavailable},
		{"unavailable", &fakeOnlineUsersService{fakeAdminService: &fakeAdminService{}, err: admin.ErrUnavailable}, http.StatusServiceUnavailable},
		{"invalid request", &fakeOnlineUsersService{fakeAdminService: &fakeAdminService{}, err: admin.ErrOnlineUsersRequest}, http.StatusBadRequest},
		{"database failed", &fakeOnlineUsersService{fakeAdminService: &fakeAdminService{}, err: errors.New("query failed")}, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter(config.Config{AdminPath: "admin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 8, IsAdmin: 1}}), WithAdminService(tc.service))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/stat/online/users?auth_data=test", nil))
			if rec.Code != tc.status || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || strings.Contains(rec.Body.String(), `"users":`) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
