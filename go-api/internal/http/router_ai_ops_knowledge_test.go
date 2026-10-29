package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeAIKnowledge struct {
	fakeAIOps
	list    aiops.KnowledgeListRequest
	save    aiops.KnowledgeSaveRequest
	version int64
}

func (f *fakeAIKnowledge) ListKnowledge(_ context.Context, actor int64, req aiops.KnowledgeListRequest) (aiops.KnowledgeListResult, error) {
	f.called(actor)
	f.list = req
	return aiops.KnowledgeListResult{Data: []aiops.KnowledgeEntry{}, Total: 1, Current: 1, PageSize: 20}, f.err
}
func (f *fakeAIKnowledge) GetKnowledge(_ context.Context, actor, id int64) (aiops.KnowledgeEntry, error) {
	f.called(actor)
	f.id = id
	return aiops.KnowledgeEntry{ID: id, Content: "fixture"}, f.err
}
func (f *fakeAIKnowledge) SaveKnowledge(_ context.Context, actor int64, req aiops.KnowledgeSaveRequest) (aiops.KnowledgeEntry, error) {
	f.called(actor)
	f.save = req
	return aiops.KnowledgeEntry{ID: 1, Version: 1}, f.err
}
func (f *fakeAIKnowledge) DeleteKnowledge(_ context.Context, actor, id, version int64) error {
	f.called(actor)
	f.id = id
	f.version = version
	return f.err
}
func knowledgeHTTP(t *testing.T, service aiOpsService, identity *session.Identity, method, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(config.Config{AdminPath: "test"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAIOps(service))
	r := httptest.NewRequest(method, "/api/v1/test/ai-ops/"+action, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer fixture")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func TestAIOpsKnowledgeHTTPContractAndVisibility(t *testing.T) {
	admin := &session.Identity{ID: 7, IsAdmin: 1}
	staff := &session.Identity{ID: 8, IsStaff: 1}
	for _, tt := range []struct {
		method, action, body string
		identity             *session.Identity
		want, calls          int
	}{
		{"GET", "knowledge?query=router&status=active&category=network&current=2&page_size=3", "", admin, 200, 1},
		{"GET", "knowledge/detail?id=4", "", staff, 200, 1},
		{"GET", "knowledge/detail?id=no", "", admin, 400, 0},
		{"GET", "knowledge?current=no", "", admin, 400, 0},
		{"GET", "knowledge", "", &session.Identity{ID: 9}, 403, 0},
		{"GET", "knowledge", "", nil, 403, 0},
		{"POST", "knowledge", "{}", admin, 405, 0},
		{"POST", "knowledge/save", `{"title":"fixture","content":"fixture","category":"other"}`, staff, 200, 1},
		{"POST", "knowledge/save", `{"id":1,"expected_version":1,"title":"fixture","content":"fixture","category":"other"}`, staff, 403, 0},
		{"POST", "knowledge/save", `{"source_kind":"ai","title":"fixture"}`, admin, 400, 0},
		{"POST", "knowledge/save", `{"source_session_id":123}`, admin, 400, 0},
		{"POST", "knowledge/save", `{"created_by":123}`, admin, 400, 0},
		{"POST", "knowledge/delete", `{"id":1,"expected_version":2,"confirm":true}`, admin, 200, 1},
		{"POST", "knowledge/delete", `{"id":1,"expected_version":2}`, admin, 400, 0},
		{"POST", "knowledge/delete", `{"id":1,"confirm":true}`, admin, 400, 0},
		{"POST", "knowledge/delete", `{"id":1,"expected_version":2,"confirm":true}`, staff, 403, 0},
	} {
		f := &fakeAIKnowledge{}
		w := knowledgeHTTP(t, f, tt.identity, tt.method, tt.action, tt.body)
		if w.Code != tt.want || f.calls != tt.calls {
			t.Fatalf("%s %s => %d calls%d body%s", tt.method, tt.action, w.Code, f.calls, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("knowledge response cacheable")
		}
		if strings.Contains(tt.action, "query=router") && (f.list.Query != "router" || f.list.Current != 2 || f.list.PageSize != 3 || f.list.Category != "network" || f.list.Status != "active") {
			t.Fatal("query fields lost", f.list)
		}
	}
	f := &fakeAIKnowledge{}
	body, _ := json.Marshal(aiops.KnowledgeSaveRequest{Title: "完整中文", Content: strings.Repeat("中", 12000), Category: "other"})
	w := knowledgeHTTP(t, f, admin, "POST", "knowledge/save", string(body))
	if w.Code != 200 || len([]rune(f.save.Content)) != 12000 {
		t.Fatal("valid Unicode body rejected", w.Code)
	}
	w = knowledgeHTTP(t, &fakeAIOps{}, admin, "GET", "knowledge", "")
	if w.Code != 503 {
		t.Fatal("legacy service missing knowledge not handled", w.Code)
	}
	for _, err := range []error{errors.New("postgres://hidden-database-password"), aiops.ErrKnowledgeCredential, aiops.ErrConflict} {
		f.err = err
		w = knowledgeHTTP(t, f, admin, "GET", "knowledge", "")
		if strings.Contains(w.Body.String(), "hidden-database-password") {
			t.Fatal("raw error exposed")
		}
		if errors.Is(err, aiops.ErrKnowledgeCredential) && (w.Code != 400 || !strings.Contains(w.Body.String(), "不能保存")) {
			t.Fatal("credential error hidden", w.Code, w.Body.String())
		}
	}
}
