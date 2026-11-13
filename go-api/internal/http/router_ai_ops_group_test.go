package httpapi

import (
	"strings"
	"testing"

	"forest/go-api/internal/session"
)

func TestAIOpsServerGroupEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity *session.Identity
		body     string
		status   int
		group    string
	}{
		{"staff saves group", &session.Identity{ID: 7, IsStaff: 1}, `{"id":31,"version":4,"group":"洛杉矶"}`, 200, "洛杉矶"},
		{"empty clears group", &session.Identity{ID: 7, IsStaff: 1}, `{"id":31,"version":4,"group":""}`, 200, ""},
		{"regular user denied", &session.Identity{ID: 7}, `{"id":31,"version":4,"group":"洛杉矶"}`, 403, ""},
		{"invalid id", &session.Identity{ID: 7, IsStaff: 1}, `{"id":0,"version":4,"group":"洛杉矶"}`, 400, ""},
		{"missing version", &session.Identity{ID: 7, IsStaff: 1}, `{"id":31,"group":"洛杉矶"}`, 400, ""},
		{"unexpected field", &session.Identity{ID: 7, IsStaff: 1}, `{"id":31,"version":4,"group":"洛杉矶","password":"secret"}`, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAIOps{}
			w := aiOpsTestRequest(t, fake, tc.identity, "POST", "server/group", tc.body)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				if fake.calls != 1 || fake.id != 31 || !strings.Contains(w.Body.String(), `"group":"`+tc.group+`"`) {
					t.Fatalf("missing group update: body=%s calls=%d", w.Body.String(), fake.calls)
				}
			} else if fake.calls != 0 {
				t.Fatal("invalid/unauthorized request reached service")
			}
		})
	}
}
