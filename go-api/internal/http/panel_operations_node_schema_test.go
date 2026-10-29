package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

func TestPanelNodeSaveRequiredFieldsArePublishedAndValidated(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	actor := aiops.PanelOperationActor{ID: 1, IsAdmin: true}
	operations := map[string]aiops.PanelOperation{}
	for _, operation := range p.Operations() {
		operations[operation.Name] = operation
	}
	for _, tc := range []struct {
		protocol string
		extra    string
		required []string
	}{
		{"shadowsocks", `{"cipher":"aes-128-gcm"}`, []string{"cipher"}},
		{"vmess", `{"tls":1,"network":"tcp"}`, []string{"tls", "network"}},
		{"vless", `{"tls":1,"network":"tcp","port":443}`, []string{"tls", "network"}},
		{"trojan", `{"network":"tcp"}`, []string{"network"}},
		{"tuic", `{"insecure":0,"disable_sni":0,"zero_rtt_handshake":0}`, []string{"insecure", "disable_sni", "zero_rtt_handshake"}},
		{"hysteria", `{"version":2,"insecure":0}`, []string{"version", "insecure"}},
		{"anytls", `{"insecure":0}`, []string{"insecure"}},
		{"v2node", `{"protocol":"vless","tls":1,"network":"tcp","disable_sni":0,"zero_rtt_handshake":0}`, []string{"protocol", "tls", "network", "disable_sni", "zero_rtt_handshake"}},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			name := "nodes." + tc.protocol + ".save"
			operation, ok := operations[name]
			if !ok {
				t.Fatal("node save operation missing from model catalog", name)
			}
			// panel_operation_describe serializes this published operation directly.
			// Verify the wire declaration, not just an internal required-fields map.
			wire, err := json.Marshal(operation)
			if err != nil {
				t.Fatal(err)
			}
			var described struct {
				Parameters struct {
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"parameters"`
			}
			if err := json.Unmarshal(wire, &described); err != nil {
				t.Fatal(err)
			}
			required := append([]string{"name", "group_id", "host", "port", "server_port", "rate"}, tc.required...)
			if len(described.Parameters.Required) != len(required) {
				t.Fatal("published required fields differ from backend contract", described.Parameters.Required)
			}
			published := map[string]bool{}
			for _, field := range described.Parameters.Required {
				published[field] = true
			}
			if published["id"] || described.Parameters.Properties["id"] == nil {
				t.Fatal("node save must support new records without id and updates with id")
			}
			for _, field := range required {
				if !published[field] || described.Parameters.Properties[field] == nil {
					t.Fatal("model cannot see backend required field", field)
				}
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(`{"name":"fixture node","group_id":["1"],"host":"192.0.2.1","port":"443","server_port":443,"rate":1}`), &payload); err != nil {
				t.Fatal(err)
			}
			var extra map[string]any
			if err := json.Unmarshal([]byte(tc.extra), &extra); err != nil {
				t.Fatal(err)
			}
			for field, value := range extra {
				payload[field] = value
			}
			encode := func() json.RawMessage {
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			if err := p.Validate(context.Background(), actor, name, encode()); err != nil {
				t.Fatal("valid new node rejected", err)
			}
			for _, field := range required {
				value := payload[field]
				delete(payload, field)
				if err := p.Validate(context.Background(), actor, name, encode()); err == nil {
					t.Error("missing required field reached execution boundary", field)
				}
				payload[field] = value
			}
			payload["id"] = 7
			if err := p.Validate(context.Background(), actor, name, encode()); err != nil {
				t.Fatal("complete node update with id rejected", err)
			}
		})
	}
}

func TestPanelV2nodeSaveRejectsIncompleteConnectionAndAcceptsExplicitSettings(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	actor := aiops.PanelOperationActor{ID: 1, IsAdmin: true}
	const name = "nodes.v2node.save"
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{"tls":1,"host":"192.0.2.1","name":"fixture entry","port":"443","rate":1,"show":0,"network":"tcp","group_id":["1"],"listen_ip":"0.0.0.0","server_port":443,"tls_settings":{"fingerprint":"safari","server_name":"fixture.example.test","server_port":"443","allow_insecure":"1","reject_unknown_sni":"0"},"network_settings":{},"client_entry_only":1}`), &payload); err != nil {
		t.Fatal(err)
	}
	validate := func() error {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return p.Validate(context.Background(), actor, name, raw)
	}
	if err := validate(); err == nil {
		t.Fatal("incomplete V2node settings were accepted")
	}
	payload["protocol"] = "vless"
	if err := validate(); err == nil {
		t.Fatal("missing explicit SNI and zero-RTT settings were accepted")
	}
	payload["disable_sni"], payload["zero_rtt_handshake"] = 0, 0
	for _, rate := range []any{1, "1"} {
		payload["rate"] = rate
		if err := validate(); err != nil {
			t.Fatal("valid V2node settings or compatible rate rejected", rate, err)
		}
	}
	for _, operation := range p.Operations() {
		if operation.Name != name {
			continue
		}
		for _, phrase := range []string{"底层协议", "模板", "不能", "disable_sni", "zero_rtt_handshake", "明确设置"} {
			if !strings.Contains(operation.Description, phrase) {
				t.Error("model catalog lacks explicit-setting guidance", phrase)
			}
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(operation.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"protocol", "disable_sni", "zero_rtt_handshake"} {
			if description := schema.Properties[field].Description; !strings.Contains(description, "模板") || !strings.Contains(description, "不能") {
				t.Error("field schema permits guessing connection settings", field, description)
			}
		}
	}
}
