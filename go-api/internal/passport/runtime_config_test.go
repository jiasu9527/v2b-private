package passport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRegisterUsesRuntimeEmailVerify(t *testing.T) {
	root, restoreWD := prepareRuntimeConfigFixture(t, map[string]any{
		"email_verify": 0,
	})
	defer restoreWD()

	cfg := config.Load()
	runtimeState := config.NewRuntimeState(cfg)

	writeRuntimeAdminJSON(t, root, map[string]any{
		"email_verify": 1,
	})
	runtimeState.Reload()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_runtime_kv`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_auth_session`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).WillReturnResult(sqlmock.NewResult(0, 0))

	service := NewDBServiceWithConfig(cfg, db).WithRuntimeConfig(runtimeState)
	_, err = service.Register(context.Background(), RegisterRequest{
		Email:    "user@example.com",
		Password: "password123",
	})
	if err == nil || !strings.Contains(err.Error(), "Email verification code cannot be empty") {
		t.Fatalf("expected missing email verification code error, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRuntimeMailSettingsUsesReloadedRuntimeConfig(t *testing.T) {
	root, restoreWD := prepareRuntimeConfigFixture(t, map[string]any{
		"app_name":           "Forest Old",
		"app_url":            "https://old.example.com",
		"email_host":         "smtp-old.example.com",
		"email_port":         2525,
		"email_username":     "old-user",
		"email_password":     "old-pass",
		"email_from_address": "old@example.com",
		"email_from_name":    "Forest Old",
		"email_template":     "old-template",
	})
	defer restoreWD()

	cfg := config.Load()
	runtimeState := config.NewRuntimeState(cfg)
	service := NewDBServiceWithConfig(cfg, nil).WithRuntimeConfig(runtimeState)

	oldRoot := passportProjectRoot
	passportProjectRoot = filepath.Join(root, "missing-project-root")
	defer func() { passportProjectRoot = oldRoot }()

	settings := service.runtimeMailSettings()
	if settings.Host != "smtp-old.example.com" {
		t.Fatalf("expected initial host from loaded config, got %q", settings.Host)
	}

	writeRuntimeAdminJSON(t, root, map[string]any{
		"app_name":           "Forest New",
		"app_url":            "https://new.example.com",
		"email_host":         "smtp-new.example.com",
		"email_port":         587,
		"email_username":     "new-user",
		"email_password":     "new-pass",
		"email_from_address": "new@example.com",
		"email_from_name":    "Forest New",
		"email_template":     "new-template",
	})
	runtimeState.Reload()

	settings = service.runtimeMailSettings()
	if settings.Host != "smtp-new.example.com" {
		t.Fatalf("expected reloaded host, got %q", settings.Host)
	}
	if settings.Port != 587 {
		t.Fatalf("expected reloaded port, got %d", settings.Port)
	}
	if settings.Username != "new-user" {
		t.Fatalf("expected reloaded username, got %q", settings.Username)
	}
	if settings.Password != "new-pass" {
		t.Fatalf("expected reloaded password, got %q", settings.Password)
	}
	if settings.From != "new@example.com" {
		t.Fatalf("expected reloaded from address, got %q", settings.From)
	}
	if settings.FromName != "Forest New" {
		t.Fatalf("expected reloaded from name, got %q", settings.FromName)
	}
	if settings.Template != "new-template" {
		t.Fatalf("expected reloaded template, got %q", settings.Template)
	}
	if settings.AppName != "Forest New" {
		t.Fatalf("expected reloaded app name, got %q", settings.AppName)
	}
	if settings.AppURL != "https://new.example.com" {
		t.Fatalf("expected reloaded app url, got %q", settings.AppURL)
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
