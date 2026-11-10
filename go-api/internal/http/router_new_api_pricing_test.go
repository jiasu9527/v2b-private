package httpapi

import (
	"context"
	"net/http"
	"testing"

	"forest/go-api/internal/newapibridge"
)

var _ newAPIWorkspacePricingService = (*newapibridge.Service)(nil)

func (s *workspaceHTTPFixture) WorkspacePricing(_ context.Context, id int64, auth string) (newapibridge.WorkspacePricing, error) {
	s.actor, s.issuedAuth = id, auth
	s.calls++
	return newapibridge.WorkspacePricing{Scope: "public_catalog", Currency: "CNY", SourceURL: "https://ai.example.test/pricing"}, nil
}

func TestWorkspacePricingRouteFreshIdentityAndNoWriteInputs(t *testing.T) {
	s := &workspaceHTTPFixture{}
	r, sessions := workspaceTestRouter(s, &workspacePasswordVerifier{})
	w := newAPIHTTPCall(t, r, "GET", "/api/v1/user/new-api/pricing", "", "panel-auth")
	if w.Code != 200 || s.actor != 7007 || s.issuedAuth != "panel-auth" || sessions.freshCalls != 1 {
		t.Fatalf("status=%d actor=%d calls=%d response=%s", w.Code, s.actor, s.calls, w.Body.String())
	}
	before := s.calls
	for _, tc := range []struct{ method, path, body, token string }{
		{"GET", "pricing", "", ""},
		{"GET", "pricing?auth_data=panel-auth", "", ""},
		{"GET", "pricing?user_id=3", "", "panel-auth"},
		{"GET", "pricing?api_url=https://attacker.test", "", "panel-auth"},
		{"GET", "pricing", "{}", "panel-auth"},
		{"POST", "pricing", "{}", "panel-auth"},
		{"GET", "pricing", "", "nap_fake"},
	} {
		w = newAPIHTTPCall(t, r, tc.method, "/api/v1/user/new-api/"+tc.path, tc.body, tc.token)
		if w.Code < 400 || s.calls != before {
			t.Fatalf("accepted %s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	sessions.revoked = true
	w = newAPIHTTPCall(t, r, http.MethodGet, "/api/v1/user/new-api/pricing", "", "panel-auth")
	if w.Code < 400 || s.calls != before {
		t.Fatal("revoked session accepted", w.Code)
	}
}
