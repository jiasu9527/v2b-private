package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/appleid"
	"forest/go-api/internal/config"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/session"
)

type fakeAppleIDService struct {
	products   []appleid.Product
	orders     []appleid.Order
	order      appleid.Order
	delivery   appleid.Delivery
	credential appleid.AdminCredentialDetail

	userErr  error
	adminErr error

	listProductsCalls int
	lastUserID        int64
	lastTradeNo       string
	credentialsCalls  int
	lastAdminID       int64
	lastAdminOrderID  int64
	lastDisable       appleid.AdminDisableInventoryRequest
	lastReplace       appleid.AdminReplaceInventoryRequest
	lastRefund        appleid.AdminMarkRefundedRequest
	auditCalls        int
	lastAudit         appleid.AdminAuditListRequest
}

type strictAppleIDSession struct {
	identity *session.Identity
}

func (s strictAppleIDSession) Authenticate(_ context.Context, _ string, requireAdmin bool) (*session.Identity, error) {
	if s.identity == nil || requireAdmin && s.identity.IsAdmin == 0 {
		return nil, session.ErrUnauthorized
	}
	return s.identity, nil
}

func (strictAppleIDSession) ListSessions(context.Context, int64) (map[string]session.SessionMeta, error) {
	return nil, nil
}

func (strictAppleIDSession) RemoveSession(context.Context, int64, string) (bool, error) {
	return false, nil
}

func (f *fakeAppleIDService) ListProducts(context.Context) ([]appleid.Product, error) {
	f.listProductsCalls++
	return f.products, f.userErr
}

func (f *fakeAppleIDService) CreateOrder(_ context.Context, userID int64, _ appleid.CreateOrderRequest) (appleid.Order, error) {
	f.lastUserID = userID
	return f.order, f.userErr
}

func (f *fakeAppleIDService) ListOrders(_ context.Context, userID int64) ([]appleid.Order, error) {
	f.lastUserID = userID
	return f.orders, f.userErr
}

func (f *fakeAppleIDService) OrderDetail(_ context.Context, userID int64, tradeNo string) (appleid.Order, error) {
	f.lastUserID = userID
	f.lastTradeNo = tradeNo
	return f.order, f.userErr
}

func (f *fakeAppleIDService) Delivery(_ context.Context, userID int64, tradeNo string) (appleid.Delivery, error) {
	f.lastUserID = userID
	f.lastTradeNo = tradeNo
	return f.delivery, f.userErr
}

func (f *fakeAppleIDService) CancelOrder(_ context.Context, userID int64, tradeNo string) error {
	f.lastUserID = userID
	f.lastTradeNo = tradeNo
	return f.userErr
}

func (f *fakeAppleIDService) AdminListProducts(context.Context) ([]appleid.AdminProduct, error) {
	return nil, f.adminErr
}

func (f *fakeAppleIDService) AdminSaveProduct(context.Context, appleid.AdminSaveProductRequest) (appleid.AdminProduct, error) {
	return appleid.AdminProduct{}, f.adminErr
}

func (f *fakeAppleIDService) AdminSetProductEnabled(context.Context, appleid.AdminSetProductEnabledRequest) error {
	return f.adminErr
}

func (f *fakeAppleIDService) AdminDeleteProduct(context.Context, int64) error {
	return f.adminErr
}

func (f *fakeAppleIDService) AdminAddInventoryBatch(context.Context, appleid.AdminAddInventoryRequest) (appleid.AdminAddInventoryResult, error) {
	return appleid.AdminAddInventoryResult{}, f.adminErr
}

func (f *fakeAppleIDService) AdminListInventory(context.Context, appleid.AdminInventoryListRequest) (appleid.AdminInventoryListResult, error) {
	return appleid.AdminInventoryListResult{}, f.adminErr
}

func (f *fakeAppleIDService) AdminDisableInventory(_ context.Context, req appleid.AdminDisableInventoryRequest) error {
	f.lastDisable = req
	return f.adminErr
}

func (f *fakeAppleIDService) AdminListOrders(context.Context, appleid.AdminOrderListRequest) (appleid.AdminOrderListResult, error) {
	return appleid.AdminOrderListResult{}, f.adminErr
}

func (f *fakeAppleIDService) AdminGetOrderDetail(context.Context, int64) (appleid.AdminOrderDetail, error) {
	return appleid.AdminOrderDetail{}, f.adminErr
}

