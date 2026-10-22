package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"forest/go-api/internal/usermigration"
)

const migrationTestPreviewID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const migrationTestSecret = "migration-secret-must-not-appear-in-response"

type fakeUserMigrationService struct {
	calls       []string
	actorID     int64
	source      usermigration.SourceConfig
	preview     usermigration.PreviewRequest
	apply       usermigration.ApplyRequest
	receiptID   string
	lastCtx     context.Context
	err         error
	inspection  usermigration.Inspection
	result      usermigration.PreviewResult
	receipt     usermigration.Receipt
	mailRequest usermigration.MailRequest
	mailInfo    usermigration.MailInfo
	mailTask    usermigration.MailTask
}

func (f *fakeUserMigrationService) record(ctx context.Context, actorID int64, action string) {
	f.calls = append(f.calls, action)
	f.actorID, f.lastCtx = actorID, ctx
}

func (f *fakeUserMigrationService) Inspect(ctx context.Context, actorID int64, req usermigration.SourceConfig) (usermigration.Inspection, error) {
	f.record(ctx, actorID, "inspect")
	f.source = req
	return f.inspection, f.err
}

func (f *fakeUserMigrationService) Preview(ctx context.Context, actorID int64, req usermigration.PreviewRequest) (usermigration.PreviewResult, error) {
	f.record(ctx, actorID, "preview")
	f.preview = req
	return f.result, f.err
}

func (f *fakeUserMigrationService) Apply(ctx context.Context, actorID int64, req usermigration.ApplyRequest) (usermigration.Receipt, error) {
	f.record(ctx, actorID, "apply")
	f.apply = req
	return f.receipt, f.err
}

func (f *fakeUserMigrationService) Receipt(ctx context.Context, actorID int64, id string) (usermigration.Receipt, error) {
	f.record(ctx, actorID, "receipt")
	f.receiptID = id
	return f.receipt, f.err
}

func (f *fakeUserMigrationService) MailInfo(ctx context.Context, actorID int64, id string) (usermigration.MailInfo, error) {
	f.record(ctx, actorID, "mail/status")
	f.receiptID = id
	return f.mailInfo, f.err
}

func (f *fakeUserMigrationService) SendMail(ctx context.Context, actorID int64, req usermigration.MailRequest) (usermigration.MailTask, error) {
	f.record(ctx, actorID, "mail/send")
	f.mailRequest = req
	return f.mailTask, f.err
}

type userMigrationRoute struct {
	action, method, query, body string
}

var userMigrationRoutes = []userMigrationRoute{
	{action: "inspect", method: http.MethodPost, body: `{"host":"source.example.test","port":3306,"database":"xboard","username":"reader","password":"` + migrationTestSecret + `","tls":true}`},
	{action: "preview", method: http.MethodPost, body: `{"source":{"host":"source.example.test","port":3306,"database":"xboard","username":"reader","password":"` + migrationTestSecret + `","tls":true},"plan_mapping":{"1":7,"2":9},"locking_balance_policy":"discard","confirm_locking_balance":true}`},
	{action: "apply", method: http.MethodPost, body: `{"preview_id":"` + migrationTestPreviewID + `","confirm":true}`},
	{action: "receipt", method: http.MethodGet, query: "?preview_id=" + migrationTestPreviewID},
	{action: "mail/status", method: http.MethodGet, query: "?preview_id=" + migrationTestPreviewID},
	{action: "mail/send", method: http.MethodPost, body: `{"preview_id":"` + migrationTestPreviewID + `","request_id":"` + migrationTestPreviewID + `","subject":"迁移通知","content":"请登录新站获取订阅。","confirm":true}`},
}

func newUserMigrationRouter(sessions session.Service, service userMigrationService, opts ...Option) http.Handler {
	options := []Option{WithSessionService(sessions), func(state *routerState) { state.userMigration = service }}
	return NewRouter(config.Config{AdminPath: "localadmin"}, append(options, opts...)...)
}

