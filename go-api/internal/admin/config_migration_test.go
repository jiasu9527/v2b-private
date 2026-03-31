package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyConfigAutoDetectsPHPConfigFile(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()

	writeAdminLegacyConfigFixture(t, sourceRoot, `<?php
return [
    'secure_path' => 'legacy-admin',
    'app_name' => 'Forest Legacy',
];
`)

	migrated, err := MigrateLegacyConfig(sourceRoot, targetRoot)
	if err != nil {
		t.Fatalf("migrate legacy config: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("expected one migrated config file, got %d", migrated)
	}

	raw, err := os.ReadFile(filepath.Join(targetRoot, "config", "admin.json"))
	if err != nil {
		t.Fatalf("read migrated admin.json: %v", err)
	}

	if !strings.Contains(string(raw), "legacy-admin") {
		t.Fatalf("expected migrated config to contain legacy values, got %s", string(raw))
	}
}
