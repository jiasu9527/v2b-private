package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type cnyWorkspaceFixture struct {
	mu                                    sync.Mutex
	base                                  *workspaceFixture
	orders                                map[string]remoteTopUp
	pays, credits, quotes                 int
	dropPay, dishonestQuote, corruptOrder bool
	hashChanged, rateChanged              bool
}

func (f *cnyWorkspaceFixture) quote(fen int64) workspaceCNYQuote {
	q := fixtureCNYQuote(fen)
	if f.hashChanged {
		q.QuoteHash = strings.Repeat("b", 64)
	}
	if f.dishonestQuote {
		q.CreditedQuota++
	}
	return q
}

func (f *cnyWorkspaceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/status" && r.URL.Path != "/api/user/topup/info" && r.URL.Path != "/api/user/amount" && r.URL.Path != "/api/user/pay" && r.URL.Path != "/api/user/topup" && r.URL.Path != "/api/user/topup/self" && r.URL.Path != "/api/user/epay/notify" {
		f.base.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
	if r.URL.Path != "/api/status" && r.URL.Path != "/api/user/epay/notify" {
		want := "Bearer user-access"
		if r.URL.Path == "/api/user/topup" {
			want = "Bearer " + f.base.cfg.AdminToken
		}
		if r.Header.Get("Authorization") != want {
			http.Error(w, "bad identity", 403)
			return
		}
	}
	switch r.URL.Path {
	case "/api/status":
		rate := 7.3
		if f.rateChanged {
			rate = 8
		}
		write(map[string]any{"quota_per_unit": 500000, "quota_display_type": "CNY", "usd_exchange_rate": rate})
	case "/api/user/topup/info":
		write(map[string]any{"enable_online_topup": true, "min_topup": 1, "min_amount_fen": 100, "supports_cny_amount_fen": true, "pay_methods": []map[string]any{{"type": "wallet"}}})
	case "/api/user/amount":
		var b struct {
			AmountFen int64 `json:"amount_fen"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&b) != nil || b.AmountFen < 100 {
			http.Error(w, "invalid CNY quote request", 400)
			return
		}
		f.quotes++
		write(f.quote(b.AmountFen))
	case "/api/user/pay":
		var b struct {
			AmountFen         int64  `json:"amount_fen"`
			ExpectedQuoteHash string `json:"expected_quote_hash"`
			PaymentMethod     string `json:"payment_method"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&b) != nil || b.AmountFen < 100 || b.PaymentMethod != "wallet" || b.ExpectedQuoteHash != f.quote(b.AmountFen).QuoteHash {
			http.Error(w, "invalid CNY payment request", 400)
			return
		}
		f.pays++
		trade := fmt.Sprintf("cny-order-%d", f.pays)
		topup := fixtureCNYTopUp(f.quote(b.AmountFen))
		topup.ID, topup.UserID, topup.TradeNo = int64(f.pays), 4, trade
		topup.Money, topup.PaymentMethod, topup.PaymentProvider, topup.Status = json.Number(formatMoneyFen(b.AmountFen)), "wallet", "epay", "pending"
		if f.corruptOrder {
			topup.CreditedQuota = 0
		}
		f.orders[trade] = topup
		if f.dropPay {
			if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = c.Close()
			}
			return
		}
		params := walletFixtureParams(f.base.cfg, trade, b.AmountFen)
		m := map[string]string{}
		for key := range params {
			m[key] = params.Get(key)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "success", "data": m, "url": f.base.cfg.SiteURL + "/new-api/submit.php"})
	case "/api/user/topup", "/api/user/topup/self":
		items := []map[string]any{}
		for _, order := range f.orders {
			if keyword := r.URL.Query().Get("keyword"); keyword != "" && keyword != order.TradeNo {
				continue
			}
			raw, _ := json.Marshal(order)
			var row map[string]any
			_ = json.Unmarshal(raw, &row)
			row["amount"], row["create_time"] = 0, time.Now().Unix()
			items = append(items, row)
		}
		write(map[string]any{"items": items, "total": len(items)})
	case "/api/user/epay/notify":
		_ = r.ParseForm()
		if !validEpaySignature(r.PostForm, f.base.cfg.MerchantKey) {
			http.Error(w, "bad callback", 403)
			return
		}
		order, ok := f.orders[r.PostForm.Get("out_trade_no")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if order.Status != "success" {
			order.Status = "success"
			f.orders[order.TradeNo] = order
			f.credits++
		}
		_, _ = w.Write([]byte("success"))
	}
}

