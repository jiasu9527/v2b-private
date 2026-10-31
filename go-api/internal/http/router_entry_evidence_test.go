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

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/session"
)

type evidenceHTTPStore struct {
	calls  int
	filter entryevidence.Filter
}

func (s *evidenceHTTPStore) List(_ context.Context, f entryevidence.Filter) (entryevidence.Page, error) {
	s.calls++
	s.filter = f
	return entryevidence.Page{Items: []entryevidence.Record{}, Limit: f.Limit, Offset: f.Offset}, nil
}
func (s *evidenceHTTPStore) Status(context.Context) (entryevidence.Status, error) {
	s.calls++
	return entryevidence.Status{KeepDays: 30}, nil
}
func (s *evidenceHTTPStore) SaveSettings(_ context.Context, days int) (entryevidence.Settings, error) {
	s.calls++
	return entryevidence.Settings{KeepDays: days}, nil
}

func TestEntryEvidenceHTTPPermissionsAndInput(t *testing.T) {
	for _, tc := range []struct {
		name, method, action, body string
		actor                      *session.Identity
		want                       int
	}{
		{"ordinary denied", "GET", "status", "", &session.Identity{ID: 1}, 403},
		{"staff read", "GET", "status", "", &session.Identity{ID: 1, IsStaff: 1}, 200},
		{"admin read", "GET", "status", "", &session.Identity{ID: 1, IsAdmin: 1}, 200},
		{"staff cannot change retention", "POST", "settings", `{"keep_days":7}`, &session.Identity{ID: 1, IsStaff: 1}, 403},
		{"staff cannot delete evidence", "POST", "case-delete", `{"case_id":1}`, &session.Identity{ID: 1, IsStaff: 1}, 403},
		{"admin settings", "POST", "settings", `{"keep_days":7}`, &session.Identity{ID: 1, IsAdmin: 1}, 200},
		{"write via get denied", "GET", "settings", "", &session.Identity{ID: 1, IsAdmin: 1}, 405},
		{"unknown body field", "POST", "settings", `{"keep_days":7,"actor_id":99}`, &session.Identity{ID: 1, IsAdmin: 1}, 400},
		{"wrong content type", "POST", "settings", "keep_days=7", &session.Identity{ID: 1, IsAdmin: 1}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://fixture/?auth_data=fixture", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			store := &evidenceHTTPStore{}
			handleEntryEvidence(w, r, strictAppleIDSession{identity: tc.actor}, nil, store, tc.action)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.want != 200 && store.calls != 0 {
				t.Fatal("denied request reached store")
			}
			if w.Header().Get("Cache-Control") == "" {
				t.Fatal("account evidence response cache not disabled")
			}
		})
	}
	q := url.Values{"kind": {"split_group"}, "target_id": {"9"}, "account": {"71"}, "host": {"192.0.2.1"}, "port": {"54101"}, "ua": {"Forest"}, "from": {"100"}, "to": {"200"}, "current": {"2"}, "page_size": {"50"}}
	f, err := parseEntryDeliveryFilter(q)
	if err != nil || f.GroupID != 9 || f.UserID != 71 || f.Email != "" || f.Limit != 50 || f.Offset != 50 || f.From != 100 || f.To != 200 {
		t.Fatal(f, err)
	}
	for _, key := range []string{"from", "to", "user_id", "policy_id", "group_id", "current", "page_size"} {
		bad := url.Values{}
		bad.Set(key, "-1")
		if _, err := parseEntryDeliveryFilter(bad); err == nil {
			t.Fatal("negative value accepted", key)
		}
	}
	if _, err := parseEntryDeliveryFilter(url.Values{"current": {"1", "2"}}); err == nil {
		t.Fatal("ambiguous duplicate page accepted")
	}
	if _, err := parseEntryDeliveryFilter(url.Values{"current": {"102"}, "page_size": {"100"}}); err == nil {
		t.Fatal("out-of-range delivery offset accepted")
	}
}

func TestEntryEvidenceTypedNilAndLargeSelection(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://fixture/?auth_data=fixture", nil)
	var store *entryevidence.Store
	handleEntryEvidence(w, r, strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}, nil, store, "status")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	ids := make([]int64, 10000)
	for i := range ids {
		ids[i] = int64(1000000 + i)
	}
	body, _ := json.Marshal(admin.CreateEntryEvidenceCaseRequest{SelectedUserIDs: ids})
	if len(body) <= 32<<10 {
		t.Fatal("fixture must exercise old body limit")
	}
	r = httptest.NewRequest("POST", "http://fixture/", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	var req admin.CreateEntryEvidenceCaseRequest
	if !decodeEntryEvidenceJSON(w, r, &req) || len(req.SelectedUserIDs) != 10000 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestEntryEvidenceOperationsAreDiscoverableAndBounded(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	for _, name := range []string{"status", "deliveries", "candidates", "context", "freeze", "cases", "case", "split_preview", "split_execute", "case_update", "case_delete", "settings"} {
		if _, ok := p.operations["entry_evidence."+name]; !ok {
			t.Fatal("missing operation", name)
		}
	}
	staff := aiops.PanelOperationActor{ID: 1}
	for _, name := range []string{"case_delete", "settings"} {
		if err := p.Validate(context.Background(), staff, "entry_evidence."+name, []byte(`{}`)); !errors.Is(err, aiops.ErrForbidden) {
			t.Fatal("employee destructive privilege", name, err)
		}
	}
	for _, name := range []string{"status", "deliveries", "cases"} {
		if err := p.Validate(context.Background(), staff, "entry_evidence."+name, []byte(`{}`)); err != nil {
			t.Fatal("employee read blocked", name, err)
		}
	}
	w := httptest.NewRecorder()
	handleEntryEvidenceError(w, admin.ErrEntryEvidenceConflict)
	if w.Code != http.StatusConflict {
		t.Fatal(w.Code)
	}
}
