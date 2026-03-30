package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func loadAdminConfigStore(path string) (*phpConfigFile, error) {
	cfg, err := loadJSONConfigFile(path)
	if err != nil {
		return nil, err
	}
	if fileExists(path) || len(cfg.values) > 0 {
		return cfg, nil
	}
	return loadPHPConfigFile(legacyAdminConfigPath())
}

func loadThemeConfigStore(path string) (*phpConfigFile, error) {
	cfg, err := loadJSONConfigFile(path)
	if err != nil {
		return nil, err
	}
	if fileExists(path) || len(cfg.values) > 0 {
		return cfg, nil
	}
	legacyPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".php"
	return loadPHPConfigFile(legacyPath)
}

func loadJSONConfigFile(path string) (*phpConfigFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &phpConfigFile{order: []string{}, values: map[string]phpConfigValue{}}, nil
		}
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	values := map[string]any{}
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("decode json config %s: %w", path, err)
	}

	keys := make([]string, 0, len(values))
	result := &phpConfigFile{order: make([]string, 0, len(values)), values: make(map[string]phpConfigValue, len(values))}
	for key, value := range values {
		normalized, err := normalizeConfigValue(value)
		if err != nil {
			return nil, fmt.Errorf("normalize json config %s key %s: %w", path, key, err)
		}
		keys = append(keys, key)
		result.values[key] = normalized
	}
	sort.Strings(keys)
	result.order = append(result.order, keys...)
	return result, nil
}

func (f *phpConfigFile) marshalJSON() string {
	values := make(map[string]any, len(f.values))
	for _, key := range f.order {
		value, ok := f.values[key]
		if !ok {
			continue
		}
		values[key] = configValueToJSON(value)
	}
	for key, value := range f.values {
		if _, ok := values[key]; ok {
			continue
		}
		values[key] = configValueToJSON(value)
	}
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(raw) + "\n"
}

func configValueToJSON(value phpConfigValue) any {
	switch value.kind {
	case phpConfigNil:
		return nil
	case phpConfigArray:
		items := make([]any, 0, len(value.array))
		for _, item := range value.array {
			items = append(items, configValueToJSON(item))
		}
		return items
	case phpConfigRaw:
		return strings.TrimSpace(value.scalar)
	default:
		return value.scalar
	}
}

func writeJSONConfigFile(path string, cfg *phpConfigFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(cfg.marshalJSON()), 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func MigrateLegacyConfig(sourceRoot, targetRoot string) (int, int, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	targetRoot = strings.TrimSpace(targetRoot)
	if targetRoot == "" {
		targetRoot = adminProjectRoot
	}
	if sourceRoot == "" {
		sourceRoot = targetRoot
	}

	migratedConfig := 0
	sourceAdminPath := filepath.Join(sourceRoot, "config", "v2board.php")
	targetAdminPath := filepath.Join(targetRoot, "config", "admin.json")
	if !fileExists(targetAdminPath) && fileExists(sourceAdminPath) {
		cfg, err := loadPHPConfigFile(sourceAdminPath)
		if err != nil {
			return 0, 0, err
		}
		if err := writeJSONConfigFile(targetAdminPath, cfg); err != nil {
			return 0, 0, err
		}
		migratedConfig = 1
	}

	migratedThemes := 0
	themeMatches, err := filepath.Glob(filepath.Join(sourceRoot, "config", "theme", "*.php"))
	if err != nil {
		return migratedConfig, 0, err
	}
	for _, sourceThemePath := range themeMatches {
		name := strings.TrimSuffix(filepath.Base(sourceThemePath), filepath.Ext(sourceThemePath))
		if strings.TrimSpace(name) == "" {
			continue
		}
		targetThemePath := filepath.Join(targetRoot, "config", "theme", name+".json")
		if fileExists(targetThemePath) {
			continue
		}
		cfg, err := loadPHPConfigFile(sourceThemePath)
		if err != nil {
			return migratedConfig, migratedThemes, err
		}
		if err := writeJSONConfigFile(targetThemePath, cfg); err != nil {
			return migratedConfig, migratedThemes, err
		}
		migratedThemes++
	}

	return migratedConfig, migratedThemes, nil
}
