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

type fakeOnlineStatsService struct {
	*fakeAdminService
	result admin.OnlineStats
	err    error
	calls  int
}

func (s *fakeOnlineStatsService) GetOnlineStats(context.Context) (admin.OnlineStats, error) {
	s.calls++
	return s.result, s.err
}

func TestRouterOnlineStatsAdministratorResponse(t *testing.T) {
	for _, identity := range []*session.Identity{{ID: 7, IsAdmin: 1}, {ID: 8, IsAdmin: 1, IsStaff: 1}} {
		service := &fakeOnlineStatsService{fakeAdminService: &fakeAdminService{}, result: admin.OnlineStats{
			GeneratedAt: 1792830000, OnlineWindowSeconds: 600, CurrentOnline: 9, Unique24h: 25, Unique7d: 90, TotalUsers: 100,
			GeoLocatedUsers: 7, GeoUnknownUsers: 2, Countries: []admin.OnlineRegion{{Code: "CN", Name: "中国", Users: 7, Share: 77.78}},
			ChinaProvinces: []admin.OnlineRegion{{Code: "440000", Name: "广东", Users: 7, Share: 77.78}},
			UnknownReasons: []admin.OnlineUnknownReason{{Code: "not_reported", Name: "节点尚未上报用户地址", Users: 2}},
		}}
		sessions := &fakeSessionService{user: identity}
		router := NewRouter(config.Config{AdminPath: "online-admin"}, WithSessionService(sessions), WithAdminService(service))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/online-admin/stat/online", nil)
		req.Header.Set("Authorization", "Bearer online-admin-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || service.calls != 1 || !sessions.lastRequireAdmin {
			t.Fatalf("admin response status=%d calls=%d requireAdmin=%v body=%s", rec.Code, service.calls, sessions.lastRequireAdmin, rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("online statistics response must not be cached")
		}
		var response struct {
			Data admin.OnlineStats `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Data.CurrentOnline != 9 || response.Data.Unique24h != 25 || response.Data.Unique7d != 90 || len(response.Data.Countries) != 1 || response.Data.Countries[0].Code != "CN" || len(response.Data.ChinaProvinces) != 1 || response.Data.ChinaProvinces[0].Code != "440000" {
			t.Fatalf("response mismatch: %+v", response.Data)
		}
	}
}

func TestRouterOnlineStatsRejectsUnauthorizedRolesAndMethods(t *testing.T) {
	tests := []struct {
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
		{"put", http.MethodPut, &session.Identity{ID: 7, IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"delete", http.MethodDelete, &session.Identity{ID: 7, IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"head", http.MethodHead, &session.Identity{ID: 7, IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeOnlineStatsService{fakeAdminService: &fakeAdminService{}}
			sessions := &fakeSessionService{user: tc.identity, authErr: tc.err}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAdminService(service))
			req := httptest.NewRequest(tc.method, "/api/v1/localadmin/stat/online?auth_data=test", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status || service.calls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", rec.Code, tc.status, service.calls, rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("rejected statistics requests must not be cached")
			}
			if tc.status == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("unexpected Allow header: %q", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestRouterOnlineStatsReportsUnavailableAndErrors(t *testing.T) {
	tests := []struct {
		name    string
		service admin.Service
		status  int
	}{
		{"nil service", nil, http.StatusServiceUnavailable},
		{"older implementation", &fakeAdminService{}, http.StatusServiceUnavailable},
		{"unavailable", &fakeOnlineStatsService{fakeAdminService: &fakeAdminService{}, err: admin.ErrUnavailable}, http.StatusServiceUnavailable},
		{"database failure", &fakeOnlineStatsService{fakeAdminService: &fakeAdminService{}, err: errors.New("database query failed")}, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 7, IsAdmin: 1}}), WithAdminService(tc.service))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/online?auth_data=test", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status || strings.Contains(rec.Body.String(), `"current_online":0`) {
				t.Fatalf("failure must not resemble successful empty statistics: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("missing no-store on statistics error")
			}
		})
	}
}