func cnyWorkspacePostgresFixture(t *testing.T) (*Service, *cnyWorkspaceFixture) {
	t.Helper()
	s, base := workspacePostgresFixture(t)
	f := &cnyWorkspaceFixture{base: base, orders: map[string]remoteTopUp{}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	base.cfg.APIURL = server.URL
	base.cfg.MinAmountFen = 100
	walletSaveFixtureConfig(t, s, base.cfg)
	s.client = server.Client()
	connectWorkspace(t, s)
	return s, f
}

func cnyQuoteKey(t *testing.T, s *Service, fen int64) string {
	t.Helper()
	result, err := s.QuoteWorkspaceTransfer(context.Background(), 1, "auth", WorkspaceQuoteRequest{AmountFen: fen, RequestID: fmt.Sprintf("cny-quote-%d", fen)})
	if err != nil {
		t.Fatal(err)
	}
	view := result.(map[string]any)
	if view["display_currency"] != "CNY" || view["display_credit"] != formatMoneyFen(fen) || view["credited_quota"] != fixtureCNYQuote(fen).CreditedQuota {
		t.Fatalf("incorrect CNY preview: %v", view)
	}
	return view["quote_id"].(string)
}

func TestWorkspaceCNYPostgresCentAmountsAndConcurrentConfirmation(t *testing.T) {
	for _, fen := range []int64{100, 123, 1000} {
		t.Run(formatMoneyFen(fen), func(t *testing.T) {
			s, f := cnyWorkspacePostgresFixture(t)
			s.db.SetMaxOpenConns(3)
			key := cnyQuoteKey(t, s, fen)
			var wg sync.WaitGroup
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := s.ConfirmWorkspaceTransfer(context.Background(), 1, "auth", key); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			var balance, count int64
			if err := s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM v2_newapi_transfer`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if balance != 10000-fen || count != 1 || f.pays != 1 {
				t.Fatalf("balance=%d transfers=%d pays=%d", balance, count, f.pays)
			}
			if err := s.ProcessTransfers(context.Background(), 2); err != nil {
				t.Fatal(err)
			}
			if f.credits != 1 {
				t.Fatalf("credits=%d", f.credits)
			}
		})
	}
}

func TestWorkspaceCNYPostgresRejectsMinimumAndChangedQuoteBeforePayment(t *testing.T) {
	for _, mutation := range []string{"minimum", "hash", "rate", "dishonest"} {
		t.Run(mutation, func(t *testing.T) {
			s, f := cnyWorkspacePostgresFixture(t)
			var err error
			if mutation == "minimum" {
				_, err = s.QuoteWorkspaceTransfer(context.Background(), 1, "auth", WorkspaceQuoteRequest{AmountFen: 99, RequestID: "cny-under-minimum"})
				if !errors.Is(err, ErrLimitExceeded) {
					t.Fatalf("under-minimum result: %v", err)
				}
			} else if mutation == "dishonest" {
				f.dishonestQuote = true
				_, err = s.QuoteWorkspaceTransfer(context.Background(), 1, "auth", WorkspaceQuoteRequest{AmountFen: 123, RequestID: "cny-dishonest-quote"})
				if !errors.Is(err, ErrRemoteUnavailable) {
					t.Fatalf("dishonest result: %v", err)
				}
			} else {
				key := cnyQuoteKey(t, s, 123)
				f.hashChanged, f.rateChanged = mutation == "hash", mutation == "rate"
				_, err = s.ConfirmWorkspaceTransfer(context.Background(), 1, "auth", key)
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("changed quote result: %v", err)
				}
			}
			var balance int64
			if err := s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if f.pays != 0 || balance != 10000 {
				t.Fatalf("rejected quote had side effects: pays=%d balance=%d", f.pays, balance)
			}
		})
	}
}

func TestWorkspaceCNYPostgresUnknownCreateRecoversExactSnapshot(t *testing.T) {
	s, f := cnyWorkspacePostgresFixture(t)
	key := cnyQuoteKey(t, s, 123)
	f.dropPay = true
	if _, err := s.ConfirmWorkspaceTransfer(context.Background(), 1, "auth", key); !errors.Is(err, ErrWorkspaceUnknown) {
		t.Fatalf("lost response=%v", err)
	}
	if _, err := s.ConfirmWorkspaceTransfer(context.Background(), 1, "auth", key); !errors.Is(err, ErrWorkspaceUnknown) {
		t.Fatalf("unknown retried=%v", err)
	}
	view, err := s.ReviewWorkspaceTransfer(context.Background(), 1, "auth", key)
	if err != nil {
		t.Fatal(err)
	}
	candidates := view.(map[string]any)["candidates"].([]map[string]any)
	if len(candidates) != 1 || candidates[0]["trade_no"] != "cny-order-1" {
		t.Fatalf("candidates=%v", candidates)
	}
	if _, err = s.ResolveWorkspaceTransfer(context.Background(), 1, "auth", WorkspaceResolveRequest{QuoteID: key, RemoteTradeNo: "cny-order-1"}); err != nil {
		t.Fatal(err)
	}
	var balance int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if f.pays != 1 || balance != 9877 {
		t.Fatalf("recovery recreated or misdebited: pays=%d balance=%d", f.pays, balance)
	}
}

func TestWorkspaceCNYPostgresCorruptOriginalOrderCannotDebit(t *testing.T) {
	s, f := cnyWorkspacePostgresFixture(t)
	key := cnyQuoteKey(t, s, 123)
	f.corruptOrder = true
	if _, err := s.ConfirmWorkspaceTransfer(context.Background(), 1, "auth", key); !errors.Is(err, ErrConflict) {
		t.Fatalf("corrupt order result=%v", err)
	}
	var balance int64
	if err := s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 10000 || f.pays != 1 {
		t.Fatalf("corrupt order debited: balance=%d pays=%d", balance, f.pays)
	}
}
