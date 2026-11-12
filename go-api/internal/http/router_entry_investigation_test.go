package httpapi

import (
	"context"
	"errors"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"net/http/httptest"
	"strings"
	"testing"
)

type investigationHTTPService struct {
	admin.Service
	calls         []string
	actor, caseID int64
	request       any
	err           error
}

func (f *investigationHTTPService) result(action string, actor, id int64, req any) (admin.EntryInvestigation, error) {
	f.calls = append(f.calls, action)
	f.actor = actor
	f.caseID = id
	f.request = req
	return admin.EntryInvestigation{CaseID: id, Revision: 4}, f.err
}
func (f *investigationHTTPService) StartEntryInvestigation(_ context.Context, a int64, r admin.EntryInvestigationStartRequest) (admin.EntryInvestigation, error) {
	return f.result("start", a, r.CaseID, r)
}
func (f *investigationHTTPService) GetEntryInvestigation(_ context.Context, a, id int64) (admin.EntryInvestigation, error) {
	return f.result("get", a, id, nil)
}
func (f *investigationHTTPService) ListEntryInvestigations(_ context.Context, a int64, r admin.EntryEvidenceListRequest) (admin.EntryInvestigationsPage, error) {
	_, e := f.result("list", a, 0, r)
	return admin.EntryInvestigationsPage{Items: []admin.EntryInvestigation{}, Current: r.Current, PageSize: r.PageSize}, e
}
func (f *investigationHTTPService) RefreshEntryInvestigation(_ context.Context, a, id int64) (admin.EntryInvestigation, error) {
	return f.result("refresh", a, id, nil)
}
func (f *investigationHTTPService) ConfirmEntryInvestigationFailure(_ context.Context, a int64, r admin.EntryInvestigationConfirmRequest) (admin.EntryInvestigation, error) {
	return f.result("confirm", a, r.CaseID, r)
}
func (f *investigationHTTPService) NextEntryInvestigationRound(_ context.Context, a int64, r admin.EntryInvestigationNextRequest) (admin.EntryInvestigation, error) {
	return f.result("next", a, r.CaseID, r)
}
func TestEntryInvestigationHTTPRoutesAndAuthorization(t *testing.T) {
	for _, tc := range []struct{ action, method, query, body, called string }{
		{"investigation", "GET", "case_id=12", "", "get"},
		{"investigation-list", "GET", "current=2&page_size=10", "", "list"},
		{"investigation-start", "POST", "", `{"case_id":12,"revision":2,"blocked_at":1794480000,"note":"confirmed"}`, "start"},
		{"investigation-refresh", "POST", "", `{"case_id":12}`, "refresh"},
		{"investigation-confirm", "POST", "", `{"case_id":12,"revision":4,"branch":"A","blocked_at":1794480000,"note":"confirmed","control_status":"unknown"}`, "confirm"},
		{"investigation-next", "POST", "", `{"case_id":12,"revision":4}`, "next"},
	} {
		for _, role := range []string{"user", "staff", "admin"} {
			t.Run(tc.action+"/"+role, func(t *testing.T) {
				id := &session.Identity{ID: 17}
				if role == "staff" {
					id.IsStaff = 1
				}
				if role == "admin" {
					id.IsAdmin = 1
				}
				req := httptest.NewRequest(tc.method, "http://fixture/?auth_data=fixture&"+tc.query, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				f := &investigationHTTPService{}
				handleEntryEvidence(w, req, strictAppleIDSession{identity: id}, f, nil, tc.action)
				if role == "user" {
					if w.Code != 403 || len(f.calls) != 0 {
						t.Fatal(w.Code, f.calls)
					}
					return
				}
				if w.Code != 200 || len(f.calls) != 1 || f.calls[0] != tc.called || f.actor != 17 {
					t.Fatal(w.Code, w.Body.String(), f.calls, f.actor)
				}
				if w.Header().Get("Cache-Control") == "" || w.Header().Get("Vary") != "Authorization" {
					t.Fatal("cache guard")
				}
				if tc.called == "list" {
					q := f.request.(admin.EntryEvidenceListRequest)
					if q.Current != 2 || q.PageSize != 10 {
						t.Fatal(q)
					}
				} else if f.caseID != 12 {
					t.Fatal(f.caseID)
				}
			})
		}
	}
}
func TestEntryInvestigationHTTPRejectsInvalidTransport(t *testing.T) {
	for _, tc := range []struct {
		action, method, query, body string
		status                      int
	}{
		{"investigation", "POST", "case_id=12", "", 405}, {"investigation-next", "GET", "", "", 405},
		{"investigation", "GET", "", "", 400}, {"investigation", "GET", "case_id=0", "", 400},
		{"investigation", "GET", "case_id=12&case_id=13", "", 400}, {"investigation", "GET", "case_id=12&refresh=true", "", 400},
		{"investigation-list", "GET", "current=1&current=2", "", 400}, {"investigation-list", "GET", "page_size=-1", "", 400}, {"investigation-list", "GET", "actor_id=9", "", 400},
		{"investigation-refresh", "POST", "", `{"case_id":0}`, 400}, {"investigation-start", "POST", "", `{"case_id":12,"actor_id":9}`, 400},
		{"investigation-confirm", "POST", "", `{"case_id":12,"confirmed_by":9}`, 400},
		{"investigation-next", "POST", "", `{"case_id":12,"revision":4,"selected_user_ids":[1]}`, 400}, {"investigation-next", "POST", "", `{"case_id":12,"revision":4} {}`, 400},
	} {
		t.Run(tc.action+"/"+tc.query+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://fixture/?auth_data=fixture&"+tc.query, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f := &investigationHTTPService{}
			handleEntryEvidence(w, r, strictAppleIDSession{identity: &session.Identity{ID: 17, IsStaff: 1}}, f, nil, tc.action)
			if w.Code != tc.status || len(f.calls) != 0 {
				t.Fatal(w.Code, w.Body.String(), f.calls)
			}
		})
	}
}
func TestEntryInvestigationHTTPPersistedReadsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{admin.ErrEntryEvidenceNotFound, 404}, {admin.ErrEntryEvidenceConflict, 409}, {admin.ErrEntryEvidenceForbidden, 403}, {admin.ErrEntryEvidenceInvalid, 400}, {errors.New("private database output"), 500}} {
		r := httptest.NewRequest("GET", "http://fixture/?auth_data=fixture&case_id=12", nil)
		w := httptest.NewRecorder()
		f := &investigationHTTPService{err: tc.err}
		handleEntryEvidence(w, r, strictAppleIDSession{identity: &session.Identity{ID: 17, IsStaff: 1}}, f, nil, "investigation")
		if w.Code != tc.status || len(f.calls) != 1 || f.calls[0] != "get" {
			t.Fatal(w.Code, w.Body.String(), f.calls)
		}
		if strings.Contains(w.Body.String(), "private database") {
			t.Fatal("error leaked")
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://fixture/?auth_data=fixture&case_id=12", nil)
	handleEntryEvidence(w, r, strictAppleIDSession{identity: &session.Identity{ID: 17, IsStaff: 1}}, &fakeAdminService{}, nil, "investigation")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestEntryInvestigationCatalogContract(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	actor := aiops.PanelOperationActor{ID: 17}
	valid := map[string]string{
		"investigation": `{"case_id":12}`, "investigation_list": `{"current":1,"page_size":20}`,
		"investigation_start":   `{"case_id":12,"revision":2,"blocked_at":1794480000,"note":"confirmed","min_observe_seconds":86400,"max_rounds":10,"stop_users":8}`,
		"investigation_refresh": `{"case_id":12}`,
		"investigation_confirm": `{"case_id":12,"revision":4,"branch":"B","blocked_at":1794480000,"note":"confirmed","control_status":"healthy"}`,
		"investigation_next":    `{"case_id":12,"revision":4}`,
	}
	for name, body := range valid {
		op, ok := p.operations["entry_evidence."+name]
		if !ok {
			t.Fatal(name)
		}
		read := name == "investigation" || name == "investigation_list"
		if op.Mutating == read || op.AdminOnly {
			t.Fatal(op)
		}
		if e := p.Validate(context.Background(), actor, op.Name, []byte(body)); e != nil {
			t.Fatal(name, e)
		}
		for _, stale := range []string{"不能借全权限自动执行", "即使完全访问也保留人工确认"} {
			if strings.Contains(op.Description, stale) {
				t.Fatal("stale investigation-only approval policy", name, stale)
			}
		}
	}
	for _, name := range []string{"investigation_start", "investigation_confirm"} {
		if !strings.Contains(p.operations["entry_evidence."+name].Description, "当前会话权限") {
			t.Fatal("missing existing access-mode contract", name)
		}
	}
	if strings.Contains(p.operations["entry_evidence.candidates"].Description, "全部页核对后才能冻结") {
		t.Fatal("AI must not paginate every user to freeze a server snapshot")
	}
	for _, tc := range []struct{ name, body string }{
		{"investigation", `{"case_id":0}`}, {"investigation", `{"case_id":12,"refresh":true}`}, {"investigation_list", `{"page_size":101}`},
		{"investigation_start", `{"case_id":12,"revision":2,"blocked_at":1794480000,"note":"confirmed","max_rounds":31}`},
		{"investigation_confirm", `{"case_id":12,"revision":4,"branch":"R","blocked_at":1794480000,"note":"confirmed","control_status":"healthy"}`},
		{"investigation_next", `{"case_id":12,"revision":4,"user_ids":[1,2]}`},
	} {
		if e := p.Validate(context.Background(), actor, "entry_evidence."+tc.name, []byte(tc.body)); e == nil {
			t.Fatal("invalid accepted", tc.name, tc.body)
		}
	}
}
