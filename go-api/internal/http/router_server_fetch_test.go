package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"

	"github.com/vmihailenco/msgpack/v5"
)

func int64Ptr(value int64) *int64 {
	return &value
}

func TestRouterServerUniProxyUserEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       7,
			NodeType: "vmess",
			GroupIDs: []int64{1, 2},
		},
		users: []nodeapi.AvailableUser{
			{ID: 9, UUID: "user-uuid", SpeedLimit: int64Ptr(100), DeviceLimit: int64Ptr(2)},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/user?token=secret&node_type=v2ray&node_id=7", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if nodeService.lastNode.NodeType != "vmess" || nodeService.lastNode.NodeID != 7 {
		t.Fatalf("unexpected node lookup: %#v", nodeService.lastNode)
	}
	if len(nodeService.lastGroupIDs) != 2 || nodeService.lastGroupIDs[0] != 1 || nodeService.lastGroupIDs[1] != 2 {
		t.Fatalf("unexpected group ids: %#v", nodeService.lastGroupIDs)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("expected etag header")
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	users, ok := payload["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("unexpected users payload: %#v", payload["users"])
	}
	user, ok := users[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected user payload: %#v", users[0])
	}
	if user["uuid"] != "user-uuid" || user["speed_limit"] != float64(100) || user["device_limit"] != float64(2) {
		t.Fatalf("unexpected user payload: %#v", user)
	}
}

func TestRouterServerUniProxyUserEndpointMsgpack(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       7,
			NodeType: "vmess",
			GroupIDs: []int64{1},
		},
		users: []nodeapi.AvailableUser{
			{ID: 9, UUID: "user-uuid"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/user?token=secret&node_type=v2ray&node_id=7", nil)
	req.Header.Set("X-Response-Format", "msgpack")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/x-msgpack") {
		t.Fatalf("expected msgpack content type, got %q", contentType)
	}

	var payload map[string]any
	if err := msgpack.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode msgpack: %v", err)
	}
	users, ok := payload["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("unexpected msgpack users payload: %#v", payload["users"])
	}
}

func TestRouterServerUniProxyConfigEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       7,
			NodeType: "vmess",
			RouteIDs: []int64{3},
			Fields: map[string]any{
				"server_port":     int64(8443),
				"network":         "ws",
				"networkSettings": map[string]any{"path": "/ws"},
				"tls":             int64(1),
			},
		},
		routes: []map[string]any{
			{"id": int64(3), "match": []any{"geosite:cn"}, "action": "direct"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/config?token=secret&node_type=v2ray&node_id=7", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	if payload["server_port"] != float64(8443) || payload["network"] != "ws" || payload["tls"] != float64(1) {
		t.Fatalf("unexpected config payload: %#v", payload)
	}
	baseConfig, ok := payload["base_config"].(map[string]any)
	if !ok || baseConfig["push_interval"] != float64(60) || baseConfig["pull_interval"] != float64(60) {
		t.Fatalf("unexpected base config: %#v", payload["base_config"])
	}
	routes, ok := payload["routes"].([]any)
	if !ok || len(routes) != 1 {
		t.Fatalf("unexpected routes: %#v", payload["routes"])
	}
}

