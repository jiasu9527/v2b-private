package admin

import (
	"encoding/json"
	"strings"
	"testing"

	cfgpkg "forest/go-api/internal/config"
)

func TestNormalizeManagedServerObjectFieldsParsesJSONString(t *testing.T) {
	item := map[string]any{
		"tls_settings":        `{"server_name":"edge.example.com","allow_insecure":"1"}`,
		"network_settings":    `{"path":"/ws"}`,
		"encryption_settings": `{"mode":"native"}`,
		"tlsSettings":         `{"serverName":"legacy.example.com","allowInsecure":"0"}`,
	}

	normalizeManagedServerObjectFields(item)

	for _, key := range []string{"tls_settings", "network_settings", "encryption_settings", "tlsSettings"} {
		value, ok := item[key].(map[string]any)
		if !ok {
			t.Fatalf("expected %s to decode into map, got %#v", key, item[key])
		}
		if len(value) == 0 {
			t.Fatalf("expected %s map to stay non-empty", key)
		}
	}
}

func TestNormalizeManagedServerObjectFieldsKeepsExistingMap(t *testing.T) {
	original := map[string]any{"server_name": "edge.example.com"}
	item := map[string]any{
		"tls_settings": original,
	}

	normalizeManagedServerObjectFields(item)

	got, ok := item["tls_settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected existing map to stay map, got %#v", item["tls_settings"])
	}
	if !jsonEqual(got, original) {
		t.Fatalf("expected existing map to stay unchanged, got %#v", got)
	}
}

func TestV2nodeInstallCommandUsesRepoDefaultInstaller(t *testing.T) {
	root := t.TempDir()

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{"id": int64(7)})

	if strings.Contains(command, "your-node-installer.example") {
		t.Fatalf("expected default installer placeholder to be removed, got %q", command)
	}
	if strings.Contains(command, "jiasu9527/v2b-private") {
		t.Fatalf("expected node install command to avoid panel repo, got %q", command)
	}
	if !strings.HasPrefix(command, "wget -N https://raw.githubusercontent.com/jiasu9527/v2node/main/script/install.sh && bash install.sh") {
		t.Fatalf("expected v2node repo default installer command, got %q", command)
	}
	if !strings.Contains(command, "--api-host https://panel.example.com") {
		t.Fatalf("expected api host flag in command, got %q", command)
	}
	if !strings.Contains(command, "--node-id 7") {
		t.Fatalf("expected node id flag in command, got %q", command)
	}
}

func jsonEqual(left, right map[string]any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

func TestV2nodeInstallCommandSupportsConfiguredScriptURL(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"server_api_url":                 "https://api.example.com",
		"server_token":                   "forest-secret",
		"server_node_install_script_url": "https://download.example.com/node/install.sh",
	})

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{"id": 3})

	if !strings.HasPrefix(command, "wget -N https://download.example.com/node/install.sh && bash install.sh") {
		t.Fatalf("expected configured script url in command, got %q", command)
	}
	if !strings.Contains(command, "--api-host https://api.example.com") {
		t.Fatalf("expected server_api_url override in command, got %q", command)
	}
	if !strings.Contains(command, "--node-id 3") {
		t.Fatalf("expected node id flag in command, got %q", command)
	}
	if !strings.Contains(command, "--api-key forest-secret") {
		t.Fatalf("expected api key flag in command, got %q", command)
	}
}

func TestNormalizeManagedServerSavePayloadV2nodeKeepsSendThrough(t *testing.T) {
	payload := map[string]any{
		"group_id":           []any{float64(1)},
		"name":               "Node-A",
		"host":               "node.example.com",
		"listen_ip":          "0.0.0.0",
		"send_through":       "198.51.100.7",
		"port":               "443",
		"server_port":        float64(8443),
		"rate":               "1",
		"protocol":           "vless",
		"tls":                float64(1),
		"network":            "tcp",
		"disable_sni":        float64(0),
		"zero_rtt_handshake": float64(0),
	}

	_, values, err := normalizeManagedServerSavePayload("v2node", payload)
	if err != nil {
		t.Fatalf("normalizeManagedServerSavePayload() error = %v", err)
	}
	if got := values["send_through"]; got != "198.51.100.7" {
		t.Fatalf("send_through = %#v, want %q", got, "198.51.100.7")
	}
}
