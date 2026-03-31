package admin

import (
	"strings"
	"testing"

	cfgpkg "forest/go-api/internal/config"
)

func TestV2nodeInstallCommandUsesNeutralDefaultTemplate(t *testing.T) {
	root := t.TempDir()

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{"id": int64(7)})

	if strings.Contains(command, "raw.githubusercontent.com") {
		t.Fatalf("expected install command to avoid hardcoded third-party script hosts, got %q", command)
	}
	if strings.Contains(strings.ToLower(command), "v2node") {
		t.Fatalf("expected install command to avoid legacy project name, got %q", command)
	}
	if !strings.Contains(command, "your-node-installer.example") {
		t.Fatalf("expected neutral installer placeholder, got %q", command)
	}
	if !strings.Contains(command, "--api-host https://panel.example.com") {
		t.Fatalf("expected api host flag in command, got %q", command)
	}
	if !strings.Contains(command, "--node-id 7") {
		t.Fatalf("expected node id flag in command, got %q", command)
	}
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

	if !strings.Contains(command, "https://download.example.com/node/install.sh") {
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
