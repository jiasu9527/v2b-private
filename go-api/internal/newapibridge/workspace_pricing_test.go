package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestWorkspacePricingPublicCatalogPreservesFieldsWithoutCredentials(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != "GET" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Auth-Session") != "" {
			t.Error("private credentials or unexpected request reached public endpoint")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"usd_exchange_rate":7.3,"quota_per_unit":500000,"secret":"DO_NOT_FORWARD"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[{"model_name":"test-text","quota_type":0,"model_ratio":0,"model_price":0,"completion_ratio":2,"cache_ratio":0,"billing_mode":"tiered_expr","billing_expr":"tier(\"standard\",p*2+c*10)","enable_groups":["default"],"supported_endpoint_types":["openai"],"admin_token":"DO_NOT_FORWARD","billing_plugin_variants":[{"plugin_key":"image","plugin_name":"Images","billing_expr":"tier(\"image\",fixed(0.1))*image_count","billing_mode":"tiered_expr","secret":"DO_NOT_FORWARD","billing_usage_schema":{"seconds":{"type":"number","unit":"second","description":{"zh":"每秒价格"},"secret":"DO_NOT_FORWARD"}},"billing_usage_examples":[{"label":"3秒","facts":{"seconds":3}}]}]}],"group_ratio":{"default":0,"vip":1.5},"usable_group":{"default":"默认","vip":"VIP"},"auto_groups":["default"],"vendors":[{"id":1,"name":"provider","secret":"DO_NOT_FORWARD"}],"supported_endpoint":{"openai":{"path":"/v1/chat/completions","method":"POST","secret":"DO_NOT_FORWARD"}},"pricing_version":"snapshot","secret":"DO_NOT_FORWARD"}`))
	}))
	defer server.Close()
	client := server.Client()
	client.Jar, _ = cookiejar.New(nil)
	u, _ := url.Parse(server.URL)
	client.Jar.SetCookies(u, []*http.Cookie{{Name: "private", Value: "session"}})
	svc := &Service{client: client}
	got, err := svc.fetchWorkspacePricing(context.Background(), Settings{APIURL: server.URL, AdminToken: "private-admin", MerchantKey: "private-key"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "DO_NOT_FORWARD") || strings.Contains(string(raw), "private-") {
		t.Fatalf("private data leaked: %s", raw)
	}
	if len(paths) != 2 || paths[0] != "/api/pricing" || paths[1] != "/api/status" {
		t.Fatal(paths)
	}
	if got.SourceURL != server.URL+"/pricing" || got.Scope != "public_catalog" || got.Currency != "CNY" || got.USDExchangeRate == nil || *got.USDExchangeRate != 7.3 || got.QuotaPerUnit == nil || *got.QuotaPerUnit != 500000 {
		t.Fatalf("metadata=%s", raw)
	}
	m := got.Catalog.Data[0]
	if m.CacheRatio == nil || *m.CacheRatio != 0 || m.ModelRatio == nil || *m.ModelRatio != 0 || got.Catalog.GroupRatio["default"] != 0 {
		t.Fatalf("explicit zero lost: %s", raw)
	}
	if len(m.BillingPluginVariants) != 1 || len(m.BillingPluginVariants[0].BillingUsageExamples) != 1 || got.Catalog.PricingVersion != "snapshot" || len(got.Catalog.SupportedEndpoint) != 1 {
		t.Fatalf("metadata lost: %s", raw)
	}
}

func TestWorkspacePricingInvalidOrRedirectCatalogIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"business failure", 200, `{"success":false,"data":[]}`},
		{"invalid json", 200, `oops`},
		{"missing catalog", 200, `{"success":true}`},
		{"wrong model shape", 200, `{"success":true,"data":[{"model_name":5}]}`},
		{"http failure", 503, `{"success":true,"data":[]}`},
		{"oversize", 200, strings.Repeat(" ", workspacePricingBodyLimit+1)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := (&Service{client: server.Client()}).fetchWorkspacePricing(context.Background(), Settings{APIURL: server.URL})
			if !errors.Is(err, ErrRemoteUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	hits := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer redirect.Close()
	_, err := (&Service{client: redirect.Client()}).fetchWorkspacePricing(context.Background(), Settings{APIURL: redirect.URL})
	if !errors.Is(err, ErrRemoteUnavailable) || hits != 0 {
		t.Fatalf("redirect followed: %v hits %d", err, hits)
	}
}

func TestWorkspacePricingUnavailableCurrencyRemainsUnknownNotZero(t *testing.T) {
	for _, body := range []string{`{"success":false}`, `{"success":true,"data":{"usd_exchange_rate":0,"quota_per_unit":-1}}`, `{"success":true,"data":{}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/status" {
					_, _ = w.Write([]byte(body))
					return
				}
				_, _ = w.Write([]byte(`{"success":true,"data":[],"group_ratio":{}}`))
			}))
			defer server.Close()
			got, err := (&Service{client: server.Client()}).fetchWorkspacePricing(context.Background(), Settings{APIURL: server.URL})
			if err != nil || got.USDExchangeRate != nil || got.QuotaPerUnit != nil || len(got.Warnings) == 0 {
				t.Fatalf("got=%+v error=%v", got, err)
			}
		})
	}
}

func TestWorkspacePricingBeforeAccountConnectionAndDisabledBridge(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Service{db: db, appKey: "test-key", schemaReady: true}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"usd_exchange_rate":7.3,"quota_per_unit":500000}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer server.Close()
	s.client = server.Client()
	cfg := Settings{Enabled: true, APIURL: server.URL}
	expectConfig := func(enabled bool) {
		mock.ExpectQuery(`SELECT email,banned FROM v2_user WHERE id=\$1`).WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"email", "banned"}).AddRow("person@example.test", 0))
		cfg.Enabled = enabled
		raw, _ := json.Marshal(cfg)
		cipher, e := s.seal(raw)
		if e != nil {
			t.Fatal(e)
		}
		mock.ExpectQuery(`SELECT config_cipher, revision FROM v2_newapi_settings WHERE id=1`).WillReturnRows(sqlmock.NewRows([]string{"config_cipher", "revision"}).AddRow(cipher, 1))
	}
	expectConfig(true)
	got, err := s.WorkspacePricing(context.Background(), 3, "current-forest-session")
	if err != nil || got.Scope != "public_catalog" || calls != 2 {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
	// No v2_newapi_account lookup is expected: inspecting prices must not connect an account.
	expectConfig(false)
	if _, err = s.WorkspacePricing(context.Background(), 3, "current-forest-session"); !errors.Is(err, ErrDisabled) || calls != 2 {
		t.Fatalf("disabled: error=%v calls=%d", err, calls)
	}
	if _, err = s.WorkspacePricing(context.Background(), 3, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty auth: %v", err)
	}
	mock.ExpectQuery(`SELECT email,banned FROM v2_user WHERE id=\$1`).WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"email", "banned"}).AddRow("person@example.test", 1))
	if _, err = s.WorkspacePricing(context.Background(), 3, "session"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("banned: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
