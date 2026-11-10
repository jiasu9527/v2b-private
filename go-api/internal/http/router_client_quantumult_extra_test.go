package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/subscribelink"
)

func TestQuantumultXExtraSSPluginsCannotBecomePlainSS(t *testing.T) {
	for _, tc := range []struct {
		plugin string
		ok     bool
	}{
		{"v2ray-plugin;mode=websocket;tls;host=cdn.example.test;path=/ws", false},
		{"unknown;obfs=http", false},
		{"obfs-local;obfs=tls;unknown=required", false},
		{"obfs-local", false},
		{"obfs-local;obfs=tls;obfs-host=cdn.example.test", true},
		{"simple-obfs;obfs=http;obfs-host=cdn.example.test;obfs-uri=/fixed", true},
	} {
		t.Run(tc.plugin, func(t *testing.T) {
			node, err := subscribelink.Parse("ss://YWVzLTEyOC1nY206Zml4dHVyZQ@example.test:443?plugin=" + url.QueryEscape(tc.plugin))
			if err != nil {
				t.Fatal(err)
			}
			line, ok := buildQuantumultXProxyLine("fallback", node)
			if ok != tc.ok || (ok && (!strings.Contains(line, "obfs=") || !strings.Contains(line, "obfs-host=cdn.example.test"))) {
				t.Fatalf("plugin semantics lost: ok=%v line=%q", ok, line)
			}
		})
	}
}

func TestQuantumultXExtraVMessPreservesOriginalConnectionFields(t *testing.T) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawURLEncoding} {
		raw := `{"add":"example.test","port":"443","id":"11111111-2222-3333-4444-555555555555","net":"tcp","type":"http","host":"cdn.example.test","path":"/fixed","aid":"64","scy":"none"}`
		node, err := subscribelink.Parse("vmess://" + encoding.EncodeToString([]byte(raw)))
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(node)
		line, ok := buildQuantumultXProxyLine("fallback", node)
		if !ok {
			t.Fatal("representable VMess extra skipped")
		}
		for _, field := range []string{"method=none", "aead=false", "obfs=http", "obfs-host=cdn.example.test", "obfs-uri=/fixed", "password=11111111-2222-3333-4444-555555555555"} {
			if !strings.Contains(line, field) {
				t.Errorf("lost %s: %s", field, line)
			}
		}
		after, _ := json.Marshal(node)
		if string(before) != string(after) {
			t.Fatal("renderer modified original extra node")
		}
	}
}

func TestQuantumultXHTTPTransportIsNotTCPHTTPObfuscation(t *testing.T) {
	node := map[string]any{"type": "vmess", "host": "example.test", "port": 443, "network": "http", "network_settings": map[string]any{"Host": "cdn.example.test", "path": "/h2"}}
	if line, ok := buildQuantumultXProxyLine("fixture", node); ok {
		t.Fatalf("HTTP transport was silently converted to TCP HTTP obfuscation: %s", line)
	}
}