func (f *fakeAppleIDService) AdminGetOrderCredentials(_ context.Context, orderID, adminID int64) (appleid.AdminCredentialDetail, error) {
	f.credentialsCalls++
	f.lastAdminOrderID = orderID
	f.lastAdminID = adminID
	return f.credential, f.adminErr
}

func (f *fakeAppleIDService) AdminReplaceInventory(_ context.Context, req appleid.AdminReplaceInventoryRequest) (appleid.AdminReplaceInventoryResult, error) {
	f.lastReplace = req
	return appleid.AdminReplaceInventoryResult{}, f.adminErr
}

func (f *fakeAppleIDService) AdminMarkOrderRefunded(_ context.Context, req appleid.AdminMarkRefundedRequest) error {
	f.lastRefund = req
	return f.adminErr
}

func (f *fakeAppleIDService) AdminListAudits(_ context.Context, req appleid.AdminAuditListRequest) (appleid.AdminAuditListResult, error) {
	f.auditCalls++
	f.lastAudit = req
	return appleid.AdminAuditListResult{}, f.adminErr
}

type fakeAppleIDPaymentService struct {
	checkoutCalls int
	lastUserID    int64
	lastRequest   payment.CheckoutRequest
	result        payment.CheckoutResult
	err           error
}

func (f *fakeAppleIDPaymentService) Checkout(_ context.Context, userID int64, req payment.CheckoutRequest) (payment.CheckoutResult, error) {
	f.checkoutCalls++
	f.lastUserID = userID
	f.lastRequest = req
	return f.result, f.err
}

func (*fakeAppleIDPaymentService) Notify(context.Context, string, string, payment.NotifyRequest) (string, error) {
	return "", nil
}

func TestAppleIDRoutesRequireExpectedAuthentication(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		requireAdmin bool
	}{
		{name: "user login", method: http.MethodGet, path: "/api/v1/apple-id/products?auth_data=invalid"},
		{name: "administrator", method: http.MethodGet, path: "/api/v1/localadmin/apple-id/product/fetch?auth_data=invalid", requireAdmin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessionService{authErr: session.ErrUnauthorized}
			service := &fakeAppleIDService{}
			router := NewRouter(
				config.Config{AdminPath: "localadmin"},
				WithSessionService(sessions),
				WithAppleIDServices(service, service),
			)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if sessions.lastRequireAdmin != tt.requireAdmin {
				t.Fatalf("requireAdmin = %v, want %v", sessions.lastRequireAdmin, tt.requireAdmin)
			}
			if service.listProductsCalls != 0 || service.credentialsCalls != 0 {
				t.Fatalf("business service called before authentication: %+v", service)
			}
		})
	}
}

func TestAdminAppleIDRouteRejectsNonAdminUser(t *testing.T) {
	service := &fakeAppleIDService{}
	router := NewRouter(
		config.Config{AdminPath: "localadmin"},
		WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 41, Email: "user@example.com"}}),
		WithAppleIDServices(service, service),
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/apple-id/product/fetch?auth_data=user-token", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if service.listProductsCalls != 0 || service.credentialsCalls != 0 {
		t.Fatalf("admin business service called for non-admin user: %+v", service)
	}
}

func TestAppleIDOrderDetailHidesOtherUsersOrder(t *testing.T) {
	sessions := &fakeSessionService{user: &session.Identity{ID: 41, Email: "other@example.com"}}
	service := &fakeAppleIDService{userErr: appleid.ErrOrderNotFound}
	router := NewRouter(
		config.Config{},
		WithSessionService(sessions),
		WithAppleIDServices(service, service),
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apple-id/orders/apple-owned-by-7?auth_data=user-token", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if service.lastUserID != 41 || service.lastTradeNo != "apple-owned-by-7" {
		t.Fatalf("unexpected scoped lookup: user=%d trade=%q", service.lastUserID, service.lastTradeNo)
	}
}

