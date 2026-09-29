package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/appleid"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func TestAppleIDAdminCancelRequiresAdmin(t *testing.T) {
	for _, tt := range []struct {
		name     string
		identity *session.Identity
		token    string
	}{
		{name: "no login"},
		{name: "invalid session", token: "invalid"},
		{name: "ordinary user", token: "user-token", identity: &session.Identity{ID: 41}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeAppleIDService{}
			router := NewRouter(config.Config{AdminPath: "localadmin"},
				WithSessionService(strictAppleIDSession{identity: tt.identity}),
				WithAppleIDServices(service, service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/order/cancel", strings.NewReader("id=11"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tt.token != "" {
				req.Header.Set("Authorization", tt.token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if service.lastCancel != (appleid.AdminCancelOrderRequest{}) {
				t.Fatalf("cancellation service called before administrator authentication: %+v", service.lastCancel)
			}
			assertAppleIDNoStore(t, rec)
		})
	}
}

func TestAppleIDAdminCancelRejectsOtherMethods(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			service := &fakeAppleIDService{}
			router := NewRouter(config.Config{AdminPath: "localadmin"},
				WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 77, IsAdmin: 1}}),
				WithAppleIDServices(service, service))
			req := httptest.NewRequest(method, "/api/v1/localadmin/apple-id/order/cancel?auth_data=admin-token&id=11", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("status = %d, Allow = %q; want 405 and POST", rec.Code, rec.Header().Get("Allow"))
			}
			if service.lastCancel != (appleid.AdminCancelOrderRequest{}) {
				t.Fatalf("cancellation service called for %s: %+v", method, service.lastCancel)
			}
			assertAppleIDNoStore(t, rec)
		})
	}
}

func TestAppleIDAdminCancelRejectsInvalidID(t *testing.T) {
	for _, body := range []string{"", "id=0", "id=-1", "id=not-a-number", "id=1.5", "id=9223372036854775808"} {
		t.Run(body, func(t *testing.T) {
			service := &fakeAppleIDService{}
			router := NewRouter(config.Config{AdminPath: "localadmin"},
				WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 77, IsAdmin: 1}}),
				WithAppleIDServices(service, service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/order/cancel?auth_data=admin-token", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if service.lastCancel != (appleid.AdminCancelOrderRequest{}) {
				t.Fatalf("cancellation service called with invalid ID: %+v", service.lastCancel)
			}
			assertAppleIDNoStore(t, rec)
		})
	}
}

func TestAppleIDAdminCancelMapsOrderErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: appleid.ErrOrderNotFound, status: http.StatusNotFound},
		{name: "already paid", err: appleid.ErrOrderStatus, status: http.StatusConflict},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeAppleIDService{adminErr: tt.err}
			router := NewRouter(config.Config{AdminPath: "localadmin"},
				WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 77, IsAdmin: 1}}),
				WithAppleIDServices(service, service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/order/cancel?auth_data=admin-token", strings.NewReader("id=11"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.status, rec.Body.String())
			}
			if service.lastCancel.OrderID != 11 || service.lastCancel.AdminID != 77 {
				t.Fatalf("unexpected cancellation: %+v", service.lastCancel)
			}
			assertAppleIDNoStore(t, rec)
		})
	}
}

func TestAppleIDAdminCancelUsesSessionActorAndDisablesCache(t *testing.T) {
	service := &fakeAppleIDService{}
	router := NewRouter(config.Config{AdminPath: "localadmin"},
		WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 77, IsAdmin: 1}}),
		WithAppleIDServices(service, service))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/order/cancel", strings.NewReader(`{"auth_data":"admin-token","id":11,"reason":"customer request","admin_id":99,"actor_user_id":99}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		Data bool `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || !result.Data {
		t.Fatalf("unexpected cancellation response: %s; error=%v", rec.Body.String(), err)
	}
	if service.lastCancel != (appleid.AdminCancelOrderRequest{OrderID: 11, AdminID: 77, Reason: "customer request"}) {
		t.Fatalf("request actor overrode session actor or request data was lost: %+v", service.lastCancel)
	}
	assertAppleIDNoStore(t, rec)
}
