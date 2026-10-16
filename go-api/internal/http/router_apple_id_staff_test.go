package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type appleOperatorRoute struct {
	method string
	action string
	query  string
	body   string
}

var appleOperatorRoutes = []appleOperatorRoute{
	{method: http.MethodGet, action: "product/fetch"},
	{method: http.MethodPost, action: "product/save", body: `{"name":"VIP account","region":"US","price":1000}`},
	{method: http.MethodPost, action: "product/show", body: `{"id":3,"enabled":true}`},
	{method: http.MethodPost, action: "product/drop", body: `{"id":3}`},
	{method: http.MethodPost, action: "inventory/import", body: `{"product_id":3,"items":[{"credential":"account / password / recovery"}]}`},
	{method: http.MethodGet, action: "inventory/fetch"},
	{method: http.MethodPost, action: "inventory/disable", body: `{"id":4}`},
	{method: http.MethodGet, action: "order/fetch"},
	{method: http.MethodGet, action: "order/detail", query: "&id=5"},
	{method: http.MethodPost, action: "order/credentials", body: `{"id":5}`},
	{method: http.MethodPost, action: "order/replace", body: `{"id":5,"reason":"replace defective account"}`},
	{method: http.MethodPost, action: "order/refund", body: `{"id":5,"reason":"already refunded"}`},
	{method: http.MethodPost, action: "order/cancel", body: `{"id":5,"reason":"cancel unpaid order"}`},
	{method: http.MethodGet, action: "order/audits", query: "&id=5"},
	{method: http.MethodGet, action: "finance/stats"},
	{method: http.MethodGet, action: "finance/transactions"},
}

type freshAppleOperatorSession struct {
	strictAppleIDSession
	current                 *session.Identity
	freshErr                error
	freshCalls, cachedCalls int
	lastRequireAdmin        bool
	lastToken               string
}

func (s *freshAppleOperatorSession) Authenticate(ctx context.Context, token string, requireAdmin bool) (*session.Identity, error) {
	s.cachedCalls++
	return s.strictAppleIDSession.Authenticate(ctx, token, requireAdmin)
}

func (s *freshAppleOperatorSession) AuthenticateFresh(_ context.Context, token string, requireAdmin bool) (*session.Identity, error) {
	s.freshCalls++
	s.lastToken, s.lastRequireAdmin = token, requireAdmin
	return s.current, s.freshErr
}

func requestAppleOperator(router http.Handler, route appleOperatorRoute) *httptest.ResponseRecorder {
	req := httptest.NewRequest(route.method, "/api/v1/localadmin/apple-id/"+route.action+"?auth_data=operator-token"+route.query, strings.NewReader(route.body))
	if route.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAppleIDEntireManagementModuleAcceptsFreshStaffAndAdministrators(t *testing.T) {
	for _, identity := range []*session.Identity{{ID: 41, IsStaff: 1}, {ID: 77, IsAdmin: 1}, {ID: 88, IsAdmin: 1, IsStaff: 1}} {
		for _, route := range appleOperatorRoutes {
			t.Run(fmt.Sprintf("role_%d_%d/%s", identity.IsAdmin, identity.IsStaff, route.action), func(t *testing.T) {
				service := &fakeAppleIDService{}
				// Even a stale ordinary cache must not hide newly granted access.
				sessions := &freshAppleOperatorSession{strictAppleIDSession: strictAppleIDSession{identity: &session.Identity{ID: identity.ID}}, current: identity}
				router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAppleIDServices(service, service))
				rec := requestAppleOperator(router, route)
				if rec.Code != http.StatusOK || service.adminCalls != 1 || sessions.freshCalls != 1 || sessions.cachedCalls != 0 || sessions.lastRequireAdmin {
					t.Fatalf("status=%d service=%d fresh=%d cached=%d requireAdmin=%v body=%s", rec.Code, service.adminCalls, sessions.freshCalls, sessions.cachedCalls, sessions.lastRequireAdmin, rec.Body.String())
				}
				assertAppleIDNoStore(t, rec)
				if rec.Header().Get("Vary") != "Authorization" {
					t.Fatalf("missing authorization vary: %v", rec.Header())
				}
			})
		}
	}
}

func TestAppleIDEveryManagementEndpointRejectsRevokedStaffDespiteCachedRole(t *testing.T) {
	for _, state := range []struct {
		name     string
		identity *session.Identity
		err      error
	}{
		{name: "ordinary user", identity: &session.Identity{ID: 41}},
		{name: "banned or revoked session", err: session.ErrUnauthorized},
		{name: "nil identity"},
		{name: "invalid user ID", identity: &session.Identity{IsStaff: 1}},
		{name: "invalid role flag", identity: &session.Identity{ID: 41, IsStaff: 2}},
	} {
		for _, route := range appleOperatorRoutes {
			t.Run(state.name+"/"+route.action, func(t *testing.T) {
				service := &fakeAppleIDService{}
				sessions := &freshAppleOperatorSession{strictAppleIDSession: strictAppleIDSession{identity: &session.Identity{ID: 41, IsAdmin: 1, IsStaff: 1}}, current: state.identity, freshErr: state.err}
				router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAppleIDServices(service, service))
				rec := requestAppleOperator(router, route)
				if rec.Code != http.StatusForbidden || service.adminCalls != 0 || sessions.freshCalls != 1 || sessions.cachedCalls != 0 {
					t.Fatalf("stale privilege accepted: status=%d service=%d fresh=%d cached=%d body=%s", rec.Code, service.adminCalls, sessions.freshCalls, sessions.cachedCalls, rec.Body.String())
				}
				assertAppleIDNoStore(t, rec)
			})
		}
	}
}

