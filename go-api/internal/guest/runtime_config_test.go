package guest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"forest/go-api/internal/config"
)

func TestConfigUsesRuntimeEmailVerify(t *testing.T) {
	root, restoreWD := prepareRuntimeConfigFixture(t, map[string]any{
		"email_verify": 0,
		"invite_force": 0,
	})
	defer restoreWD()

	cfg := config.Load()
	runtimeState := config.NewRuntimeState(cfg)
	service := NewDBService(cfg, nil).WithRuntimeConfig(runtimeState)

	payload, err := service.Config(context.Background())
	if err != nil {
		t.Fatalf("config before reload: %v", err)
	}
	if payload["is_email_verify"] != 0 {
		t.Fatalf("expected is_email_verify=0 before reload, got %#v", payload["is_email_verify"])
	}

	writeRuntimeAdminJSON(t, root, map[string]any{
		"email_verify": 1,
		"invite_force": 1,
	})
	runtimeState.Reload()

	payload, err = service.Config(context.Background())
	if err != nil {
		t.Fatalf("config after reload: %v", err)
	}
	if payload["is_email_verify"] != 1 {
		t.Fatalf("expected is_email_verify=1 after reload, got %#v", payload["is_email_verify"])
	}
	if payload["is_invite_force"] != 1 {
		t.Fatalf("expected is_invite_force=1 after reload, got %#v", payload["is_invite_force"])
	}
}

func prepareRuntimeConfigFixture(t *testing.T, values map[string]any) (string, func()) {
	t.Helper()

	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	workDir := filepath.Join(root, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	writeRuntimeAdminJSON(t, root, values)

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	restore := func() {
		_ = os.Chdir(prevWD)
	}
	return root, restore
}

func writeRuntimeAdminJSON(t *testing.T, root string, values map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}
}
