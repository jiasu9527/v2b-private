package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type workspaceFixture struct {
	mu                             sync.Mutex
	s                              *Service
	cfg                            Settings
	user                           workspaceUser
	refreshes, pays, chats         int
	badSubject, dropPay, challenge bool
	hasPassword                    bool
	wallet                         string
	orders                         map[string]remoteTopUp
	passwords                      []string
}

func (f *workspaceFixture) write(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}
func (f *workspaceFixture) bundle(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "fixture-refresh", HttpOnly: true})
	f.user.HasPassword = f.hasPassword
	u := f.user
	if f.badSubject {
		u.OIDCID = "forest:2"
	}
	f.write(w, map[string]any{"access_token": "user-access", "access_expires_at": time.Now().Add(15 * time.Minute).Unix(), "session": map[string]any{"sid": "session-one"}, "user": u, "has_password": f.hasPassword})
}
func (f *workspaceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/api/user/topup") && r.URL.Path == "/api/user/topup" || r.URL.Path == "/api/user/4" {
		if r.Header.Get("Authorization") != "Bearer "+f.cfg.AdminToken {
			http.Error(w, "wrong admin credential", 403)
			return
		}
	}
	switch r.URL.Path {
	case "/api/status":
		f.write(w, map[string]any{"quota_per_unit": 500000, "quota_display_type": "USD", "usd_exchange_rate": 7.2})
	case "/api/oauth/state":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		token := "fixture-state-login"
		if b["intent"] == "verify" {
			token = "fixture-state-verify"
		}
		f.write(w, map[string]any{"flow_token": token})
	case "/api/oauth/oidc":
		var grant oauthGrant
		if e := f.s.readToken(r.Context(), "oauth_code", r.URL.Query().Get("code"), true, &grant); e != nil || grant.UserID != 1 {
			http.Error(w, "wrong grant", 403)
			return
		}
		if r.URL.Query().Get("state") == "fixture-state-verify" {
			f.write(w, map[string]any{"proof_token": "fixture-proof"})
			return
		}
		if f.challenge {
			f.write(w, map[string]any{"require_verification": true, "flow_token": "login-challenge", "expires_at": time.Now().Add(5 * time.Minute).Unix(), "methods": []map[string]any{{"method": "2fa", "available": true}}})
			return
		}
		f.bundle(w)
	case "/api/user/login/verify":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["code"] != "123456" {
			f.write(w, map[string]any{})
			return
		}
		f.bundle(w)
	case "/api/user/auth/refresh":
		cookie, e := r.Cookie("new_api_refresh")
		if e != nil || cookie.Value != "fixture-refresh" || r.Header.Get("X-Auth-Session") != "session-one" {
			http.Error(w, "bad refresh", 403)
			return
		}
		f.refreshes++
		f.bundle(w)
	case "/api/subscription/self/preference":
		f.wallet = "wallet_only"
		f.write(w, map[string]any{"billing_preference": f.wallet})
	case "/api/subscription/self":
		f.write(w, map[string]any{"billing_preference": f.wallet})
	case "/api/user/self":
		if r.Header.Get("Authorization") != "Bearer user-access" {
			http.Error(w, "wrong user credential", 403)
			return
		}
		if r.Method == http.MethodPut {
			if r.Header.Get("X-Security-Proof") != "fixture-proof" {
				http.Error(w, "missing proof", 403)
				return
			}
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.passwords = append(f.passwords, b["password"].(string))
			f.hasPassword = true
			f.bundle(w)
			return
		}
		u := f.user
		u.HasPassword = f.hasPassword
		if f.badSubject {
			u.OIDCID = "forest:2"
		}
		f.write(w, u)
	case "/api/user/models":
		f.write(w, []string{"test-model"})
	case "/pg/chat/completions":
		if r.Header.Get("Authorization") != "Bearer user-access" {
			http.Error(w, "wrong chat credential", 403)
			return
		}
		f.chats++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
	case "/api/user/topup/info":
		f.write(w, map[string]any{"enable_online_topup": true, "min_topup": 1, "discount": map[string]any{}, "pay_methods": []map[string]any{{"type": "wallet"}}})
	case "/api/user/amount":
		var b struct {
			Amount int64 `json:"amount"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "success", "data": formatMoneyFen(b.Amount * 700)})
	case "/api/user/pay":
		f.pays++
		trade := "workspace-order"
		f.orders[trade] = remoteTopUp{ID: 1, UserID: 4, Money: json.Number("14.00"), TradeNo: trade, PaymentMethod: "wallet", PaymentProvider: "epay", Status: "pending"}
		if f.dropPay {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		params := walletFixtureParams(f.cfg, trade, 1400)
		p := map[string]string{}
		for k := range params {
			p[k] = params.Get(k)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "success", "data": p, "url": f.cfg.SiteURL + "/new-api/submit.php"})
	case "/api/user/topup":
		items := []remoteTopUp{}
		if v, ok := f.orders[r.URL.Query().Get("keyword")]; ok {
			items = append(items, v)
		}
		f.write(w, map[string]any{"items": items, "total": len(items)})
	case "/api/user/topup/self":
		items := []map[string]any{}
		for _, v := range f.orders {
			items = append(items, map[string]any{"id": v.ID, "user_id": v.UserID, "money": v.Money, "trade_no": v.TradeNo, "payment_method": v.PaymentMethod, "payment_provider": v.PaymentProvider, "status": v.Status, "amount": 2, "create_time": time.Now().Unix()})
		}
		f.write(w, map[string]any{"items": items, "total": len(items)})
	case "/api/user/4":
		f.write(w, remoteUser{ID: 4, OIDCID: f.user.OIDCID, Status: 1})
	case "/api/verify/methods":
		method := "oauth"
		if f.hasPassword {
			method = "password"
		}
		f.write(w, map[string]any{"methods": []map[string]any{{"method": method, "available": true}}, "oauth_providers": []map[string]any{{"slug": "oidc"}}, "password_encryption_enabled": false})
	case "/api/verify":
		f.write(w, map[string]any{"proof_token": "fixture-proof"})
	case "/api/token/":
		if r.Method == http.MethodPost {
			f.write(w, nil)
		} else {
			f.write(w, map[string]any{"items": []map[string]any{{"id": 1, "name": "key", "key": "SECRET-MUST-NOT-LEAK", "user_id": 4, "remain_quota": 500000}}, "total": 1})
		}
	case "/api/log/self":
		f.write(w, map[string]any{"items": []map[string]any{{"id": 1, "model_name": "test-model", "quota": 200, "content": "PRIVATE-UPSTREAM", "other": "PRIVATE"}}, "total": 1})
	default:
		http.NotFound(w, r)
	}
}
func workspacePostgresFixture(t *testing.T) (*Service, *workspaceFixture) {
	s, cfg, _ := walletPostgresFixture(t)
	_, e := s.db.Exec(`ALTER TABLE v2_user ADD COLUMN email TEXT NOT NULL DEFAULT 'fixture@example.test'`)
	if e != nil {
		t.Fatal(e)
	}
	f := &workspaceFixture{s: s, user: workspaceUser{ID: 4, Username: "forest_1", OIDCID: "forest:1", Status: 1, Quota: 1250000, UsedQuota: 250000}, orders: map[string]remoteTopUp{}, wallet: "subscription_first"}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	cfg.APIURL = server.URL
	cfg.ClientID = "forest"
	cfg.ClientSecret = strings.Repeat("x", 32)
	f.cfg = cfg
	s.client = server.Client()
	walletSaveFixtureConfig(t, s, cfg)
	return s, f
}
func connectWorkspace(t *testing.T, s *Service) {
	t.Helper()
	v, e := s.ConnectAccount(context.Background(), 1, "valid-forest-session")
	if e != nil || v["connected"] != true {
		t.Fatalf("connect=%v err=%v", v, e)
	}
}
func TestWorkspaceAccountFlowAndIsolation(t *testing.T) {
	s, f := workspacePostgresFixture(t)
	ctx := context.Background()
	before, e := s.WorkspaceAccount(ctx, 1, "auth")
	if e != nil || before["connected"] != false {
		t.Fatalf("unconnected=%v %v", before, e)
	}
	connectWorkspace(t, s)
	account, e := s.WorkspaceAccount(ctx, 1, "auth")
	if e != nil || account["display_balance"] != "2.500000" || account["currency"] != "USD" {
		t.Fatalf("account=%v err=%v", account, e)
	}
	keys, e := s.WorkspaceKeys(ctx, 1, "auth")
	raw, _ := json.Marshal(keys)
	if e != nil || strings.Contains(string(raw), "SECRET") {
		t.Fatalf("keys=%s %v", raw, e)
	}
	logs, e := s.WorkspaceUsage(ctx, 1, "auth", 1, 20)
	raw, _ = json.Marshal(logs)
	if e != nil || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("logs=%s %v", raw, e)
	}
	body, e := s.OpenWorkspaceChat(ctx, 1, "auth", map[string]any{"model": "test-model", "messages": []any{}, "stream": true})
	if e != nil {
		t.Fatal(e)
	}
	raw, e = io.ReadAll(body)
	body.Close()
	if e != nil || !strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("stream %s %v", raw, e)
	}
	if _, e = s.OpenWorkspaceChat(ctx, 1, "auth", map[string]any{"model": "unavailable", "stream": true}); !errors.Is(e, ErrWorkspaceModel) {
		t.Fatalf("bad model %v", e)
	}
	if account, e = s.WorkspaceAccount(ctx, 1, "auth"); e != nil || account["connected"] != true {
		t.Fatalf("unavailable model invalidated account: %v %v", account, e)
	}
	f.mu.Lock()
	f.badSubject = true
	f.mu.Unlock()
	if _, e = s.WorkspaceAccount(ctx, 1, "auth"); !errors.Is(e, ErrForbidden) {
		t.Fatalf("subject bypass %v", e)
	}
	if _, e = s.WorkspaceKeys(ctx, 2, "auth"); !errors.Is(e, ErrWorkspaceNotConnected) {
		t.Fatalf("cross-user %v", e)
	}
}
func TestWorkspaceRefreshSingleFlight(t *testing.T) {
	s, f := workspacePostgresFixture(t)
	s.db.SetMaxOpenConns(3)
	connectWorkspace(t, s)
	ctx := context.Background()
	c, e := s.loadWorkspaceSession(ctx, 1, f.cfg.APIURL)
	if e != nil {
		t.Fatal(e)
	}
	c.ExpiresAt = 1
	if e = s.storeWorkspaceSession(ctx, 1, f.cfg, c, f.user); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.WorkspaceAccount(ctx, 1, "auth"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if f.refreshes != 1 {
		t.Fatalf("refreshes=%d", f.refreshes)
	}
}
func TestWorkspaceQuoteConcurrentConfirmAndUnknownRecovery(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent", true: "lost_response"}[unknown], func(t *testing.T) {
			s, f := workspacePostgresFixture(t)
			s.db.SetMaxOpenConns(3)
			connectWorkspace(t, s)
			ctx := context.Background()
			f.dropPay = unknown
			view, e := s.QuoteWorkspaceTransfer(ctx, 1, "auth", WorkspaceQuoteRequest{AmountFen: 1400, RequestID: "quote-request-1"})
			if e != nil {
				t.Fatal(e)
			}
			key := view.(map[string]any)["quote_id"].(string)
			if view.(map[string]any)["credited_quota"] != int64(1000000) {
				t.Fatal(view)
			}
			if unknown {
				if _, e = s.ConfirmWorkspaceTransfer(ctx, 1, "auth", key); !errors.Is(e, ErrWorkspaceUnknown) {
					t.Fatalf("wanted unknown: %v", e)
				}
				if _, e = s.ConfirmWorkspaceTransfer(ctx, 1, "auth", key); !errors.Is(e, ErrWorkspaceUnknown) {
					t.Fatal(e)
				}
				if _, e = s.QuoteWorkspaceTransfer(ctx, 1, "auth", WorkspaceQuoteRequest{AmountFen: 1400, RequestID: "quote-request-2"}); !errors.Is(e, ErrWorkspaceUnknown) {
					t.Fatal(e)
				}
				review, e := s.ReviewWorkspaceTransfer(ctx, 1, "auth", key)
				if e != nil || len(review.(map[string]any)["candidates"].([]map[string]any)) != 1 {
					t.Fatalf("review=%v %v", review, e)
				}
				if _, e = s.ResolveWorkspaceTransfer(ctx, 1, "auth", WorkspaceResolveRequest{QuoteID: key, RemoteTradeNo: "workspace-order"}); e != nil {
					t.Fatal(e)
				}
			} else {
				var wg sync.WaitGroup
				for i := 0; i < 10; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if _, e := s.ConfirmWorkspaceTransfer(ctx, 1, "auth", key); e != nil {
							t.Error(e)
						}
					}()
				}
				wg.Wait()
			}
			if f.pays != 1 {
				t.Fatalf("created %d", f.pays)
			}
			var balance, count int64
			_ = s.db.QueryRow(`SELECT balance FROM v2_user WHERE id=1`).Scan(&balance)
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_transfer`).Scan(&count)
			if balance != 8600 || count != 1 {
				t.Fatalf("balance=%d transfers=%d", balance, count)
			}
		})
	}
}
func TestWorkspacePasswordAndChallenge(t *testing.T) {
	s, f := workspacePostgresFixture(t)
	ctx := context.Background()
	f.challenge = true
	view, e := s.ConnectAccount(ctx, 1, "auth")
	if e != nil {
		t.Fatal(e)
	}
	key := view["challenge"].(map[string]any)["id"].(string)
	if _, e = s.VerifyWorkspaceLogin(ctx, 1, "auth", WorkspaceLoginVerification{ChallengeID: key, Code: "123456"}); e != nil {
		t.Fatal(e)
	}
	prep, e := s.PrepareWorkspacePassword(ctx, 1, "auth", false)
	if e != nil {
		t.Fatal(e)
	}
	pass, e := s.ConfirmWorkspacePassword(ctx, 1, "auth", WorkspacePasswordRequest{ChallengeID: prep["challenge_id"].(string), Method: "oauth"})
	if e != nil || len(pass["password"].(string)) < 32 {
		t.Fatalf("password %v %v", pass, e)
	}
	shown, e := s.RevealWorkspacePassword(ctx, 1, "auth")
	if e != nil || shown["password"] != pass["password"] {
		t.Fatal(e)
	}
	var stored string
	_ = s.db.QueryRow(`SELECT password_cipher FROM v2_newapi_account`).Scan(&stored)
	if strings.Contains(stored, pass["password"].(string)) {
		t.Fatal("plaintext persisted")
	}
	var audit int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_workspace_audit`).Scan(&audit)
	if audit != 2 {
		t.Fatalf("audit=%d", audit)
	}
	var challengeCipher string
	if e = s.db.QueryRow(`SELECT payload_cipher FROM v2_newapi_workspace_action WHERE id=$1`, prep["challenge_id"]).Scan(&challengeCipher); e != nil {
		t.Fatal(e)
	}
	challengeRaw, e := s.open(challengeCipher)
	if e != nil || strings.Contains(string(challengeRaw), pass["password"].(string)) {
		t.Fatal("completed password challenge retains credential")
	}
	reset, e := s.PrepareWorkspacePassword(ctx, 1, "auth", true)
	if e != nil {
		t.Fatal(e)
	}
	next, e := s.ConfirmWorkspacePassword(ctx, 1, "auth", WorkspacePasswordRequest{ChallengeID: reset["challenge_id"].(string), Method: "password"})
	if e != nil || next["password"] == pass["password"] {
		t.Fatalf("reset=%v %v", next, e)
	}
	c, e := s.loadWorkspaceSession(ctx, 1, f.cfg.APIURL)
	if e != nil {
		t.Fatal(e)
	}
	c.SessionID = "new-session-after-external-password-change"
	if e = s.storeWorkspaceSession(ctx, 1, f.cfg, c, f.user); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RevealWorkspacePassword(ctx, 1, "auth"); !errors.Is(e, ErrConflict) {
		t.Fatalf("old password survived session change: %v", e)
	}

}

func TestWorkspaceAmountSearchRoundingAndDiscount(t *testing.T) {
	amount, e := workspaceFindAmount(context.Background(), 1, 1000000, nil, func(n int64) (int64, error) { return (n*100 + 1) / 3, nil })
	if e != nil || amount != 30000 {
		t.Fatalf("amount=%d err=%v", amount, e)
	}
	amount, e = workspaceFindAmount(context.Background(), 1, 1400, []int64{3}, func(n int64) (int64, error) {
		if n == 3 {
			return 1400, nil
		}
		return n * 700, nil
	})
	if e != nil || amount != 3 {
		t.Fatalf("discount amount=%d err=%v", amount, e)
	}
}
func TestWorkspaceNegativeQuotaCanRecharge(t *testing.T) {
	s, f := workspacePostgresFixture(t)
	f.user.Quota = -50000
	connectWorkspace(t, s)
	v, e := s.WorkspaceAccount(context.Background(), 1, "auth")
	if e != nil || v["display_balance"] != "-0.100000" {
		t.Fatalf("negative=%v %v", v, e)
	}
	if _, e = s.QuoteWorkspaceTransfer(context.Background(), 1, "auth", WorkspaceQuoteRequest{AmountFen: 1400, RequestID: "negative-quota-1"}); e != nil {
		t.Fatal(e)
	}
}
func TestWorkspaceErrorsAreClassifiedWithoutLeaks(t *testing.T) {
	cases := []struct {
		status int
		raw    string
		want   error
	}{{402, `{"message":"PRIVATE_SECRET"}`, ErrWorkspaceQuota}, {429, `{"message":"PRIVATE_SECRET"}`, ErrWorkspaceRateLimit}, {400, `{"error":{"code":"context_length_exceeded","message":"PRIVATE_SECRET"}}`, ErrWorkspaceContext}, {200, `{"success":false,"code":"SECURITY_VERIFICATION_FAILED"}`, ErrWorkspaceVerification}, {503, `{"error":{"code":"channel_not_found"}}`, ErrWorkspaceModel}}
	for _, v := range cases {
		if e := workspaceRemoteError(v.status, []byte(v.raw)); e != v.want || strings.Contains(e.Error(), "PRIVATE") {
			t.Errorf("got %v want %v", e, v.want)
		}
	}
}

func TestWorkspaceLocksDifferentUsersPreserveSmallPool(t *testing.T) {
	s, _ := workspacePostgresFixture(t)
	s.db.SetMaxOpenConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for id := int64(1); id <= 12; id++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			lock, e := s.workspaceLock(ctx, id, "https://fixture.test")
			if e != nil {
				t.Error(e)
				return
			}
			defer lock.Rollback()
			var result int
			if e = s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&result); e != nil {
				t.Error(e)
			}
		}(id)
	}
	wg.Wait()
}
func TestWorkspaceActionCleanupRetainsUnknownAndFinancialEvidence(t *testing.T) {
	s, f := workspacePostgresFixture(t)
	ctx := context.Background()
	for _, kind := range []string{"password", "login", "transfer_quote", "key_create"} {
		for _, state := range []string{"ready", "done", "sending"} {
			id, e := s.saveWorkspaceAction(ctx, 1, f.cfg.APIURL, kind, "", map[string]string{"secret": "fixture"}, time.Now().Unix()-1)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.db.Exec(`UPDATE v2_newapi_workspace_action SET state=$2 WHERE id=$1`, id, state); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := s.CleanupWorkspaceActions(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_workspace_action`).Scan(&count); e != nil || count != 8 {
		t.Fatalf("retained=%d err=%v", count, e)
	}
}