func TestAppleIDStaffMutationActorAlwaysComesFromFreshSession(t *testing.T) {
	for _, action := range []string{"inventory/disable", "order/credentials", "order/replace", "order/cancel", "order/refund"} {
		t.Run(action, func(t *testing.T) {
			service := &fakeAppleIDService{}
			sessions := &freshAppleOperatorSession{strictAppleIDSession: strictAppleIDSession{identity: &session.Identity{ID: 999, IsAdmin: 1}}, current: &session.Identity{ID: 41, IsStaff: 1}}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAppleIDServices(service, service))
			rec := requestAppleOperator(router, appleOperatorRoute{method: http.MethodPost, action: action, query: "&admin_id=999&actor_user_id=999", body: `{"id":5,"reason":"customer requested","admin_id":998,"actor_id":998,"actor_user_id":998}`})
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			got := map[string]int64{"inventory/disable": service.lastDisable.AdminID, "order/credentials": service.lastAdminID, "order/replace": service.lastReplace.AdminID, "order/cancel": service.lastCancel.AdminID, "order/refund": service.lastRefund.AdminID}[action]
			if got != 41 {
				t.Fatalf("actor was forged or loaded from cache: %d", got)
			}
			assertAppleIDNoStore(t, rec)
		})
	}
}

func TestAppleIDStaffAuthenticationPreservesJSONAndFormPayloads(t *testing.T) {
	for _, body := range []struct{ contentType, data string }{
		{"application/json", `{"auth_data":"body-token","name":"VIP account","region":"US","price":1234}`},
		{"application/x-www-form-urlencoded", "auth_data=body-token&name=VIP+account&region=US&price=1234"},
	} {
		service := &fakeAppleIDService{}
		sessions := &freshAppleOperatorSession{current: &session.Identity{ID: 41, IsStaff: 1}}
		router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAppleIDServices(service, service))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/product/save", strings.NewReader(body.data))
		req.Header.Set("Content-Type", body.contentType)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || sessions.lastToken != "body-token" || service.lastSaveProduct.Name != "VIP account" || service.lastSaveProduct.Price != 1234 {
			t.Fatalf("payload lost after authentication: status=%d token=%s request=%+v", rec.Code, sessions.lastToken, service.lastSaveProduct)
		}
	}
	service := &fakeAppleIDService{}
	sessions := &freshAppleOperatorSession{current: &session.Identity{ID: 41, IsStaff: 1}}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAppleIDServices(service, service))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/inventory/import", strings.NewReader(`{"auth_data":"body-token","product_id":3,"items":[{"credential":"account password secret answers"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || service.lastImport.ProductID != 3 || len(service.lastImport.Items) != 1 || service.lastImport.Items[0].Credential != "account password secret answers" {
		t.Fatalf("inventory import body lost: status=%d req=%+v", rec.Code, service.lastImport)
	}
}

func TestAppleIDStaffGrantDoesNotOpenOtherAdministratorModulesOrClearHistory(t *testing.T) {
	for _, route := range []appleOperatorRoute{
		{method: http.MethodGet, action: "/api/v1/localadmin/user/fetch"},
		{method: http.MethodGet, action: "/api/v1/localadmin/config/fetch"},
		{method: http.MethodGet, action: "/api/v1/localadmin/plan/fetch"},
		{method: http.MethodGet, action: "/api/v1/localadmin/support/operations"},
		{method: http.MethodPost, action: "/api/v1/localadmin/support/operations/clear", body: `{"through_id":10,"confirm":true}`},
		{method: http.MethodPost, action: "/api/v1/staff/user/update", body: `{"id":8,"is_admin":1}`},
	} {
		t.Run(route.action, func(t *testing.T) {
			support := &fakeSupportService{enabled: true}
			apple := &fakeAppleIDService{}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 41, IsStaff: 1}}), WithAppleIDServices(apple, apple), WithAdminService(&fakeAdminService{}), WithSupportService(support))
			req := httptest.NewRequest(route.method, route.action+"?auth_data=staff-token", strings.NewReader(route.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || support.reads != 0 || support.writes != 0 || apple.adminCalls != 0 {
				t.Fatalf("unrelated privilege expanded: status=%d reads=%d writes=%d apple=%d body=%s", rec.Code, support.reads, support.writes, apple.adminCalls, rec.Body.String())
			}
		})
	}
}
