package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

type fakeDNSProbeService struct {
	authenticatedSecrets []string
	heartbeats           []admin.DNSProbeHeartbeatRequest
	reports              []admin.DNSProbeResultsRequest
	tasks                []admin.DNSProbeTask
	heartbeatResult      admin.DNSProbeHeartbeatResult
	reportResult         admin.DNSProbeReportResult
	authErr              error
	heartbeatErr         error
	tasksErr             error
	reportErr            error
}

func (f *fakeDNSProbeService) AuthenticateDNSProbe(_ context.Context, rawSecret string) (admin.DNSProbeIdentity, error) {
	f.authenticatedSecrets = append(f.authenticatedSecrets, rawSecret)
	if f.authErr != nil || rawSecret != "good-secret" {
		return admin.DNSProbeIdentity{}, admin.ErrDNSProbeUnauthorized
	}
	return admin.DNSProbeIdentity{ID: 7}, nil
}

func (f *fakeDNSProbeService) HeartbeatDNSProbe(_ context.Context, probeID int64, request admin.DNSProbeHeartbeatRequest) (admin.DNSProbeHeartbeatResult, error) {
	if probeID != 7 {
		return admin.DNSProbeHeartbeatResult{}, errors.New("unexpected probe id")
	}
	f.heartbeats = append(f.heartbeats, request)
	return f.heartbeatResult, f.heartbeatErr
}

func (f *fakeDNSProbeService) ListDNSProbeTasks(_ context.Context, probeID int64) ([]admin.DNSProbeTask, error) {
	if probeID != 7 {
		return nil, errors.New("unexpected probe id")
	}
	if f.tasks == nil {
		return []admin.DNSProbeTask{}, f.tasksErr
	}
	return f.tasks, f.tasksErr
}

func (f *fakeDNSProbeService) ReportDNSProbeResults(_ context.Context, probeID int64, request admin.DNSProbeResultsRequest) (admin.DNSProbeReportResult, error) {
	if probeID != 7 {
		return admin.DNSProbeReportResult{}, errors.New("unexpected probe id")
	}
	f.reports = append(f.reports, request)
	if len(request.Results) > 500 {
		return admin.DNSProbeReportResult{}, admin.ErrDNSProbeInvalidRequest
	}
	return f.reportResult, f.reportErr
}

func TestProbeBearerAuthenticationIsStrictAndUniform(t *testing.T) {
	tests := []struct {
		name       string
		authValues []string
		authErr    error
		wantStatus int
	}{
		{name: "valid", authValues: []string{"Bearer good-secret"}, wantStatus: http.StatusOK},
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", authValues: []string{"Basic good-secret"}, wantStatus: http.StatusUnauthorized},
		{name: "wrong case", authValues: []string{"bearer good-secret"}, wantStatus: http.StatusUnauthorized},
		{name: "extra spaces", authValues: []string{"Bearer  good-secret"}, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", authValues: []string{"Bearer wrong-secret"}, wantStatus: http.StatusUnauthorized},
		{name: "disabled", authValues: []string{"Bearer good-secret"}, authErr: admin.ErrDNSProbeUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: "multiple headers", authValues: []string{"Bearer good-secret", "Bearer second"}, wantStatus: http.StatusUnauthorized},
		{name: "oversized token", authValues: []string{"Bearer " + strings.Repeat("x", 513)}, wantStatus: http.StatusUnauthorized},
	}
	var unauthorizedBody string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDNSProbeService{authErr: test.authErr}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithDNSProbeService(service))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/probe/tasks", nil)
			for _, value := range test.authValues {
				req.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusUnauthorized {
				if response.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("WWW-Authenticate = %q", response.Header().Get("WWW-Authenticate"))
				}
				if unauthorizedBody == "" {
					unauthorizedBody = response.Body.String()
				}
				if response.Body.String() != unauthorizedBody {
					t.Fatalf("unauthorized body differs: %q != %q", response.Body.String(), unauthorizedBody)
				}
			}
		})
	}
}

