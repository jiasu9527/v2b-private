package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/session"
)

type newAPIHTTPFixture struct {
	newAPIBridgeService
	query      newapibridge.TransferQuery
	actor      int64
	calls      int
	payError   error
	saved      newapibridge.SettingsSaveRequest
	issuedAuth string
	public     map[string]any
}

func (f *newAPIHTTPFixture) PublicInfo(context.Context) (map[string]any, error) {
	if f.public != nil {
		return f.public, nil
	}
	return map[string]any{"enabled": true, "api_url": "https://ai.example.test", "site_url": "https://panel.example.test"}, nil
}
func (f *newAPIHTTPFixture) Overview(_ context.Context, id int64) (map[string]any, error) {
	f.actor = id
	return map[string]any{"balance_fen": 1200}, nil
}
func (f *newAPIHTTPFixture) GetConfig(context.Context) (map[string]any, error) {
	f.calls++
	return map[string]any{"has_admin_token": true, "revision": 1}, nil
}
func (f *newAPIHTTPFixture) SaveConfig(_ context.Context, id int64, req newapibridge.SettingsSaveRequest) (map[string]any, error) {
	f.calls++
	f.actor = id
	f.saved = req
	return map[string]any{"revision": 2}, nil
}
func (f *newAPIHTTPFixture) ListTransfers(_ context.Context, q newapibridge.TransferQuery) (newapibridge.TransferPage, error) {
	f.query = q
	f.calls++
	return newapibridge.TransferPage{Items: []newapibridge.Transfer{}, Current: q.Current, PageSize: q.PageSize}, nil
}
func (f *newAPIHTTPFixture) ListBindings(_ context.Context, current, pageSize int) (newapibridge.BindingPage, error) {
	f.calls++
	return newapibridge.BindingPage{Items: []newapibridge.Binding{}, Current: current, PageSize: pageSize}, nil
}
func (f *newAPIHTTPFixture) CheckConnection(context.Context) (newapibridge.CheckResult, error) {
	f.calls++
	return newapibridge.CheckResult{}, nil
}
func (f *newAPIHTTPFixture) RetryTransfer(_ context.Context, id, actor int64) error {
	f.actor = actor
	f.calls++
	return nil
}
func (f *newAPIHTTPFixture) Pay(_ context.Context, id int64, checkout string) (newapibridge.Transfer, error) {
	f.actor = id
	f.calls++
	return newapibridge.Transfer{ID: 9, UserID: id, Status: "pending"}, f.payError
}
func (f *newAPIHTTPFixture) PortalAuthentication(_ context.Context, token string) (int64, string, error) {
	if token != "nap_valid" {
		return 0, "", newapibridge.ErrForbidden
	}
	return 7, "panel-auth", nil
}
func (f *newAPIHTTPFixture) IssuePortalSession(_ context.Context, id int64, auth string) (string, error) {
	f.actor = id
	f.issuedAuth = auth
	return "nap_scoped", nil
}
func (f *newAPIHTTPFixture) OAuthAuthentication(_ context.Context, token string) (int64, string, error) {
	return 7, "panel-auth", nil
}
func (f *newAPIHTTPFixture) UserInfo(_ context.Context, id int64) (newapibridge.OAuthUser, error) {
	f.calls++
	return newapibridge.OAuthUser{Subject: "forest:7", Email: "user@example.test"}, nil
}
func (f *newAPIHTTPFixture) ExchangeCode(_ context.Context, r newapibridge.OAuthTokenRequest) (map[string]any, error) {
	f.calls++
	return map[string]any{"access_token": "scoped-access", "token_type": "Bearer"}, nil
}
func (f *newAPIHTTPFixture) ValidateOAuth(context.Context, newapibridge.OAuthRequest) error {
	return nil
}

type newAPIFreshSession struct {
	session.Service
	identity   *session.Identity
	revoked    bool
	freshCalls int
}

func (s *newAPIFreshSession) Authenticate(context.Context, string, bool) (*session.Identity, error) {
	panic("newapi must bypass cached identity")
}
func (s *newAPIFreshSession) AuthenticateFresh(_ context.Context, token string, admin bool) (*session.Identity, error) {
	s.freshCalls++
	if token != "panel-auth" || s.revoked || s.identity == nil || (admin && s.identity.IsAdmin != 1) {
		return nil, session.ErrUnauthorized
	}
	return s.identity, nil
}

