package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

type fakeMiniBridge struct {
	fakeAITelegram
	identity                             aiops.MiniIdentity
	authErr                              error
	init                                 string
	sendCalls, approveCalls, rejectCalls int
	checks, revokeAfter                  int
}

func (f *fakeMiniBridge) AuthenticateMini(_ context.Context, init string) (aiops.MiniAuthResult, error) {
	f.init = init
	return aiops.MiniAuthResult{Token: "tma_fixture", ExpiresAt: 123, User: f.identity}, f.authErr
}
func (f *fakeMiniBridge) AuthenticateMiniToken(_ context.Context, token string) (aiops.MiniIdentity, error) {
	f.checks++
	if f.revokeAfter > 0 && f.checks >= f.revokeAfter {
		return aiops.MiniIdentity{}, aiops.ErrForbidden
	}
	if token != "tma_fixture" {
		return aiops.MiniIdentity{}, aiops.ErrForbidden
	}
	return f.identity, f.authErr
}
func (f *fakeMiniBridge) MiniSendMessage(_ context.Context, _ string, req aiops.SendMessageRequest) (aiops.SendMessageResult, error) {
	f.sendCalls++
	return aiops.SendMessageResult{Session: aiops.Session{ID: 1, Source: "telegram"}}, nil
}
func (f *fakeMiniBridge) MiniApproveAction(_ context.Context, _ string, sid, aid int64) (aiops.Action, error) {
	f.approveCalls++
	return aiops.Action{ID: aid, SessionID: sid}, nil
}
func (f *fakeMiniBridge) MiniRejectAction(_ context.Context, _ string, sid, aid int64) (aiops.Action, error) {
	f.rejectCalls++
	return aiops.Action{ID: aid, SessionID: sid}, nil
}
func (f *fakeMiniBridge) RunMiniOperation(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func TestTelegramMiniHTTPAuthenticationAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, header string
		admin                            bool
		status, baseCalls, bridgeCalls   int
	}{
		{"auth", "POST", "auth", `{"init_data":"signed-fixture"}`, "", true, 200, 0, 0},
		{"auth fields", "POST", "auth", `{"init_data":"signed","user_id":1}`, "", true, 400, 0, 0},
		{"auth method", "GET", "auth", "", "", true, 405, 0, 0},
		{"identity", "GET", "identity", "", "Bearer tma_fixture", true, 200, 0, 0},
		{"URL token", "GET", "identity?auth_data=tma_fixture", "", "", true, 401, 0, 0},
		{"URL rejected even with header", "GET", "identity?auth_data=x", "", "Bearer tma_fixture", true, 401, 0, 0},
		{"plain token", "GET", "identity", "", "tma_fixture", true, 401, 0, 0},
		{"normal JWT", "GET", "identity", "", "Bearer fixture", true, 401, 0, 0},
		{"sessions", "GET", "sessions", "", "Bearer tma_fixture", false, 200, 1, 0},
		{"send", "POST", "chat/send", `{"model":"fixture","content":"task","request_id":"fixture-request"}`, "Bearer tma_fixture", false, 202, 0, 1},
		{"staff reasoning", "POST", "chat/send", `{"model":"fixture","content":"task","reasoning_effort":"high"}`, "Bearer tma_fixture", false, 403, 0, 0},
		{"approve", "POST", "action/approve", `{"session_id":1,"id":2}`, "Bearer tma_fixture", false, 202, 0, 1},
		{"reject", "POST", "action/reject", `{"session_id":1,"id":2}`, "Bearer tma_fixture", false, 200, 0, 1},
		{"invalid action", "POST", "action/approve", `{"session_id":0,"id":2}`, "Bearer tma_fixture", false, 400, 0, 0},
		{"staff cannot configure", "POST", "config/save", `{}`, "Bearer tma_fixture", false, 403, 0, 0},
		{"staff cannot delete", "POST", "session/delete", `{"id":1,"confirm":true}`, "Bearer tma_fixture", false, 403, 0, 0},
		{"no binding config", "POST", "telegram/config", `{}`, "Bearer tma_fixture", true, 404, 0, 0},
		{"no payment", "POST", "payment/save", `{}`, "Bearer tma_fixture", true, 404, 0, 0},
		{"no standalone create", "POST", "session/create", `{}`, "Bearer tma_fixture", true, 404, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMiniBridge{identity: aiops.MiniIdentity{ID: 9, IsStaff: 1}}
			if tc.admin {
				f.identity.IsAdmin = 1
				f.identity.IsStaff = 0
			}
			base := &fakeAIOps{}
			router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(base), WithAIOpsTelegram(f))
			req := httptest.NewRequest(tc.method, "/api/v1/ai-ops-mini/"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", tc.header)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || base.calls != tc.baseCalls || f.sendCalls+f.approveCalls+f.rejectCalls != tc.bridgeCalls {
				t.Fatalf("status=%d base=%d bridge=%d body=%s", w.Code, base.calls, f.sendCalls+f.approveCalls+f.rejectCalls, w.Body.String())
			}
			if !json.Valid(w.Body.Bytes()) || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("non-JSON or cacheable", w.Body.String())
			}
			if base.calls > 0 && base.actor != 9 {
				t.Fatal("actor not derived from server verified binding")
			}
		})
	}
}

func TestTelegramMiniRevokedIdentityAndNormalBackendIsolation(t *testing.T) {
	f := &fakeMiniBridge{identity: aiops.MiniIdentity{ID: 9, IsAdmin: 1}, authErr: aiops.ErrForbidden}
	base := &fakeAIOps{}
	router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(base), WithAIOpsTelegram(f), WithSessionService(strictAppleIDSession{}))
	for _, path := range []string{"/api/v1/ai-ops-mini/identity", "/api/v1/ai-ops-mini/sessions", "/api/v1/test/ai-ops/sessions"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer tma_fixture")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		want := 401
		if strings.Contains(path, "/test/") {
			want = 403
		}
		if w.Code != want || base.calls != 0 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}

func TestTelegramMiniShellIsSeparateAndSafeToEmbed(t *testing.T) {
	router := NewRouter(config.Config{AdminPath: "hidden-test-admin", PublicDir: t.TempDir()})
	for _, method := range []string{"GET", "HEAD", "POST"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/ai-ops-mini", nil))
		if method == "POST" {
			if w.Code != 405 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal(w.Code, w.Header())
		}
		if method == "HEAD" {
			if w.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
			continue
		}
		body := w.Body.String()
		for _, s := range []string{"telegram_mini_app: true", "https://telegram.org/js/telegram-web-app.js", "/assets/admin-new/admin.js", "viewport-fit=cover"} {
			if !strings.Contains(body, s) {
				t.Fatal("missing shell marker", s)
			}
		}
		if strings.Contains(body, "hidden-test-admin") || strings.Contains(body, "auth_data") {
			t.Fatal("shell leaked backend auth context")
		}
	}
}

func TestTelegramMiniStreamRevocationCheckedBeforeNextDraft(t *testing.T) {
	f := &fakeMiniBridge{identity: aiops.MiniIdentity{ID: 9, IsStaff: 1}, revokeAfter: 3}
	ai := &streamTestOps{}
	router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(ai), WithAIOpsTelegram(f))
	req := httptest.NewRequest("GET", "/api/v1/ai-ops-mini/session/stream?id=7", nil)
	req.Header.Set("Authorization", "Bearer tma_fixture")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"auth_invalid"`) || strings.Contains(w.Body.String(), "正在逐步输出") {
		t.Fatal("revoked Mini credential received a draft or missing invalidation", w.Code, w.Body.String())
	}
}