func TestProbeHeartbeatUsesCanonicalTrustedProxyIPAndRejectsTrailingJSON(t *testing.T) {
	service := &fakeDNSProbeService{heartbeatResult: admin.DNSProbeHeartbeatResult{PrewarmCount: 1}}
	router := NewRouter(config.Config{ProbeTrustedProxyCIDRs: []string{"192.0.2.0/24"}}, WithDNSProbeService(service))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/probe/heartbeat", strings.NewReader(`{"version":"v1.2.3","arch":"amd64"}`))
	req.Header.Set("Authorization", "Bearer good-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CF-Connecting-IP", "2001:0db8:0:0::7")
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(service.heartbeats) != 1 || service.heartbeats[0].PublicIP != "2001:db8::7" {
		t.Fatalf("heartbeats = %#v", service.heartbeats)
	}

	for _, body := range []string{
		`{"version":"v1","arch":"amd64"}{}`,
		`{"version":"v1","arch":"amd64","unknown":true}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/probe/heartbeat", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer good-secret")
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "203.0.113.7:1234"
		badResponse := httptest.NewRecorder()
		router.ServeHTTP(badResponse, request)
		if badResponse.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, response = %s", body, badResponse.Code, badResponse.Body.String())
		}
	}
	if len(service.heartbeats) != 1 {
		t.Fatalf("malformed heartbeat reached service: %#v", service.heartbeats)
	}
}

func TestProbeTasksAndResultsPublicRoutesUseJSONEnvelope(t *testing.T) {
	service := &fakeDNSProbeService{
		tasks:        []admin.DNSProbeTask{{TargetID: 11, GroupID: 3, CheckHost: "edge.example.com", CheckPort: 443, TCPTimeoutMS: 3000, CheckIntervalSec: 30}},
		reportResult: admin.DNSProbeReportResult{Accepted: 1, PrewarmCount: 2, GroupIDs: []int64{3}},
	}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithDNSProbeService(service))

	tasksRequest := httptest.NewRequest(http.MethodGet, "/api/v1/probe/tasks", nil)
	tasksRequest.Header.Set("Authorization", "Bearer good-secret")
	tasksResponse := httptest.NewRecorder()
	router.ServeHTTP(tasksResponse, tasksRequest)
	if tasksResponse.Code != http.StatusOK || !strings.Contains(tasksResponse.Body.String(), `"target_id":11`) {
		t.Fatalf("tasks response = %d %s", tasksResponse.Code, tasksResponse.Body.String())
	}

	resultsBody := `{"results":[{"result_id":"r1","target_id":11,"success":true,"latency_ms":17,"error":"","resolved_ip":"203.0.113.11"}]}`
	resultsRequest := httptest.NewRequest(http.MethodPost, "/api/v1/probe/results", strings.NewReader(resultsBody))
	resultsRequest.Header.Set("Authorization", "Bearer good-secret")
	resultsRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	resultsResponse := httptest.NewRecorder()
	router.ServeHTTP(resultsResponse, resultsRequest)
	if resultsResponse.Code != http.StatusOK || !strings.Contains(resultsResponse.Body.String(), `"accepted":1`) {
		t.Fatalf("results response = %d %s", resultsResponse.Code, resultsResponse.Body.String())
	}
	if len(service.reports) != 1 || len(service.reports[0].Results) != 1 || service.reports[0].Results[0].ResultID != "r1" {
		t.Fatalf("reports = %#v", service.reports)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(resultsResponse.Body.Bytes(), &envelope); err != nil || envelope["data"] == nil {
		t.Fatalf("invalid JSON envelope: %v, %s", err, resultsResponse.Body.String())
	}
}

func TestProbeResultsRejectMalformedTrailingOversizedAndBatchLimit(t *testing.T) {
	service := &fakeDNSProbeService{}
	router := NewRouter(config.Config{}, WithDNSProbeService(service))

	batchItems := make([]string, 501)
	for index := range batchItems {
		batchItems[index] = `{"result_id":"r` + strconv.Itoa(index+1) + `","target_id":11,"success":false,"latency_ms":null}`
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "malformed", body: `{"results":[`, wantStatus: http.StatusBadRequest},
		{name: "trailing", body: `{"results":[]} {}`, wantStatus: http.StatusBadRequest},
		{name: "oversized", body: `{"results":[],"padding":"` + strings.Repeat("x", (1<<20)+1) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "batch limit", body: `{"results":[` + strings.Join(batchItems, ",") + `]}`, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/probe/results", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer good-secret")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProbePOSTRoutesRequireJSONContentType(t *testing.T) {
	service := &fakeDNSProbeService{}
	router := NewRouter(config.Config{}, WithDNSProbeService(service))
	for _, path := range []string{"/api/v1/probe/heartbeat", "/api/v1/probe/results"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer good-secret")
		request.Header.Set("Content-Type", "text/plain")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}
}

func TestProbeRoutesRejectWrongMethods(t *testing.T) {
	service := &fakeDNSProbeService{}
	router := NewRouter(config.Config{}, WithDNSProbeService(service))
	for _, test := range []struct {
		method string
		path   string
		allow  string
	}{
		{method: http.MethodGet, path: "/api/v1/probe/heartbeat", allow: http.MethodPost},
		{method: http.MethodPost, path: "/api/v1/probe/tasks", allow: http.MethodGet},
		{method: http.MethodGet, path: "/api/v1/probe/results", allow: http.MethodPost},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s %s = %d allow %q", test.method, test.path, response.Code, response.Header().Get("Allow"))
		}
	}
}

func TestProbeResultsHeartbeatRequiredIsConflictNotServerError(t *testing.T) {
	service := &fakeDNSProbeService{reportErr: admin.ErrDNSProbeHeartbeatRequired}
	router := NewRouter(config.Config{}, WithDNSProbeService(service))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/probe/results", strings.NewReader(`{"results":[]}`))
	request.Header.Set("Authorization", "Bearer good-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "心跳") {
		t.Fatalf("heartbeat-required response is not actionable: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "good-secret") || strings.Contains(response.Body.String(), "probe 7") {
		t.Fatalf("response leaked credential or probe identity: %s", response.Body.String())
	}
}

func TestProbeRequestIPRequiresTrustedSocketPeer(t *testing.T) {
	tests := []struct {
		name       string
		trusted    []string
		cf         string
		forwarded  string
		realIP     string
		remoteAddr string
		want       string
	}{
		{name: "trusted cloudflare first", trusted: []string{"10.0.0.0/8"}, cf: "2001:0db8::8", forwarded: "203.0.113.9, 10.0.0.2", remoteAddr: "10.0.0.1:80", want: "2001:db8::8"},
		{name: "default docker bridge proxy", forwarded: "210.16.170.148, 172.18.0.1", remoteAddr: "172.18.0.1:80", want: "210.16.170.148"},
		{name: "trusted forwarded first item", trusted: []string{"10.0.0.0/8"}, forwarded: "203.0.113.9, not-an-ip", remoteAddr: "10.0.0.1:80", want: "203.0.113.9"},
		{name: "invalid forwarded first item rejects whole header", trusted: []string{"10.0.0.0/8"}, forwarded: "invalid, 203.0.113.9", realIP: "198.51.100.5", remoteAddr: "10.0.0.1:80", want: "198.51.100.5"},
		{name: "untrusted public peer ignores spoofed cloudflare", trusted: []string{"10.0.0.0/8"}, cf: "203.0.113.8", forwarded: "203.0.113.9", remoteAddr: "198.51.100.7:443", want: "198.51.100.7"},
		{name: "trusted ipv6 proxy", trusted: []string{"2001:db8:1::/48"}, cf: "2001:db8:2::8", remoteAddr: "[2001:db8:1::7]:443", want: "2001:db8:2::8"},
		{name: "invalid CIDR ignored", trusted: []string{"not-a-cidr"}, cf: "203.0.113.8", remoteAddr: "10.0.0.1:80", want: "10.0.0.1"},
		{name: "invalid peer", trusted: []string{"127.0.0.0/8"}, cf: "203.0.113.8", remoteAddr: "not-an-ip", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("CF-Connecting-IP", test.cf)
			request.Header.Set("X-Forwarded-For", test.forwarded)
			request.Header.Set("X-Real-IP", test.realIP)
			request.RemoteAddr = test.remoteAddr
			resolver := newProbeRequestIPResolver(test.trusted)
			if got := resolver.Resolve(request); got != test.want {
				t.Fatalf("probe request IP = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGlobalRequestIPKeepsLegacyBehaviorSeparateFromProbeTrust(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.7:443"
	request.Header.Set("CF-Connecting-IP", "203.0.113.8")
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")
	if got := requestIP(request); got != "203.0.113.9" {
		t.Fatalf("global requestIP = %q, want legacy XFF behavior", got)
	}
}
