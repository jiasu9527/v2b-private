package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeClientEntryMonitorAdminService struct {
	*fakeAdminService
	overview      admin.ClientEntryMonitorOverview
	savedOverview admin.ClientEntryMonitorOverview
	runs          []admin.ClientEntryMonitorRun
	runID         int64
	err           error
	lastSave      admin.ClientEntryMonitorSaveRequest
	lastPolicyIDs []int64
	lastRunUserID int64
	lastRunChatID int64
	lastRunsLimit int64
	clearDeleted  int64
	clearCalled   bool
}

func (f *fakeClientEntryMonitorAdminService) ListClientEntryMonitors(context.Context) (admin.ClientEntryMonitorOverview, error) {
	return f.overview, f.err
}

func (f *fakeClientEntryMonitorAdminService) SaveClientEntryMonitors(_ context.Context, request admin.ClientEntryMonitorSaveRequest) (admin.ClientEntryMonitorOverview, error) {
	f.lastSave = request
	return f.savedOverview, f.err
}

func (f *fakeClientEntryMonitorAdminService) StartClientEntryMonitorRunForPolicies(_ context.Context, policyIDs []int64, userID, chatID int64) (int64, error) {
	f.lastPolicyIDs = append([]int64(nil), policyIDs...)
	f.lastRunUserID = userID
	f.lastRunChatID = chatID
	return f.runID, f.err
}

func (f *fakeClientEntryMonitorAdminService) ListClientEntryMonitorRuns(_ context.Context, limit int64) ([]admin.ClientEntryMonitorRun, error) {
	f.lastRunsLimit = limit
	return f.runs, f.err
}

func (f *fakeClientEntryMonitorAdminService) ClearClientEntryMonitorRuns(context.Context) (int64, error) {
	f.clearCalled = true
	return f.clearDeleted, f.err
}

func clientEntryMonitorRouter(service *fakeClientEntryMonitorAdminService) http.Handler {
	return NewRouter(
		config.Config{AdminPath: "control"},
		WithSessionService(&fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}),
		WithAdminService(service),
	)
}

func TestClientEntryMonitorRoutesMapConfigurationRunAndHistory(t *testing.T) {
	service := &fakeClientEntryMonitorAdminService{
		fakeAdminService: &fakeAdminService{},
		overview: admin.ClientEntryMonitorOverview{
			Revision: 7,
			Items:    []admin.ClientEntryMonitorRecord{{ID: 3, PolicyID: 12}},
		},
		savedOverview: admin.ClientEntryMonitorOverview{Revision: 8},
		runs:          []admin.ClientEntryMonitorRun{{ID: 91, Status: "completed"}},
		runID:         92,
		clearDeleted:  3,
	}
	router := clientEntryMonitorRouter(service)

	if rec := dnsFailoverRequest(router, http.MethodGet, "/api/v1/control/dns-failover/entry-monitors", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revision":7`) || !strings.Contains(rec.Body.String(), `"policy_id":12`) || strings.Contains(rec.Body.String(), `"probe_ids"`) {
		t.Fatalf("GET configuration: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors", `{"revision":7,"items":[]}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revision":8`) {
		t.Fatalf("PUT configuration: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.lastSave.Revision != 7 || service.lastSave.Items == nil || len(service.lastSave.Items) != 0 {
		t.Fatalf("save request = %#v", service.lastSave)
	}
	if rec := dnsFailoverRequest(router, http.MethodPost, "/api/v1/control/dns-failover/entry-monitors/run", `{"policy_ids":[12,13]}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"run_id":92`) {
		t.Fatalf("POST run: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.lastPolicyIDs) != 2 || service.lastPolicyIDs[0] != 12 || service.lastPolicyIDs[1] != 13 || service.lastRunUserID != 0 || service.lastRunChatID != 0 {
		t.Fatalf("run request = policies %#v user=%d chat=%d", service.lastPolicyIDs, service.lastRunUserID, service.lastRunChatID)
	}
	if rec := dnsFailoverRequest(router, http.MethodGet, "/api/v1/control/dns-failover/entry-monitors/runs?limit=5", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":91`) {
		t.Fatalf("GET runs: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.lastRunsLimit != 5 {
		t.Fatalf("runs limit = %d, want 5", service.lastRunsLimit)
	}
	if rec := dnsFailoverRequest(router, http.MethodDelete, "/api/v1/control/dns-failover/entry-monitors/runs", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":3`) {
		t.Fatalf("DELETE runs: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !service.clearCalled {
		t.Fatal("DELETE runs did not call the clear service")
	}
}

func TestClientEntryMonitorRoutesRejectInvalidContracts(t *testing.T) {
	service := &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}}
	router := clientEntryMonitorRouter(service)

	for _, test := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodPut, "/api/v1/control/dns-failover/entry-monitors", `{"revision":1,"group_id":9,"items":[]}`, http.StatusBadRequest},
		{http.MethodPut, "/api/v1/control/dns-failover/entry-monitors", `{"revision":1,"items":[{"policy_id":9,"enabled":true,"probe_ids":[7],"targets":[]}]}`, http.StatusBadRequest},
		{http.MethodPut, "/api/v1/control/dns-failover/entry-monitors", `{"revision":1}`, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/control/dns-failover/entry-monitors/runs?limit=bad", "", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/control/dns-failover/entry-monitors/runs", `{}`, http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/v1/control/dns-failover/entry-monitors", "", http.StatusMethodNotAllowed},
	} {
		rec := dnsFailoverRequest(router, test.method, test.path, test.body)
		if rec.Code != test.status {
			t.Fatalf("%s %s: status=%d body=%s", test.method, test.path, rec.Code, rec.Body.String())
		}
	}
}

func TestClientEntryMonitorRevisionConflictIsHTTP409(t *testing.T) {
	service := &fakeClientEntryMonitorAdminService{
		fakeAdminService: &fakeAdminService{},
		err:              admin.ErrClientEntryMonitorRevisionConflict,
	}
	rec := dnsFailoverRequest(clientEntryMonitorRouter(service), http.MethodPut,
		"/api/v1/control/dns-failover/entry-monitors", `{"revision":1,"items":[]}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "刷新后重试") {
		t.Fatalf("revision conflict: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientEntryMonitorErrorsKeepTheirOwnStatusAndMessage(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		method  string
		path    string
		body    string
		status  int
		message string
	}{
		{name: "validation", err: errors.New("所选探针不存在或已停用"), method: http.MethodPut,
			path: "/api/v1/control/dns-failover/entry-monitors", body: `{"revision":1,"items":[]}`,
			status: http.StatusBadRequest, message: "所选探针不存在或已停用"},
		{name: "running", err: errors.New("用户入口检测正在进行，请稍后查看结果"), method: http.MethodPost,
			path: "/api/v1/control/dns-failover/entry-monitors/run", body: `{"policy_ids":[]}`,
			status: http.StatusConflict, message: "用户入口检测正在进行"},
		{name: "unavailable", err: admin.ErrUnavailable, method: http.MethodGet,
			path: "/api/v1/control/dns-failover/entry-monitors", status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}, err: test.err}
			rec := dnsFailoverRequest(clientEntryMonitorRouter(service), test.method, test.path, test.body)
			if rec.Code != test.status || (test.message != "" && !strings.Contains(rec.Body.String(), test.message)) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
