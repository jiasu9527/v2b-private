package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/session"
)

var _ newAPIWorkspaceRechargeService = (*newapibridge.Service)(nil)

type rechargeHTTPFixture struct {
	newAPIHTTPFixture
	request  newapibridge.WorkspaceRechargeRequest
	checkout newapibridge.WorkspaceRechargeCheckoutRequest
	base     string
	err      error
}

func (f *rechargeHTTPFixture) record(id int64, auth string) {
	f.actor = id
	f.issuedAuth = auth
	f.calls++
}
func (f *rechargeHTTPFixture) WorkspaceRechargeOptions(_ context.Context, id int64, auth string) (any, error) {
	f.record(id, auth)
	return map[string]any{"enabled": true, "currency": "CNY", "payment_methods": []any{}}, f.err
}
func (f *rechargeHTTPFixture) CreateWorkspaceRecharge(_ context.Context, id int64, auth string, r newapibridge.WorkspaceRechargeRequest) (newapibridge.RechargeOrder, error) {
	f.record(id, auth)
	f.request = r
	return newapibridge.RechargeOrder{TradeNo: "AIT123", AmountFen: r.AmountFen, Status: "pending"}, f.err
}
func (f *rechargeHTTPFixture) CheckoutWorkspaceRecharge(_ context.Context, id int64, r newapibridge.WorkspaceRechargeCheckoutRequest, base string) (newapibridge.RechargeCheckout, error) {
	f.record(id, "")
	f.checkout = r
	f.base = base
	return newapibridge.RechargeCheckout{}, f.err
}
func (f *rechargeHTTPFixture) WorkspaceRechargeDetail(_ context.Context, id int64, trade string) (newapibridge.RechargeOrder, error) {
	f.record(id, "")
	return newapibridge.RechargeOrder{TradeNo: trade, Status: "paid_pending_credit"}, f.err
}
func (f *rechargeHTTPFixture) WorkspaceRecharges(_ context.Context, id int64, page, size int) (newapibridge.RechargePage, error) {
	f.record(id, "")
	return newapibridge.RechargePage{}, f.err
}

func rechargeTestRouter(f *rechargeHTTPFixture) (http.Handler, *newAPIFreshSession) {
	s := &newAPIFreshSession{identity: &session.Identity{ID: 81812}}
	return NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(s), WithNewAPIBridge(f)), s
}

func TestRechargeHTTPAuthenticationAndStrictInputs(t *testing.T) {
	f := &rechargeHTTPFixture{}
	r, sessions := rechargeTestRouter(f)
	base := "/api/v1/user/new-api/recharge/"
	w := newAPIHTTPCall(t, r, "GET", base+"options", "", "panel-auth")
	if w.Code != 200 || f.actor != 81812 || f.issuedAuth != "panel-auth" || sessions.freshCalls != 1 {
		t.Fatal(w.Code, w.Body.String(), f.actor)
	}
	before := f.calls
	for _, tt := range []struct{ method, path, body, auth string }{
		{"GET", "options", "", "nap_valid"},
		{"GET", "options?auth_data=panel-auth", "", ""},
		{"GET", "options?user_id=9", "", "panel-auth"},
		{"GET", "options", "{}", "panel-auth"},
		{"GET", "create", "", "panel-auth"},
		{"POST", "create", `null`, "panel-auth"},
		{"POST", "create", `{"amount_fen":100,"request_id":"request-one","user_id":3}`, "panel-auth"},
		{"POST", "create", `{"amount_fen":-1,"request_id":"request-one"}`, "panel-auth"},
		{"POST", "create", `{"amount_fen":1.5,"request_id":"request-one"}`, "panel-auth"},
		{"POST", "create", `{"amount_fen":100,"request_id":"request-one"} {}`, "panel-auth"},
		{"POST", "create?amount_fen=100", `{"amount_fen":100,"request_id":"request-one"}`, "panel-auth"},
		{"POST", "checkout", `{"trade_no":"AIT123","payment_id":1,"amount_fen":1}`, "panel-auth"},
		{"POST", "checkout", `{"trade_no":"AIT123","payment_id":0}`, "panel-auth"},
		{"POST", "checkout", `{"trade_no":"ordinary-order","payment_id":1}`, "panel-auth"},
		{"POST", "checkout", `{"trade_no":"AIT123","payment_id":1,"remote_user_id":8}`, "panel-auth"},
		{"GET", "detail?trade_no=AIT123&trade_no=AIT456", "", "panel-auth"},
		{"GET", "detail?trade_no=AIT123&user_id=3", "", "panel-auth"},
		{"GET", "detail?trade_no=AIT%3Cscript%3E", "", "panel-auth"},
		{"GET", "detail", "", "panel-auth"},
		{"GET", "orders?current=0", "", "panel-auth"},
		{"GET", "orders?page_size=101", "", "panel-auth"},
		{"GET", "orders?status=succeeded", "", "panel-auth"},
	} {
		w = newAPIHTTPCall(t, r, tt.method, base+tt.path, tt.body, tt.auth)
		if w.Code < 400 || f.calls != before {
			t.Fatalf("%s status=%d calls=%d body=%s", tt.path, w.Code, f.calls, w.Body.String())
		}
	}
	w = newAPIHTTPCall(t, r, "POST", base+"create", `{"amount_fen":100,"request_id":"request-one"}`, "panel-auth")
	if w.Code != 200 || f.request.AmountFen != 100 || f.actor != 81812 || f.issuedAuth != "panel-auth" {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal(w.Header())
	}
	w = newAPIHTTPCall(t, r, "POST", base+"checkout", `{"trade_no":"AIT123","payment_id":2}`, "panel-auth")
	if w.Code != 200 || f.checkout.TradeNo != "AIT123" || f.checkout.PaymentID != 2 || f.actor != 81812 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = newAPIHTTPCall(t, r, "GET", base+"detail?trade_no=AIT123", "", "panel-auth")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "paid_pending_credit") {
		t.Fatal(w.Code, w.Body.String())
	}
	sessions.revoked = true
	before = f.calls
	w = newAPIHTTPCall(t, r, "GET", base+"orders", "", "panel-auth")
	if w.Code < 400 || f.calls != before {
		t.Fatal("revoked identity used")
	}
}

func TestRechargeHTTPReturnOriginAndErrors(t *testing.T) {
	f := &rechargeHTTPFixture{}
	r, _ := rechargeTestRouter(f)
	req := httptest.NewRequest("POST", "/api/v1/user/new-api/recharge/checkout", strings.NewReader(`{"trade_no":"AIT123","payment_id":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer panel-auth")
	req.Header.Set("Origin", "https://forest.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || f.base != "https://forest.example" {
		t.Fatal(w.Code, w.Body.String(), f.base)
	}
	for _, e := range []error{payment.ErrCheckoutInProgress, payment.ErrCheckoutConfigChanged, payment.ErrOrderPaidOrMissing, payment.ErrPaymentMethodUnavailable, payment.ErrUnsupportedGateway, newapibridge.ErrWorkspaceUnknown} {
		w = httptest.NewRecorder()
		rechargeError(w, e)
		if w.Code != 409 {
			t.Fatal(e, w.Code, w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	rechargeError(w, errors.New("secret upstream response"))
	if w.Code < 400 || strings.Contains(w.Body.String(), "secret upstream response") {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w.Body.String())
	}
}
