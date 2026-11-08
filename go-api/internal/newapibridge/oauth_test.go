package newapibridge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func oauthFixture(t *testing.T) (*Service, Settings, OAuthRequest) {
	t.Helper()
	s, c, _ := walletPostgresFixture(t)
	if _, e := s.db.Exec(`ALTER TABLE v2_user ADD COLUMN email TEXT NOT NULL DEFAULT 'user@example.test'`); e != nil {
		t.Fatal(e)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	c.APIURL = "https://ai.example.test"
	c.ClientID = "forest"
	c.ClientSecret = strings.Repeat("s", 40)
	c.MerchantKey = strings.Repeat("m", 40)
	c.SigningKey = base64.RawStdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key))
	walletSaveFixtureConfig(t, s, c)
	return s, c, OAuthRequest{ClientID: c.ClientID, RedirectURI: c.APIURL + "/oauth/oidc", ResponseType: "code", Scope: "openid profile email", State: "opaque-state-value", Nonce: "nonce-value"}
}

func TestOAuthPostgresSingleUseAndVerifiedIDToken(t *testing.T) {
	s, c, req := oauthFixture(t)
	ctx := context.Background()
	target, e := s.Authorize(ctx, 1, "original-panel-session", req)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(target)
	if u.Query().Get("state") != req.State || u.Host != "ai.example.test" {
		t.Fatal(target)
	}
	exchange := OAuthTokenRequest{ClientID: c.ClientID, ClientSecret: "wrong", GrantType: "authorization_code", Code: u.Query().Get("code"), RedirectURI: req.RedirectURI}
	if _, e = s.ExchangeCode(ctx, exchange); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	exchange.ClientSecret = c.ClientSecret
	result, e := s.ExchangeCode(ctx, exchange)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExchangeCode(ctx, exchange); !errors.Is(e, ErrForbidden) {
		t.Fatal("code replay accepted", e)
	}
	key, _ := parseSigningKey(c.SigningKey)
	parsed, e := jwt.Parse(result["id_token"].(string), func(token *jwt.Token) (any, error) {
		if token.Header["kid"] != signingKeyID(key) {
			t.Fatal("wrong key id")
		}
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(c.SiteURL+"/new-api"), jwt.WithAudience(c.ClientID))
	if e != nil || !parsed.Valid {
		t.Fatal(e)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["sub"] != "forest:1" || claims["nonce"] != req.Nonce {
		t.Fatal(claims)
	}
	id, auth, e := s.OAuthAuthentication(ctx, result["access_token"].(string))
	if e != nil || id != 1 || auth != "original-panel-session" {
		t.Fatal(id, e)
	}
	userinfo, e := s.UserInfo(ctx, id)
	if e != nil || userinfo.Subject != "forest:1" || userinfo.Email != "user@example.test" {
		t.Fatal(userinfo, e)
	}
	if _, e = s.db.Exec(`UPDATE v2_user SET banned=1 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.UserInfo(ctx, id); !errors.Is(e, ErrForbidden) {
		t.Fatal("banned identity accepted", e)
	}
}

func TestOAuthPostgresPKCEExpiryRevisionAndConcurrency(t *testing.T) {
	s, c, req := oauthFixture(t)
	ctx := context.Background()
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	req.CodeChallenge = base64.RawURLEncoding.EncodeToString(sum[:])
	req.CodeChallengeMethod = "S256"
	issue := func() OAuthTokenRequest {
		t.Helper()
		target, e := s.Authorize(ctx, 1, "panel-session", req)
		if e != nil {
			t.Fatal(e)
		}
		u, _ := url.Parse(target)
		return OAuthTokenRequest{ClientID: c.ClientID, ClientSecret: c.ClientSecret, GrantType: "authorization_code", Code: u.Query().Get("code"), RedirectURI: req.RedirectURI, CodeVerifier: verifier}
	}
	bad := issue()
	bad.CodeVerifier = strings.Repeat("w", 43)
	if _, e := s.ExchangeCode(ctx, bad); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	expired := issue()
	if _, e := s.db.Exec(`UPDATE v2_newapi_token SET expires_at=1 WHERE token_hash=$1`, tokenHash(expired.Code)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ExchangeCode(ctx, expired); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	parallel := issue()
	results := make(chan error, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.ExchangeCode(ctx, parallel); results <- e }()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrForbidden) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("single-use concurrency", success)
	}
	stale := issue()
	c.Revision++
	walletSaveFixtureConfig(t, s, c)
	if _, e := s.ExchangeCode(ctx, stale); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
}

func TestPortalPostgresTicketDoesNotExposePanelCredentialAndScopesSessions(t *testing.T) {
	s, _, _ := oauthFixture(t)
	ctx := context.Background()
	link, e := s.IssuePortalTicket(ctx, 1, "PANEL_SESSION_SECRET")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(link, "PANEL_SESSION_SECRET") || !strings.Contains(link, "#ticket=") {
		t.Fatal("unsafe launch link")
	}
	u, _ := url.Parse(link)
	q, _ := url.ParseQuery(u.Fragment)
	ticket := q.Get("ticket")
	id, auth, e := s.RedeemPortalTicket(ctx, ticket)
	if e != nil || id != 1 || auth != "PANEL_SESSION_SECRET" {
		t.Fatal(id, e)
	}
	if _, _, e = s.RedeemPortalTicket(ctx, ticket); !errors.Is(e, ErrForbidden) {
		t.Fatal("ticket reused", e)
	}
	token, e := s.IssuePortalSession(ctx, id, auth)
	if e != nil {
		t.Fatal(e)
	}
	id, auth, e = s.PortalAuthentication(ctx, token)
	if e != nil || id != 1 || auth != "PANEL_SESSION_SECRET" {
		t.Fatal(id, e)
	}
	if _, _, e = s.OAuthAuthentication(ctx, strings.TrimPrefix(token, "nap_")); !errors.Is(e, ErrForbidden) {
		t.Fatal("portal token elevated to OAuth", e)
	}
	if e = s.RevokePortalSession(ctx, token); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.PortalAuthentication(ctx, token); !errors.Is(e, ErrForbidden) {
		t.Fatal("revocation ignored", e)
	}
	var cipher string
	if e = s.db.QueryRow(`SELECT payload_cipher FROM v2_newapi_token LIMIT 1`).Scan(&cipher); e == nil && strings.Contains(cipher, "PANEL_SESSION_SECRET") {
		t.Fatal("stored plaintext credential")
	}
}

func TestOAuthRequestValidationAndOrigin(t *testing.T) {
	c := Settings{Enabled: true, APIURL: "https://ai.example.test", ClientID: "forest"}
	valid := OAuthRequest{ClientID: "forest", RedirectURI: c.APIURL + "/oauth/oidc", ResponseType: "code", Scope: "openid profile email", State: "12345678"}
	if e := validateOAuthRequest(c, valid); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*OAuthRequest){func(r *OAuthRequest) { r.RedirectURI += "?redirect=https://evil.test" }, func(r *OAuthRequest) { r.RedirectURI = "https://ai.example.test.evil.test/oauth/oidc" }, func(r *OAuthRequest) { r.ClientID = "other" }, func(r *OAuthRequest) { r.ResponseType = "token" }, func(r *OAuthRequest) { r.Scope = "openid admin" }, func(r *OAuthRequest) { r.State = "" }, func(r *OAuthRequest) { r.CodeChallenge = "abc"; r.CodeChallengeMethod = "plain" }} {
		req := valid
		mutate(&req)
		if e := validateOAuthRequest(c, req); e == nil {
			t.Fatal(req)
		}
	}
	for _, bad := range []string{"http://ai.example.test", "https://user:pass@ai.example.test", "https://ai.example.test/path", "https://ai.example.test/?key=secret", "https://ai.example.test/#secret", "https://ai.example.test?", "//ai.example.test"} {
		if _, e := normalizeOrigin(bad); e == nil {
			t.Fatal(bad)
		}
	}
}

func TestConfigPostgresCASAndSecretRedaction(t *testing.T) {
	s, c, _ := oauthFixture(t)
	ctx := context.Background()
	req := SettingsSaveRequest{Enabled: true, APIURL: c.APIURL, SiteURL: c.SiteURL, ClientID: c.ClientID, MerchantID: c.MerchantID, AdminUserID: c.AdminUserID, MinAmountFen: 100, MaxAmountFen: 10000, DailyLimitFen: 10000, ExpectedRevision: c.Revision}
	view, e := s.SaveConfig(ctx, 9, req)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(view)
	for _, secret := range []string{c.ClientSecret, c.MerchantKey, c.AdminToken, c.SigningKey} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret exposed")
		}
	}
	loaded, e := s.loadSettings(ctx)
	if e != nil || loaded.ClientSecret != c.ClientSecret || loaded.Revision != c.Revision+1 {
		t.Fatal("blank did not preserve secret", e)
	}
	if _, e = s.SaveConfig(ctx, 9, req); !errors.Is(e, ErrConflict) {
		t.Fatal("stale save accepted", e)
	}
	var encrypted string
	if e = s.db.QueryRow(`SELECT config_cipher FROM v2_newapi_settings WHERE id=1`).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(encrypted, c.AdminToken) {
		t.Fatal("unsealed config")
	}
	_, e = s.db.Exec(`INSERT INTO v2_newapi_checkout(id,api_url,remote_trade_no,user_id,remote_user_id,amount_fen,payment_method,config_revision,config_cipher,created_at,expires_at) VALUES('fixture',$1,'fixture',1,4,100,'wallet',1,'cipher',1,9999999999);`, c.APIURL)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec(`INSERT INTO v2_newapi_transfer(checkout_id,api_url,remote_trade_no,user_id,remote_user_id,amount_fen,balance_before,balance_after,status,next_attempt_at,created_at) VALUES('fixture',$1,'fixture',1,4,100,10000,9900,'pending',0,1)`, c.APIURL)
	if e != nil {
		t.Fatal(e)
	}
	req.ExpectedRevision = loaded.Revision
	req.APIURL = "https://other.example.test"
	if _, e = s.SaveConfig(ctx, 9, req); !errors.Is(e, ErrConflict) {
		t.Fatal("pending destination changed", e)
	}
	req.APIURL = "https://AI.EXAMPLE.TEST/"
	req.AdminToken = "rotated-admin-token"
	req.Enabled = false
	if _, e = s.SaveConfig(ctx, 9, req); e != nil {
		t.Fatal("cannot disable/rotate", e)
	}
	if e = s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_transfer`).Scan(&count); e != nil || count != 1 {
		t.Fatal("cleanup touched financial records", count, e)
	}
}

func TestEnvelopeAuthenticatedAndWrongAppKeyFails(t *testing.T) {
	s := &Service{appKey: "fixture-key"}
	cipher, e := s.seal([]byte("secret"))
	if e != nil {
		t.Fatal(e)
	}
	plain, e := s.open(cipher)
	if e != nil || string(plain) != "secret" {
		t.Fatal(e)
	}
	other := &Service{appKey: "other"}
	if _, e = other.open(cipher); e == nil {
		t.Fatal("wrong key accepted")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(cipher)
	raw[len(raw)-1] ^= 1
	if _, e = s.open(base64.RawURLEncoding.EncodeToString(raw)); e == nil {
		t.Fatal("tamper accepted")
	}
}

func TestSettingsViewDoesNotInventRelativeEndpoints(t *testing.T) {
	for _, name := range []string{"authorize_url", "token_url", "userinfo_url", "epay_url", "callback_url"} {
		if settingsView(Settings{})[name] != "" {
			t.Fatalf("empty origins must not produce %s", name)
		}
	}
	if settingsView(Settings{SiteURL: "https://panel.example.test", APIURL: "https://ai.example.test"})["callback_url"] != "https://ai.example.test/oauth/oidc" {
		t.Fatal("incorrect callback")
	}
}
