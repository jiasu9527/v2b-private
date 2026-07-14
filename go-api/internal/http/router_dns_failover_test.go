package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func dnsFailoverRouter(service *fakeAdminService) http.Handler {
	return NewRouter(config.Config{AdminPath: "control"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}), WithAdminService(service))
}

func dnsFailoverRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestDNSFailoverRoutesRejectUnauthenticatedRequests(t *testing.T) {
	router := NewRouter(config.Config{AdminPath: "control"}, WithSessionService(&fakeSessionService{authErr: session.ErrUnauthorized}), WithAdminService(&fakeAdminService{}))
	rec := dnsFailoverRequest(router, http.MethodGet, "/api/v1/control/dns-failover/settings", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDNSFailoverSettingsAndProbeSecretAreMappedSafely(t *testing.T) {
	service := &fakeAdminService{dnsFailoverSettings: admin.DNSFailoverSettings{ProbeAPIURL: "https://probe.example/api"}, dnsProbeCreate: admin.DNSProbeCreateResult{Probe: admin.DNSProbeRecord{ID: 7, Name: "cn-a"}, Secret: "s p'ec"}, dnsProbes: []admin.DNSProbeRecord{{ID: 7, Name: "cn-a"}}}
	router := dnsFailoverRouter(service)
	for _, tc := range []struct{ method, path, body string }{{http.MethodGet, "/api/v1/control/dns-failover/settings", ""}, {http.MethodPut, "/api/v1/control/dns-failover/settings", `{"dns_probe_api_url":"https://new.example"}`}, {http.MethodPost, "/api/v1/control/dns-failover/probes", `{"name":"cn-a"}`}, {http.MethodGet, "/api/v1/control/dns-failover/probes", ""}} {
		rec := dnsFailoverRequest(router, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if tc.method == http.MethodPost && (!strings.Contains(rec.Body.String(), `"secret":"s p'ec"`) || !strings.Contains(rec.Body.String(), "/probe/install.sh")) {
			t.Fatalf("create response=%s", rec.Body.String())
		}
		if tc.method == http.MethodGet && tc.path != "/api/v1/control/dns-failover/settings" && strings.Contains(rec.Body.String(), "s p'ec") {
			t.Fatalf("listed secret leaked: %s", rec.Body.String())
		}
	}
	if service.lastDNSFailoverSettingsSave.ProbeAPIURL != "https://new.example" || service.lastDNSProbeCreate.Name != "cn-a" {
		t.Fatalf("request mapping failed: %#v %#v", service.lastDNSFailoverSettingsSave, service.lastDNSProbeCreate)
	}
}

func TestDNSFailoverRuleEventsAndManualRoutesMapRequests(t *testing.T) {
	service := &fakeAdminService{dnsRule: admin.DNSFailoverRuleRecord{ID: 9}, dnsEvents: admin.DNSFailoverEventListResult{Total: 31, Current: 2, PageSize: 10}}
	router := dnsFailoverRouter(service)
	requests := []struct{ method, path, body string }{{http.MethodGet, "/api/v1/control/dns-failover/rules", ""}, {http.MethodGet, "/api/v1/control/dns-failover/rules/9", ""}, {http.MethodPost, "/api/v1/control/dns-failover/rules", `{"name":"r","domain_id":1,"domain":"example.com","record_id":2,"subdomain":"@","targets":[{"sort":0,"name":"primary","dns_type":"A","dns_value":"1.1.1.1","check_host":"1.1.1.1","check_port":443,"enabled":true}],"probe_ids":[7]}`}, {http.MethodPut, "/api/v1/control/dns-failover/rules/9", `{"name":"r","domain_id":1,"domain":"example.com","record_id":2,"subdomain":"@","targets":[{"id":2,"sort":3,"name":"primary","dns_type":"A","dns_value":"1.1.1.1","check_host":"1.1.1.1","check_port":443,"enabled":true}]}`}, {http.MethodPatch, "/api/v1/control/dns-failover/rules/9/enabled", `{"enabled":false}`}, {http.MethodPost, "/api/v1/control/dns-failover/rules/9/manual-switch", `{"target_id":2}`}, {http.MethodDelete, "/api/v1/control/dns-failover/rules/9", ""}, {http.MethodGet, "/api/v1/control/dns-failover/events?group=9&event_type=switch&current=2&page_size=10", ""}}
	for _, tc := range requests {
		rec := dnsFailoverRequest(router, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	if service.lastDNSRuleSave.ID == nil || *service.lastDNSRuleSave.ID != 9 || len(service.lastDNSRuleSave.Targets) != 1 || service.lastDNSRuleSave.Targets[0].Sort != 3 {
		t.Fatalf("rule mapping=%#v", service.lastDNSRuleSave)
	}
	if service.lastDNSManualGroupID != 9 || service.lastDNSManualTargetID != 2 {
		t.Fatalf("manual mapping=%d/%d", service.lastDNSManualGroupID, service.lastDNSManualTargetID)
	}
	if service.lastDNSEvents.GroupID == nil || *service.lastDNSEvents.GroupID != 9 || service.lastDNSEvents.EventType != "switch" || service.lastDNSEvents.Current != 2 || service.lastDNSEvents.PageSize != 10 {
		t.Fatalf("events mapping=%#v", service.lastDNSEvents)
	}
}

func TestDNSFailoverProbeEnabledAndRevokeMapToDisable(t *testing.T) {
	service := &fakeAdminService{}
	router := dnsFailoverRouter(service)
	if rec := dnsFailoverRequest(router, http.MethodPatch, "/api/v1/control/dns-failover/probes/7/enabled", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if service.lastDNSProbeID != 7 || !service.lastDNSProbeEnabled {
		t.Fatalf("enable mapping: id=%d enabled=%v", service.lastDNSProbeID, service.lastDNSProbeEnabled)
	}
	if rec := dnsFailoverRequest(router, http.MethodPatch, "/api/v1/control/dns-failover/probes/7/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if service.lastDNSProbeID != 7 || service.lastDNSProbeEnabled {
		t.Fatalf("revoke mapping: id=%d enabled=%v", service.lastDNSProbeID, service.lastDNSProbeEnabled)
	}
}

func TestDNSFailoverRoutesRejectMalformedJSONAndSurfaceManualConflict(t *testing.T) {
	service := &fakeAdminService{err: errors.New("busy DNS failover group")}
	router := dnsFailoverRouter(service)
	rec := dnsFailoverRequest(router, http.MethodPost, "/api/v1/control/dns-failover/probes", `{"name":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = dnsFailoverRequest(router, http.MethodPost, "/api/v1/control/dns-failover/rules/1/manual-switch", `{"target_id":2}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("manual status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if !strings.Contains(payload["message"].(string), "进行") {
		t.Fatalf("manual message=%#v", payload)
	}
}

func TestDNSFailoverInstallCommandEscapesURLAndParameters(t *testing.T) {
	command := dnsFailoverInstallCommand("https://probe.example/a b/", "secret'; touch /tmp/pwned", "cn west")
	for _, want := range []string{"https://probe.example/a b/probe/install.sh", "--api-url", "--token", "--name", "'\\\"'\\\"'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("install command missing %q: %s", want, command)
		}
	}
}
