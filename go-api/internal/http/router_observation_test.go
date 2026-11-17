package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
)

type fakeObservationNodeService struct {
	fakeNodeService
	report        nodeapi.ObservationReportRequest
	inserted      int64
	reportErr     error
	configEnabled bool
	configMode    string
	configUsers   []int64
}

func (f *fakeObservationNodeService) ProvisionObservation(context.Context, string, int64, bool, string, []int64) (string, error) {
	return "", nil
}
func (f *fakeObservationNodeService) ObservationNodes(context.Context) ([]map[string]any, error) {
	return nil, nil
}
func (f *fakeObservationNodeService) ObservationNodeConfig(context.Context, string, int64) (nodeapi.ObservationNodeSettings, error) {
	mode := f.configMode
	if mode == "" {
		mode = nodeapi.ObservationModeAllowlist
	}
	return nodeapi.ObservationNodeSettings{Configured: f.configUsers != nil || f.configMode != "", Enabled: f.configEnabled, Mode: mode, AllowedUserIDs: f.configUsers}, nil
}
func (f *fakeObservationNodeService) ReportObservation(_ context.Context, req nodeapi.ObservationReportRequest) (int64, error) {
	f.report = req
	return f.inserted, f.reportErr
}
func (f *fakeObservationNodeService) ObservationEvents(context.Context, nodeapi.ObservationFilter) (map[string]any, error) {
	return nil, nil
}
func (f *fakeObservationNodeService) ObservationSummary(context.Context, nodeapi.ObservationFilter) (map[string]any, error) {
	return nil, nil
}
func (f *fakeObservationNodeService) RunObservationMaintenance(context.Context) {}

func TestObservationReportHasBoundedStrictPayloadAndHeaderToken(t *testing.T) {
	svc := &fakeObservationNodeService{inserted: 1}
	router := NewRouter(config.Config{}, WithNodeService(svc))
	path := "/api/v1/server/UniProxy/observation?node_type=v2node&node_id=7"
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"unknown field", `{"events":[{"event_id":"event_123","user_id":9,"occurred_at":1,"target_host":"example.com","target_port":443,"network":"tcp","result":"connected","secret":"oops"}]}`, http.StatusBadRequest},
		{"oversized", strings.Repeat("x", maxObservationJSONBody+1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	body := `{"events":[{"event_id":"event_123","user_id":9,"occurred_at":` + strconv.FormatInt(time.Now().Unix(), 10) + `,"target_host":"example.com","target_port":443,"network":"tcp","result":"route_attempt"}]}`
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Observation-Token", "private-node-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || svc.report.Token != "private-node-token" || svc.report.NodeID != 7 || len(svc.report.Events) != 1 {
		t.Fatalf("status=%d request=%+v", rec.Code, svc.report)
	}
	// A removed user's queued event is invalid, not a bad node token. The
	// node can split a 400 batch and continue reporting later valid events.
	svc.reportErr = nodeapi.ErrObservationUserNotAllowed
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Observation-Token", "private-node-token")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("removed user status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestObservationNodeUserFetchFiltersAllowlist(t *testing.T) {
	svc := &fakeObservationNodeService{
		fakeNodeService: fakeNodeService{
			server: nodeapi.ServerRecord{ID: 7, NodeType: "v2node", GroupIDs: []int64{1}},
			users:  []nodeapi.AvailableUser{{ID: 10, UUID: "allowed"}, {ID: 11, UUID: "excluded"}},
		},
		configEnabled: true,
		configUsers:   []int64{10},
	}
	router := NewRouter(config.Config{ServerToken: "shared"}, WithNodeService(svc))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/user?token=shared&node_type=v2node&node_id=7", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "allowed") || strings.Contains(rec.Body.String(), "excluded") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/config?token=shared&node_type=v2node&node_id=7", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"observation_audit":{"allowed_user_ids":[10],"enable":true,"mode":"allowlist","report_interval":60}`) {
		t.Fatalf("config status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=shared&node_id=7", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"observation_audit":{"allowed_user_ids":[10],"enable":true,"mode":"allowlist","report_interval":60}`) {
		t.Fatalf("legacy config status=%d body=%s", rec.Code, rec.Body.String())
	}
	svc.configEnabled = false
	svc.configUsers = []int64{}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/user?token=shared&node_type=v2node&node_id=7", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "allowed") || strings.Contains(rec.Body.String(), "excluded") {
		t.Fatalf("empty paused allowlist did not revoke users: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=shared&node_id=7", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"observation_audit":{"allowed_user_ids":[],"enable":false,"mode":"allowlist","report_interval":60}`) {
		t.Fatalf("empty paused allowlist config status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestObservationAllModeRetainsEveryEligibleNodeUser(t *testing.T) {
	svc := &fakeObservationNodeService{
		fakeNodeService: fakeNodeService{
			server: nodeapi.ServerRecord{ID: 7, NodeType: "v2node", GroupIDs: []int64{1}},
			users:  []nodeapi.AvailableUser{{ID: 10, UUID: "first-user"}, {ID: 11, UUID: "second-user"}},
		},
		configMode:    nodeapi.ObservationModeAll,
		configUsers:   []int64{},
		configEnabled: true,
	}
	router := NewRouter(config.Config{ServerToken: "shared"}, WithNodeService(svc))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/user?token=shared&node_type=v2node&node_id=7", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "first-user") || !strings.Contains(rec.Body.String(), "second-user") {
		t.Fatalf("all-mode user fetch status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=shared&node_id=7", nil)
	req.Header.Set("X-Observation-Capability", observationAllCapability)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"observation_audit":{"allowed_user_ids":[],"enable":true,"mode":"all","report_interval":60}`) {
		t.Fatalf("all-mode config status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v2/server/config?token=shared&node_id=7", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired || strings.Contains(rec.Body.String(), `"observation_audit"`) {
		t.Fatalf("legacy node must be rejected without empty-list config: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
