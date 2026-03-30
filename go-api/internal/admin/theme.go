package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	errThemeNotFound       = errors.New("theme not found")
	errThemeManifestBroken = errors.New("theme manifest invalid")
)

type themeField struct {
	Name         string
	DefaultValue any
}

func (s *DBService) ListThemes(_ context.Context) (map[string]any, error) {
	appConfig, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(adminThemeTemplatePath())
	if err != nil {
		return nil, err
	}

	themes := make(map[string]any)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}

		manifest, fields, err := loadThemeManifest(name)
		if err != nil {
			if errors.Is(err, errThemeNotFound) || errors.Is(err, errThemeManifestBroken) {
				continue
			}
			return nil, err
		}
		if _, err := ensureThemeConfig(fields, name); err != nil {
			return nil, err
		}
		themes[name] = manifest
	}

	return map[string]any{
		"themes": themes,
		"active": appConfig.stringValue("frontend_theme", "v2board"),
	}, nil
}

func (s *DBService) GetThemeConfig(_ context.Context, name string) (map[string]any, error) {
	_, fields, err := loadThemeManifest(strings.TrimSpace(name))
	if err != nil {
		return nil, publicThemeError(err)
	}

	cfg, err := ensureThemeConfig(fields, name)
	if err != nil {
		return nil, err
	}
	return themeConfigValues(cfg, fields), nil
}

func (s *DBService) SaveThemeConfig(_ context.Context, name string, values map[string]any) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("参数有误")
	}

	_, fields, err := loadThemeManifest(name)
	if err != nil {
		return nil, publicThemeError(err)
	}

	cfg := &phpConfigFile{
		order:  make([]string, 0, len(fields)),
		values: make(map[string]phpConfigValue, len(fields)),
	}
	result := make(map[string]any, len(fields))
	for _, field := range fields {
		value, ok := values[field.Name]
		if !ok {
			value = ""
		}
		normalized, err := normalizeConfigValue(value)
		if err != nil || normalized.kind == phpConfigArray {
			return nil, errors.New("参数有误")
		}
		cfg.order = append(cfg.order, field.Name)
		cfg.values[field.Name] = normalized
		result[field.Name] = themeScalarValue(normalized)
	}

	if err := os.MkdirAll(filepath.Dir(adminThemeConfigPath(name)), 0o755); err != nil {
		return nil, errors.New("修改失败")
	}
	if err := writeJSONConfigFile(adminThemeConfigPath(name), cfg); err != nil {
		return nil, errors.New("修改失败")
	}
	return result, nil
}

func loadThemeManifest(name string) (map[string]any, []themeField, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, errThemeNotFound
	}

	raw, err := os.ReadFile(adminThemeManifestPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, errThemeNotFound
		}
		return nil, nil, err
	}

	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, nil, errThemeManifestBroken
	}

	configs, ok := manifest["configs"].([]any)
	if !ok {
		return nil, nil, errThemeManifestBroken
	}

	fields := make([]themeField, 0, len(configs))
	for _, item := range configs {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, nil, errThemeManifestBroken
		}
		name, ok := entry["field_name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, nil, errThemeManifestBroken
		}
		defaultValue, exists := entry["default_value"]
		if !exists {
			defaultValue = ""
		}
		fields = append(fields, themeField{
			Name:         strings.TrimSpace(name),
			DefaultValue: defaultValue,
		})
	}

	return manifest, fields, nil
}

func ensureThemeConfig(fields []themeField, name string) (*phpConfigFile, error) {
	path := adminThemeConfigPath(name)
	cfg, err := loadThemeConfigStore(path)
	if err != nil {
		return nil, err
	}
	if len(cfg.values) > 0 {
		if !fileExists(path) {
			if err := writeJSONConfigFile(path, cfg); err != nil {
				return nil, err
			}
		}
		return cfg, nil
	}

	newCfg := &phpConfigFile{
		order:  make([]string, 0, len(fields)),
		values: make(map[string]phpConfigValue, len(fields)),
	}
	for _, field := range fields {
		normalized, err := normalizeConfigValue(field.DefaultValue)
		if err != nil || normalized.kind == phpConfigArray {
			return nil, errors.New("主题配置文件有误")
		}
		newCfg.order = append(newCfg.order, field.Name)
		newCfg.values[field.Name] = normalized
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := writeJSONConfigFile(path, newCfg); err != nil {
		return nil, err
	}
	return loadThemeConfigStore(path)
}

func themeConfigValues(cfg *phpConfigFile, fields []themeField) map[string]any {
	result := make(map[string]any, len(fields))
	for _, field := range fields {
		value, ok := cfg.values[field.Name]
		if !ok {
			if field.DefaultValue != nil {
				result[field.Name] = field.DefaultValue
			} else {
				result[field.Name] = ""
			}
			continue
		}
		result[field.Name] = themeScalarValue(value)
	}
	return result
}

func themeScalarValue(value phpConfigValue) any {
	switch value.kind {
	case phpConfigNil:
		return nil
	case phpConfigScalar, phpConfigRaw:
		return value.scalar
	default:
		return valueToString(value)
	}
}

func publicThemeError(err error) error {
	switch {
	case errors.Is(err, errThemeNotFound):
		return errors.New("主题不存在")
	case errors.Is(err, errThemeManifestBroken):
		return errors.New("主题配置文件有误")
	default:
		return fmt.Errorf("%w", err)
	}
}

func adminThemeManifestPath(name string) string {
	return filepath.Join(adminProjectRoot, "public", "theme", name, "config.json")
}

func adminThemeConfigPath(name string) string {
	return filepath.Join(adminProjectRoot, "config", "theme", name+".json")
}