func userMigrationAdminSession() *freshAppleOperatorSession {
	return &freshAppleOperatorSession{
		strictAppleIDSession: strictAppleIDSession{identity: &session.Identity{ID: 999, IsAdmin: 1}},
		current:              &session.Identity{ID: 41, IsAdmin: 1},
	}
}

func requestUserMigration(t *testing.T, router http.Handler, route userMigrationRoute, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(route.method, "/api/v1/localadmin/migration/xboard/"+route.action+route.query, strings.NewReader(route.body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertUserMigrationNoStore(t, rec)
	return rec
}

func assertUserMigrationNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertAppleIDNoStore(t, rec)
	if rec.Header().Get("Expires") != "0" || rec.Header().Get("Vary") != "Authorization" {
		t.Fatalf("migration response may be cached: %v", rec.Header())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("migration response is not JSON: %v", rec.Header())
	}
}

func TestUserMigrationUsesFreshAdministratorAndSessionOwner(t *testing.T) {
	for _, route := range userMigrationRoutes {
		t.Run(route.action, func(t *testing.T) {
			sessions := userMigrationAdminSession()
			service := &fakeUserMigrationService{
				inspection: usermigration.Inspection{TotalUsers: 23},
				result:     usermigration.PreviewResult{PreviewID: migrationTestPreviewID, CanApply: true},
				receipt:    usermigration.Receipt{PreviewID: migrationTestPreviewID, Status: "completed", Imported: 23},
			}
			started := time.Now()
			rec := requestUserMigration(t, newUserMigrationRouter(sessions, service), route, "  admin-header-token  ")
			if rec.Code != http.StatusOK || len(service.calls) != 1 || service.calls[0] != route.action {
				t.Fatalf("status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
			}
			if sessions.freshCalls != 1 || sessions.cachedCalls != 0 || !sessions.lastRequireAdmin || sessions.lastToken != "admin-header-token" {
				t.Fatalf("not a fresh administrator lookup: fresh=%d cached=%d requireAdmin=%v token=%q", sessions.freshCalls, sessions.cachedCalls, sessions.lastRequireAdmin, sessions.lastToken)
			}
			if service.actorID != 41 {
				t.Fatalf("owner must come from fresh session, not cached session: %d", service.actorID)
			}
			deadline, ok := service.lastCtx.Deadline()
			if !ok || deadline.Before(started) || deadline.After(time.Now().Add(120*time.Second)) {
				t.Fatalf("service context has missing or excessive deadline: %v %v", deadline, ok)
			}
			if !errors.Is(service.lastCtx.Err(), context.Canceled) {
				t.Fatalf("operation context not cleaned up: %v", service.lastCtx.Err())
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope["data"] == nil {
				t.Fatalf("missing JSON data envelope: %s err=%v", rec.Body.String(), err)
			}
			if strings.Contains(rec.Body.String(), migrationTestSecret) {
				t.Fatal("source password was echoed in success response")
			}
			switch route.action {
			case "inspect":
				if service.source.Host != "source.example.test" || service.source.Port != 3306 || service.source.Database != "xboard" || service.source.Username != "reader" || service.source.Password != migrationTestSecret || !service.source.TLS {
					t.Fatal("source configuration was not decoded intact")
				}
			case "preview":
				if service.preview.Source.Password != migrationTestSecret || service.preview.PlanMapping["1"] != 7 || service.preview.PlanMapping["2"] != 9 || service.preview.LockingBalancePolicy != "discard" || !service.preview.ConfirmLockingBalance {
					t.Fatal("preview mapping or explicit locked-balance policy was lost")
				}
			case "apply":
				if service.apply.PreviewID != migrationTestPreviewID || !service.apply.Confirm {
					t.Fatalf("apply confirmation lost: %+v", service.apply)
				}
			case "receipt", "mail/status":
				if service.receiptID != migrationTestPreviewID {
					t.Fatalf("receipt ID=%q", service.receiptID)
				}
			}
		})
	}
}

func TestUserMigrationPreviewRegenerationFlagsAreIndependentAndDefaultOff(t *testing.T) {
	for _, tc := range []struct {
		name, fields  string
		tokens, uuids bool
	}{
		{"omitted", "", false, false},
		{"explicit false", `,"regenerate_tokens":false,"regenerate_uuids":false`, false, false},
		{"tokens only", `,"regenerate_tokens":true`, true, false},
		{"uuids only", `,"regenerate_uuids":true`, false, true},
		{"both", `,"regenerate_tokens":true,"regenerate_uuids":true`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeUserMigrationService{result: usermigration.PreviewResult{
				PreviewID: migrationTestPreviewID, CanApply: true,
				RegenerateTokens: tc.tokens, RegenerateUUIDs: tc.uuids,
			}}
			route := userMigrationRoutes[1]
			route.body = strings.TrimSuffix(route.body, "}") + tc.fields + "}"
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "header-token")
			if rec.Code != http.StatusOK || len(service.calls) != 1 || service.preview.RegenerateTokens != tc.tokens || service.preview.RegenerateUUIDs != tc.uuids {
				t.Fatalf("preview lost regeneration options: status=%d calls=%v tokens=%v uuids=%v", rec.Code, service.calls, service.preview.RegenerateTokens, service.preview.RegenerateUUIDs)
			}
			var response struct {
				Data struct {
					RegenerateTokens *bool `json:"regenerate_tokens"`
					RegenerateUUIDs  *bool `json:"regenerate_uuids"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Data.RegenerateTokens == nil || response.Data.RegenerateUUIDs == nil || *response.Data.RegenerateTokens != tc.tokens || *response.Data.RegenerateUUIDs != tc.uuids {
				t.Fatalf("preview response lost regeneration options: %s err=%v", rec.Body.String(), err)
			}
		})
	}
}

func TestUserMigrationPreviewRejectsInvalidRegenerationFlagTypes(t *testing.T) {
	for _, field := range []string{"regenerate_tokens", "regenerate_uuids"} {
		for _, value := range []string{`"true"`, `"` + migrationTestSecret + `"`, `0`, `1`, `{}`, `[]`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				service := &fakeUserMigrationService{}
				route := userMigrationRoutes[1]
				route.body = strings.TrimSuffix(route.body, "}") + `,"` + field + `":` + value + `}`
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "header-token")
				if rec.Code != http.StatusBadRequest || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
					t.Fatalf("nonboolean flag accepted or reflected: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
				}
			})
		}
	}
}

func TestUserMigrationApplyCannotOverridePreviewRegenerationSettings(t *testing.T) {
	for _, field := range []string{"regenerate_tokens", "regenerate_uuids"} {
		for _, value := range []string{"true", "false"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				service := &fakeUserMigrationService{}
				route := userMigrationRoutes[2]
				route.body = strings.TrimSuffix(route.body, "}") + `,"` + field + `":` + value + `}`
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "header-token")
				if rec.Code != http.StatusBadRequest || len(service.calls) != 0 {
					t.Fatalf("Apply may override preview options: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
				}
			})
		}
	}
}

func TestUserMigrationReceiptResponsesKeepRegenerationFlags(t *testing.T) {
	for _, route := range userMigrationRoutes[2:4] {
		for _, options := range []struct{ tokens, uuids bool }{{}, {tokens: true}, {uuids: true}, {tokens: true, uuids: true}} {
			t.Run(fmt.Sprintf("%s/tokens=%v/uuids=%v", route.action, options.tokens, options.uuids), func(t *testing.T) {
				service := &fakeUserMigrationService{receipt: usermigration.Receipt{
					PreviewID: migrationTestPreviewID, Status: "completed", Imported: 2,
					RegenerateTokens: options.tokens, RegenerateUUIDs: options.uuids,
				}}
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "header-token")
				var response struct {
					Data struct {
						RegenerateTokens *bool `json:"regenerate_tokens"`
						RegenerateUUIDs  *bool `json:"regenerate_uuids"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Data.RegenerateTokens == nil || response.Data.RegenerateUUIDs == nil || *response.Data.RegenerateTokens != options.tokens || *response.Data.RegenerateUUIDs != options.uuids {
					t.Fatalf("receipt response lost regeneration options: %s err=%v", rec.Body.String(), err)
				}
			})
		}
	}
}