func TestAppleIDDeliveryDisablesCaching(t *testing.T) {
	sessions := &fakeSessionService{user: &session.Identity{ID: 7, Email: "buyer@example.com"}}
	service := &fakeAppleIDService{delivery: appleid.Delivery{
		OrderID: 19, TradeNo: "apple-19", Account: "buyer@example.com", Password: "secret", ProductID: 3,
	}}
	router := NewRouter(
		config.Config{},
		WithSessionService(sessions),
		WithAppleIDServices(service, service),
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apple-id/orders/apple-19/delivery?auth_data=user-token", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertAppleIDNoStore(t, rec)
	if service.lastUserID != 7 || service.lastTradeNo != "apple-19" {
		t.Fatalf("unexpected delivery scope: user=%d trade=%q", service.lastUserID, service.lastTradeNo)
	}
}

func TestAppleIDPaymentDisablesCachingAndChecksOutAppleOrder(t *testing.T) {
	sessions := &fakeSessionService{user: &session.Identity{ID: 7, Email: "buyer@example.com"}}
	payments := &fakeAppleIDPaymentService{result: payment.CheckoutResult{Type: 1, Data: "https://pay.example.com/apple-19"}}
	router := NewRouter(
		config.Config{},
		WithSessionService(sessions),
		WithPaymentService(payments),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/apple-id/orders/apple-19/payment?auth_data=user-token",
		strings.NewReader("method=9&token=checkout-token"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertAppleIDNoStore(t, rec)
	if payments.checkoutCalls != 1 || payments.lastUserID != 7 {
		t.Fatalf("unexpected checkout call: calls=%d user=%d", payments.checkoutCalls, payments.lastUserID)
	}
	if payments.lastRequest.TradeNo != "apple-19" || payments.lastRequest.MethodID != 9 || payments.lastRequest.Token != "checkout-token" {
		t.Fatalf("unexpected checkout request: %+v", payments.lastRequest)
	}
}

func TestAppleIDPaymentRejectsSubscriptionTradeNumber(t *testing.T) {
	sessions := &fakeSessionService{user: &session.Identity{ID: 7}}
	payments := &fakeAppleIDPaymentService{}
	router := NewRouter(
		config.Config{},
		WithSessionService(sessions),
		WithPaymentService(payments),
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/apple-id/orders/T-subscription-19/payment?auth_data=user-token",
		strings.NewReader("method=9"),
	))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertAppleIDNoStore(t, rec)
	if payments.checkoutCalls != 0 {
		t.Fatalf("payment service called %d times for subscription trade number", payments.checkoutCalls)
	}
}

func TestAppleIDPaymentMapsBusinessErrorsBeforePaymentErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "missing apple order", err: fmt.Errorf("lookup external order: %w", appleid.ErrOrderNotFound), wantStatus: http.StatusNotFound},
		{name: "apple stock conflict", err: appleid.ErrOutOfStock, wantStatus: http.StatusConflict},
		{name: "apple service unavailable", err: appleid.ErrUnavailable, wantStatus: http.StatusServiceUnavailable},
		{name: "payment method behavior unchanged", err: payment.ErrPaymentMethodUnavailable, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payments := &fakeAppleIDPaymentService{err: tt.err}
			router := NewRouter(
				config.Config{},
				WithSessionService(&fakeSessionService{user: &session.Identity{ID: 7}}),
				WithPaymentService(payments),
			)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPost,
				"/api/v1/apple-id/orders/apple-19/payment?auth_data=user-token",
				strings.NewReader("method=9"),
			))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			assertAppleIDNoStore(t, rec)
			if payments.checkoutCalls != 1 {
				t.Fatalf("checkout calls = %d, want 1", payments.checkoutCalls)
			}
		})
	}
}

