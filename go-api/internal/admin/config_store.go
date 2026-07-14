package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	cfgpkg "forest/go-api/internal/config"
)

var adminConfigUpdateMu sync.Mutex

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
	adminConfigUpdateMu.Lock()
	defer adminConfigUpdateMu.Unlock()
	return writeJSONConfigFileAtomic(path, cfg)
}

func updateAdminConfigStore(path string, mutate func(*phpConfigFile) error) error {
	adminConfigUpdateMu.Lock()
	defer adminConfigUpdateMu.Unlock()

	cfg, err := loadAdminConfigStore(path)
	if err != nil {
		return err
	}
	if err := mutate(cfg); err != nil {
		return err
	}
	return writeJSONConfigFileAtomic(path, cfg)
}

func createAdminConfigStoreIfMissing(path string, cfg *phpConfigFile) (bool, error) {
	adminConfigUpdateMu.Lock()
	defer adminConfigUpdateMu.Unlock()

	if fileExists(path) {
		return false, nil
	}
	if err := writeJSONConfigFileAtomic(path, cfg); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSONConfigFileAtomic(path string, cfg *phpConfigFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temp.Write([]byte(cfg.marshalJSON())); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func MigrateLegacyConfig(sourceRoot, targetRoot string) (int, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	targetRoot = strings.TrimSpace(targetRoot)
	if targetRoot == "" {
		targetRoot = adminProjectRoot
	}
	if sourceRoot == "" {
		sourceRoot = targetRoot
	}

	migratedConfig := 0
	sourceAdminPath := cfgpkg.ResolveLegacyPHPConfigPathFromRoot(sourceRoot)
	targetAdminPath := filepath.Join(targetRoot, "config", "admin.json")
	if !fileExists(targetAdminPath) && fileExists(sourceAdminPath) {
		cfg, err := loadPHPConfigFile(sourceAdminPath)
		if err != nil {
			return 0, err
		}
		created, err := createAdminConfigStoreIfMissing(targetAdminPath, cfg)
		if err != nil {
			return 0, err
		}
		if created {
			migratedConfig = 1
		}
	}

	return migratedConfig, nil
}