func newAPIHTTPCall(t *testing.T, router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestNewAPIHTTPUserIsolationAndPaymentFields(t *testing.T) {
	svc := &newAPIHTTPFixture{}
	sessions := &newAPIFreshSession{identity: &session.Identity{ID: 7}}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithNewAPIBridge(svc))
	w := newAPIHTTPCall(t, router, "GET", "/api/v1/user/new-api/transfers?current=2&page_size=10", "", "panel-auth")
	if w.Code != 200 || svc.query.UserID != 7 || svc.query.Current != 2 {
		t.Fatal(w.Code, w.Body.String(), svc.query)
	}
	for _, path := range []string{"/api/v1/user/new-api/transfers?user_id=8", "/api/v1/user/new-api/transfers?user_id=0", "/api/v1/user/new-api/transfers?current=0", "/api/v1/user/new-api/transfers?ignored=x", "/api/v1/user/new-api/transfers?page_size=999", "/api/v1/user/new-api/transfers?status=all", "/api/v1/user/new-api/transfers?current=1&current=2"} {
		before := svc.calls
		w = newAPIHTTPCall(t, router, "GET", path, "", "panel-auth")
		if w.Code < 400 || svc.calls != before {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"checkout_id":"test","user_id":8}`, `{"checkout_id":"test","amount_fen":1}`, `{"checkout_id":"test"} {}`} {
		before := svc.calls
		w = newAPIHTTPCall(t, router, "POST", "/api/v1/user/new-api/pay", body, "panel-auth")
		if w.Code != 400 || svc.calls != before {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	w = newAPIHTTPCall(t, router, "POST", "/api/v1/user/new-api/pay", `{"checkout_id":"test"}`, "panel-auth")
	if w.Code != 200 || svc.actor != 7 || !strings.Contains(w.Body.String(), `"pending"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	svc.payError = errors.New("SECRET_DATABASE_PASSWORD")
	w = newAPIHTTPCall(t, router, "POST", "/api/v1/user/new-api/pay", `{"checkout_id":"test"}`, "panel-auth")
	if w.Code != 503 || strings.Contains(w.Body.String(), "SECRET_DATABASE_PASSWORD") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") == "" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal(w.Header())
	}
}

func TestNewAPIHTTPAdminOnlyFreshAndNoQueryCredentials(t *testing.T) {
	svc := &newAPIHTTPFixture{}
	sessions := &newAPIFreshSession{identity: &session.Identity{ID: 7, IsStaff: 1}}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithNewAPIBridge(svc))
	for _, path := range []string{"/api/v1/localadmin/new-api/config", "/api/v1/localadmin/new-api/transfers"} {
		w := newAPIHTTPCall(t, router, "GET", path, "", "panel-auth")
		if w.Code < 400 || svc.calls != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	sessions.identity.IsAdmin = 1
	w := newAPIHTTPCall(t, router, "GET", "/api/v1/localadmin/new-api/config", "", "panel-auth")
	if w.Code != 200 || svc.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = newAPIHTTPCall(t, router, "POST", "/api/v1/localadmin/new-api/retry", `{"id":9}`, "panel-auth")
	if w.Code != 200 || svc.actor != 7 {
		t.Fatal(w.Code, w.Body.String())
	}
	before := svc.calls
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/localadmin/new-api/config?auth_data=panel-auth", "", "")
	if w.Code < 400 || svc.calls != before {
		t.Fatal("query credential accepted", w.Code)
	}
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/localadmin/new-api/config", "", "nap_valid")
	if w.Code != 403 || svc.calls != before {
		t.Fatal("portal gained admin", w.Code)
	}
	sessions.revoked = true
	w = newAPIHTTPCall(t, router, "POST", "/api/v1/localadmin/new-api/retry", `{"id":9}`, "panel-auth")
	if w.Code < 400 || svc.calls != before {
		t.Fatal("revoked session accepted", w.Code)
	}
}

func TestNewAPIAdminPageHeaderAuthContract(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/config", ""},
		{"GET", "/transfers?current=2&page_size=50&user_id=7&status=pending", ""},
		{"GET", "/bindings?current=3&page_size=20", ""},
		{"POST", "/config", `{"expected_revision":1,"enabled":false}`},
		{"POST", "/check", `{}`},
		{"POST", "/retry", `{"id":9}`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			for _, token := range []string{"", "expired", "panel-auth", "Bearer panel-auth"} {
				svc := &newAPIHTTPFixture{}
				sessions := &newAPIFreshSession{identity: &session.Identity{ID: 7, IsAdmin: 1}}
				router := NewRouter(config.Config{AdminPath: "safeadmin"}, WithSessionService(sessions), WithNewAPIBridge(svc))
				r := httptest.NewRequest(tc.method, "/api/v1/safeadmin/new-api"+tc.path, strings.NewReader(tc.body))
				r.Header.Set("Authorization", token)
				if tc.body != "" {
					r.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if token == "" || token == "expired" {
					if w.Code != http.StatusForbidden || svc.calls != 0 {
						t.Fatalf("invalid session accepted: %d %s", w.Code, w.Body.String())
					}
				} else if w.Code != http.StatusOK || svc.calls != 1 || sessions.freshCalls != 1 {
					t.Fatalf("valid page request failed: %d %s (calls=%d, fresh=%d)", w.Code, w.Body.String(), svc.calls, sessions.freshCalls)
				}
			}
		})
	}
}

