package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeEntryCollectionService struct {
	*fakeAdminService
	lastSave   admin.ClientEntryCollectionSaveRequest
	lastDelete [2]int64
	lastRemove admin.ClientEntryCollectionItem
	calls      int
	err        error
}

func (f *fakeEntryCollectionService) ListClientEntryCollections(context.Context) ([]admin.ClientEntryCollectionRecord, error) {
	f.calls++
	return []admin.ClientEntryCollectionRecord{{ID: 7, Name: "常用入口", EntryHost: "entry.example.com", Version: 3, Items: []admin.ClientEntryCollectionItem{{Kind: "policy", ID: 5}, {Kind: "split_group", ID: 6}}}}, f.err
}
func (f *fakeEntryCollectionService) SaveClientEntryCollection(_ context.Context, req admin.ClientEntryCollectionSaveRequest) (admin.ClientEntryCollectionRecord, error) {
	f.calls++
	f.lastSave = req
	return admin.ClientEntryCollectionRecord{ID: 7, Name: req.Name, EntryHost: req.EntryHost, Items: req.Items, Version: 4}, f.err
}
func (f *fakeEntryCollectionService) DeleteClientEntryCollection(_ context.Context, id, version int64) (bool, error) {
	f.calls++
	f.lastDelete = [2]int64{id, version}
	return true, f.err
}
func (f *fakeEntryCollectionService) RemoveClientEntryCollectionMember(_ context.Context, id, version int64, item admin.ClientEntryCollectionItem) (bool, error) {
	f.calls++
	f.lastDelete = [2]int64{id, version}
	f.lastRemove = item
	return true, f.err
}

const entryCollectionTestBase = "/api/v1/localadmin/server/client-entry-user-policy/collection/"

func TestRouterEntryCollectionsCRUD(t *testing.T) {
	service := &fakeEntryCollectionService{fakeAdminService: &fakeAdminService{}}
	sessions := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithAdminService(service))
	request := func(method, action, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, entryCollectionTestBase+action+"?auth_data=admin-test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", action, rec.Code, rec.Body.String())
		}
		if !sessions.lastRequireAdmin {
			t.Fatal("collection endpoint did not require admin")
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("missing no-store")
		}
		return rec
	}
	response := request(http.MethodGet, "fetch", "")
	if !strings.Contains(response.Body.String(), `"name":"常用入口"`) || !strings.Contains(response.Body.String(), `"kind":"split_group"`) {
		t.Fatalf("unexpected collection response: %s", response.Body.String())
	}
	request(http.MethodPost, "save", `{"id":7,"version":3,"name":"新入口","entry_host":"new.example.com","items":[{"kind":"policy","id":5},{"kind":"split_group","id":6}]}`)
	if service.lastSave.Name != "新入口" || service.lastSave.EntryHost != "new.example.com" || service.lastSave.Version != 3 || !reflect.DeepEqual(service.lastSave.Items, []admin.ClientEntryCollectionItem{{Kind: "policy", ID: 5}, {Kind: "split_group", ID: 6}}) {
		t.Fatalf("unexpected save: %#v", service.lastSave)
	}
	request(http.MethodPost, "remove", `{"id":7,"version":4,"item":{"kind":"split_group","id":6}}`)
	if service.lastDelete != [2]int64{7, 4} || service.lastRemove.ID != 6 || service.lastRemove.Kind != "split_group" {
		t.Fatalf("unexpected removal: %#v", service)
	}
	request(http.MethodPost, "drop", `{"id":7,"version":5}`)
	if service.lastDelete != [2]int64{7, 5} {
		t.Fatalf("unexpected drop: %v", service.lastDelete)
	}
}

func TestRouterEntryCollectionsValidationAndAuthorization(t *testing.T) {
	tests := []struct {
		name, method, action, body string
		identity                   *session.Identity
		authErr                    error
		status                     int
	}{
		{"post fetch", http.MethodPost, "fetch", "", &session.Identity{IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"get save", http.MethodGet, "save", "", &session.Identity{IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"get remove", http.MethodGet, "remove", "", &session.Identity{IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"get drop", http.MethodGet, "drop", "", &session.Identity{IsAdmin: 1}, nil, http.StatusMethodNotAllowed},
		{"anonymous", http.MethodGet, "fetch", "", nil, session.ErrUnauthorized, http.StatusForbidden},
		{"ordinary user", http.MethodPost, "save", `{}`, &session.Identity{ID: 2}, nil, http.StatusForbidden},
		{"staff", http.MethodGet, "fetch", "", &session.Identity{ID: 3, IsStaff: 1}, nil, http.StatusForbidden},
		{"malformed json", http.MethodPost, "save", `{`, &session.Identity{IsAdmin: 1}, nil, http.StatusBadRequest},
		{"missing edit version", http.MethodPost, "save", `{"id":7}`, &session.Identity{IsAdmin: 1}, nil, http.StatusBadRequest},
		{"drop missing version", http.MethodPost, "drop", `{"id":7}`, &session.Identity{IsAdmin: 1}, nil, http.StatusBadRequest},
		{"remove invalid kind", http.MethodPost, "remove", `{"id":7,"version":3,"item":{"kind":"collection","id":6}}`, &session.Identity{IsAdmin: 1}, nil, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeEntryCollectionService{fakeAdminService: &fakeAdminService{}}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(&fakeSessionService{user: tc.identity, authErr: tc.authErr}), WithAdminService(service))
			req := httptest.NewRequest(tc.method, entryCollectionTestBase+tc.action+"?auth_data=test", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d expected=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if service.calls != 0 {
				t.Fatalf("invalid request reached service %d times", service.calls)
			}
		})
	}
}
func TestRouterEntryCollectionsReportsSaveError(t *testing.T) {
	service := &fakeEntryCollectionService{fakeAdminService: &fakeAdminService{}, err: errors.New("入口合集或成员已变化，请刷新后重试")}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(&fakeSessionService{user: &session.Identity{IsAdmin: 1}}), WithAdminService(service))
	req := httptest.NewRequest(http.MethodPost, entryCollectionTestBase+"save?auth_data=test", strings.NewReader(`{"id":7,"version":1,"name":"合集","entry_host":"a.example.com","items":[{"kind":"policy","id":5}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "已变化") {
		t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
	}
}
