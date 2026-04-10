package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestBuildSubscribePayloadOmitsNilWSValues(t *testing.T) {
	server := map[string]any{
		"type":    "vmess",
		"name":    "ws-nil",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "ws",
		"tls":     int64(1),
		"network_settings": map[string]any{
			"path":    nil,
			"headers": map[string]any{"Host": nil},
		},
	}

	uri := buildVmessURI("user-uuid", server)
	raw := strings.TrimPrefix(strings.TrimSpace(uri), "vmess://")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode vmess payload: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("unmarshal vmess payload: %v", err)
	}

	if got := strings.TrimSpace(payload["path"].(string)); got != "" {
		t.Fatalf("expected empty ws path, got %q", got)
	}
	if got := strings.TrimSpace(payload["host"].(string)); got != "" {
		t.Fatalf("expected empty ws host, got %q", got)
	}
}

func TestBuildClashProxyOmitsNilWSValues(t *testing.T) {
	server := map[string]any{
		"type":    "vmess",
		"name":    "ws-nil",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "ws",
		"network_settings": map[string]any{
			"path":    nil,
			"headers": map[string]any{"Host": nil},
		},
	}

	proxy := buildClashVmessProxy("user-uuid", server)
	if _, ok := proxy["ws-opts"]; ok {
		t.Fatalf("expected ws-opts omitted when ws host/path are nil, got %#v", proxy["ws-opts"])
	}
}

func TestBuildShadowrocketPayloadOmitsNilWSValues(t *testing.T) {
	server := map[string]any{
		"type":    "vmess",
		"name":    "ws-nil",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "ws",
		"network_settings": map[string]any{
			"path":    nil,
			"headers": map[string]any{"Host": nil},
		},
	}

	uri := buildShadowrocketVmessURI("user-uuid", server)
	if strings.Contains(uri, "%3Cnil%3E") || strings.Contains(uri, "<nil>") {
		t.Fatalf("expected no nil markers in shadowrocket uri, got %q", uri)
	}
}

func TestBuildClashVlessProxyOmitsNilWSValues(t *testing.T) {
	server := map[string]any{
		"type":    "vless",
		"name":    "ws-nil",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "ws",
		"network_settings": map[string]any{
			"path":    nil,
			"headers": map[string]any{"Host": nil},
		},
	}

	proxy := buildClashVlessProxy("user-uuid", server)
	if _, ok := proxy["ws-opts"]; ok {
		t.Fatalf("expected vless ws-opts omitted when ws host/path are nil, got %#v", proxy["ws-opts"])
	}
}

func TestBuildVlessURIOmitsNilWSValues(t *testing.T) {
	server := map[string]any{
		"type":    "vless",
		"name":    "ws-nil",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "ws",
		"network_settings": map[string]any{
			"path":    nil,
			"headers": map[string]any{"Host": nil},
		},
	}

	values := parseSubscribeQuery(t, buildVlessURI("user-uuid", server))
	if got := values.Get("path"); got != "" {
		t.Fatalf("expected empty vless ws path, got %q", got)
	}
	if got := values.Get("host"); got != "" {
		t.Fatalf("expected empty vless ws host, got %q", got)
	}
}

func TestBuildTrojanURIOmitsNilWSValues(t *testing.T) {
	server := map[string]any{
		"type":    "trojan",
		"name":    "ws-nil",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "ws",
		"network_settings": map[string]any{
			"path":    nil,
			"headers": map[string]any{"Host": nil},
		},
	}

	values := parseSubscribeQuery(t, buildTrojanURI("user-uuid", server))
	if got := values.Get("path"); got != "" {
		t.Fatalf("expected empty trojan ws path, got %q", got)
	}
	if got := values.Get("host"); got != "" {
		t.Fatalf("expected empty trojan ws host, got %q", got)
	}
}

func parseSubscribeQuery(t *testing.T, raw string) url.Values {
	t.Helper()

	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parse subscribe uri: %v", err)
	}
	return parsed.Query()
}