func TestUserMigrationRejectsStaffOrdinaryRevokedAndInvalidIdentities(t *testing.T) {
	for _, state := range []struct {
		name string
		id   *session.Identity
		err  error
	}{
		{name: "ordinary", id: &session.Identity{ID: 41}},
		{name: "staff", id: &session.Identity{ID: 41, IsStaff: 1}},
		{name: "revoked", err: session.ErrUnauthorized},
		{name: "missing identity"},
		{name: "zero user ID", id: &session.Identity{IsAdmin: 1}},
		{name: "negative user ID", id: &session.Identity{ID: -1, IsAdmin: 1}},
		{name: "invalid admin flag", id: &session.Identity{ID: 41, IsAdmin: 2}},
	} {
		for _, route := range userMigrationRoutes {
			t.Run(state.name+"/"+route.action, func(t *testing.T) {
				sessions := userMigrationAdminSession()
				sessions.current, sessions.freshErr = state.id, state.err
				service := &fakeUserMigrationService{}
				rec := requestUserMigration(t, newUserMigrationRouter(sessions, service), route, "admin-header-token")
				if rec.Code != http.StatusForbidden || len(service.calls) != 0 || sessions.freshCalls != 1 || sessions.cachedCalls != 0 {
					t.Fatalf("invalid privilege accepted: status=%d calls=%v fresh=%d cached=%d body=%s", rec.Code, service.calls, sessions.freshCalls, sessions.cachedCalls, rec.Body.String())
				}
			})
		}
	}
}

