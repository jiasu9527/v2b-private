package httpapi

import (
	"encoding/base64"
	"testing"
)

func TestBuildClashVmessProxyIncludesCloudflareECHOptions(t *testing.T) {
	t.Parallel()

	proxy := buildClashVmessProxy("user-uuid", map[string]any{
		"name": "vmess-ech",
		"host": "node.example.com",
		"port": int64(443),
		"tls":  true,
		"tls_settings": map[string]any{
			"server_name": "inner.example.com",
			"ech":         "cloudflare",
		},
	})

	opts, ok := proxy["ech-opts"].(map[string]any)
	if !ok {
		t.Fatalf("expected ech-opts map, got %#v", proxy["ech-opts"])
	}
	if opts["enable"] != true {
		t.Fatalf("ech-opts.enable = %#v, want true", opts["enable"])
	}
	if got := stringValue(opts["query-server-name"]); got != "cloudflare-ech.com" {
		t.Fatalf("ech-opts.query-server-name = %q, want %q", got, "cloudflare-ech.com")
	}
}

func TestBuildClashVlessProxyIncludesCloudflareECHOptions(t *testing.T) {
	t.Parallel()

	proxy := buildClashVlessProxy("user-uuid", map[string]any{
		"name":    "vless-ech",
		"host":    "node.example.com",
		"port":    int64(443),
		"tls":     int64(1),
		"network": "tcp",
		"tls_settings": map[string]any{
			"server_name": "inner.example.com",
			"ech":         "cloudflare",
		},
	})

	opts, ok := proxy["ech-opts"].(map[string]any)
	if !ok {
		t.Fatalf("expected ech-opts map, got %#v", proxy["ech-opts"])
	}
	if opts["enable"] != true {
		t.Fatalf("ech-opts.enable = %#v, want true", opts["enable"])
	}
	if got := stringValue(opts["query-server-name"]); got != "cloudflare-ech.com" {
		t.Fatalf("ech-opts.query-server-name = %q, want %q", got, "cloudflare-ech.com")
	}
}

func TestBuildClashTrojanProxyIncludesCustomECHConfig(t *testing.T) {
	t.Parallel()

	proxy := buildClashTrojanProxy("user-uuid", map[string]any{
		"name":    "trojan-ech",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "tcp",
		"tls_settings": map[string]any{
			"server_name": "inner.example.com",
			"ech":         "custom",
			"ech_config":  "ZmFrZS1lY2gtY29uZmln",
		},
	})

	opts, ok := proxy["ech-opts"].(map[string]any)
	if !ok {
		t.Fatalf("expected ech-opts map, got %#v", proxy["ech-opts"])
	}
	if opts["enable"] != true {
		t.Fatalf("ech-opts.enable = %#v, want true", opts["enable"])
	}
	configs, ok := opts["config"].([]string)
	if !ok {
		t.Fatalf("expected ech-opts.config []string, got %#v", opts["config"])
	}
	if len(configs) != 1 || configs[0] != "ZmFrZS1lY2gtY29uZmln" {
		t.Fatalf("ech-opts.config = %#v, want single custom config", configs)
	}
}

func TestBuildVlessURIIncludesCloudflareECHQuery(t *testing.T) {
	t.Parallel()

	values := parseSubscribeQuery(t, buildVlessURI("user-uuid", map[string]any{
		"name":    "vless-ech",
		"host":    "node.example.com",
		"port":    int64(443),
		"tls":     int64(1),
		"network": "tcp",
		"tls_settings": map[string]any{
			"server_name": "inner.example.com",
			"ech":         "cloudflare",
		},
	}))

	if got := values.Get("ech"); got != "cloudflare-ech.com+https://1.1.1.1/dns-query" {
		t.Fatalf("ech query = %q, want %q", got, "cloudflare-ech.com+https://1.1.1.1/dns-query")
	}
}

func TestBuildTrojanURIIncludesCustomECHQuery(t *testing.T) {
	t.Parallel()

	values := parseSubscribeQuery(t, buildTrojanURI("user-uuid", map[string]any{
		"name":    "trojan-ech",
		"host":    "node.example.com",
		"port":    int64(443),
		"network": "tcp",
		"tls_settings": map[string]any{
			"server_name": "inner.example.com",
			"ech":         "custom",
			"ech_config":  base64.StdEncoding.EncodeToString([]byte("ech-config")),
		},
	}))

	if got := values.Get("ech"); got != base64.StdEncoding.EncodeToString([]byte("ech-config")) {
		t.Fatalf("ech query = %q, want custom config", got)
	}
}
