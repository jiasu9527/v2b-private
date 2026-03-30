package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPostgresSQLFilesDoNotContainInlineMySQLIndexes(t *testing.T) {
	t.Parallel()

	inlineIndexPattern := regexp.MustCompile(`,\s*(INDEX|KEY)\s+`)
	for _, name := range []string{"install.pgsql.sql", "update.pgsql.sql"} {
		path := filepath.Join(repoRootFromTestFile(t), "database", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		lines := strings.Split(string(raw), "\n")
		for idx, line := range lines {
			if inlineIndexPattern.MatchString(line) {
				t.Fatalf("%s:%d contains unsupported inline index syntax: %s", path, idx+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestUpdatePostgresSQLAvoidsLegacyMigrationChain(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRootFromTestFile(t), "database", "update.pgsql.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(raw)

	forbidden := []string{
		`ALTER TABLE "v2_server"`,
		`ALTER TABLE "v2_server_log"`,
		`ALTER TABLE "v2_server_v2ray"`,
		`ALTER TABLE "v2_stat_order"`,
		`ALTER TABLE "v2_tutorial"`,
		`ALTER TABLE "v2_plan";`,
		`ALTER TABLE "v2_mail_log";`,
	}
	for _, fragment := range forbidden {
		if strings.Contains(content, fragment) {
			t.Fatalf("%s should not contain legacy migration fragment %q", path, fragment)
		}
	}

	required := []string{
		`CREATE TABLE IF NOT EXISTS "failed_jobs"`,
		`CREATE TABLE IF NOT EXISTS "v2_runtime_kv"`,
		`CREATE INDEX IF NOT EXISTS "idx_v2_runtime_kv_expire_at"`,
		`CREATE TABLE IF NOT EXISTS "v2_auth_session"`,
		`CREATE INDEX IF NOT EXISTS "idx_v2_auth_session_user_id"`,
	}
	for _, fragment := range required {
		if !strings.Contains(content, fragment) {
			t.Fatalf("%s should contain %q", path, fragment)
		}
	}
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
