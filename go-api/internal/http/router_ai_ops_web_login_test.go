package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeWebLoginAIOps struct {
	fakeAIOps
	call    string
	actorID int64
	input   aiops.WebLoginRequest
	err     error
}

func (f *fakeWebLoginAIOps) GetWebLoginStatus(_ context.Context, actorID int64) (aiops.WebLoginStatus, error) {
	f.call, f.actorID = "status", actorID
	return aiops.WebLoginStatus{Enabled: true}, f.err
}

func (f *fakeWebLoginAIOps) RunWebLogin(_ context.Context, actorID int64, input aiops.WebLoginRequest) (aiops.WebLoginResult, error) {
	f.call, f.actorID, f.input = "run", actorID, input
	return aiops.WebLoginResult{Origin: "https://panel.example.net", PageTitle: "Overview", Summary: "Active", Model: "example"}, f.err
}

func TestAIOpsWebLoginPrivacyAndAccess(t *testing.T) {
	const prefix = "/api/v1/fixture-admin/ai-ops/web-login/"
	const input = `{"url":"https://panel.example.net","username":"viewer","password":"private-fixture-password","task":"status"}`
	admin := &session.Identity{ID: 7, IsAdmin: 1}
	staff := &session.Identity{ID: 8, IsStaff: 1}
	for _, tc := range []struct {
		name, path, method, auth, site, body string
		actor                                *session.Identity
		err                                  error
		status                               int
		call                                 string
	}{
		{"enabled status", prefix + "status", "GET", "Bearer fixture", "", "", admin, nil, 200, "status"},
		{"admin run", prefix + "run", "POST", "Bearer fixture", "same-origin", input, admin, nil, 200, "run"},
		{"missing authorization", prefix + "run", "POST", "", "", input, nil, nil, 403, ""},
		{"staff cannot use", prefix + "run", "POST", "Bearer fixture", "", input, staff, nil, 403, ""},
		{"cross origin", prefix + "run", "POST", "Bearer fixture", "cross-site", input, admin, nil, 403, ""},
		{"query token rejected", prefix + "status?auth_data=fixture", "GET", "", "", "", admin, nil, 403, ""},
		{"wrong method", prefix + "run", "GET", "Bearer fixture", "", "", admin, nil, 405, ""},
		{"reject unknown fields", prefix + "run", "POST", "Bearer fixture", "", `{"screenshot":true}`, admin, nil, 400, ""},
		{"mfa signaled", prefix + "run", "POST", "Bearer fixture", "", input, admin, aiops.ErrWebLoginVerification, 422, "run"},
		{"browser error body hidden", prefix + "run", "POST", "Bearer fixture", "", input, admin, fmt.Errorf("page returned private-fixture-password"), 500, "run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeWebLoginAIOps{err: tc.err}
			router := NewRouter(config.Config{AdminPath: "fixture-admin"}, WithSessionService(strictAppleIDSession{identity: tc.actor}), WithAIOps(f))
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || f.call != tc.call {
				t.Fatalf("status=%d, call=%q, wanted %d, %q; %s", w.Code, f.call, tc.status, tc.call, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-fixture-password") {
				t.Fatal("response leaked a password")
			}
			if tc.call == "run" && (f.actorID != 7 || f.input.Password != "private-fixture-password") {
				t.Fatal("private request wasn't restricted to the expected actor")
			}
			if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("web login results must not be cacheable")
			}
		})
	}
}

func TestAIOpsWebLoginDoesNotExtendTelegram(t *testing.T) {
	router := NewRouter(config.Config{AdminPath: "fixture-admin"}, WithAIOps(&fakeWebLoginAIOps{}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ai-ops-mini/web-login/run", strings.NewReader("{}")))
	if w.Code == 200 {
		t.Fatal("standard admin browser login unexpectedly enabled on Mini App")
	}
}
