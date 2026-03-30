package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cfgpkg "forest/go-api/internal/config"
)

func TestDBServiceFetchConfigAndTemplates(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"invite_campaign_enable":     1,
		"invite_commission":          20,
		"commission_withdraw_method": []string{"USDT", "支付宝"},
		"email_whitelist_suffix":     []string{"qq.com"},
		"secure_path":                "localadmin",
		"frontend_theme":             "default",
	})
	mustMkdirAll(t, filepath.Join(root, "resources", "views", "mail", "default"))
	mustMkdirAll(t, filepath.Join(root, "resources", "views", "mail", "classic"))
	mustMkdirAll(t, filepath.Join(root, "public", "theme", "default"))

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AdminPath: "localadmin"}}
	data, err := service.FetchConfig(context.Background(), "invite")
	if err != nil {
		t.Fatalf("fetch config: %v", err)
	}

	invite, ok := data["invite"].(map[string]any)
	if !ok {
		t.Fatalf("expected invite group, got %#v", data)
	}
	if invite["invite_campaign_enable"] != int64(1) {
		t.Fatalf("expected invite campaign enabled, got %#v", invite["invite_campaign_enable"])
	}
	methods, ok := invite["commission_withdraw_method"].([]string)
	if !ok || len(methods) != 2 || methods[0] != "USDT" || methods[1] != "支付宝" {
		t.Fatalf("unexpected withdraw methods: %#v", invite["commission_withdraw_method"])
	}

	emailTemplates, err := service.ListEmailTemplates(context.Background())
	if err != nil {
		t.Fatalf("list email templates: %v", err)
	}
	if len(emailTemplates) != 2 || emailTemplates[0] != "classic" || emailTemplates[1] != "default" {
		t.Fatalf("unexpected email templates: %#v", emailTemplates)
	}

	themeTemplates, err := service.ListThemeTemplates(context.Background())
	if err != nil {
		t.Fatalf("list theme templates: %v", err)
	}
	if len(themeTemplates) != 1 || themeTemplates[0] != "default" {
		t.Fatalf("unexpected theme templates: %#v", themeTemplates)
	}
}

func TestDBServiceSaveConfigPersistsJSONValues(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"secure_path":                "localadmin",
		"commission_withdraw_method": []string{"USDT"},
	})

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AdminPath: "localadmin"}}
	ok, err := service.SaveConfig(context.Background(), map[string]any{
		"secure_path":                "newsecure",
		"deposit_bounus":             []any{"100:10"},
		"commission_withdraw_method": []any{"USDT", "支付宝"},
	})
	if err != nil {
		t.Fatalf("save config: %v", err)
	}
	if !ok {
		t.Fatalf("expected save config success")
	}

	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.stringValue("secure_path", "") != "newsecure" {
		t.Fatalf("expected secure path newsecure, got %q", cfg.stringValue("secure_path", ""))
	}
	bonus := cfg.stringSliceValue("deposit_bounus", nil)
	if len(bonus) != 1 || bonus[0] != "100:10" {
		t.Fatalf("unexpected deposit bonus: %#v", bonus)
	}
	methods := cfg.stringSliceValue("commission_withdraw_method", nil)
	if len(methods) != 2 || methods[0] != "USDT" || methods[1] != "支付宝" {
		t.Fatalf("unexpected withdraw methods after save: %#v", methods)
	}

	raw, err := os.ReadFile(filepath.Join(root, "config", "admin.json"))
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	var written map[string]any
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("decode written config json: %v", err)
	}
	if written["secure_path"] != "newsecure" {
		t.Fatalf("expected secure_path in json, got %#v", written["secure_path"])
	}
}

func TestLoadBulkMailConfigFallsBackToAppNameForFromName(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"app_name":           "Forest",
		"app_url":            "https://forest.example.com",
		"email_host":         "smtp.example.com",
		"email_port":         2525,
		"email_from_address": "noreply@example.com",
		"email_template":     "forest-v2",
	})

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{
		AppName:         "forest-go-api",
		AppURL:          "http://localhost",
		MailHost:        "127.0.0.1",
		MailPort:        25,
		MailFromAddress: "env@example.com",
		MailFromName:    "forest-go-api",
	}}
	mailCfg, err := service.loadBulkMailConfig()
	if err != nil {
		t.Fatalf("load bulk mail config: %v", err)
	}
	if mailCfg.fromName != "Forest" {
		t.Fatalf("expected fromName Forest, got %q", mailCfg.fromName)
	}
	if mailCfg.appURL != "https://forest.example.com" {
		t.Fatalf("expected appURL from admin.json, got %q", mailCfg.appURL)
	}
	if mailCfg.template != "forest-v2" {
		t.Fatalf("expected template forest-v2, got %q", mailCfg.template)
	}
}

func writeAdminJSONFixture(t *testing.T, root string, values map[string]any) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(root, "config"))
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
}

func writeAdminLegacyConfigFixture(t *testing.T, root, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(root, "config"))
	if err := os.WriteFile(filepath.Join(root, "config", "v2board.php"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
