package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("APP_ADDR", "")
	t.Setenv("PUBLIC_DIR", "")
	t.Setenv("POSTGRES_DSN", "")

	cfg := Load()

	if cfg.Addr != ":8080" {
		t.Fatalf("expected default addr :8080, got %q", cfg.Addr)
	}
	if cfg.PublicDir != "../public" {
		t.Fatalf("expected default public dir ../public, got %q", cfg.PublicDir)
	}
	if cfg.PostgresDSN != "" {
		t.Fatalf("expected empty postgres dsn, got %q", cfg.PostgresDSN)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("APP_ADDR", ":9090")
	t.Setenv("PUBLIC_DIR", "/srv/app/public")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1:5432/app")

	cfg := Load()

	if cfg.Addr != ":9090" {
		t.Fatalf("expected env addr :9090, got %q", cfg.Addr)
	}
	if cfg.PublicDir != "/srv/app/public" {
		t.Fatalf("expected env public dir override, got %q", cfg.PublicDir)
	}
	if cfg.PostgresDSN != "postgres://user:pass@127.0.0.1:5432/app" {
		t.Fatalf("expected postgres dsn override, got %q", cfg.PostgresDSN)
	}
}

func TestLoadAdminJSONFallbacks(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"secure_path":                "newadmin",
		"commission_withdraw_method": []string{"USDT", "支付宝"},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	cfg := Load()
	if cfg.AdminPath != "newadmin" {
		t.Fatalf("expected admin path from admin.json, got %q", cfg.AdminPath)
	}
	if len(cfg.CommissionWithdrawMethods) != 2 || cfg.CommissionWithdrawMethods[0] != "USDT" || cfg.CommissionWithdrawMethods[1] != "支付宝" {
		t.Fatalf("unexpected json withdraw methods: %#v", cfg.CommissionWithdrawMethods)
	}
}

func TestLoadAdminJSONSiteFallbacks(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"app_name":        "Forest Site",
		"app_description": "Fast and stable",
		"app_url":         "https://forest.test",
		"logo":            "https://cdn.example.com/logo.png",
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	t.Setenv("APP_NAME", "")
	t.Setenv("APP_DESCRIPTION", "")
	t.Setenv("APP_URL", "")
	t.Setenv("LOGO_URL", "")

	cfg := Load()
	if cfg.AppName != "Forest Site" {
		t.Fatalf("expected app name from admin.json, got %q", cfg.AppName)
	}
	if cfg.AppDescription != "Fast and stable" {
		t.Fatalf("expected app description from admin.json, got %q", cfg.AppDescription)
	}
	if cfg.AppURL != "https://forest.test" {
		t.Fatalf("expected app url from admin.json, got %q", cfg.AppURL)
	}
	if cfg.Logo != "https://cdn.example.com/logo.png" {
		t.Fatalf("expected logo from admin.json, got %q", cfg.Logo)
	}
}
