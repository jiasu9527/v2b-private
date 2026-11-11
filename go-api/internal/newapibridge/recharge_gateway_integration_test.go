package newapibridge

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/payment"
)

// This test uses the real payment gateway encoder/verifier and PostgreSQL;
// only the remote New API is a local httptest fixture. No provider is contacted.
func TestRechargeGatewaySignedCallbackRoundTrip(t *testing.T) {
	s, remote := rechargeFixture(t)
	_, err := s.db.Exec(`ALTER TABLE v2_payment ADD COLUMN uuid TEXT, ADD COLUMN config TEXT, ADD COLUMN notify_domain TEXT;
UPDATE v2_payment SET uuid='local-recharge-gateway',config='{"url":"https://pay.example.test","pid":"local-merchant","key":"local-payment-key"}' WHERE id=9`)
	if err != nil {
		t.Fatal(err)
	}
	payments := payment.NewDBService(config.Config{AppURL: "https://backend.example.test"}, s.db, nil).
		WithExternalOrderManager(payment.NewExternalOrderManagers(s))
	s.WithRechargePayments(payments)
	ctx := context.Background()
	o := createRecharge(t, s, "gateway-integration")
	checkout, err := s.CheckoutWorkspaceRecharge(ctx, 1, WorkspaceRechargeCheckoutRequest{TradeNo: o.TradeNo, PaymentID: 9}, "https://forest.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Order.Status != "pending" || checkout.Order.TotalAmountFen != 128 || checkout.Checkout.Type != 1 {
		t.Fatalf("checkout: %+v", checkout)
	}
	link, ok := checkout.Checkout.Data.(string)
	if !ok {
		t.Fatal("expected hosted checkout")
	}
	u, err := url.Parse(link)
	if err != nil || u.Host != "pay.example.test" {
		t.Fatal("wrong gateway", err)
	}
	q := u.Query()
	if q.Get("money") != "1.28" || q.Get("out_trade_no") != o.TradeNo || q.Get("return_url") != "https://forest.example.test/ai?tab=records&recharge="+o.TradeNo {
		t.Fatal("gateway amount/order/return mismatch")
	}
	// Generic checkout must replay the same URL, not create another order or
	// use the origin of a later request to replace the first return destination.
	cached, err := payments.Checkout(ctx, 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9, RequestBaseURL: "https://changed.example.test"})
	if err != nil || cached.Data != link {
		t.Fatal("cached payment changed", err)
	}
	if _, err = payments.Checkout(ctx, 2, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9}); err == nil {
		t.Fatal("cross-user checkout accepted")
	}
	params := url.Values{"pid": {"local-merchant"}, "trade_no": {"local-provider-transaction"}, "out_trade_no": {o.TradeNo}, "money": {"1.28"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
	call := func(values url.Values) (string, error) {
		p := map[string]string{}
		for k := range values {
			p[k] = values.Get(k)
		}
		return payments.Notify(ctx, "EPay", "local-recharge-gateway", payment.NotifyRequest{Params: p})
	}
	params.Set("sign", "bad-signature")
	if _, err = call(params); err == nil {
		t.Fatal("unsigned payment accepted")
	}
	params.Set("money", "0.01")
	params.Set("sign", epaySignature(params, "local-payment-key"))
	if _, err = call(params); err == nil {
		t.Fatal("signed wrong amount accepted")
	}
	stillPending, err := s.WorkspaceRechargeDetail(ctx, 1, o.TradeNo)
	if err != nil || stillPending.Status != "pending" {
		t.Fatal("invalid callback mutated state", stillPending, err)
	}
	params.Set("money", "1.28")
	params.Set("sign", epaySignature(params, "local-payment-key"))
	for i := 0; i < 2; i++ {
		if result, err := call(params); err != nil || result != "success" {
			t.Fatal("callback rejected", result, err)
		}
	}
	paid, err := s.WorkspaceRechargeDetail(ctx, 1, o.TradeNo)
	if err != nil || paid.Status != "paid_pending_credit" {
		t.Fatal("callback did not enqueue credit", paid, err)
	}
	remote.mu.Lock()
	remote.failNotify = true
	remote.mu.Unlock()
	if err = s.ProcessRecharges(ctx, 5); err != nil {
		t.Fatal(err)
	}
	paid, err = s.WorkspaceRechargeDetail(ctx, 1, o.TradeNo)
	if err != nil || paid.Status != "paid_pending_credit" || paid.LastError == "" {
		t.Fatal("failed remote credit lost", paid, err)
	}
	remote.mu.Lock()
	remote.failNotify = false
	remote.dropNotify = true
	remote.mu.Unlock()
	if _, err = s.db.Exec(`UPDATE v2_newapi_recharge SET next_attempt_at=0 WHERE trade_no=$1`, o.TradeNo); err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessRecharges(ctx, 5); err != nil {
		t.Fatal(err)
	}
	done, err := s.WorkspaceRechargeDetail(ctx, 1, o.TradeNo)
	if err != nil || done.Status != "succeeded" || done.AmountFen != 123 {
		t.Fatal("remote acknowledgement recovery failed", done, err)
	}
	if result, err := call(params); err != nil || result != "success" {
		t.Fatal("completed callback replay", result, err)
	}
	if err = s.ProcessRecharges(ctx, 5); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	credits, pays := remote.credits, remote.pays
	remote.mu.Unlock()
	if credits != 1 || pays != 1 {
		t.Fatal("duplicate remote recharge", credits, pays)
	}
	var balance int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 10000 {
		t.Fatal("direct recharge modified Forest balance", balance, err)
	}
	if strings.Contains(done.LastError, "key") {
		t.Fatal("credential error leaked")
	}
}
