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

func TestInstallPostgresPlanTransferEnableUsesBigint(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRootFromTestFile(t), "database", "install.pgsql.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(raw)

	if !strings.Contains(content, `"transfer_enable" BIGINT NOT NULL`) {
		t.Fatalf("%s should create v2_plan.transfer_enable as BIGINT", path)
	}
	if strings.Contains(content, `"transfer_enable" INTEGER NOT NULL`) {
		t.Fatalf("%s should not create v2_plan.transfer_enable as INTEGER", path)
	}
}

func TestPostgresPaymentSecurityMigrationIsPresent(t *testing.T) {
	t.Parallel()

	root := repoRootFromTestFile(t)
	installRaw, err := os.ReadFile(filepath.Join(root, "database", "install.pgsql.sql"))
	if err != nil {
		t.Fatalf("read install schema: %v", err)
	}
	if !strings.Contains(string(installRaw), `CREATE INDEX "idx_v2_order_payment_callback" ON "v2_order" ("payment_id", "callback_no")`) {
		t.Fatal("install schema should index payment-scoped callback lookups")
	}
	if !strings.Contains(string(installRaw), `"checkout_result" text DEFAULT NULL`) {
		t.Fatal("install schema should persist the first successful checkout result")
	}

	updateRaw, err := os.ReadFile(filepath.Join(root, "database", "update.pgsql.sql"))
	if err != nil {
		t.Fatalf("read update schema: %v", err)
	}
	updateSQL := string(updateRaw)
	for _, fragment := range []string{`ADD COLUMN IF NOT EXISTS "checkout_result"`, `("type" = 9 OR "period" = 'deposit')`, `"commission_status" IN (0, 1)`, `"commission_balance" = 0`} {
		if !strings.Contains(updateSQL, fragment) {
			t.Fatalf("update schema should close unpaid legacy deposit commissions: missing %q", fragment)
		}
	}
}

func TestInstallPostgresManagedServersIncludeClientEntryOnly(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRootFromTestFile(t), "database", "install.pgsql.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	for _, table := range []string{
		"v2_server_shadowsocks",
		"v2_server_vmess",
		"v2_server_vless",
		"v2_server_trojan",
		"v2_server_tuic",
		"v2_server_hysteria",
		"v2_server_anytls",
		"v2_server_v2node",
	} {
		pattern := regexp.MustCompile(`(?m)^CREATE TABLE "` + regexp.QuoteMeta(table) + `" .*"client_entry_only" SMALLINT NOT NULL DEFAULT '0'`)
		if !pattern.Match(raw) {
			t.Errorf("%s should create %s.client_entry_only with a safe disabled default", path, table)
		}
	}
}

func TestUpdatePostgresManagedServersIncludeClientEntryOnly(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRootFromTestFile(t), "database", "update.pgsql.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	for _, table := range []string{
		"v2_server_shadowsocks",
		"v2_server_vmess",
		"v2_server_vless",
		"v2_server_trojan",
		"v2_server_tuic",
		"v2_server_hysteria",
		"v2_server_anytls",
		"v2_server_v2node",
	} {
		pattern := regexp.MustCompile(`(?m)^ALTER TABLE "` + regexp.QuoteMeta(table) + `" ADD COLUMN IF NOT EXISTS "client_entry_only" SMALLINT NOT NULL DEFAULT 0;`)
		if !pattern.Match(raw) {
			t.Errorf("%s should add %s.client_entry_only with a safe disabled default", path, table)
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
		`CREATE INDEX IF NOT EXISTS "idx_v2_runtime_kv_expire_at"`,
		`CREATE INDEX IF NOT EXISTS "idx_v2_auth_session_user_id"`,
		`ALTER TABLE IF EXISTS "v2_invite_code"`,
		`ALTER TABLE IF EXISTS "v2_invite_campaign"`,
		`ALTER TABLE IF EXISTS "v2_invite_campaign_record"`,
	}
	for _, fragment := range forbidden {
		if strings.Contains(content, fragment) {
			t.Fatalf("%s should not contain legacy migration fragment %q", path, fragment)
		}
	}

	required := []string{
		`CREATE TABLE IF NOT EXISTS "failed_jobs"`,
		`CREATE TABLE IF NOT EXISTS "v2_runtime_kv"`,
		`CREATE TABLE IF NOT EXISTS "v2_auth_session"`,
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
