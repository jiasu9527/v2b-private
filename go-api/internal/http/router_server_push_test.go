package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
)

type fakeNodeService struct {
	lastPush       nodeapi.TrafficPushRequest
	lastNode       nodeapi.ServerLookupRequest
	lastAlive      nodeapi.AliveReportRequest
	lastSensitive  nodeapi.SensitiveAccessReportRequest
	lastGroupIDs   []int64
	lastRouteIDs   []int64
	err            error
	server         nodeapi.ServerRecord
	users          []nodeapi.AvailableUser
	routes         []map[string]any
	alive          map[int64]int64
	sensitiveStats map[string]any
}

func (f *fakeNodeService) PushTraffic(_ context.Context, req nodeapi.TrafficPushRequest) error {
	f.lastPush = req
	return f.err
}

func (f *fakeNodeService) LookupServer(_ context.Context, req nodeapi.ServerLookupRequest) (nodeapi.ServerRecord, error) {
	f.lastNode = req
	return f.server, f.err
}

func (f *fakeNodeService) TouchLastCheck(_ context.Context, nodeType string, nodeID int64) error {
	f.lastNode = nodeapi.ServerLookupRequest{NodeType: nodeType, NodeID: nodeID}
	return f.err
}

func (f *fakeNodeService) AvailableUsers(_ context.Context, groupIDs []int64) ([]nodeapi.AvailableUser, error) {
	f.lastGroupIDs = append([]int64(nil), groupIDs...)
	return f.users, f.err
}

func (f *fakeNodeService) Routes(_ context.Context, routeIDs []int64) ([]map[string]any, error) {
	f.lastRouteIDs = append([]int64(nil), routeIDs...)
	return f.routes, f.err
}

func (f *fakeNodeService) AliveList(_ context.Context) (map[int64]int64, error) {
	return f.alive, f.err
}

func (f *fakeNodeService) ReportAlive(_ context.Context, req nodeapi.AliveReportRequest) error {
	f.lastAlive = req
	return f.err
}

func (f *fakeNodeService) ReportSensitiveAccess(_ context.Context, req nodeapi.SensitiveAccessReportRequest) error {
	f.lastSensitive = req
	return f.err
}

func (f *fakeNodeService) SensitiveAccessStats(_ context.Context, limit int64) (map[string]any, error) {
	if f.sensitiveStats != nil {
		return f.sensitiveStats, f.err
	}
	return map[string]any{"limit": limit}, f.err
}

func TestRouterServerUniProxyPushEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/UniProxy/push?token=secret&node_type=v2ray&node_id=7", strings.NewReader(`{"10":[100,200]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if nodeService.lastPush.NodeType != "vmess" || nodeService.lastPush.NodeID != 7 {
		t.Fatalf("unexpected push target: %#v", nodeService.lastPush)
	}
	if len(nodeService.lastPush.Traffic) != 1 || nodeService.lastPush.Traffic[10].U != 100 || nodeService.lastPush.Traffic[10].D != 200 {
		t.Fatalf("unexpected push payload: %#v", nodeService.lastPush.Traffic)
	}
}

func TestRouterServerUniProxySensitiveEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{ID: 7, NodeType: "v2node"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/UniProxy/sensitive?token=secret&node_type=v2node&node_id=7", strings.NewReader(`[
		{"user_id":9,"domain":"example.com","rule":"suffix:example.com","client_ip":"203.0.113.9","count":3,"first_at":1700000000,"last_at":1700000060}
	]`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if nodeService.lastSensitive.NodeType != "v2node" || nodeService.lastSensitive.NodeID != 7 {
		t.Fatalf("unexpected sensitive target: %#v", nodeService.lastSensitive)
	}
	if len(nodeService.lastSensitive.Events) != 1 {
		t.Fatalf("unexpected sensitive events: %#v", nodeService.lastSensitive.Events)
	}
	event := nodeService.lastSensitive.Events[0]
	if event.UserID != 9 || event.Domain != "example.com" || event.Rule != "suffix:example.com" || event.ClientIP != "203.0.113.9" || event.Count != 3 {
		t.Fatalf("unexpected sensitive event: %#v", event)
	}
}

func TestRouterServerDeepbworkSubmitEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/Deepbwork/submit", strings.NewReader(`{"node_id":8,"token":"secret","data":[{"user_id":9,"u":11,"d":22}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if nodeService.lastPush.NodeType != "vmess" || nodeService.lastPush.NodeID != 8 {
		t.Fatalf("unexpected legacy push target: %#v", nodeService.lastPush)
	}
	if len(nodeService.lastPush.Traffic) != 1 || nodeService.lastPush.Traffic[9].U != 11 || nodeService.lastPush.Traffic[9].D != 22 {
		t.Fatalf("unexpected legacy push payload: %#v", nodeService.lastPush.Traffic)
	}
}
