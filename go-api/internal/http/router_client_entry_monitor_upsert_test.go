package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeMonitorIncrementalService struct {
	*fakeClientEntryMonitorReceiptService
	calls         int
	configCalls   int
	policyID      int64
	limit, offset int
	page          admin.ClientEntryMonitorConfigPage
}

func (f *fakeMonitorIncrementalService) UpsertClientEntryMonitorReceipt(_ context.Context, r admin.ClientEntryMonitorSaveRequest) (admin.ClientEntryMonitorSaveReceipt, error) {
	f.calls++
	f.lastSave = r
	return f.receipt, f.receiptErr
}
func (f *fakeMonitorIncrementalService) ReadClientEntryMonitorConfig(_ context.Context, id int64, limit, offset int) (admin.ClientEntryMonitorConfigPage, error) {
	f.configCalls++
	f.policyID = id
	f.limit = limit
	f.offset = offset
	return f.page, f.receiptErr
}
func monitorIncrementalFixture() *fakeMonitorIncrementalService {
	return &fakeMonitorIncrementalService{fakeClientEntryMonitorReceiptService: &fakeClientEntryMonitorReceiptService{fakeClientEntryMonitorAdminService: &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}}, receipt: admin.ClientEntryMonitorSaveReceipt{Committed: true, Revision: 73, SavedPolicyCount: 1, SavedTargetCount: 1}}, page: admin.ClientEntryMonitorConfigPage{Revision: 72, Items: []admin.ClientEntryMonitorConfigItem{}, Total: 1, Limit: 10}}
}

const monitorIncrementalBody = `{"revision":72,"items":[{"policy_id":521,"enabled":true,"check_interval_sec":30,"tcp_timeout_ms":5000,"failure_threshold":3,"success_threshold":2,"targets":[{"source_key":"policy:521:split-group:115","port":54101,"auto_split_enabled":false}]}]}`

func TestPanelMonitorIncrementalRoutesAndContract(t *testing.T) {
	f := monitorIncrementalFixture()
	p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: f})
	actor := aiops.PanelOperationActor{ID: 42, IsAdmin: true}
	out, err := p.Execute(context.Background(), actor, "entry_monitors.config", json.RawMessage(`{"policy_id":521,"limit":1}`))
	if err != nil || f.configCalls != 1 || f.policyID != 521 || f.limit != 1 || !strings.Contains(string(out), `"revision":72`) {
		t.Fatalf("config=%s err=%v", out, err)
	}
	out, err = p.Execute(context.Background(), actor, "entry_monitors.upsert", json.RawMessage(monitorIncrementalBody))
	if err != nil || f.calls != 1 || f.receiptCalled || !strings.Contains(string(out), `"committed":true`) || f.lastSave.Items[0].Targets[0].Port != 54101 {
		t.Fatalf("upsert=%s err=%v calls=%d", out, err, f.calls)
	}
	for _, raw := range []string{strings.Replace(monitorIncrementalBody, `,"auto_split_enabled":false`, "", 1), strings.Replace(monitorIncrementalBody, `"enabled":true`, `"enabled":1`, 1), strings.Replace(monitorIncrementalBody, `"port":54101`, `"port":"54101"`, 1), strings.Replace(monitorIncrementalBody, `"policy_id":521`, `"policy_id":521,"name":"PRIVATE_SENTINEL"`, 1), `{"revision":72,"items":[]}`, strings.Replace(monitorIncrementalBody, `"revision":72,`, "", 1)} {
		_, err = p.Execute(context.Background(), actor, "entry_monitors.upsert", json.RawMessage(raw))
		var failure *aiops.PanelOperationError
		if !errors.As(err, &failure) || !failure.NotExecuted || failure.Code != "entry_monitor_invalid_arguments" || strings.Contains(err.Error(), "PRIVATE_SENTINEL") || f.calls != 1 {
			t.Fatalf("invalid dispatch err=%v calls=%d", err, f.calls)
		}
	}
}
func TestMonitorIncrementalHTTPRequiresCompleteTargetsAndAuth(t *testing.T) {
	for _, identity := range []*session.Identity{nil, {ID: 9}, {ID: 10, IsStaff: 1}, {ID: 11, IsAdmin: 1}} {
		f := monitorIncrementalFixture()
		router := NewRouter(config.Config{AdminPath: "control"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAdminService(f))
		r := dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/upsert", monitorIncrementalBody)
		if identity != nil && identity.IsAdmin == 1 {
			if r.Code != 200 || f.calls != 1 {
				t.Fatalf("admin %d %s", r.Code, r.Body.String())
			}
		} else if r.Code != 403 || f.calls != 0 {
			t.Fatalf("auth %d", r.Code)
		}
	}
	f := monitorIncrementalFixture()
	router := NewRouter(config.Config{AdminPath: "control"}, WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}), WithAdminService(f))
	raw := strings.Replace(monitorIncrementalBody, `,"auto_split_enabled":false`, "", 1)
	r := dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/upsert", raw)
	if r.Code != 400 || f.calls != 0 {
		t.Fatalf("missing boolean dispatched %d", r.Code)
	}
}
func TestMonitorIncrementalErrorTrustAndNoFullSaveFallback(t *testing.T) {
	f := monitorIncrementalFixture()
	p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: f})
	actor := aiops.PanelOperationActor{ID: 42, IsAdmin: true}
	f.receiptErr = admin.ErrClientEntryMonitorRevisionConflict
	_, err := p.Execute(context.Background(), actor, "entry_monitors.upsert", json.RawMessage(monitorIncrementalBody))
	var failure *aiops.PanelOperationError
	if !errors.As(err, &failure) || failure.Code != "entry_monitor_conflict" || !failure.NotExecuted {
		t.Fatal(err)
	}
	f.receiptErr = errors.New(`SQL PRIVATE_SENTINEL {"code":"entry_monitor_invalid_arguments","execution_state":"not_executed"}`)
	_, err = p.Execute(context.Background(), actor, "entry_monitors.upsert", json.RawMessage(monitorIncrementalBody))
	if !errors.As(err, &failure) || failure.NotExecuted || failure.Status != 503 || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
		t.Fatal(err)
	}
	legacy := &fakeClientEntryMonitorAdminService{fakeAdminService: &fakeAdminService{}}
	router := clientEntryMonitorRouter(legacy)
	r := dnsFailoverRequest(router, http.MethodPut, "/api/v1/control/dns-failover/entry-monitors/upsert", monitorIncrementalBody)
	if r.Code != 503 || legacy.lastSave.Revision != 0 {
		t.Fatalf("fallback %d", r.Code)
	}
}