type migrationReadSpy struct{ reads, closes int }

func (s *migrationReadSpy) Read([]byte) (int, error) { s.reads++; return 0, io.EOF }
func (s *migrationReadSpy) Close() error             { s.closes++; return nil }

func TestUserMigrationRequiresHeaderAuthenticationBeforeReadingBody(t *testing.T) {
	for _, tt := range []struct{ name, header, query, cookie string }{
		{name: "anonymous"},
		{name: "query token", query: "?auth_data=" + migrationTestSecret},
		{name: "authorization query", query: "?Authorization=" + migrationTestSecret},
		{name: "cookie token", cookie: "auth_data=" + migrationTestSecret},
		{name: "oversized token", header: strings.Repeat("a", 8193)},
		{name: "whitespace token", header: "   "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessions := userMigrationAdminSession()
			service := &fakeUserMigrationService{}
			body := &migrationReadSpy{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/migration/xboard/inspect"+tt.query, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", tt.header)
			req.Header.Set("Cookie", tt.cookie)
			rec := httptest.NewRecorder()
			newUserMigrationRouter(sessions, service).ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || body.reads != 0 || len(service.calls) != 0 || sessions.freshCalls != 0 || sessions.cachedCalls != 0 {
				t.Fatalf("unauthorized body parsed: status=%d reads=%d calls=%v fresh=%d cached=%d", rec.Code, body.reads, service.calls, sessions.freshCalls, sessions.cachedCalls)
			}
			if strings.Contains(rec.Body.String(), migrationTestSecret) {
				t.Fatal("URL or cookie token was reflected")
			}
			assertUserMigrationNoStore(t, rec)
		})
	}
	service := &fakeUserMigrationService{}
	route := userMigrationRoutes[0]
	route.body = `{"auth_data":"` + migrationTestSecret + `"}`
	rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "")
	if rec.Code != http.StatusForbidden || len(service.calls) != 0 {
		t.Fatalf("JSON auth must not be accepted: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUserMigrationRejectsURLCredentialsAndOwnerParameters(t *testing.T) {
	for _, route := range userMigrationRoutes {
		for _, key := range []string{"password", "username", "host", "source", "auth_data", "actor_id", "admin_id", "owner_id"} {
			t.Run(route.action+"/"+key, func(t *testing.T) {
				service := &fakeUserMigrationService{}
				candidate := route
				if candidate.query == "" {
					candidate.query = "?" + key + "=" + migrationTestSecret
				} else {
					candidate.query += "&" + key + "=" + migrationTestSecret
				}
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), candidate, "header-token")
				if rec.Code != http.StatusBadRequest || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
					t.Fatalf("URL parameters accepted or leaked: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
				}
			})
		}
	}
}

