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

type fakeClientEntryBackupIPService struct {
	*fakeAdminService
	list          admin.ClientEntryBackupIPList
	created       admin.ClientEntryBackupIPRecord
	createdBatch  []admin.ClientEntryBackupIPRecord
	updated       admin.ClientEntryBackupIPRecord
	refresh       admin.ClientEntryBackupIPRefreshResult
	err           error
	createRequest admin.ClientEntryBackupIPSaveRequest
	batchRequest  []admin.ClientEntryBackupIPSaveRequest
	updateID      int64
	updateRequest admin.ClientEntryBackupIPSaveRequest
	deleteID      int64
	refreshIDs    []int64
}

func (f *fakeClientEntryBackupIPService) ListClientEntryBackupIPs(context.Context) (admin.ClientEntryBackupIPList, error) {
	return f.list, f.err
}

func (f *fakeClientEntryBackupIPService) CreateClientEntryBackupIP(_ context.Context, request admin.ClientEntryBackupIPSaveRequest) (admin.ClientEntryBackupIPRecord, error) {
	f.createRequest = request
	return f.created, f.err
}

func (f *fakeClientEntryBackupIPService) CreateClientEntryBackupIPs(_ context.Context, request []admin.ClientEntryBackupIPSaveRequest) ([]admin.ClientEntryBackupIPRecord, error) {
	f.batchRequest = append([]admin.ClientEntryBackupIPSaveRequest(nil), request...)
	return f.createdBatch, f.err
}

func (f *fakeClientEntryBackupIPService) UpdateClientEntryBackupIP(_ context.Context, id int64, request admin.ClientEntryBackupIPSaveRequest) (admin.ClientEntryBackupIPRecord, error) {
	f.updateID, f.updateRequest = id, request
	return f.updated, f.err
}

func (f *fakeClientEntryBackupIPService) DeleteClientEntryBackupIP(_ context.Context, id int64) (bool, error) {
	f.deleteID = id
	return true, f.err
}

func (f *fakeClientEntryBackupIPService) RefreshClientEntryBackupIPs(_ context.Context, ids []int64) (admin.ClientEntryBackupIPRefreshResult, error) {
	f.refreshIDs = append([]int64(nil), ids...)
	return f.refresh, f.err
}

func clientEntryBackupIPRouter(service *fakeClientEntryBackupIPService) http.Handler {
	return NewRouter(
		config.Config{AdminPath: "control"},
		WithSessionService(&fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}),
		WithAdminService(service),
	)
}

func TestClientEntryBackupIPRoutesSupportCRUDAtomicBatchAndRefresh(t *testing.T) {
	service := &fakeClientEntryBackupIPService{
		fakeAdminService: &fakeAdminService{},
		list:             admin.ClientEntryBackupIPList{Items: []admin.ClientEntryBackupIPRecord{{ID: 1, IP: "192.0.2.1", Status: "available"}}},
		created:          admin.ClientEntryBackupIPRecord{ID: 2, IP: "192.0.2.2"},
		createdBatch:     []admin.ClientEntryBackupIPRecord{{ID: 3, IP: "192.0.2.3"}, {ID: 4, IP: "192.0.2.4"}},
		updated:          admin.ClientEntryBackupIPRecord{ID: 2, Name: "更新"},
		refresh:          admin.ClientEntryBackupIPRefreshResult{Updated: 2},
	}
	router := clientEntryBackupIPRouter(service)
	base := "/api/v1/control/dns-failover/entry-monitors/backup-ips"

	if rec := dnsFailoverRequest(router, http.MethodGet, base, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"available"`) {
		t.Fatalf("GET: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := dnsFailoverRequest(router, http.MethodPost, base, `{"ip":"192.0.2.2","port":443}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":2`) {
		t.Fatalf("POST single: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.createRequest.IP != "192.0.2.2" || service.createRequest.Port != 443 {
		t.Fatalf("single request = %#v", service.createRequest)
	}
	batch := `{"items":[{"ip":"192.0.2.3","port":443},{"ip":"192.0.2.4","port":8443}]}`
	if rec := dnsFailoverRequest(router, http.MethodPost, base, batch); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":4`) {
		t.Fatalf("POST batch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.batchRequest) != 2 || service.batchRequest[1].Port != 8443 {
		t.Fatalf("batch request = %#v", service.batchRequest)
	}
	if rec := dnsFailoverRequest(router, http.MethodPut, base+"/2", `{"name":"更新","ip":"192.0.2.2","port":443,"enabled":true}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "更新") {
		t.Fatalf("PUT: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.updateID != 2 || service.updateRequest.Enabled == nil || !*service.updateRequest.Enabled {
		t.Fatalf("update = %d %#v", service.updateID, service.updateRequest)
	}
	if rec := dnsFailoverRequest(router, http.MethodDelete, base+"/2", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) || service.deleteID != 2 {
		t.Fatalf("DELETE: status=%d body=%s id=%d", rec.Code, rec.Body.String(), service.deleteID)
	}
	if rec := dnsFailoverRequest(router, http.MethodPost, base+"/refresh", `{"ids":[3,4]}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updated":2`) {
		t.Fatalf("refresh: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(service.refreshIDs) != 2 || service.refreshIDs[0] != 3 {
		t.Fatalf("refresh ids = %#v", service.refreshIDs)
	}
}

func TestClientEntryBackupIPRoutesRejectUnknownFieldsAndMapErrors(t *testing.T) {
	base := "/api/v1/control/dns-failover/entry-monitors/backup-ips"
	service := &fakeClientEntryBackupIPService{fakeAdminService: &fakeAdminService{}}
	if rec := dnsFailoverRequest(clientEntryBackupIPRouter(service), http.MethodPost, base, `{"ip":"192.0.2.1","port":443,"probe_ids":[1]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := dnsFailoverRequest(clientEntryBackupIPRouter(service), http.MethodGet, base+"/bad", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: status=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, test := range []struct {
		err    error
		status int
	}{
		{admin.ErrClientEntryBackupIPNotFound, http.StatusNotFound},
		{admin.ErrClientEntryBackupIPConflict, http.StatusConflict},
		{admin.ErrClientEntryBackupIPInUse, http.StatusConflict},
		{admin.ErrUnavailable, http.StatusServiceUnavailable},
		{errors.New("invalid"), http.StatusBadRequest},
	} {
		service := &fakeClientEntryBackupIPService{fakeAdminService: &fakeAdminService{}, err: test.err}
		rec := dnsFailoverRequest(clientEntryBackupIPRouter(service), http.MethodGet, base, "")
		if rec.Code != test.status {
			t.Fatalf("error %v: status=%d body=%s", test.err, rec.Code, rec.Body.String())
		}
	}
}
