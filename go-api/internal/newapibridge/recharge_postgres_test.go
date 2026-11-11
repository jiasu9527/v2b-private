package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forest/go-api/internal/config"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/payment"
)

func rechargeFixture(t *testing.T) (*Service, *cnyWorkspaceFixture) {
	t.Helper()
	s, f := cnyWorkspacePostgresFixture(t)
	_, err := s.db.Exec(`CREATE TABLE v2_payment(id BIGINT PRIMARY KEY,name TEXT,payment TEXT,icon TEXT,handling_fee_fixed BIGINT,handling_fee_percent DOUBLE PRECISION,enable INTEGER,sort INTEGER); INSERT INTO v2_payment VALUES(9,'测试支付','EPay','',5,0,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.paymentMethod = "alipay"
	f.mu.Unlock()
	return s, f
}
func createRecharge(t *testing.T, s *Service, key string) RechargeOrder {
	t.Helper()
	o, err := s.CreateWorkspaceRecharge(context.Background(), 1, "auth", WorkspaceRechargeRequest{AmountFen: 123, RequestID: key})
	if err != nil || o.Status != "pending" || len(o.TradeNo) > 32 || !validRechargeTrade(o.TradeNo) {
		t.Fatalf("create: %+v %v", o, err)
	}
	return o
}
func rechargeCheckoutID(t *testing.T, s *Service, trade string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`SELECT checkout_id FROM v2_newapi_recharge WHERE trade_no=$1`, trade).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func readyRecharge(t *testing.T, s *Service, o RechargeOrder) {
	t.Helper()
	if err := s.SetExternalPayment(context.Background(), 1, o.TradeNo, 9, 5); err != nil {
		t.Fatal(err)
	}
}
func paidRecharge(t *testing.T, s *Service, o RechargeOrder) {
	t.Helper()
	readyRecharge(t, s, o)
	id, amount := int64(9), o.AmountFen+5
	if err := s.MarkExternalPaid(context.Background(), o.TradeNo, "payment-evidence", false, &id, &amount); err != nil {
		t.Fatal(err)
	}
}
func TestRechargePostgresOptionsAndConcurrentIdempotency(t *testing.T) {
	s, f := rechargeFixture(t)
	s.db.SetMaxOpenConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	options, err := s.WorkspaceRechargeOptions(ctx, 1, "auth")
	if err != nil || options.(map[string]any)["enabled"] != true {
		t.Fatalf("options: %v %v", options, err)
	}
	f.mu.Lock()
	calls := f.pays
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("options created remote order")
	}
	var wg sync.WaitGroup
	out := make(chan RechargeOrder, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := s.CreateWorkspaceRecharge(ctx, 1, "auth", WorkspaceRechargeRequest{AmountFen: 123, RequestID: "same-direct-create"})
			if e != nil {
				errs <- e
			} else {
				out <- o
			}
		}()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	trade := ""
	for o := range out {
		if trade != "" && o.TradeNo != trade {
			t.Fatal("duplicate recharge")
		}
		trade = o.TradeNo
	}
	f.mu.Lock()
	calls = f.pays
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("remote creates %d", calls)
	}
	if _, e := s.CreateWorkspaceRecharge(ctx, 1, "auth", WorkspaceRechargeRequest{AmountFen: 124, RequestID: "same-direct-create"}); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e := s.WorkspaceRechargeDetail(ctx, 2, trade); !errors.Is(e, ErrForbidden) {
		t.Fatal("cross user detail", e)
	}
	page, e := s.WorkspaceRecharges(ctx, 1, 1, 20)
	if e != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "fixture-") || strings.Contains(string(raw), "sign") {
		t.Fatal("financial credentials exposed")
	}
	var balance int64
	if e = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); e != nil || balance != 10000 {
		t.Fatal("balance changed", balance, e)
	}
	var walletTransfers int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_transfer`).Scan(&walletTransfers); e != nil || walletTransfers != 0 {
		t.Fatal("unexpected wallet transfer", e)
	}
}
func TestRechargePostgresRemoteCreateUnknownNeverReplayed(t *testing.T) {
	s, f := rechargeFixture(t)
	f.mu.Lock()
	f.dropPay = true
	f.mu.Unlock()
	req := WorkspaceRechargeRequest{AmountFen: 123, RequestID: "dropped-remote-create"}
	o, err := s.CreateWorkspaceRecharge(context.Background(), 1, "auth", req)
	if err != nil || o.Status != "prepare_unknown" {
		t.Fatal(o, err)
	}
	for i := 0; i < 3; i++ {
		retry, e := s.CreateWorkspaceRecharge(context.Background(), 1, "auth", req)
		if e != nil || retry.TradeNo != o.TradeNo || retry.Status != "prepare_unknown" {
			t.Fatal(retry, e)
		}
	}
	f.mu.Lock()
	calls := f.pays
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("replayed remote creates %d", calls)
	}
	called := false
	_, err = s.CheckoutExternalOnce(context.Background(), 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9}, func(context.Context) (payment.CheckoutResult, error) {
		called = true
		return payment.CheckoutResult{}, nil
	})
	if called || !errors.Is(err, ErrConflict) {
		t.Fatal("unknown original became payable", err)
	}
}
func TestRechargePostgresFundingIsolation(t *testing.T) {
	s, f := rechargeFixture(t)
	o := createRecharge(t, s, "funding-isolation")
	id := rechargeCheckoutID(t, s, o.TradeNo)
	c, err := scanCheckout(s.db.QueryRow(`SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	params := walletFixtureParams(f.base.cfg, c.RemoteTradeNo, o.AmountFen)
	params.Set("type", "alipay")
	params.Set("sign", epaySignature(params, f.base.cfg.MerchantKey))
	if _, err = s.CreateCheckout(context.Background(), params); !errors.Is(err, ErrConflict) {
		t.Fatal("wallet accepted direct original", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Pay(context.Background(), 1, id); !errors.Is(e, ErrForbidden) {
				t.Errorf("direct original charged wallet: %v", e)
			}
		}()
	}
	wg.Wait()
	// Opposite order: a wallet original cannot later be claimed by direct cash.
	f.mu.Lock()
	top := fixtureCNYTopUp(fixtureCNYQuote(123))
	top.ID = 99
	top.UserID = 4
	top.TradeNo = "wallet-first"
	top.Money = json.Number("1.23")
	top.PaymentProvider = "epay"
	top.PaymentMethod = "wallet"
	top.Status = "pending"
	f.orders[top.TradeNo] = top
	f.mu.Unlock()
	walletParams := walletFixtureParams(f.base.cfg, top.TradeNo, 123)
	if _, err = s.CreateCheckout(context.Background(), walletParams); err != nil {
		t.Fatal(err)
	}
	if _, err = s.createCheckoutForSource(context.Background(), walletParams, "direct"); !errors.Is(err, ErrConflict) {
		t.Fatal("direct stole wallet original", err)
	}
}
func TestRechargePostgresCheckoutOnceAndImmutableMethod(t *testing.T) {
	s, _ := rechargeFixture(t)
	s.db.SetMaxOpenConns(3)
	o := createRecharge(t, s, "once-gateway-checkout")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls atomic.Int64
	var wg sync.WaitGroup
	callback := func(ctx context.Context) (payment.CheckoutResult, error) {
		calls.Add(1)
		if err := s.SetExternalPayment(ctx, 1, o.TradeNo, 9, 5); err != nil {
			return payment.CheckoutResult{}, err
		}
		time.Sleep(20 * time.Millisecond)
		return payment.CheckoutResult{Type: 1, Data: "https://payment.example.test/pay"}, nil
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.CheckoutExternalOnce(ctx, 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9}, callback)
			if e != nil || r.Type != 1 {
				t.Errorf("checkout %v %v", r, e)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate gateway checkout", calls.Load())
	}
	if _, e := s.CheckoutExternalOnce(ctx, 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 10}, callback); !errors.Is(e, ErrConflict) {
		t.Fatal("channel changed", e)
	}
	if e := s.SetExternalPayment(ctx, 1, o.TradeNo, 9, 6); !errors.Is(e, ErrConflict) {
		t.Fatal("fee changed", e)
	}
}
func TestRechargePostgresCallbackAndCreditRetry(t *testing.T) {
	s, f := rechargeFixture(t)
	o := createRecharge(t, s, "callback-credit-retry")
	readyRecharge(t, s, o)
	channel, amount := int64(9), int64(128)
	wrong := int64(127)
	if e := s.MarkExternalPaid(context.Background(), o.TradeNo, "wrong", false, &channel, &wrong); !errors.Is(e, ErrConflict) {
		t.Fatal("accepted bad amount", e)
	}
	otherChannel := int64(10)
	if e := s.MarkExternalPaid(context.Background(), o.TradeNo, "wrong", false, &otherChannel, &amount); !errors.Is(e, ErrConflict) {
		t.Fatal("accepted bad channel", e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.MarkExternalPaid(context.Background(), o.TradeNo, "same-callback", false, &channel, &amount); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	f.mu.Lock()
	f.failNotify = true
	f.mu.Unlock()
	if err := s.ProcessRecharges(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	got, err := s.WorkspaceRechargeDetail(context.Background(), 1, o.TradeNo)
	if err != nil || got.Status != "paid_pending_credit" || got.LastError == "" {
		t.Fatal(got, err)
	}
	f.mu.Lock()
	f.failNotify = false
	f.dropNotify = true
	f.mu.Unlock()
	if _, err = s.db.Exec(`UPDATE v2_newapi_recharge SET next_attempt_at=0 WHERE trade_no=$1`, o.TradeNo); err != nil {
		t.Fatal(err)
	}
	// Disabling new orders must not cancel paid credits.
	cfg := f.base.cfg
	cfg.Enabled = false
	walletSaveFixtureConfig(t, s, cfg)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.ProcessRecharges(context.Background(), 5); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	got, err = s.WorkspaceRechargeDetail(context.Background(), 1, o.TradeNo)
	if err != nil || got.Status != "succeeded" {
		t.Fatal(got, err)
	}
	f.mu.Lock()
	credits, pays := f.credits, f.pays
	f.mu.Unlock()
	if credits != 1 || pays != 1 {
		t.Fatal("duplicate remote financial operation", credits, pays)
	}
	if err = s.MarkExternalPaid(context.Background(), o.TradeNo, "repeat", false, &channel, &amount); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkExternalPaid(context.Background(), o.TradeNo, "repeat-wrong", false, &channel, &wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("bad duplicate acknowledged", err)
	}
	var balance int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 10000 {
		t.Fatal("direct payment touched wallet", balance, err)
	}
}
func TestRechargePostgresGatewayUnknownAndEarlyCallback(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint(early), func(t *testing.T) {
			s, _ := rechargeFixture(t)
			o := createRecharge(t, s, "gateway-unknown-response")
			calls := 0
			create := func(ctx context.Context) (payment.CheckoutResult, error) {
				calls++
				if e := s.SetExternalPayment(ctx, 1, o.TradeNo, 9, 5); e != nil {
					return payment.CheckoutResult{}, e
				}
				if early {
					channel, amount := int64(9), int64(128)
					if e := s.MarkExternalPaid(ctx, o.TradeNo, "callback-before-response", false, &channel, &amount); e != nil {
						return payment.CheckoutResult{}, e
					}
				}
				return payment.CheckoutResult{}, payment.ErrRequestFailed
			}
			if _, e := s.CheckoutExternalOnce(context.Background(), 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9}, create); !errors.Is(e, payment.ErrRequestFailed) {
				t.Fatal(e)
			}
			result, e := s.CheckoutExternalOnce(context.Background(), 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9}, create)
			if early {
				if e != nil || result.Type != -1 {
					t.Fatal(result, e)
				}
			} else if !errors.Is(e, ErrWorkspaceUnknown) {
				t.Fatal(e)
			}
			if calls != 1 {
				t.Fatal("uncertain gateway request repeated", calls)
			}
		})
	}
}
func TestRechargePostgresCorruptQuotaBlocksCredit(t *testing.T) {
	s, f := rechargeFixture(t)
	o := createRecharge(t, s, "corrupt-quota-on-settle")
	paidRecharge(t, s, o)
	f.mu.Lock()
	for key, top := range f.orders {
		top.CreditedQuota++
		f.orders[key] = top
	}
	f.mu.Unlock()
	if e := s.ProcessRecharges(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	got, e := s.WorkspaceRechargeDetail(context.Background(), 1, o.TradeNo)
	if e != nil || got.Status != "paid_pending_credit" {
		t.Fatal(got, e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.credits != 0 {
		t.Fatal("corrupt quota credited")
	}
}

func TestRechargePostgresActualGatewayCheckoutAndSignedNotify(t *testing.T) {
	s, f := rechargeFixture(t)
	gatewayConfig := `{"url":"https://mock-payment.example.test","pid":"test-cash-merchant","key":"cash-gateway-secret"}`
	if _, err := s.db.Exec(`ALTER TABLE v2_payment ADD COLUMN uuid TEXT NOT NULL DEFAULT 'cash-fixture';ALTER TABLE v2_payment ADD COLUMN config TEXT NOT NULL DEFAULT '';ALTER TABLE v2_payment ADD COLUMN notify_domain TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE v2_payment SET config=$1`, gatewayConfig); err != nil {
		t.Fatal(err)
	}
	gateway := payment.NewDBService(config.Config{AppURL: "https://forest-backend.example.test"}, s.db, nil).WithExternalOrderManager(payment.NewExternalOrderManagers(s))
	s.WithRechargePayments(gateway)
	o := createRecharge(t, s, "actual-gateway-flow")
	checkout, err := s.CheckoutWorkspaceRecharge(context.Background(), 1, WorkspaceRechargeCheckoutRequest{TradeNo: o.TradeNo, PaymentID: 9}, "https://forest-frontend.example.test")
	if err != nil || checkout.Checkout.Type != 1 || checkout.Order.TotalAmountFen != 128 {
		t.Fatal(checkout, err)
	}
	parsed, err := url.Parse(checkout.Checkout.Data.(string))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("money") != "1.28" || parsed.Query().Get("out_trade_no") != o.TradeNo || parsed.Query().Get("return_url") != "https://forest-frontend.example.test/ai?tab=records&recharge="+o.TradeNo {
		t.Fatal(parsed.String())
	}
	repeated, err := gateway.Checkout(context.Background(), 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9, RequestBaseURL: "https://other.example.test"})
	if err != nil || repeated.Data != checkout.Checkout.Data {
		t.Fatal("generic endpoint bypassed replay", repeated, err)
	}
	values := url.Values{"pid": {"test-cash-merchant"}, "out_trade_no": {o.TradeNo}, "trade_no": {"gateway-cash-1"}, "money": {"1.28"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
	values.Set("sign", epaySignature(values, "cash-gateway-secret"))
	params := map[string]string{}
	for key := range values {
		params[key] = values.Get(key)
	}
	bad := map[string]string{}
	for key, v := range params {
		bad[key] = v
	}
	bad["sign"] = "invalid"
	if _, err = gateway.Notify(context.Background(), "EPay", "cash-fixture", payment.NotifyRequest{Params: bad}); err == nil {
		t.Fatal("invalid signature accepted")
	}
	before, err := s.WorkspaceRechargeDetail(context.Background(), 1, o.TradeNo)
	if err != nil || before.Status != "pending" {
		t.Fatal(before, err)
	}
	for i := 0; i < 3; i++ {
		ack, e := gateway.Notify(context.Background(), "EPay", "cash-fixture", payment.NotifyRequest{Params: params})
		if e != nil || ack != "success" {
			t.Fatal(ack, e)
		}
	}
	if err = s.ProcessRecharges(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	after, err := s.WorkspaceRechargeDetail(context.Background(), 1, o.TradeNo)
	if err != nil || after.Status != "succeeded" || after.AmountFen != 123 || after.HandlingAmountFen != 5 {
		t.Fatal(after, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.credits != 1 {
		t.Fatal("credit count", f.credits)
	}
}

func TestRechargePostgresUnsupportedChannelsAndDailyReservation(t *testing.T) {
	s, f := rechargeFixture(t)
	if _, err := s.db.Exec(`UPDATE v2_payment SET payment='StripeCredit'`); err != nil {
		t.Fatal(err)
	}
	view, err := s.WorkspaceRechargeOptions(context.Background(), 1, "auth")
	if err != nil || view.(map[string]any)["enabled"] != false {
		t.Fatal(view, err)
	}
	if _, err = s.CreateWorkspaceRecharge(context.Background(), 1, "auth", WorkspaceRechargeRequest{AmountFen: 123, RequestID: "no-supported-method"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE v2_payment SET payment='EPay'`); err != nil {
		t.Fatal(err)
	}
	cfg := f.base.cfg
	cfg.MaxAmountFen = 246
	cfg.DailyLimitFen = 246
	walletSaveFixtureConfig(t, s, cfg)
	first := createRecharge(t, s, "daily-first-reserve")
	_ = createRecharge(t, s, "daily-second-reserve")
	if _, err = s.CreateWorkspaceRecharge(context.Background(), 1, "auth", WorkspaceRechargeRequest{AmountFen: 123, RequestID: "daily-third-reserve"}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal("daily cap bypass", err)
	}
	if _, err = s.db.Exec(`UPDATE v2_payment SET payment='StripeCredit'`); err != nil {
		t.Fatal(err)
	}
	if err = s.SetExternalPayment(context.Background(), 1, first.TradeNo, 9, 5); !errors.Is(err, payment.ErrPaymentMethodUnavailable) {
		t.Fatal("generic method bypass", err)
	}
	req := SettingsSaveRequest{APIURL: "https://different-ai.example.test", MerchantID: cfg.MerchantID, ExpectedRevision: cfg.Revision}
	if _, err = s.SaveConfig(context.Background(), 1, req); !errors.Is(err, ErrConflict) {
		t.Fatal("moved live payable order", err)
	}
}

func TestRechargePostgresChangedDisplayRateRejectsNewPayment(t *testing.T) {
	s, f := rechargeFixture(t)
	o := createRecharge(t, s, "display-rate-change")
	f.mu.Lock()
	f.rateChanged = true
	f.mu.Unlock()
	called := false
	_, err := s.CheckoutExternalOnce(context.Background(), 1, payment.CheckoutRequest{TradeNo: o.TradeNo, MethodID: 9}, func(context.Context) (payment.CheckoutResult, error) {
		called = true
		return payment.CheckoutResult{Type: 1, Data: "https://payment.example.test"}, nil
	})
	if called || !errors.Is(err, ErrConflict) {
		t.Fatal("changed CNY quote accepted", called, err)
	}
}
func TestRechargeTradeAndGatewayContract(t *testing.T) {
	valid := "AIT" + strings.Repeat("a", 28)
	if len(valid) > 32 || !validRechargeTrade(valid) {
		t.Fatal("invalid payment-compatible merchant id")
	}
	for _, v := range []string{"AIT", "AIT" + strings.Repeat("a", 64), "AIT" + strings.Repeat("A", 28), "AIT" + strings.Repeat("g", 28), valid + " ", "apple-abc"} {
		if validRechargeTrade(v) {
			t.Fatalf("invalid ID accepted %q", v)
		}
	}
	for _, v := range []string{"StripeCredit", "Epay", "", "Unknown"} {
		if rechargeGatewayAllowed(v) {
			t.Fatalf("unsupported gateway %q accepted", v)
		}
	}
	if !rechargeGatewayAllowed("EPay") || !rechargeGatewayAllowed("WechatPayNative") {
		t.Fatal("supported gateways missing")
	}
}