func TestUserMigrationRejectsEveryNonemptyQueryOnPostRoutes(t *testing.T) {
	// URL.Query silently drops malformed escapes and semicolon-separated pairs.
	// Even when that leaves an empty map, credentials must not be accepted in a
	// URL alongside an otherwise valid, authenticated JSON request.
	for _, route := range userMigrationRoutes[:3] {
		for _, query := range []string{
			"?&",
			"?&&",
			"?;",
			"?%zz=" + migrationTestSecret,
			"?password=%zz",
			"?password=" + migrationTestSecret + ";",
			"?password=" + migrationTestSecret + ";username=reader",
			"?preview_id=" + migrationTestPreviewID,
		} {
			t.Run(route.action+"/"+query, func(t *testing.T) {
				service := &fakeUserMigrationService{}
				candidate := route
				candidate.query = query
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), candidate, "header-token")
				if rec.Code != http.StatusBadRequest || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
					t.Fatalf("nonempty raw query was accepted or reflected: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
				}
			})
		}
	}
}

func TestUserMigrationReceiptRejectsMalformedAmbiguousOrAdditionalQueryFields(t *testing.T) {
	for _, query := range []string{
		"?preview_id=" + migrationTestPreviewID + "&password=%zz",
		"?preview_id=" + migrationTestPreviewID + "&%zz=" + migrationTestSecret,
		"?preview_id=" + migrationTestPreviewID + "&password=" + migrationTestSecret + ";username=reader",
		"?preview_id=" + migrationTestPreviewID + "&preview_id=" + migrationTestPreviewID,
		"?preview_id=" + migrationTestPreviewID + "&preview_id=another-valid-preview-id",
		"?preview_id=" + migrationTestPreviewID + "&preview_id=",
		"?preview_id=&preview_id=" + migrationTestPreviewID,
		"?preview_id=" + migrationTestPreviewID + "&preview%5fid=" + migrationTestPreviewID,
		"?preview_id=" + migrationTestPreviewID + "&password=",
		"?preview_id=" + migrationTestPreviewID + "&owner_id=999",
		"?preview_id=" + migrationTestPreviewID + "&unexpected",
		"?preview_id=" + migrationTestPreviewID + "&=" + migrationTestSecret,
	} {
		t.Run(query, func(t *testing.T) {
			service := &fakeUserMigrationService{}
			route := userMigrationRoute{action: "receipt", method: http.MethodGet, query: query}
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "header-token")
			if rec.Code != http.StatusBadRequest || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
				t.Fatalf("ambiguous or malformed receipt query was accepted or reflected: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestUserMigrationMethodsAndUnknownRoutes(t *testing.T) {
	for _, route := range userMigrationRoutes {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead, http.MethodPatch} {
			if method == route.method {
				continue
			}
			t.Run(route.action+"/"+method, func(t *testing.T) {
				service := &fakeUserMigrationService{}
				candidate := route
				candidate.method = method
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), candidate, "header-token")
				if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != route.method || len(service.calls) != 0 {
					t.Fatalf("method accepted: status=%d allow=%q calls=%v", rec.Code, rec.Header().Get("Allow"), service.calls)
				}
			})
		}
	}
	for _, action := range []string{"", "export", "inspect/extra", "apply/extra"} {
		t.Run("unknown/"+action, func(t *testing.T) {
			service := &fakeUserMigrationService{}
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), userMigrationRoute{action: action, method: http.MethodPost, body: `{}`}, "header-token")
			if rec.Code != http.StatusNotFound || len(service.calls) != 0 {
				t.Fatalf("unknown action handled: status=%d calls=%v", rec.Code, service.calls)
			}
		})
	}
}