func TestNewAPIPortalTokenAndUserInfoRequireOriginalFreshSession(t *testing.T) {
	svc := &newAPIHTTPFixture{}
	sessions := &newAPIFreshSession{identity: &session.Identity{ID: 7}}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithNewAPIBridge(svc))
	w := newAPIHTTPCall(t, router, "GET", "/api/v1/user/new-api/info", "", "nap_valid")
	if w.Code != 200 || svc.actor != 7 || sessions.freshCalls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	sessions.revoked = true
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/user/new-api/info", "", "nap_valid")
	if w.Code < 400 {
		t.Fatal("revoked original accepted")
	}
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/new-api/oauth/userinfo", "", "scoped-token")
	if w.Code < 400 || svc.calls != 0 {
		t.Fatal("revoked OAuth original accepted")
	}
	sessions.revoked = false
	sessions.identity.ID = 8
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/new-api/oauth/userinfo", "", "scoped-token")
	if w.Code != 403 || svc.calls != 0 {
		t.Fatal("mismatched owner accepted")
	}
}

func TestNewAPIPortalLoginDoesNotReturnPanelBearer(t *testing.T) {
	svc := &newAPIHTTPFixture{}
	sessions := &newAPIFreshSession{identity: &session.Identity{ID: 7}}
	passports := &fakePassportService{authData: passport.AuthData{AuthData: "panel-auth", Token: "subscription-secret"}}
	router := NewRouter(config.Config{}, WithSessionService(sessions), WithPassportService(passports), WithNewAPIBridge(svc))
	w := newAPIHTTPCall(t, router, "POST", "/api/v1/new-api/portal/login", `{"email":"user@example.test","password":"fixture-password"}`, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "nap_scoped") || strings.Contains(w.Body.String(), "panel-auth") || strings.Contains(w.Body.String(), "subscription-secret") || svc.issuedAuth != "panel-auth" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNewAPIOAuthHTTPFormAmbiguityAndRawResponse(t *testing.T) {
	svc := &newAPIHTTPFixture{}
	router := NewRouter(config.Config{}, WithNewAPIBridge(svc))
	for _, body := range []string{"grant_type=authorization_code&code=x&client_id=x&client_id=y", "grant_type=authorization_code&code=x&unknown=1"} {
		r := httptest.NewRequest("POST", "/api/v1/new-api/oauth/token", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 400 || svc.calls != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/new-api/oauth/token", strings.NewReader("grant_type=authorization_code&code=x&client_id=x&client_secret=y"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || result["access_token"] != "scoped-access" || result["data"] != nil {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNewAPIPortalBootstrapIsEscapedAndHardened(t *testing.T) {
	svc := &newAPIHTTPFixture{public: map[string]any{"enabled": true, "api_url": "https://example.test/</script><script>alert(1)</script>"}}
	router := NewRouter(config.Config{}, WithNewAPIBridge(svc))
	w := newAPIHTTPCall(t, router, "GET", "/new-api/", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>alert(1)</script>") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || !strings.Contains(w.Body.String(), "const bootstrap={") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for _, query := range []string{"client_id=a&client_id=b", "client_id=a&unknown=b"} {
		q, _ := url.ParseQuery(query)
		if _, e := newAPIOAuthQuery(q); e == nil {
			t.Fatal(query)
		}
	}
}

func TestNewAPITokenExchangeHasIndependentBackendRateBudget(t *testing.T) {
	login := httptest.NewRequest("POST", "/api/v1/new-api/portal/login", nil)
	login.RemoteAddr = "192.0.2.37:1234"
	token := httptest.NewRequest("POST", "/api/v1/new-api/oauth/token", nil)
	token.RemoteAddr = login.RemoteAddr
	newAPILimit.Lock()
	delete(newAPILimit.hits, requestIP(login)+"|"+login.URL.Path)
	delete(newAPILimit.hits, requestIP(token)+"|"+token.URL.Path)
	newAPILimit.Unlock()
	for i := 0; i < 30; i++ {
		if !allowNewAPIPublic(login) {
			t.Fatal("login budget too small")
		}
	}
	if allowNewAPIPublic(login) {
		t.Fatal("login budget bypassed")
	}
	for i := 0; i < 300; i++ {
		if !allowNewAPIPublic(token) {
			t.Fatal("shared-backend token budget too small")
		}
	}
	if allowNewAPIPublic(token) {
		t.Fatal("token budget bypassed")
	}
}
