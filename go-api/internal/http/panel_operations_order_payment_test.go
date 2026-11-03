package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

func TestPanelOrderResponsesNeverExposeCheckoutSecrets(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		for _, checkout := range []any{
			"https://PAYMENT_SENTINEL-gateway.invalid/submit?pid=PAYMENT_SENTINEL-merchant&sign=PAYMENT_SENTINEL-sign&sign_type=MD5&notify_url=https://PAYMENT_SENTINEL-callback.invalid/notify",
			map[string]any{"url": "https://PAYMENT_SENTINEL-gateway.invalid", "pid": "PAYMENT_SENTINEL-merchant", "sign": "PAYMENT_SENTINEL-sign", "notify_url": "https://PAYMENT_SENTINEL-callback.invalid/notify"},
			[]any{map[string]any{"redirect": "PAYMENT_SENTINEL-redirect"}},
		} {
			detail := map[string]any{"id": 1, "trade_no": "fixture", "payment_id": 7, "total_amount": 1250, "commission_status": 2, "checkout_result": checkout, "checkout_claim": "PAYMENT_SENTINEL-claim", "checkout_fingerprint": "PAYMENT_SENTINEL-fingerprint", "checkout_claim_expires_at": 1790812800, "surplus_orders": []any{map[string]any{"id": 2, "checkout_result": checkout}}}
			svc := &fakeAdminService{orderList: admin.OrderListResult{Data: []map[string]any{detail}, Total: 1}, orderDetail: detail}
			p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: svc})
			for _, name := range []string{"orders.list", "orders.detail"} {
				args := json.RawMessage(`{}`)
				if name == "orders.detail" {
					args = json.RawMessage(`{"id":1}`)
				}
				out, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: isAdmin}, name, args)
				if err != nil || strings.Contains(string(out), "PAYMENT_SENTINEL") || strings.Contains(string(out), "1790812800") {
					t.Fatalf("%s admin=%t leaked checkout: %s %v", name, isAdmin, out, err)
				}
				for _, expected := range []string{`"payment_id":7`, `"total_amount":1250`, `"commission_status":2`, `"trade_no":"fixture"`} {
					if !strings.Contains(string(out), expected) {
						t.Fatalf("business data lost %s: %s", expected, out)
					}
				}
			}
			// Adapter marshals a copy; the actual order/checkout source stays intact.
			source, _ := json.Marshal(svc.orderDetail)
			if !strings.Contains(string(source), "PAYMENT_SENTINEL") {
				t.Fatal("scrubber altered actual checkout response source")
			}
		}
	}
}

func TestPanelFilterEchoHidesSensitiveValues(t *testing.T) {
	raw := []byte(`{"applied_filters":[{"key":"token","condition":"=","value":"FILTER_SECRET_TOKEN"},{"key":"uuid","condition":"=","value":"FILTER_SECRET_UUID"},{"key":"password","condition":"=","value":"FILTER_SECRET_PASSWORD"},{"key":"api_key","condition":"in","values":["FILTER_SECRET_API"]},{"key":"id","condition":"=","value":"42"},{"key":"created_at","condition":"<","value":"1790812800"}]}`)
	out, err := sanitizePanelResponse(raw, "application/json")
	if err != nil || strings.Contains(string(out), "FILTER_SECRET") {
		t.Fatal(string(out), err)
	}
	for _, value := range []string{`"key":"id"`, `"key":"token"`, `"value":"42"`, `"value":"1790812800"`} {
		if !strings.Contains(string(out), value) {
			t.Fatal("safe applied filter lost", string(out))
		}
	}
}

func TestPanelFilterShapeCannotExposeArbitraryCredentialKey(t *testing.T) {
	out, err := sanitizePanelResponse([]byte(`{"config":{"key":"RAW_SECRET_KEY","condition":"=","value":"visible"}}`), "application/json")
	if err != nil || strings.Contains(string(out), "RAW_SECRET_KEY") {
		t.Fatal(string(out), err)
	}
}