func TestUserMigrationStrictBoundedJSONNeverEchoesParserDetails(t *testing.T) {
	for _, route := range userMigrationRoutes[:3] {
		for _, tt := range []struct{ name, body string }{
			{name: "unknown field", body: `{"` + migrationTestSecret + `":true}`},
			{name: "forged actor", body: `{"actor_id":999}`},
			{name: "truncated", body: `{"password":"` + migrationTestSecret},
			{name: "array", body: `["` + migrationTestSecret + `"]`},
			{name: "scalar", body: `"` + migrationTestSecret + `"`},
			{name: "trailing object", body: route.body + `{}`},
			{name: "trailing null", body: route.body + `null`},
			{name: "trailing text", body: route.body + migrationTestSecret},
			{name: "empty", body: ""},
			{name: "body cap", body: route.body + strings.Repeat(" ", 64<<10)},
		} {
			t.Run(route.action+"/"+tt.name, func(t *testing.T) {
				service := &fakeUserMigrationService{}
				candidate := route
				candidate.body = tt.body
				rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), candidate, "header-token")
				if rec.Code != http.StatusBadRequest || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
					t.Fatalf("unsafe decoder result: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
				}
			})
		}
	}
}

func TestUserMigrationRequiresJSONContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=test", "application/json; broken"} {
		t.Run(contentType, func(t *testing.T) {
			service := &fakeUserMigrationService{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/migration/xboard/inspect", strings.NewReader(userMigrationRoutes[0].body))
			req.Header.Set("Authorization", "header-token")
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			newUserMigrationRouter(userMigrationAdminSession(), service).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnsupportedMediaType || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
				t.Fatalf("unexpected content-type result: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
			}
			assertUserMigrationNoStore(t, rec)
		})
	}
	service := &fakeUserMigrationService{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/migration/xboard/inspect", strings.NewReader(userMigrationRoutes[0].body))
	req.Header.Set("Authorization", "header-token")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	newUserMigrationRouter(userMigrationAdminSession(), service).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(service.calls) != 1 {
		t.Fatalf("JSON charset should be supported: %d %s", rec.Code, rec.Body.String())
	}
	assertUserMigrationNoStore(t, rec)
}

