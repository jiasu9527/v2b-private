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
