package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type walletRemoteFixture struct {
	mu                sync.Mutex
	orders            map[string]remoteTopUp
	subject           string
	notifies, credits int
	dropResponse      bool
	callbackFails     bool
	onUser            func()
	merchantKey       string
	adminToken        string
}

func (f *walletRemoteFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && f.adminToken != "" && r.Header.Get("Authorization") != "Bearer "+f.adminToken {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/user/topup":
		items := []remoteTopUp{}
		if item, ok := f.orders[r.URL.Query().Get("keyword")]; ok {
			items = append(items, item)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"total": len(items), "items": items}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/user/4":
		if f.onUser != nil {
			f.onUser()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": remoteUser{ID: 4, OIDCID: f.subject, Status: 1}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/user/epay/notify":
		_ = r.ParseForm()
		if !validEpaySignature(r.PostForm, f.merchantKey) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
		f.notifies++
		if f.callbackFails {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		order, ok := f.orders[r.PostForm.Get("out_trade_no")]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if order.Status != "success" {
			f.credits++
			order.Status = "success"
			f.orders[order.TradeNo] = order
		}
		if f.dropResponse {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte("success"))
	default:
		http.NotFound(w, r)
	}
}

func walletPostgresFixture(t *testing.T) (*Service, Settings, *walletRemoteFixture) {
	t.Helper()
	dsn := os.Getenv("FOREST_NEWAPI_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("FOREST_AIOPS_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL test DSN")
	}
	pgcfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*pgcfg)
	schema := fmt.Sprintf("newapi_wallet_%d", time.Now().UnixNano())
	if _, err = db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	pgcfg.RuntimeParams["search_path"] = schema
	scoped := stdlib.OpenDB(*pgcfg)
	t.Cleanup(func() { scoped.Close(); _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
	if _, err = scoped.Exec(`CREATE TABLE v2_user(id BIGINT PRIMARY KEY,balance BIGINT NOT NULL,banned BIGINT NOT NULL DEFAULT 0,updated_at BIGINT NOT NULL DEFAULT 0);INSERT INTO v2_user(id,balance) VALUES(1,10000),(2,10000)`); err != nil {
		t.Fatal(err)
	}
	f := &walletRemoteFixture{orders: map[string]remoteTopUp{}, subject: "forest:1", merchantKey: "fixture-merchant-key", adminToken: "fixture-admin-token"}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	s := NewService(config.Config{AppKey: "fixture-app-key"}, scoped)
	s.client = server.Client()
	if err = s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := Settings{Enabled: true, APIURL: server.URL, SiteURL: "https://panel.example.test", MerchantID: "fixture", MerchantKey: "fixture-merchant-key", AdminToken: "fixture-admin-token", AdminUserID: 1, MinAmountFen: 1, MaxAmountFen: 10000, DailyLimitFen: 10000, Revision: 1}
	walletSaveFixtureConfig(t, s, cfg)
	return s, cfg, f
}

func walletSaveFixtureConfig(t *testing.T, s *Service, cfg Settings) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := s.seal(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO v2_newapi_settings(id,revision,config_cipher,updated_by,updated_at) VALUES(1,$1,$2,1,1) ON CONFLICT(id) DO UPDATE SET revision=EXCLUDED.revision,config_cipher=EXCLUDED.config_cipher`, cfg.Revision, cipher)
	if err != nil {
		t.Fatal(err)
	}
}

func walletFixtureCheckout(t *testing.T, s *Service, cfg Settings, f *walletRemoteFixture, trade string, amount int64) Checkout {
	t.Helper()
	f.mu.Lock()
	f.orders[trade] = remoteTopUp{ID: int64(len(f.orders) + 1), UserID: 4, Money: json.Number(formatMoneyFen(amount)), TradeNo: trade, PaymentProvider: "epay", PaymentMethod: "wallet", Status: "pending"}
	f.mu.Unlock()
	params := walletFixtureParams(cfg, trade, amount)
	c, err := s.CreateCheckout(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func walletFixtureParams(cfg Settings, trade string, amount int64) url.Values {
	params := url.Values{"pid": {cfg.MerchantID}, "out_trade_no": {trade}, "money": {formatMoneyFen(amount)}, "name": {"fixture"}, "type": {"wallet"}, "notify_url": {cfg.APIURL + "/api/user/epay/notify"}, "return_url": {cfg.APIURL + "/usage-logs"}, "sign_type": {"MD5"}}
	params.Set("sign", epaySignature(params, cfg.MerchantKey))
	return params
}

func TestWalletPostgresConcurrentSameOrderAndLostCallbackResponse(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	c := walletFixtureCheckout(t, s, cfg, f, "same-order", 1234)
	c2, err := s.CreateCheckout(context.Background(), walletFixtureParams(cfg, "same-order", 1234))
	if err != nil || c.ID != c2.ID {
		t.Fatal(c2, err)
	}
	var wg sync.WaitGroup
	results := make(chan Transfer, 20)
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := s.Pay(context.Background(), 1, c.ID)
			if err != nil {
				failures <- err
			} else {
				results <- value
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var id int64
	for transfer := range results {
		if id == 0 {
			id = transfer.ID
		}
		if transfer.ID != id || transfer.BalanceBefore != 10000 || transfer.BalanceAfter != 8766 {
			t.Fatal(transfer)
		}
	}
	var balance, count int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 8766 {
		t.Fatal(balance, err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_transfer`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = s.Pay(context.Background(), 2, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-user payment", err)
	}
	if _, err = s.GetCheckout(context.Background(), 2, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-user checkout", err)
	}
	// Once debited, disabling new transfers must not strand money. The saved
	// destination snapshot is sufficient to retry even after config changes.
	cfg.Enabled = false
	cfg.Revision++
	cfg.APIURL = "https://do-not-send.example.test"
	walletSaveFixtureConfig(t, s, cfg)
	f.mu.Lock()
	f.dropResponse = true
	f.mu.Unlock()
	if err = s.ProcessTransfers(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTransfers(context.Background(), TransferQuery{UserID: 1})
	if err != nil || page.Total != 1 || page.Items[0].Status != "succeeded" {
		t.Fatal(page, err)
	}
	if err = s.ProcessTransfers(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	notifies, credits := f.notifies, f.credits
	f.mu.Unlock()
	if notifies != 1 || credits != 1 {
		t.Fatal(notifies, credits)
	}
	if _, err = s.Pay(context.Background(), 1, c.ID); err != nil {
		t.Fatal("paid replay when disabled", err)
	}
}

func TestWalletPostgresCompetingOrdersCannotOverdraw(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	_, err := s.db.Exec(`UPDATE v2_user SET balance=500 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	a := walletFixtureCheckout(t, s, cfg, f, "a", 400)
	b := walletFixtureCheckout(t, s, cfg, f, "b", 400)
	errorsC := make(chan error, 2)
	for _, c := range []Checkout{a, b} {
		go func(id string) { _, err := s.Pay(context.Background(), 1, id); errorsC <- err }(c.ID)
	}
	success, insufficient := 0, 0
	for i := 0; i < 2; i++ {
		err := <-errorsC
		if err == nil {
			success++
		} else if errors.Is(err, ErrInsufficientBalance) {
			insufficient++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || insufficient != 1 {
		t.Fatal(success, insufficient)
	}
	var balance int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 100 {
		t.Fatal(balance, err)
	}
}

func TestWalletPostgresDailyLimitAndRemoteTamper(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	cfg.DailyLimitFen = 1000
	walletSaveFixtureConfig(t, s, cfg)
	a := walletFixtureCheckout(t, s, cfg, f, "a", 600)
	b := walletFixtureCheckout(t, s, cfg, f, "b", 500)
	if _, err := s.Pay(context.Background(), 1, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pay(context.Background(), 1, b.ID); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
	c := walletFixtureCheckout(t, s, cfg, f, "c", 100)
	f.mu.Lock()
	changed := f.orders["c"]
	changed.Money = json.Number("2.00")
	f.orders["c"] = changed
	f.mu.Unlock()
	if _, err := s.Pay(context.Background(), 1, c.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	f.mu.Lock()
	changed.Money = json.Number("1.00")
	f.orders["c"] = changed
	f.subject = "forest:2"
	f.mu.Unlock()
	if _, err := s.Pay(context.Background(), 1, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	var balance int64
	if err := s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 9400 {
		t.Fatal(balance, err)
	}
}

func TestWalletPostgresConfigurationRaceAndExpiredCheckout(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	c := walletFixtureCheckout(t, s, cfg, f, "config-race", 100)
	f.mu.Lock()
	f.onUser = func() { cfg.Revision++; walletSaveFixtureConfig(t, s, cfg); f.onUser = nil }
	f.mu.Unlock()
	if _, err := s.Pay(context.Background(), 1, c.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var balance int64
	if err := s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 10000 {
		t.Fatal(balance, err)
	}
	c = walletFixtureCheckout(t, s, cfg, f, "expired", 100)
	if _, err := s.db.Exec(`UPDATE v2_newapi_checkout SET expires_at=1 WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pay(context.Background(), 1, c.ID); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestWalletPostgresRetryNeverDebitsTwice(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	c := walletFixtureCheckout(t, s, cfg, f, "retry", 100)
	tx, err := s.Pay(context.Background(), 1, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.callbackFails = true
	f.mu.Unlock()
	if err = s.ProcessTransfers(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTransfers(context.Background(), TransferQuery{UserID: 1, Status: "pending", TradeNo: "retry"})
	if err != nil || page.Total != 1 || !strings.Contains(page.Items[0].LastError, "原订单") {
		t.Fatal(page, err)
	}
	f.mu.Lock()
	f.callbackFails = false
	f.mu.Unlock()
	if err = s.RetryTransfer(context.Background(), tx.ID, 7); err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessTransfers(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListTransfers(context.Background(), TransferQuery{UserID: 1})
	if err != nil || page.Items[0].Status != "succeeded" || page.Items[0].LastRetryBy != 7 {
		t.Fatal(page, err)
	}
	var balance int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 9900 {
		t.Fatal(balance, err)
	}
	if err = s.RetryTransfer(context.Background(), tx.ID, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("completed transfer retried", err)
	}
}

func TestWalletPostgresPendingTransferUsesRotatedCredentialsForSameMerchant(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	c := walletFixtureCheckout(t, s, cfg, f, "rotation", 100)
	if _, err := s.Pay(context.Background(), 1, c.ID); err != nil {
		t.Fatal(err)
	}
	// Both secrets have been revoked on the remote, while new transfers are
	// disabled. Refreshing credentials must not require another local debit.
	cfg.Enabled = false
	cfg.Revision++
	cfg.MerchantKey = "rotated-merchant-key"
	cfg.AdminToken = "rotated-admin-token"
	walletSaveFixtureConfig(t, s, cfg)
	f.mu.Lock()
	f.merchantKey, f.adminToken = cfg.MerchantKey, cfg.AdminToken
	f.mu.Unlock()
	if err := s.ProcessTransfers(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTransfers(context.Background(), TransferQuery{UserID: 1})
	if err != nil || page.Total != 1 || page.Items[0].Status != "succeeded" {
		t.Fatal(page, err)
	}
	var balance int64
	if err = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance); err != nil || balance != 9900 {
		t.Fatal(balance, err)
	}
	f.mu.Lock()
	credits := f.credits
	f.mu.Unlock()
	if credits != 1 {
		t.Fatal("credits", credits)
	}
}

func TestWalletPostgresClaimLeaseOnlyOneWorker(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	c := walletFixtureCheckout(t, s, cfg, f, "lease", 100)
	if _, err := s.Pay(context.Background(), 1, c.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	leases := make(chan walletLease, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := s.claimWalletTransfer(context.Background())
			if err == nil {
				leases <- lease
			} else if !errors.Is(err, sql.ErrNoRows) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(leases)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	count := 0
	var lease walletLease
	for item := range leases {
		count++
		lease = item
	}
	if count != 1 {
		t.Fatal("claims", count)
	}
	if err := s.RetryTransfer(context.Background(), lease.ID, 7); !errors.Is(err, ErrConflict) {
		t.Fatal("active lease reset", err)
	}
	if _, err := s.db.Exec(`UPDATE v2_newapi_transfer SET lease_until=1 WHERE id=$1`, lease.ID); err != nil {
		t.Fatal(err)
	}
	newLease, err := s.claimWalletTransfer(context.Background())
	if err != nil || newLease.Token == lease.Token || newLease.Attempts != 2 {
		t.Fatal(newLease, err)
	}
	if err = s.completeWalletTransfer(context.Background(), lease); !errors.Is(err, ErrConflict) {
		t.Fatal("stale worker completion", err)
	}
}

func TestWalletPostgresSignedMismatchNeverCreatesCheckout(t *testing.T) {
	s, cfg, f := walletPostgresFixture(t)
	f.mu.Lock()
	f.orders["bad"] = remoteTopUp{ID: 1, UserID: 4, TradeNo: "bad", Money: json.Number("1.00"), PaymentProvider: "epay", PaymentMethod: "wallet", Status: "pending"}
	f.mu.Unlock()
	for _, params := range []url.Values{walletFixtureParams(cfg, "bad", 200), walletFixtureParams(cfg, "bad", 100)} {
		if params.Get("money") == "1.00" {
			params.Set("notify_url", "https://attacker.example/callback")
			params.Set("sign", epaySignature(params, cfg.MerchantKey))
		}
		if _, err := s.CreateCheckout(context.Background(), params); err == nil {
			t.Fatal("accepted invalid checkout")
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_checkout`).Scan(&count); err != nil || count != 0 {
		t.Fatal(strconv.Itoa(count), err)
	}
}
