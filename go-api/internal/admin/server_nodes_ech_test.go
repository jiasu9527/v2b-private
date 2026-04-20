package admin

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestNormalizeManagedServerSavePayloadV2nodeGeneratesCustomECHMaterial(t *testing.T) {
	payload := map[string]any{
		"group_id":           []any{float64(1)},
		"name":               "Node-ECH",
		"host":               "node.example.com",
		"listen_ip":          "0.0.0.0",
		"port":               "443",
		"server_port":        float64(443),
		"rate":               "1",
		"protocol":           "vless",
		"tls":                float64(1),
		"network":            "tcp",
		"disable_sni":        float64(0),
		"zero_rtt_handshake": float64(0),
		"tls_settings": map[string]any{
			"ech":             "custom",
			"ech_server_name": "cover.example.com",
		},
	}

	_, values, err := normalizeManagedServerSavePayload("v2node", payload)
	if err != nil {
		t.Fatalf("normalizeManagedServerSavePayload() error = %v", err)
	}

	tlsSettings := decodeJSONStringMap(t, values["tls_settings"])
	if got := tlsSettings["ech"]; got != "custom" {
		t.Fatalf("ech = %#v, want %q", got, "custom")
	}
	if got := tlsSettings["ech_server_name"]; got != "cover.example.com" {
		t.Fatalf("ech_server_name = %#v, want %q", got, "cover.example.com")
	}
	for _, key := range []string{"ech_key", "ech_config"} {
		raw, _ := tlsSettings[key].(string)
		if raw == "" {
			t.Fatalf("expected %s to be generated, got empty", key)
		}
		if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
			t.Fatalf("expected %s to be base64, got %q: %v", key, raw, err)
		}
	}
}

func TestNormalizeManagedServerSavePayloadV2nodeDisablesCustomECHWithoutServerName(t *testing.T) {
	payload := map[string]any{
		"group_id":           []any{float64(1)},
		"name":               "Node-ECH",
		"host":               "node.example.com",
		"listen_ip":          "0.0.0.0",
		"port":               "443",
		"server_port":        float64(443),
		"rate":               "1",
		"protocol":           "vless",
		"tls":                float64(1),
		"network":            "tcp",
		"disable_sni":        float64(0),
		"zero_rtt_handshake": float64(0),
		"tls_settings": map[string]any{
			"ech":        "custom",
			"ech_key":    "ZmFrZS1rZXk=",
			"ech_config": "ZmFrZS1jb25maWc=",
		},
	}

	_, values, err := normalizeManagedServerSavePayload("v2node", payload)
	if err != nil {
		t.Fatalf("normalizeManagedServerSavePayload() error = %v", err)
	}

	tlsSettings := decodeJSONStringMap(t, values["tls_settings"])
	if got := tlsSettings["ech"]; got != "" {
		t.Fatalf("ech = %#v, want empty string when custom outer SNI is missing", got)
	}
}

func decodeJSONStringMap(t *testing.T, value any) map[string]any {
	t.Helper()

	raw, ok := value.(string)
	if !ok {
		t.Fatalf("expected JSON string, got %#v", value)
	}

	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal json map: %v", err)
	}
	return decoded
}
