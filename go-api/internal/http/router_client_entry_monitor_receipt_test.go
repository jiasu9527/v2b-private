package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeClientEntryMonitorReceiptService struct {
	*fakeClientEntryMonitorAdminService
	receipt       admin.ClientEntryMonitorSaveReceipt
	receiptCalled bool
	receiptErr    error
}

func (f *fakeClientEntryMonitorReceiptService) SaveClientEntryMonitorReceipt(_ context.Context, request admin.ClientEntryMonitorSaveRequest) (admin.ClientEntryMonitorSaveReceipt, error) {
	f.receiptCalled = true
	f.lastSave = request
	return f.receipt, f.receiptErr
}

func TestClientEntryMonitorReceiptRouteKeepsOverviewCompatible(t *testing.T) {
	service := &fakeClientEntryMonitorReceiptService{
		fakeClientEntryMonitorAdminService: &fakeClientEntryMonitorAdminService{
			fakeAdminService: &fakeAdminService{},
			savedOverview:    admin.ClientEntryMonitorOverview{Revision: 8, Items: []admin.ClientEntryMonitorRecord{{PolicyID: 12}}, Policies: []admin.ClientEntryMonitorPolicy{{ID: 12}}, Probes: []admin.DNSProbeRecord{}},
		},
		receipt: admin.ClientEntryMonitorSaveReceipt{Committed: true, Revision: 8, SavedPolicyCount: 1, SavedTargetCount: 2},
	}
	router := NewRouter(config.Config{AdminPath: "control"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}), WithAdminService(service))
	rec := dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors", `{"revision":7,"items":[]}`)
	if rec.Code != http.StatusOK || service.receiptCalled || !strings.Contains(rec.Body.String(), `"policies"`) || !strings.Contains(rec.Body.String(), `"probes"`) || !strings.Contains(rec.Body.String(), `"policy_id":12`) {
		t.Fatalf("legacy overview changed: %d %s", rec.Code, rec.Body.String())
	}
	rec = dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/save-receipt", `{"revision":7,"items":[]}`)
	if rec.Code != http.StatusOK || !service.receiptCalled || !strings.Contains(rec.Body.String(), `"committed":true`) || !strings.Contains(rec.Body.String(), `"revision":8`) || strings.Contains(rec.Body.String(), `"policies"`) || strings.Contains(rec.Body.String(), `"items"`) || len(rec.Body.String()) > 400 {
		t.Fatalf("compact receipt: %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		method, body string
		status       int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPut, `{"revision":7}`, http.StatusBadRequest},
		{http.MethodPut, `{"revision":7,"items":[],"extra":true}`, http.StatusBadRequest},
	} {
		service.receiptCalled = false
		rec = dnsFailoverRequest(router, tc.method, "/api/v1/control/dns-failover/entry-monitors/save-receipt", tc.body)
		if rec.Code != tc.status || service.receiptCalled {
			t.Fatalf("invalid request: %d %s", rec.Code, rec.Body.String())
		}
	}
	service.receiptErr = admin.ErrClientEntryMonitorRevisionConflict
	rec = dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/save-receipt", `{"revision":7,"items":[]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
}

func TestClientEntryMonitorReceiptRouteDoesNotFallbackToOverview(t *testing.T) {
	service := &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}}
	rec := dnsFailoverRequest(clientEntryMonitorRouter(service), http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/save-receipt", `{"revision":7,"items":[]}`)
	if rec.Code != http.StatusServiceUnavailable || service.lastSave.Revision != 0 {
		t.Fatalf("must not dispatch overview save: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPanelOperationMonitorSaveUsesCommittedReceipt(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		service := &fakeClientEntryMonitorReceiptService{
			fakeClientEntryMonitorAdminService: &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}},
			receipt:                            admin.ClientEntryMonitorSaveReceipt{Committed: true, Revision: 8},
		}
		p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: service})
		got, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: isAdmin}, "entry_monitors.save", json.RawMessage(`{"revision":7,"items":[]}`))
		if err != nil || !service.receiptCalled || !strings.Contains(string(got), `"committed":true`) || strings.Contains(string(got), `"items"`) {
			t.Fatalf("admin=%t receipt=%s err=%v", isAdmin, got, err)
		}
	}
}

func TestClientEntryMonitorReceiptRouteRequiresAdministratorSession(t *testing.T) {
	service := &fakeClientEntryMonitorReceiptService{
		fakeClientEntryMonitorAdminService: &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}},
	}
	for _, identity := range []*session.Identity{nil, {ID: 9}, {ID: 10, IsStaff: 1}} {
		router := NewRouter(config.Config{AdminPath: "control"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAdminService(service))
		rec := dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/save-receipt", `{"revision":7,"items":[]}`)
		if rec.Code != http.StatusForbidden || service.receiptCalled {
			t.Fatalf("ordinary/staff browser session performed admin write: %d %s", rec.Code, rec.Body.String())
		}
	}
}
