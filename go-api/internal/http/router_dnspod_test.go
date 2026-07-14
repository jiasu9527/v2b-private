package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/dnspod"
	"forest/go-api/internal/session"
)

func newDNSPodAdminRouter(adminService *fakeAdminService) (http.Handler, *fakeSessionService) {
	sessions := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}
	return NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessions),
		WithAdminService(adminService),
	), sessions
}

func TestRouterAdminDNSPodConfigNeverReturnsSecretKey(t *testing.T) {
	service := &fakeAdminService{dnspodConfig: admin.DNSPodConfigStatus{Configured: true, SecretIDMasked: "AKID12****7890", Source: "config", Edition: dnspod.EditionInternational}}
	router, sessions := newDNSPodAdminRouter(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/dns/config?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !sessions.lastRequireAdmin || !strings.Contains(rec.Body.String(), `"secret_id_masked":"AKID12****7890"`) || strings.Contains(strings.ToLower(rec.Body.String()), "secret_key") {
		t.Fatalf("unexpected config response: %s", rec.Body.String())
	}
}

func TestRouterAdminDNSPodConfigSavePassesTokenCredentials(t *testing.T) {
	service := &fakeAdminService{dnspodConfig: admin.DNSPodConfigStatus{Configured: true, Edition: dnspod.EditionInternational}}
	router, _ := newDNSPodAdminRouter(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/dns/config/save", strings.NewReader(`{"auth_data":"jwt-admin","auth_type":"token","api_token":"730060,token-value","edition":"international","verify":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if service.lastDNSPodConfigSave.Edition != dnspod.EditionInternational || service.lastDNSPodConfigSave.AuthType != dnspod.AuthTypeToken || service.lastDNSPodConfigSave.APIToken != "730060,token-value" || !service.lastDNSPodConfigSave.Verify {
		t.Fatalf("unexpected DNSPod config save: %#v", service.lastDNSPodConfigSave)
	}
}

func TestRouterAdminDNSPodDomainAndRecordLists(t *testing.T) {
	service := &fakeAdminService{
		dnspodDomains: dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{DomainID: 7, Name: "example.com", Grade: "DP_FREE"}}, Total: 1},
		dnspodRecords: dnspod.DescribeRecordListResult{Records: []dnspod.Record{{RecordID: 8, Name: "www", Type: "A", Value: "192.0.2.1"}}, Total: 1},
	}
	router, _ := newDNSPodAdminRouter(service)

	domainReq := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/dns/domain/list?auth_data=jwt-admin&current=2&page_size=25&keyword=example", nil)
	domainRec := httptest.NewRecorder()
	router.ServeHTTP(domainRec, domainReq)
	if domainRec.Code != http.StatusOK || service.lastDNSPodDomainList.Current != 2 || service.lastDNSPodDomainList.PageSize != 25 || !strings.Contains(domainRec.Body.String(), `"example.com"`) {
		t.Fatalf("unexpected domain response=%d %s request=%#v", domainRec.Code, domainRec.Body.String(), service.lastDNSPodDomainList)
	}

	recordReq := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/dns/record/list?auth_data=jwt-admin&domain=example.com&domain_id=7&current=1&page_size=50&record_type=A&keyword=www", nil)
	recordRec := httptest.NewRecorder()
	router.ServeHTTP(recordRec, recordReq)
	if recordRec.Code != http.StatusOK || service.lastDNSPodRecordList.Domain != "example.com" || service.lastDNSPodRecordList.DomainID != 7 || service.lastDNSPodRecordList.RecordType != "A" || !strings.Contains(recordRec.Body.String(), `"192.0.2.1"`) {
		t.Fatalf("unexpected record response=%d %s request=%#v", recordRec.Code, recordRec.Body.String(), service.lastDNSPodRecordList)
	}
}

func TestRouterAdminDNSPodSaveRecordParsesAllFields(t *testing.T) {
	service := &fakeAdminService{dnspodMutation: dnspod.RecordMutationResult{RecordID: 91}}
	router, _ := newDNSPodAdminRouter(service)
	body := `{"auth_data":"jwt-admin","domain":"example.com","domain_id":7,"record_id":8,"sub_domain":"www","record_type":"A","record_line":"默认","record_line_id":"0=0","value":"192.0.2.2","ttl":600,"mx":10,"weight":20}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/dns/record/save", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := service.lastDNSPodRecordSave
	if got.Domain != "example.com" || got.DomainID != 7 || got.RecordID != 8 || got.RecordLineID != "0=0" || got.TTL != 600 || got.MX != 10 || got.Weight == nil || *got.Weight != 20 {
		t.Fatalf("unexpected record save: %#v", got)
	}
}

func TestRouterAdminDNSPodDeleteAndStatusPassDomainID(t *testing.T) {
	service := &fakeAdminService{}
	router, _ := newDNSPodAdminRouter(service)

	deleteReq := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/dns/record/delete", strings.NewReader(`{"auth_data":"jwt-admin","domain":"example.com","domain_id":7,"record_id":8}`))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK || service.lastDNSPodDeleteDomainID != 7 || service.lastDNSPodDeleteID != 8 {
		t.Fatalf("unexpected delete response=%d %s domain_id=%d record_id=%d", deleteRec.Code, deleteRec.Body.String(), service.lastDNSPodDeleteDomainID, service.lastDNSPodDeleteID)
	}

	statusReq := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/dns/record/status", strings.NewReader(`{"auth_data":"jwt-admin","domain":"example.com","domain_id":7,"record_id":8,"status":"disable"}`))
	statusReq.Header.Set("Content-Type", "application/json")
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || service.lastDNSPodStatusDomainID != 7 || service.lastDNSPodStatusID != 8 || service.lastDNSPodStatus != "DISABLE" {
		t.Fatalf("unexpected status response=%d %s domain_id=%d record_id=%d status=%s", statusRec.Code, statusRec.Body.String(), service.lastDNSPodStatusDomainID, service.lastDNSPodStatusID, service.lastDNSPodStatus)
	}
}

func TestRouterAdminDNSPodRejectsInvalidMutationAndWrongMethod(t *testing.T) {
	service := &fakeAdminService{}
	router, _ := newDNSPodAdminRouter(service)

	wrongMethod := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/dns/record/save?auth_data=jwt-admin", nil)
	wrongMethodRec := httptest.NewRecorder()
	router.ServeHTTP(wrongMethodRec, wrongMethod)
	if wrongMethodRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", wrongMethodRec.Code, wrongMethodRec.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/dns/record/delete", strings.NewReader(`{"auth_data":"jwt-admin","domain":"example.com","record_id":0}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalidRec := httptest.NewRecorder()
	router.ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", invalidRec.Code, invalidRec.Body.String())
	}
}