func TestUserMigrationApplyRequiresExplicitConfirmationAndValidPreviewID(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"preview_id":"` + migrationTestPreviewID + `"}`,
		`{"preview_id":"` + migrationTestPreviewID + `","confirm":false}`,
		`{"preview_id":"` + migrationTestPreviewID + `","confirm":"true"}`,
		`{"preview_id":"short","confirm":true}`,
		`{"preview_id":"../../not-an-id.txt","confirm":true}`,
		`{"preview_id":"contains%2fencodedslash","confirm":true}`,
		`{"preview_id":"` + strings.Repeat("x", 129) + `","confirm":true}`,
		`{"preview_id":"` + migrationTestPreviewID + `","confirm":true,"actor_id":999}`,
	} {
		service := &fakeUserMigrationService{}
		rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), userMigrationRoute{action: "apply", method: http.MethodPost, body: body}, "header-token")
		if rec.Code != http.StatusBadRequest || len(service.calls) != 0 {
			t.Fatalf("invalid apply reached service: status=%d calls=%v body=%s request=%s", rec.Code, service.calls, rec.Body.String(), body)
		}
	}
}

func TestUserMigrationReceiptValidatesIDAndKeepsOwnerScope(t *testing.T) {
	for _, query := range []string{"", "?preview_id=short", "?preview_id=..%2F..%2Fsecret-file", "?preview_id=" + strings.Repeat("x", 129)} {
		service := &fakeUserMigrationService{}
		rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), userMigrationRoute{action: "receipt", method: http.MethodGet, query: query}, "header-token")
		if rec.Code != http.StatusBadRequest || len(service.calls) != 0 {
			t.Fatalf("invalid receipt ID accepted: query=%q status=%d calls=%v", query, rec.Code, service.calls)
		}
	}
	for _, id := range []string{strings.Repeat("0", 64), strings.Repeat("f", 64), migrationTestPreviewID} {
		service := &fakeUserMigrationService{receipt: usermigration.Receipt{PreviewID: id, Status: "completed", Imported: 5}}
		rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), userMigrationRoute{action: "receipt", method: http.MethodGet, query: "?preview_id=" + id}, "header-token")
		if rec.Code != http.StatusOK || service.actorID != 41 || service.receiptID != id {
			t.Fatalf("receipt query lost owner/ID: status=%d actor=%d id=%q", rec.Code, service.actorID, service.receiptID)
		}
		var response struct {
			Data usermigration.Receipt `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Data.Imported != 5 || response.Data.PreviewID != id || response.Data.Status != "completed" {
			t.Fatalf("receipt not returned: %s err=%v", rec.Body.String(), err)
		}
	}
}

func TestUserMigrationPreviewIDContractMatchesStored64LowercaseHex(t *testing.T) {
	for _, tt := range []struct {
		name, id string
		valid    bool
	}{
		{"empty", "", false},
		{"old minimum", strings.Repeat("a", 16), false},
		{"one short", strings.Repeat("a", 63), false},
		{"one long", strings.Repeat("a", 65), false},
		{"old maximum", strings.Repeat("a", 128), false},
		{"all zero", strings.Repeat("0", 64), true},
		{"all lower hex", strings.Repeat("f", 64), true},
		{"mixed lower hex", migrationTestPreviewID, true},
		{"upper hex", strings.Repeat("A", 64), false},
		{"mixed uppercase", "A" + migrationTestPreviewID[1:], false},
		{"outside hex", strings.Repeat("g", 64), false},
		{"hyphen", "-" + migrationTestPreviewID[1:], false},
		{"underscore", "_" + migrationTestPreviewID[1:], false},
		{"space", " " + migrationTestPreviewID[1:], false},
		{"newline", "\n" + migrationTestPreviewID[1:], false},
		{"multibyte", "é" + migrationTestPreviewID[2:], false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := validMigrationPreviewID(tt.id); got != tt.valid {
				t.Fatalf("validMigrationPreviewID(%q)=%v want=%v", tt.id, got, tt.valid)
			}
			service := &fakeUserMigrationService{}
			body, err := json.Marshal(usermigration.ApplyRequest{PreviewID: tt.id, Confirm: true})
			if err != nil {
				t.Fatal(err)
			}
			route := userMigrationRoute{action: "apply", method: http.MethodPost, body: string(body)}
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), route, "header-token")
			if tt.valid {
				if rec.Code != http.StatusOK || len(service.calls) != 1 || service.apply.PreviewID != tt.id {
					t.Fatalf("valid preview ID rejected: status=%d calls=%v", rec.Code, service.calls)
				}
			} else if rec.Code != http.StatusBadRequest || len(service.calls) != 0 {
				t.Fatalf("invalid preview ID reached service: status=%d calls=%v", rec.Code, service.calls)
			}
		})
	}
}