func TestAppleIDUnknownErrorDoesNotExposeInternalDetails(t *testing.T) {
	service := &fakeAppleIDService{userErr: errors.New("postgres connection contains private credential")}
	router := NewRouter(
		config.Config{},
		WithSessionService(&fakeSessionService{user: &session.Identity{ID: 7}}),
		WithAppleIDServices(service, service),
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apple-id/orders/apple-19?auth_data=user-token", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "private credential") {
		t.Fatalf("response exposed internal error: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "系统繁忙，请稍后重试") {
		t.Fatalf("response missing generic error: %s", rec.Body.String())
	}
}

func TestAppleIDPaymentUnknownErrorDoesNotExposeInternalDetails(t *testing.T) {
	payments := &fakeAppleIDPaymentService{err: errors.New("postgres connection contains private credential")}
	router := NewRouter(
		config.Config{},
		WithSessionService(&fakeSessionService{user: &session.Identity{ID: 7}}),
		WithPaymentService(payments),
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/apple-id/orders/apple-19/payment?auth_data=user-token", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "postgres") || strings.Contains(rec.Body.String(), "private credential") {
		t.Fatalf("payment response exposed internal error: %s", rec.Body.String())
	}
	assertAppleIDNoStore(t, rec)
}

func TestAdminAppleIDCredentialsRequireAdminAndCarryAuditActor(t *testing.T) {
	sessions := &fakeSessionService{user: &session.Identity{ID: 77, Email: "admin@example.com", IsAdmin: 1}}
	service := &fakeAppleIDService{credential: appleid.AdminCredentialDetail{
		OrderID: 11, TradeNo: "apple-11", InventoryID: 8, ProductID: 3, Account: "buyer@example.com", Password: "secret",
	}}
	router := NewRouter(
		config.Config{AdminPath: "localadmin"},
		WithSessionService(sessions),
		WithAppleIDServices(service, service),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/localadmin/apple-id/order/credentials?auth_data=admin-token",
		strings.NewReader("id=11"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertAppleIDNoStore(t, rec)
	if !sessions.lastRequireAdmin {
		t.Fatal("credentials endpoint did not require administrator authentication")
	}
	if service.credentialsCalls != 1 || service.lastAdminOrderID != 11 || service.lastAdminID != 77 {
		t.Fatalf("audit actor was not forwarded: calls=%d order=%d admin=%d", service.credentialsCalls, service.lastAdminOrderID, service.lastAdminID)
	}
}

func TestAdminAppleIDMutationRoutesCarryAuditActor(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		assert func(*testing.T, *fakeAppleIDService)
	}{
		{
			name: "disable inventory",
			path: "/api/v1/localadmin/apple-id/inventory/disable?auth_data=admin-token",
			body: "id=8",
			assert: func(t *testing.T, service *fakeAppleIDService) {
				if service.lastDisable.InventoryID != 8 || service.lastDisable.AdminID != 77 {
					t.Fatalf("unexpected disable request: %+v", service.lastDisable)
				}
			},
		},
		{
			name: "replace inventory",
			path: "/api/v1/localadmin/apple-id/order/replace?auth_data=admin-token",
			body: "id=11&inventory_id=9&reason=login+failed",
			assert: func(t *testing.T, service *fakeAppleIDService) {
				if service.lastReplace.OrderID != 11 || service.lastReplace.AdminID != 77 || service.lastReplace.NewInventoryID == nil || *service.lastReplace.NewInventoryID != 9 {
					t.Fatalf("unexpected replace request: %+v", service.lastReplace)
				}
			},
		},
		{
			name: "confirm refund",
			path: "/api/v1/localadmin/apple-id/order/refund?auth_data=admin-token",
			body: "id=11&reason=gateway+refund",
			assert: func(t *testing.T, service *fakeAppleIDService) {
				if service.lastRefund.OrderID != 11 || service.lastRefund.AdminID != 77 {
					t.Fatalf("unexpected refund request: %+v", service.lastRefund)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessionService{user: &session.Identity{ID: 77, IsAdmin: 1}}
			service := &fakeAppleIDService{}
			router := NewRouter(
				config.Config{AdminPath: "localadmin"},
				WithSessionService(sessions),
				WithAppleIDServices(service, service),
			)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if !sessions.lastRequireAdmin {
				t.Fatal("mutation did not require administrator authentication")
			}
			tt.assert(t, service)
		})
	}
}

func TestAdminAppleIDAuditsRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "current", query: "current=not-a-number&page_size=20"},
		{name: "page size", query: "current=1&page_size=not-a-number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeAppleIDService{}
			router := NewRouter(
				config.Config{AdminPath: "localadmin"},
				WithSessionService(&fakeSessionService{user: &session.Identity{ID: 77, IsAdmin: 1}}),
				WithAppleIDServices(service, service),
			)

			rec := httptest.NewRecorder()
			path := "/api/v1/localadmin/apple-id/order/audits?auth_data=admin-token&id=11&" + tt.query
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if service.auditCalls != 0 {
				t.Fatalf("audit service called %d times for invalid pagination", service.auditCalls)
			}
		})
	}
}

func assertAppleIDNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q", got)
	}
}