func TestWorkspaceAccountRecoversUnknownQuoteAcrossSessions(t *testing.T) {
	s, f := workspacePostgresFixture(t)
	connectWorkspace(t, s)
	ctx := context.Background()
	f.dropPay = true
	value, e := s.QuoteWorkspaceTransfer(ctx, 1, "original-session", WorkspaceQuoteRequest{AmountFen: 1400, RequestID: "recover-cross-session"})
	if e != nil {
		t.Fatal(e)
	}
	key := value.(map[string]any)["quote_id"].(string)
	if _, e = s.ConfirmWorkspaceTransfer(ctx, 1, "original-session", key); !errors.Is(e, ErrWorkspaceUnknown) {
		t.Fatalf("confirm=%v", e)
	}
	for _, auth := range []string{"another-browser-session", "another-device-session"} {
		account, e := s.WorkspaceAccount(ctx, 1, auth)
		if e != nil {
			t.Fatal(e)
		}
		pending, ok := account["pending_transfer_quote"].(map[string]any)
		if !ok || pending["quote_id"] != key || pending["status"] != "sending" || len(pending) != 2 {
			t.Fatalf("pending=%v", pending)
		}
	}
	if _, e = s.db.Exec(`DELETE FROM v2_newapi_account WHERE user_id=1`); e != nil {
		t.Fatal(e)
	}
	account, e := s.WorkspaceAccount(ctx, 1, "fresh-forest-session")
	if e != nil || account["connected"] != false || account["pending_transfer_quote"].(map[string]any)["quote_id"] != key {
		t.Fatalf("unconnected account=%v err=%v", account, e)
	}
	other, e := s.WorkspaceAccount(ctx, 2, "different-user-session")
	if e != nil {
		t.Fatal(e)
	}
	pending, ok := other["pending_transfer_quote"].(map[string]any)
	if ok && pending != nil {
		t.Fatalf("cross-user quote leak=%v", pending)
	}
	cfg := f.cfg
	cfg.APIURL = "https://different-instance.example.test"
	walletSaveFixtureConfig(t, s, cfg)
	different, e := s.WorkspaceAccount(ctx, 1, "auth")
	if e != nil {
		t.Fatal(e)
	}
	pending, ok = different["pending_transfer_quote"].(map[string]any)
	if ok && pending != nil {
		t.Fatalf("cross-instance quote leak=%v", pending)
	}
}
