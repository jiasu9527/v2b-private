package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDBServiceListThemesInitializesMissingThemeConfig(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"frontend_theme": "default",
	})
	writeThemeManifestFixture(t, root, "default", `{
  "name": "default",
  "configs": [
    {"field_name": "theme_color", "default_value": "default"},
    {"field_name": "custom_html"}
  ]
}`)

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{}
	data, err := service.ListThemes(context.Background())
	if err != nil {
		t.Fatalf("list themes: %v", err)
	}

	if data["active"] != "default" {
		t.Fatalf("expected active theme default, got %#v", data["active"])
	}
	themes, ok := data["themes"].(map[string]any)
	if !ok {
		t.Fatalf("expected themes map, got %#v", data["themes"])
	}
	themeData, ok := themes["default"].(map[string]any)
	if !ok {
		t.Fatalf("expected default theme data, got %#v", themes["default"])
	}
	if themeData["name"] != "default" {
		t.Fatalf("expected theme name default, got %#v", themeData["name"])
	}

	raw, err := os.ReadFile(filepath.Join(root, "config", "theme", "default.json"))
	if err != nil {
		t.Fatalf("read initialized theme config: %v", err)
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decode initialized theme config: %v", err)
	}
	if content["theme_color"] != "default" {
		t.Fatalf("expected theme_color default in config, got %#v", content["theme_color"])
	}
	if content["custom_html"] != "" {
		t.Fatalf("expected custom_html empty string in config, got %#v", content["custom_html"])
	}
}

func TestDBServiceGetAndSaveThemeConfig(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"frontend_theme": "default",
	})
	writeThemeManifestFixture(t, root, "default", `{
  "name": "default",
  "configs": [
    {"field_name": "theme_color", "default_value": "default"},
    {"field_name": "theme_header", "default_value": "dark"},
    {"field_name": "custom_html"}
  ]
}`)
	writeThemeJSONFixture(t, root, "default", map[string]any{
		"theme_color":   "green",
		"theme_header":  "light",
		"custom_html":   "hello",
		"ignored_field": "ignored",
	})

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{}
	values, err := service.GetThemeConfig(context.Background(), "default")
	if err != nil {
		t.Fatalf("get theme config: %v", err)
	}
	if values["theme_color"] != "green" || values["theme_header"] != "light" || values["custom_html"] != "hello" {
		t.Fatalf("unexpected theme config values: %#v", values)
	}

	saved, err := service.SaveThemeConfig(context.Background(), "default", map[string]any{
		"theme_color":   "black",
		"custom_html":   "<b>hi</b>",
		"ignored_field": "ignored",
	})
	if err != nil {
		t.Fatalf("save theme config: %v", err)
	}
	if saved["theme_color"] != "black" {
		t.Fatalf("expected theme_color black, got %#v", saved["theme_color"])
	}
	if saved["theme_header"] != "" {
		t.Fatalf("expected missing theme_header saved as empty string, got %#v", saved["theme_header"])
	}

	cfg, err := loadThemeConfigStore(filepath.Join(root, "config", "theme", "default.json"))
	if err != nil {
		t.Fatalf("reload theme config: %v", err)
	}
	if cfg.stringValue("theme_color", "") != "black" {
		t.Fatalf("expected theme_color black on disk, got %q", cfg.stringValue("theme_color", ""))
	}
	if cfg.stringValue("custom_html", "") != "<b>hi</b>" {
		t.Fatalf("expected custom_html persisted, got %q", cfg.stringValue("custom_html", ""))
	}
	if cfg.stringValue("theme_header", "") != "" {
		t.Fatalf("expected theme_header empty string on disk, got %q", cfg.stringValue("theme_header", ""))
	}
	if _, ok := cfg.values["ignored_field"]; ok {
		t.Fatalf("expected ignored_field omitted from written config")
	}
}

func writeThemeManifestFixture(t *testing.T, root, name, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(root, "public", "theme", name))
	if err := os.WriteFile(filepath.Join(root, "public", "theme", name, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write theme manifest fixture: %v", err)
	}
}

func writeThemeJSONFixture(t *testing.T, root, name string, values map[string]any) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(root, "config", "theme"))
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatalf("marshal theme config fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "theme", name+".json"), raw, 0o644); err != nil {
		t.Fatalf("write theme config fixture: %v", err)
	}
}