func TestUserMigrationErrorsHaveStableSafeCodes(t *testing.T) {
	for _, tt := range []struct {
		name, code string
		err        error
		status     int
	}{
		{"invalid", "invalid_parameter", usermigration.ErrInvalidParameter, 400},
		{"unavailable", "unavailable", usermigration.ErrUnavailable, 503},
		{"source connection", "source_connection", usermigration.ErrSourceConnection, 400},
		{"source read", "source_schema", usermigration.ErrSourceRead, 400},
		{"source schema", "source_schema", usermigration.ErrSourceSchema, 400},
		{"source limit", "source_limit", usermigration.ErrSourceLimit, 400},
		{"busy", "busy", usermigration.ErrBusy, 409},
		{"expired", "preview_expired", usermigration.ErrPreviewExpired, 409},
		{"blocked", "preview_blocked", usermigration.ErrPreviewBlocked, 409},
		{"stale", "preview_stale", usermigration.ErrPreviewStale, 409},
		{"receipt missing", "receipt_not_found", usermigration.ErrReceiptNotFound, 404},
		{"commit unknown", "commit_unknown", usermigration.ErrCommitUnknown, 409},
		{"target write", "target_write", usermigration.ErrTargetWrite, 500},
		{"target read", "target_read", usermigration.ErrTargetRead, 503},
		{"deadline", "timeout", context.DeadlineExceeded, 504},
		{"canceled", "timeout", context.Canceled, 504},
		{"unknown", "migration_failed", errors.New("driver failure"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeUserMigrationService{err: fmt.Errorf("mysql reader:%s@tcp(source.example.test:3306): %w", migrationTestSecret, tt.err)}
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service), userMigrationRoutes[0], "header-token")
			var response struct{ Code, Message string }
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != tt.status || response.Code != tt.code || response.Message == "" || len(service.calls) != 1 {
				t.Fatalf("incorrect safe error mapping: status=%d code=%q calls=%v body=%s err=%v", rec.Code, response.Code, service.calls, rec.Body.String(), err)
			}
			for _, secret := range []string{migrationTestSecret, "source.example.test", "mysql reader:"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatal("source connection or internal error leaked")
				}
			}
		})
	}
}

func TestUserMigrationAuthenticationErrorsDoNotLeakDetails(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"unauthorized", fmt.Errorf("%s: %w", migrationTestSecret, session.ErrUnauthorized), http.StatusForbidden},
		{"unavailable", fmt.Errorf("%s: %w", migrationTestSecret, session.ErrUnavailable), http.StatusServiceUnavailable},
		{"unexpected", fmt.Errorf("database DSN contains %s", migrationTestSecret), http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessions := userMigrationAdminSession()
			sessions.freshErr = tt.err
			service := &fakeUserMigrationService{}
			rec := requestUserMigration(t, newUserMigrationRouter(sessions, service), userMigrationRoutes[0], "header-token")
			if rec.Code != tt.status || len(service.calls) != 0 || strings.Contains(rec.Body.String(), migrationTestSecret) {
				t.Fatalf("authentication error was not safely handled: status=%d calls=%v body=%s", rec.Code, service.calls, rec.Body.String())
			}
		})
	}
}

func TestUserMigrationUnavailableAndTypedNilOption(t *testing.T) {
	var missing *usermigration.DBService
	for _, route := range userMigrationRoutes {
		t.Run("missing service/"+route.action, func(t *testing.T) {
			rec := requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), nil, WithUserMigrationService(missing)), route, "header-token")
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("typed nil must not panic or enable service: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	service := &fakeUserMigrationService{}
	rec := requestUserMigration(t, newUserMigrationRouter(nil, service), userMigrationRoutes[0], "header-token")
	if rec.Code != http.StatusServiceUnavailable || len(service.calls) != 0 {
		t.Fatalf("missing sessions must fail closed: %d calls=%v", rec.Code, service.calls)
	}
	// A missing optional concrete service must not replace an already supplied
	// implementation with a non-nil interface containing a nil pointer.
	rec = requestUserMigration(t, newUserMigrationRouter(userMigrationAdminSession(), service, WithUserMigrationService(missing)), userMigrationRoutes[0], "header-token")
	if rec.Code != http.StatusOK || len(service.calls) != 1 {
		t.Fatalf("typed nil option replaced working implementation: %d calls=%v", rec.Code, service.calls)
	}
}
