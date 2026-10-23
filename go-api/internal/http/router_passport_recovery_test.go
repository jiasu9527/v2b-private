package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestRouterPassportSendEmailVerifyRecoveryIntent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		wantPresent bool
		wantIntent  int
	}{
		{"json number", "application/json", `{"email":"legacy@example.com","isforget":1}`, true, 1},
		{"json string", "application/json", `{"email":"legacy@example.com","isforget":"1"}`, true, 1},
		{"form recovery", "application/x-www-form-urlencoded", "email=legacy%40example.com&isforget=1", true, 1},
		{"registration", "application/json", `{"email":"legacy@example.com","isforget":0}`, true, 0},
		{"omitted", "application/json", `{"email":"legacy@example.com"}`, false, 0},
		{"null", "application/json", `{"email":"legacy@example.com","isforget":null}`, false, 0},
		{"boolean", "application/json", `{"email":"legacy@example.com","isforget":true}`, false, 0},
		{"invalid", "application/json", `{"email":"legacy@example.com","isforget":"forgot"}`, false, 0},
		{"other intent", "application/json", `{"email":"legacy@example.com","isforget":2}`, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakePassportService{}
			router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/passport/comm/sendEmailVerify", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			got := service.lastSendVerify
			if got.Email != "legacy@example.com" || got.HasIsForget != tc.wantPresent || got.IsForget != tc.wantIntent {
				t.Fatalf("unexpected recovery intent: %#v", got)
			}
		})
	}
}