func TestRouterServerUniProxyConfigEndpointKeepsVlessPrivateKeys(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       7,
			NodeType: "vless",
			Fields: map[string]any{
				"server_port": int64(8443),
				"network":     "tcp",
				"tls":         int64(2),
				"flow":        "xtls-rprx-vision",
				"tls_settings": map[string]any{
					"private_key": "tls-private",
					"public_key":  "tls-public",
				},
				"encryption": "none",
				"encryption_settings": map[string]any{
					"private_key": "enc-private",
					"public_key":  "enc-public",
				},
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/config?token=secret&node_type=vless&node_id=7", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	tlsSettings, ok := payload["tls_settings"].(map[string]any)
	if !ok || tlsSettings["private_key"] != "tls-private" {
		t.Fatalf("expected tls private key kept, got %#v", payload["tls_settings"])
	}
	encryptionSettings, ok := payload["encryption_settings"].(map[string]any)
	if !ok || encryptionSettings["private_key"] != "enc-private" {
		t.Fatalf("expected encryption private key kept, got %#v", payload["encryption_settings"])
	}
}

func TestRouterServerUniProxyConfigEndpointAdditionalProtocols(t *testing.T) {
	tests := []struct {
		name     string
		nodeType string
		fields   map[string]any
		check    func(t *testing.T, payload map[string]any)
	}{
		{
			name:     "shadowsocks",
			nodeType: "shadowsocks",
			fields: map[string]any{
				"server_port": int64(18866),
				"cipher":      "chacha20-ietf-poly1305",
				"obfs":        "http",
				"obfs_settings": map[string]any{
					"host": "cdn.example.com",
				},
			},
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["server_port"] != float64(18866) || payload["cipher"] != "chacha20-ietf-poly1305" || payload["obfs"] != "http" {
					t.Fatalf("unexpected shadowsocks payload: %#v", payload)
				}
				obfsSettings, ok := payload["obfs_settings"].(map[string]any)
				if !ok || obfsSettings["host"] != "cdn.example.com" {
					t.Fatalf("unexpected shadowsocks obfs settings: %#v", payload["obfs_settings"])
				}
			},
		},
		{
			name:     "trojan",
			nodeType: "trojan",
			fields: map[string]any{
				"host":        "edge.example.com",
				"network":     "tcp",
				"server_port": int64(12521),
				"server_name": "m.ctrip.com",
			},
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["host"] != "edge.example.com" || payload["network"] != "tcp" || payload["server_port"] != float64(12521) || payload["server_name"] != "m.ctrip.com" {
					t.Fatalf("unexpected trojan payload: %#v", payload)
				}
			},
		},
		{
			name:     "tuic",
			nodeType: "tuic",
			fields: map[string]any{
				"server_port":        int64(12330),
				"server_name":        "m.ctrip.com",
				"congestion_control": "cubic",
				"zero_rtt_handshake": int64(1),
			},
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["server_port"] != float64(12330) || payload["server_name"] != "m.ctrip.com" || payload["congestion_control"] != "cubic" || payload["zero_rtt_handshake"] != true {
					t.Fatalf("unexpected tuic payload: %#v", payload)
				}
			},
		},
		{
			name:     "hysteria",
			nodeType: "hysteria",
			fields: map[string]any{
				"version":         int64(2),
				"host":            "156.229.160.135(Ureq)",
				"server_port":     int64(443),
				"server_name":     "download.example.com",
				"up_mbps":         int64(0),
				"down_mbps":       int64(0),
				"obfs":            "",
				"obfs_password":   "",
			},
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["version"] != float64(2) || payload["server_port"] != float64(443) || payload["ignore_client_bandwidth"] != true {
					t.Fatalf("unexpected hysteria payload: %#v", payload)
				}
			},
		},
		{
			name:     "anytls",
			nodeType: "anytls",
			fields: map[string]any{
				"server_port":  int64(443),
				"server_name":  "m.ctrip.com",
				"padding_scheme": []any{"stop=8"},
			},
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["server_port"] != float64(443) || payload["server_name"] != "m.ctrip.com" {
					t.Fatalf("unexpected anytls payload: %#v", payload)
				}
				padding, ok := payload["padding_scheme"].([]any)
				if !ok || len(padding) != 1 || padding[0] != "stop=8" {
					t.Fatalf("unexpected anytls padding_scheme: %#v", payload["padding_scheme"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeService := &fakeNodeService{
				server: nodeapi.ServerRecord{
					ID:       7,
					NodeType: tt.nodeType,
					Fields:   tt.fields,
				},
			}
			router := NewRouter(
				config.Config{AppName: "forest-go", ServerToken: "secret"},
				WithNodeService(nodeService),
			)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/config?token=secret&node_type="+tt.nodeType+"&node_id=7", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("expected json body: %v", err)
			}
			baseConfig, ok := payload["base_config"].(map[string]any)
			if !ok || baseConfig["push_interval"] != float64(60) || baseConfig["pull_interval"] != float64(60) {
				t.Fatalf("unexpected base config: %#v", payload["base_config"])
			}
			tt.check(t, payload)
		})
	}
}

func TestRouterServerUniProxyAliveListEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{ID: 7, NodeType: "vmess"},
		alive:  map[int64]int64{11: 2},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/UniProxy/alivelist?token=secret&node_type=vmess&node_id=7", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	alive, ok := payload["alive"].(map[string]any)
	if !ok || alive["11"] != float64(2) {
		t.Fatalf("unexpected alive payload: %#v", payload["alive"])
	}
}

func TestRouterServerUniProxyAliveEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{ID: 7, NodeType: "vmess"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/UniProxy/alive?token=secret&node_type=vmess&node_id=7", strings.NewReader(`{"11":["1.1.1.1_1","2.2.2.2_1"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if nodeService.lastAlive.NodeID != 7 || nodeService.lastAlive.NodeType != "vmess" {
		t.Fatalf("unexpected alive target: %#v", nodeService.lastAlive)
	}
	if len(nodeService.lastAlive.Users[11]) != 2 {
		t.Fatalf("unexpected alive payload: %#v", nodeService.lastAlive.Users)
	}
}

func TestRouterServerDeepbworkUserEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       8,
			NodeType: "vmess",
			GroupIDs: []int64{1},
		},
		users: []nodeapi.AvailableUser{
			{ID: 9, UUID: "user-uuid", SpeedLimit: int64Ptr(100), DeviceLimit: int64Ptr(2)},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/Deepbwork/user?token=secret&node_id=8", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	if payload["msg"] != "ok" {
		t.Fatalf("unexpected response: %#v", payload)
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data payload: %#v", payload["data"])
	}
	user, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected user payload: %#v", data[0])
	}
	if _, exists := user["uuid"]; exists {
		t.Fatalf("expected uuid to be omitted, got %#v", user)
	}
	v2rayUser, ok := user["v2ray_user"].(map[string]any)
	if !ok || v2rayUser["uuid"] != "user-uuid" || v2rayUser["email"] != "user-uuid@v2board.user" {
		t.Fatalf("unexpected v2ray payload: %#v", user["v2ray_user"])
	}
}

func TestRouterServerShadowsocksUserEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       9,
			NodeType: "shadowsocks",
			GroupIDs: []int64{1},
			Fields: map[string]any{
				"server_port": int64(8443),
				"cipher":      "aes-128-gcm",
			},
		},
		users: []nodeapi.AvailableUser{
			{ID: 9, UUID: "user-uuid"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/ShadowsocksTidalab/user?token=secret&node_id=9", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data payload: %#v", payload["data"])
	}
	user, ok := data[0].(map[string]any)
	if !ok || user["secret"] != "user-uuid" || user["port"] != float64(8443) || user["cipher"] != "aes-128-gcm" {
		t.Fatalf("unexpected shadowsocks payload: %#v", data[0])
	}
}

func TestRouterServerTrojanUserEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       10,
			NodeType: "trojan",
			GroupIDs: []int64{1},
		},
		users: []nodeapi.AvailableUser{
			{ID: 9, UUID: "user-uuid", SpeedLimit: int64Ptr(100)},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/TrojanTidalab/user?token=secret&node_id=10", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json body: %v", err)
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data payload: %#v", payload["data"])
	}
	user, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected trojan payload: %#v", data[0])
	}
	if _, exists := user["uuid"]; exists {
		t.Fatalf("expected uuid to be omitted, got %#v", user)
	}
	trojanUser, ok := user["trojan_user"].(map[string]any)
	if !ok || trojanUser["password"] != "user-uuid" {
		t.Fatalf("unexpected trojan user payload: %#v", user["trojan_user"])
	}
}

func TestRouterServerDeepbworkConfigEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       8,
			NodeType: "vmess",
			Fields: map[string]any{
				"server_port":     int64(8443),
				"network":         "ws",
				"networkSettings": map[string]any{"path": "/ws"},
				"tls":             int64(1),
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/Deepbwork/config?token=secret&node_id=8&local_port=23333", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected config json: %v", err)
	}
	inbounds, ok := payload["inbounds"].([]any)
	if !ok || len(inbounds) < 2 {
		t.Fatalf("unexpected inbounds: %#v", payload["inbounds"])
	}
}

func TestRouterServerDeepbworkConfigEndpointUsesSnakeCaseFields(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       8,
			NodeType: "vmess",
			Fields: map[string]any{
				"server_port":   int64(8443),
				"network":       "ws",
				"network_settings": map[string]any{"path": "/ws-snake"},
				"tls":           int64(1),
				"tls_settings":  map[string]any{"serverName": "edge.example.com", "allowInsecure": int64(1)},
				"dns_settings":  map[string]any{"servers": []any{"8.8.8.8"}},
				"rule_settings": map[string]any{"domain": []any{"geosite:google"}},
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/Deepbwork/config?token=secret&node_id=8&local_port=23333", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected config json: %v", err)
	}

	inbounds, ok := payload["inbounds"].([]any)
	if !ok || len(inbounds) < 1 {
		t.Fatalf("unexpected inbounds: %#v", payload["inbounds"])
	}
	proxy, ok := inbounds[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected proxy inbound: %#v", inbounds[0])
	}
	streamSettings, ok := proxy["streamSettings"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected stream settings: %#v", proxy["streamSettings"])
	}
	wsSettings, ok := streamSettings["wsSettings"].(map[string]any)
	if !ok || wsSettings["path"] != "/ws-snake" {
		t.Fatalf("expected snake_case network settings to be used, got %#v", streamSettings["wsSettings"])
	}
	tlsSettings, ok := streamSettings["tlsSettings"].(map[string]any)
	if !ok || tlsSettings["serverName"] != "edge.example.com" || tlsSettings["allowInsecure"] != true {
		t.Fatalf("expected snake_case tls settings to be used, got %#v", streamSettings["tlsSettings"])
	}
	dnsSettings, ok := payload["dns"].(map[string]any)
	if !ok {
		t.Fatalf("expected dns settings, got %#v", payload["dns"])
	}
	servers, ok := dnsSettings["servers"].([]any)
	if !ok || len(servers) == 0 || servers[0] != "8.8.8.8" {
		t.Fatalf("expected snake_case dns settings to be used, got %#v", dnsSettings["servers"])
	}
	routing, ok := payload["routing"].(map[string]any)
	if !ok {
		t.Fatalf("expected routing settings, got %#v", payload["routing"])
	}
	rules, ok := routing["rules"].([]any)
	if !ok || len(rules) < 2 {
		t.Fatalf("expected extra routing rule, got %#v", routing["rules"])
	}
}

func TestRouterServerTrojanConfigEndpoint(t *testing.T) {
	nodeService := &fakeNodeService{
		server: nodeapi.ServerRecord{
			ID:       10,
			NodeType: "trojan",
			Fields: map[string]any{
				"server_port": int64(8443),
				"server_name": "node.example.com",
				"host":        "node.example.com",
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", ServerToken: "secret"},
		WithNodeService(nodeService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/TrojanTidalab/config?token=secret&node_id=10&local_port=10000", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected config json: %v", err)
	}
	if payload["local_port"] != float64(8443) {
		t.Fatalf("unexpected trojan local port: %#v", payload)
	}
	ssl, ok := payload["ssl"].(map[string]any)
	if !ok || ssl["sni"] != "node.example.com" {
		t.Fatalf("unexpected trojan ssl payload: %#v", payload["ssl"])
	}
}
