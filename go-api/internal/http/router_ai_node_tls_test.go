package httpapi

import (
	"encoding/json"
	"forest/go-api/internal/admin"
	"testing"
)

func TestAINodeVMessUpdatePreservesDeepbworkTLS(t *testing.T) {
	var current, changes map[string]any
	if err := json.Unmarshal([]byte(`{"id":1,"name":"test","host":"192.0.2.1","port":"443","server_port":443,"group_id":"[1]","rate":"1","tls":1,"network":"tcp","tlsSettings":"{\"serverName\":\"old.test\",\"allowInsecure\":0}","created_at":1}`), &current); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"tls_settings":{"server_name":"new.test","allow_insecure":1}}`), &changes); err != nil {
		t.Fatal(err)
	}
	values, _, _, err := admin.PrepareAIManagedNodeUpdate("vmess", current, changes)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal([]byte(values["tlsSettings"].(string)), &settings); err != nil {
		t.Fatal(err)
	}
	stream := map[string]any{}
	applyDeepbworkTLS(stream, 1, settings)
	got := stream["tlsSettings"].(map[string]any)
	if got["serverName"] != "new.test" || got["allowInsecure"] != true {
		t.Fatalf("AI-edited TLS not reflected in actual node config: %v", got)
	}
}
