package httpapi

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildGeneralSubscribePayloadInfersLegacyV2nodeProtocol(t *testing.T) {
	payload := buildGeneralSubscribePayload("user-uuid", []map[string]any{
		{
			"type":     "v2node",
			"protocol": nil,
			"name":     "Legacy-V2node",
			"host":     "node.example.com",
			"port":     int64(443),
			"network":  "ws",
			"tls":      int64(1),
			"tls_settings": map[string]any{
				"server_name": "node.example.com",
			},
			"network_settings": map[string]any{
				"path": "/ws",
				"headers": map[string]any{
					"Host": "node.example.com",
				},
			},
		},
	})

	if strings.TrimSpace(payload) == "" {
		t.Fatal("expected legacy v2node payload to stay non-empty")
	}

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !strings.Contains(string(decoded), "vmess://") {
		t.Fatalf("expected vmess uri in payload, got %q", string(decoded))
	}
}

func TestBuildGeneralSubscribePayloadEmitsExtraShareLinkExactly(t *testing.T) {
	const raw = "trojan://external-secret@58.247.187.151:20003?allowInsecure=1&peer=mpvideo.qpic.cn#Hong%20Kong%20%7C%2001"
	payload := buildGeneralSubscribePayload("subscriber-uuid", []map[string]any{{
		"type":                          "trojan",
		"client_entry_extra_node":       int64(1),
		"client_entry_extra_uri":        raw,
		"client_entry_extra_credential": "external-secret",
	}})
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got := strings.TrimSpace(string(decoded)); got != raw {
		t.Fatalf("raw extra link changed: %q", got)
	}
}

func TestClashExtraTrojanUsesLinkPassword(t *testing.T) {
	server := map[string]any{
		"type":                          "trojan",
		"name":                          "Extra",
		"host":                          "extra.example.com",
		"port":                          int64(443),
		"client_entry_extra_node":       int64(1),
		"client_entry_extra_credential": "external-secret",
	}
	proxy, ok := buildClashStandardProxy("subscriber-uuid", server)
	if !ok || proxy["password"] != "external-secret" {
		t.Fatalf("unexpected proxy: %#v", proxy)
	}
	outbound, ok := buildSingBoxOutbound("subscriber-uuid", server)
	if !ok || outbound["password"] != "external-secret" {
		t.Fatalf("unexpected sing-box outbound: %#v", outbound)
	}
	surge, ok := buildSurgeProxyLine("subscriber-uuid", server)
	if !ok || !strings.Contains(surge, "password=external-secret") || strings.Contains(surge, "subscriber-uuid") {
		t.Fatalf("unexpected Surge line: %q", surge)
	}
}
