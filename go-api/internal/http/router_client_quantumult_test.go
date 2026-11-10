package httpapi

import (
	"encoding/base64"
	"strings"
	"testing"

	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/subscribelink"
)

const quantumultXTestUUID = "11111111-2222-3333-4444-555555555555"
const quantumultXTestRealityKey = "k4Uxez0sjl8bKaZH2Vgi8-WDFshML51QkxKFLWFIONk"

func quantumultXNode(overrides map[string]any) map[string]any {
	node := map[string]any{"type": "vmess", "id": 7, "name": "测试入口", "host": "192.0.2.7", "port": 54101, "network": "tcp"}
	for key, value := range overrides {
		node[key] = value
	}
	return node
}

func TestQuantumultXNativeNodes(t *testing.T) {
	cases := []struct {
		name string
		node map[string]any
		want []string
		omit []string
	}{
		{"vmess_tcp", nil, []string{"vmess=192.0.2.7:54101,", "method=chacha20-poly1305", "password=" + quantumultXTestUUID, "aead=true", "udp-relay=true", "tag=测试入口"}, []string{"tls", "obfs", "username="}},
		{"v2node_alias", map[string]any{"type": "v2node", "protocol": "v2ray"}, []string{"vmess="}, nil},
		{"ipv6", map[string]any{"host": "2001:db8::7", "port": "443"}, []string{"vmess=[2001:db8::7]:443,"}, nil},
		{"vmess_tls_aliases", map[string]any{"tls": true, "tlsSettings": map[string]any{"serverName": "tls.example.test", "allowInsecure": true}}, []string{"obfs=over-tls", "obfs-host=tls.example.test", "tls-verification=false"}, []string{"skip-cert-verify", "tls-host="}},
		{"vmess_ws", map[string]any{"network": "ws", "networkSettings": map[string]any{"path": "/node", "security": "aes-128-gcm", "headers": map[string]any{"Host": "ws.example.test"}}}, []string{"obfs=ws,", "obfs-uri=/node", "obfs-host=ws.example.test", "method=aes-128-gcm"}, []string{"tls-verification"}},
		{"vmess_wss", map[string]any{"network": "ws", "tls": 1, "network_settings": map[string]any{"path": "/node", "headers": map[string]any{"Host": "ws.example.test"}}, "tls_settings": map[string]any{"server_name": "ws.example.test", "alpn": []string{"h2", "http/1.1"}}}, []string{"obfs=wss", "tls-verification=true", "tls-alpn=02683208687474702f312e31"}, []string{"tls-host="}},
		{"vmess_http", map[string]any{"network_settings": map[string]any{"header": map[string]any{"type": "http", "request": map[string]any{"headers": map[string]any{"Host": []any{"http.example.test"}}, "path": []any{"/path"}}}}}, []string{"obfs=http", "obfs-host=http.example.test", "obfs-uri=/path"}, nil},
		{"vmess_legacy_aead", map[string]any{"alterId": 64}, []string{"aead=false"}, nil},
		{"ss", map[string]any{"type": "ss", "cipher": "aes-128-gcm"}, []string{"shadowsocks=192.0.2.7:54101,", "method=aes-128-gcm", "password=" + quantumultXTestUUID}, []string{"aead"}},
		{"ss_simple_tls_not_real_tls", map[string]any{"type": "ss", "cipher": "aes-256-gcm", "obfs": "tls", "obfs-host": "obfs.example.test"}, []string{"obfs=tls,", "obfs-host=obfs.example.test"}, []string{"tls-verification", "over-tls"}},
		{"ss_http", map[string]any{"type": "ss", "cipher": "aes-256-gcm", "obfs": "http", "obfs-host": "obfs.example.test", "obfs-path": "/simple"}, []string{"obfs=http", "obfs-uri=/simple"}, nil},
		{"trojan_tls", map[string]any{"type": "trojan", "server_name": "tls.example.test", "allow_insecure": 1}, []string{"trojan=", "over-tls=true", "tls-host=tls.example.test", "tls-verification=false"}, []string{"obfs="}},
		{"trojan_wss", map[string]any{"type": "trojan", "network": "ws", "server_name": "ws.example.test", "network_settings": map[string]any{"path": "/trojan", "headers": map[string]any{"Host": "ws.example.test"}}}, []string{"trojan=", "obfs=wss", "obfs-host=ws.example.test", "obfs-uri=/trojan"}, []string{"over-tls", "tls-host="}},
		{"vless_plain", map[string]any{"type": "vless", "encryption": "none"}, []string{"vless=", "method=none"}, []string{"aead", "obfs="}},
		{"vless_reality_vision", map[string]any{"type": "v2node", "protocol": "vless", "tls": "2", "flow": "xtls-rprx-vision", "tls_settings": map[string]any{"server_name": "reality.example.test", "public_key": quantumultXTestRealityKey, "short_id": "0123456789abcdef"}}, []string{"vless=", "vless-flow=xtls-rprx-vision", "obfs=over-tls", "reality-base64-pubkey=" + quantumultXTestRealityKey, "reality-hex-shortid=0123456789abcdef", "fast-open=false"}, nil},
		{"anytls", map[string]any{"type": "anytls", "server_name": "tls.example.test", "insecure": true}, []string{"anytls=", "over-tls=true", "tls-host=tls.example.test", "tls-verification=false"}, []string{"obfs="}},
		{"safe_tag", map[string]any{"name": "测试,入口\r\n注入"}, []string{"tag=测试，入口  注入"}, []string{"\n", "\r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, ok := buildQuantumultXProxyLine(quantumultXTestUUID, quantumultXNode(tc.node))
			if !ok {
				t.Fatal("supported node was skipped")
			}
			for _, want := range tc.want {
				if !strings.Contains(line, want) {
					t.Errorf("missing %q in %q", want, line)
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(line, omit) {
					t.Errorf("unexpected %q in %q", omit, line)
				}
			}
		})
	}
}

func TestQuantumultXSkipsUnrepresentableNodes(t *testing.T) {
	cases := []struct {
		name string
		node map[string]any
	}{
		{"hysteria2", map[string]any{"type": "hysteria2"}},
		{"tuic", map[string]any{"type": "tuic"}},
		{"grpc", map[string]any{"network": "grpc"}},
		{"http_transport", map[string]any{"network": "http"}},
		{"ss_http_transport", map[string]any{"type": "ss", "cipher": "aes-256-gcm", "network": "http"}},
		{"xhttp", map[string]any{"network": "xhttp"}},
		{"httpupgrade", map[string]any{"network": "httpupgrade"}},
		{"unknown_network", map[string]any{"network": "future"}},
		{"trojan_grpc", map[string]any{"type": "trojan", "network": "grpc"}},
		{"anytls_ws", map[string]any{"type": "anytls", "network": "ws"}},
		{"wss_host_sni_conflict", map[string]any{"network": "ws", "tls": 1, "tls_settings": map[string]any{"server_name": "tls.example.test"}, "network_settings": map[string]any{"headers": map[string]any{"Host": "different.example.test"}}}},
		{"ws_custom_headers", map[string]any{"network": "ws", "network_settings": map[string]any{"headers": map[string]any{"X-Required": "secret"}}}},
		{"path_comma", map[string]any{"network": "ws", "network_settings": map[string]any{"path": "/hello,tag=other"}}},
		{"path_line_injection", map[string]any{"network": "ws", "network_settings": map[string]any{"path": "/hello\nvmess=evil"}}},
		{"host_injection", map[string]any{"host": "node.test,tag=evil"}},
		{"invalid_ipv6", map[string]any{"host": "example.test:123"}},
		{"ipv6_bad_brackets", map[string]any{"host": "[[2001:db8::7]]"}},
		{"zero_port", map[string]any{"port": 0}},
		{"large_port", map[string]any{"port": 65536}},
		{"fractional_port", map[string]any{"port": 443.5}},
		{"port_hopping", map[string]any{"port": "443-445"}},
		{"vless_encryption", map[string]any{"type": "vless", "encryption": "mlkem768x25519plus"}},
		{"vless_bad_flow", map[string]any{"type": "vless", "tls": 1, "flow": "future"}},
		{"vless_vision_no_tls", map[string]any{"type": "vless", "flow": "xtls-rprx-vision"}},
		{"vless_vision_ws", map[string]any{"type": "vless", "tls": 1, "network": "ws", "flow": "xtls-rprx-vision"}},
		{"ech", map[string]any{"tls": 1, "tls_settings": map[string]any{"ech": "cloudflare"}}},
		{"ech_config", map[string]any{"tls": 1, "tls_settings": map[string]any{"ech_config": "AEXAMPLE"}}},
		{"reality_no_key", map[string]any{"tls": 2}},
		{"reality_bad_key", map[string]any{"tls": 2, "tls_settings": map[string]any{"public_key": "bad"}}},
		{"reality_bad_shortid", map[string]any{"tls": 2, "tls_settings": map[string]any{"public_key": quantumultXTestRealityKey, "short_id": "wrong"}}},
		{"unsupported_ss_cipher", map[string]any{"type": "ss", "cipher": "future-cipher"}},
		{"unknown_ss_obfs", map[string]any{"type": "ss", "cipher": "aes-128-gcm", "obfs": "future"}},
		{"tls_and_http_header", map[string]any{"tls": 1, "network_settings": map[string]any{"header": map[string]any{"type": "http"}}}},
		{"multi_http_host", map[string]any{"network_settings": map[string]any{"header": map[string]any{"type": "http", "request": map[string]any{"headers": map[string]any{"Host": []any{"first.test", "second.test"}}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if line, ok := buildQuantumultXProxyLine(quantumultXTestUUID, quantumultXNode(tc.node)); ok || line != "" {
				t.Fatalf("unrepresentable node output: %q", line)
			}
		})
	}
	for _, password := range []string{"", "bad,tag=evil", "bad\nvmess=evil", " padded "} {
		if line, ok := buildQuantumultXProxyLine(password, quantumultXNode(nil)); ok || line != "" {
			t.Fatalf("invalid password was emitted: %q", line)
		}
	}
}

func TestQuantumultXSS2022ManagedAndExtraCredentials(t *testing.T) {
	for _, tc := range []struct {
		cipher string
		length int
	}{{"2022-blake3-aes-128-gcm", 16}, {"2022-blake3-aes-256-gcm", 32}} {
		t.Run(tc.cipher, func(t *testing.T) {
			node := quantumultXNode(map[string]any{"type": "shadowsocks", "cipher": tc.cipher, "created_at": int64(12345)})
			line, ok := buildQuantumultXProxyLine(quantumultXTestUUID, node)
			password := nodeapi.ServerKey(12345, tc.length) + ":" + base64.StdEncoding.EncodeToString([]byte(quantumultXTestUUID[:tc.length]))
			if !ok || !strings.Contains(line, "password="+password+",") {
				t.Fatal("managed SS2022 password derivation mismatch")
			}
			node[subscribelink.MarkerField] = true
			node[subscribelink.CredentialField] = "extra-server-key:extra-user-key"
			line, ok = buildQuantumultXProxyLine(quantumultXTestUUID, node)
			if !ok || !strings.Contains(line, "password=extra-server-key:extra-user-key,") || strings.Contains(line, password) {
				t.Fatal("extra SS2022 password was re-derived")
			}
		})
	}
	for _, uri := range []string{
		"trojan://independent-password@extra.example.test:443?sni=extra.example.test#Extra",
		"vless://22222222-2222-2222-2222-222222222222@extra.example.test:443?security=tls&sni=extra.example.test#Extra",
		"ss://YWVzLTEyOC1nY206aW5kZXBlbmRlbnQtcGFzc3dvcmQ@extra.example.test:443#Extra",
	} {
		node, err := subscribelink.Parse(uri)
		if err != nil {
			t.Fatal(err)
		}
		line, ok := buildQuantumultXProxyLine(quantumultXTestUUID, node)
		if !ok || strings.Contains(line, quantumultXTestUUID) || strings.Contains(line, "://") {
			t.Fatalf("extra credential or format changed: %q", line)
		}
	}
}

func TestQuantumultXPayloadTracksOnlyEmittedNodes(t *testing.T) {
	c := deliveryCollector()
	payload := buildQuantumultXPayload(quantumultXTestUUID, []map[string]any{
		quantumultXNode(nil),
		quantumultXNode(map[string]any{"id": 8, "type": "tuic"}),
		quantumultXNode(map[string]any{"id": 9, "network": "grpc"}),
	}, c)
	if !strings.HasPrefix(payload, "vmess=") || strings.Count(payload, "\n") != 1 || strings.Contains(payload, "[server_local]") {
		t.Fatalf("expected one plain node resource: %q", payload)
	}
	r, nodes := c.Finish(200, int64(len(payload)), "")
	if !r.ResponseComplete || r.Coverage != "complete" || len(nodes) != 1 || nodes[0].ServerID != 7 || nodes[0].DeliveredHost != "192.0.2.7" || nodes[0].Port != "54101" {
		t.Fatalf("record=%+v nodes=%+v", r, nodes)
	}
	if got := buildQuantumultXPayload(quantumultXTestUUID, nil); got != "" {
		t.Fatalf("empty resource contained data: %q", got)
	}
}

func TestQuantumultXAnyTLSModeMatchesOtherSubscriptionFormats(t *testing.T) {
	// Switching the panel back to TLS retains the nested Reality settings.
	// Those inactive fields must not change the actual handshake protocol.
	for _, key := range []string{quantumultXTestRealityKey, "inactive-invalid-key"} {
		for _, mode := range []any{1, "1"} {
			t.Run(stringValue(mode)+"/"+key, func(t *testing.T) {
				node := quantumultXNode(map[string]any{"type": "v2node", "protocol": "anytls", "tls": mode, "tls_settings": map[string]any{
					"server_name": "tls.example.test", "allow_insecure": "1", "public_key": key, "short_id": "0123456789abcdef", "alpn": []string{"h2", "http/1.1"},
				}})
				line, ok := buildQuantumultXProxyLine(quantumultXTestUUID, node)
				if !ok || strings.Contains(line, "reality-") {
					t.Fatalf("ordinary TLS was rejected or changed into Reality: %s", line)
				}
				for _, want := range []string{"over-tls=true", "tls-host=tls.example.test", "tls-verification=false", "tls-alpn=02683208687474702f312e31", "password=" + quantumultXTestUUID} {
					if !strings.Contains(line, want) {
						t.Errorf("lost active TLS option %s: %s", want, line)
					}
				}
				_, normalized := normalizeSubscribeServer(node)
				uri := buildAnyTLSURI(quantumultXTestUUID, normalized)
				if strings.Contains(uri, "reality") || !strings.Contains(uri, "insecure=1") || !strings.Contains(uri, "sni=tls.example.test") {
					t.Fatalf("unexpected generic URI TLS mode: %s", uri)
				}
				outbound := buildSingBoxAnyTLSOutbound(quantumultXTestUUID, normalized)
				if _, enabled := serverMap(outbound, "tls")["reality"]; enabled {
					t.Fatal("ordinary TLS unexpectedly enables Reality in sing-box")
				}
			})
		}
	}
	for _, mode := range []any{2, "2"} {
		node := quantumultXNode(map[string]any{"type": "v2node", "protocol": "anytls", "tls": mode, "tls_settings": map[string]any{"public_key": quantumultXTestRealityKey, "short_id": "0123456789abcdef", "server_name": "tls.example.test"}})
		if line, ok := buildQuantumultXProxyLine(quantumultXTestUUID, node); !ok || !strings.Contains(line, "reality-base64-pubkey="+quantumultXTestRealityKey) || !strings.Contains(line, "reality-hex-shortid=0123456789abcdef") {
			t.Fatalf("active Reality lost: %s", line)
		}
	}
}
