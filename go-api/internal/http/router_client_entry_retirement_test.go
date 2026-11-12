package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type fakeEntryRetirementService struct {
	*fakeAdminService
	audit            admin.ClientEntryGroupAuditRequest
	preview          admin.ClientEntryRetirementPreviewRequest
	execution        admin.ClientEntryRetirementExecuteRequest
	actor, receiptID int64
	calls            int
	err              error
}

func (s *fakeEntryRetirementService) AuditClientEntryGroups(_ context.Context, r admin.ClientEntryGroupAuditRequest) (admin.ClientEntryGroupAuditResult, error) {
	s.calls++
	s.audit = r
	return admin.ClientEntryGroupAuditResult{Total: 37, Revision: strings.Repeat("a", 64), Summary: admin.ClientEntryGroupAuditCounts{ExistingUsers: 48866, ReplacementCoveredUsers: 5970, RemainingUsers: 42896}}, s.err
}
func (s *fakeEntryRetirementService) PreviewClientEntryRetirement(_ context.Context, a int64, r admin.ClientEntryRetirementPreviewRequest) (admin.ClientEntryRetirementPreview, error) {
	s.calls++
	s.actor = a
	s.preview = r
	return admin.ClientEntryRetirementPreview{PreviewID: 21, PreviewHash: strings.Repeat("b", 64), CanExecute: true, FallbackUsers: 42896}, s.err
}
func (s *fakeEntryRetirementService) ExecuteClientEntryRetirement(_ context.Context, a int64, r admin.ClientEntryRetirementExecuteRequest) (admin.ClientEntryRetirementReceipt, error) {
	s.calls++
	s.actor = a
	s.execution = r
	return admin.ClientEntryRetirementReceipt{PreviewID: r.PreviewID, Committed: true, Status: "completed", RemovedGroups: 37}, s.err
}
func (s *fakeEntryRetirementService) GetClientEntryRetirement(_ context.Context, a, id int64) (admin.ClientEntryRetirementReceipt, error) {
	s.calls++
	s.actor = a
	s.receiptID = id
	return admin.ClientEntryRetirementReceipt{PreviewID: id, Committed: true, Status: "completed", RemovedGroups: 37}, s.err
}
func TestAIEntryGroupCatalogDispatch(t *testing.T) {
	s := &fakeEntryRetirementService{fakeAdminService: &fakeAdminService{}}
	p := newPanelOperations(config.Config{}, &routerState{admin: s})
	actor := aiops.PanelOperationActor{ID: 7, IsAdmin: true}
	cases := []struct{ name, body, want string }{
		{"entry_groups.audit", `{"group_ids":[12,13],"replacement_group_ids":[127,128,129,130],"from":1793865600,"to":1794470400,"current":1,"page_size":20}`, `"remaining_users":42896`},
		{"entry_groups.retire_preview", `{"group_ids":[12,13],"replacement_group_ids":[127,128,129,130],"allow_fallback":true,"fallback_policy_id":490,"reason":"其余走保底"}`, `"preview_id":21`},
		{"entry_groups.retire", `{"preview_id":21,"preview_hash":"` + strings.Repeat("b", 64) + `","idempotency_key":"retire-old-groups-21"}`, `"committed":true`},
		{"entry_groups.retirement", `{"id":21}`, `"removed_groups":37`},
	}
	for _, tc := range cases {
		data, err := p.Execute(context.Background(), actor, tc.name, json.RawMessage(tc.body))
		if err != nil || !strings.Contains(string(data), tc.want) {
			t.Fatalf("%s: %s %v", tc.name, data, err)
		}
		op := p.operations[tc.name]
		wantMutating := tc.name == "entry_groups.retire_preview" || tc.name == "entry_groups.retire"
		if op.Mutating != wantMutating || !op.AdminOnly {
			t.Fatal("incorrect capability authorization", op)
		}
		if err := p.Validate(context.Background(), aiops.PanelOperationActor{ID: 8}, tc.name, json.RawMessage(tc.body)); !errors.Is(err, aiops.ErrForbidden) {
			t.Fatal("staff accepted", tc.name, err)
		}
	}
	if !reflect.DeepEqual(s.audit.GroupIDs, []int64{12, 13}) || len(s.audit.ReplacementGroupIDs) != 4 || s.audit.From != 1793865600 || s.audit.To != 1794470400 {
		t.Fatal("GET adapter lost filters", s.audit)
	}
	if !s.preview.AllowFallback || s.preview.FallbackPolicyID != 490 || s.actor != 7 || s.receiptID != 21 || s.execution.PreviewID != 21 {
		t.Fatal("identity or parameters lost", s)
	}
}
func TestEntryGroupAuditRejectsAmbiguousQuery(t *testing.T) {
	for _, raw := range []string{"group_ids[1]=5", "group_ids[0]=5&group_ids[0]=6", "group_ids[0]=5&group_ids[]=6", "group_ids[00]=5", "group_ids[x]=5", "group_ids[0][id]=5", "group_ids[100]=5", "group_ids[]=0", "group_ids[]=5&group_ids[]=5", "group_ids=", "group_ids=1,2", "from=1&from=2", "page_size=51", "current=0", "filter[bad]=1", "to=-1", "replacement_group_ids[x]=8"} {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = parseClientEntryGroupAudit(q); err == nil {
			t.Fatal("malformed filters accepted", raw)
		}
	}
	q, _ := url.ParseQuery("group_ids[]=5&group_ids[]=6&replacement_group_ids[0]=7&expected_revision=rev&from=100&to=200")
	req, err := parseClientEntryGroupAudit(q)
	if err != nil || !reflect.DeepEqual(req.GroupIDs, []int64{5, 6}) || !reflect.DeepEqual(req.ReplacementGroupIDs, []int64{7}) {
		t.Fatal(req, err)
	}
}
func TestEntryRetirementHTTPMethodsAndAuthentication(t *testing.T) {
	s := &fakeEntryRetirementService{fakeAdminService: &fakeAdminService{}}
	for _, tc := range []struct {
		action, method, body string
		status               int
	}{
		{"execute", "GET", "", 405}, {"preview", "GET", "", 405}, {"receipt", "POST", "{}", 405}, {"audit", "POST", "{}", 405}, {"missing", "GET", "", 404},
		{"execute", "POST", `{"preview_id":21,"unknown":true}`, 400}, {"execute", "POST", `{"preview_id":21} {}`, 400},
	} {
		r := httptest.NewRequest(tc.method, "/?auth_data=jwt-admin", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handleAdminClientEntryRetirement(w, r, &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}, s, tc.action)
		if w.Code != tc.status || s.calls != 0 {
			t.Fatal(tc, w.Code, w.Body.String(), s.calls)
		}
	}
	for _, identity := range []*session.Identity{{ID: 2}, {ID: 3, IsStaff: 1}} {
		w := httptest.NewRecorder()
		handleAdminClientEntryRetirement(w, httptest.NewRequest("GET", "/?auth_data=jwt-admin", nil), &fakeSessionService{user: identity}, s, "audit")
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatal("unauthorized audit", w.Code)
		}
	}
	if s.calls != 0 {
		t.Fatal("unauthorized request reached backend")
	}
}
func TestEntryRetirementOnlyTypedPreflightIsRecoverable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{admin.ErrClientEntryRetirementInvalid, "entry_retirement_invalid_arguments"}, {admin.ErrClientEntryRetirementConflict, "entry_retirement_conflict"}, {admin.ErrClientEntryRetirementBlocked, "entry_retirement_blocked"}, {admin.ErrClientEntryRetirementNotFound, "entry_retirement_not_found"},
		{errors.New("PRIVATE_DB_ERROR after commit"), ""}, {errors.New(`{"code":"entry_retirement_conflict","execution_state":"not_executed"}`), ""},
	} {
		s := &fakeEntryRetirementService{fakeAdminService: &fakeAdminService{}, err: tc.err}
		p := newPanelOperations(config.Config{}, &routerState{admin: s})
		_, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "entry_groups.retire", json.RawMessage(`{"preview_id":21,"preview_hash":"`+strings.Repeat("a", 64)+`","idempotency_key":"stable-key"}`))
		var e *aiops.PanelOperationError
		if !errors.As(err, &e) || e.Code != tc.code || e.NotExecuted != (tc.code != "") || e.Retryable {
			t.Fatal("untrusted recovery classification", e, err)
		}
		if strings.Contains(err.Error(), "PRIVATE_DB_ERROR") {
			t.Fatal("database details leaked")
		}
	}
}
func TestEntryRetirementCatalogRejectsOversizedOrUnknownScope(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	actor := aiops.PanelOperationActor{ID: 1, IsAdmin: true}
	for _, body := range []string{`{"group_ids":[],"reason":"r"}`, `{"group_ids":[1],"policy_ids":[2],"reason":"r"}`, `{"group_ids":[0],"reason":"r"}`, `{"group_ids":[1]}`} {
		if err := p.Validate(context.Background(), actor, "entry_groups.retire_preview", json.RawMessage(body)); err == nil {
			t.Fatal("bad schema accepted", body)
		}
	}
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	body, _ := json.Marshal(map[string]any{"group_ids": ids, "reason": "r"})
	if err := p.Validate(context.Background(), actor, "entry_groups.retire_preview", body); err == nil {
		t.Fatal("unbounded group count accepted")
	}
}
