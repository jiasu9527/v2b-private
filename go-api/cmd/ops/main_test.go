package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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
	if !strings.Contains(string(installRaw), `"checkout_claim" varchar(64) DEFAULT NULL`) || !strings.Contains(string(installRaw), `"checkout_claim_expires_at" BIGINT DEFAULT NULL`) || !strings.Contains(string(installRaw), `"checkout_fingerprint" varchar(64) DEFAULT NULL`) {
		t.Fatal("install schema should support short-lived per-order checkout claims")
	}
	if !strings.Contains(string(installRaw), `CREATE TABLE "v2_order_payment_attempt"`) || !strings.Contains(string(installRaw), `CONSTRAINT "uniq_v2_order_payment_attempt_method" UNIQUE ("order_id", "payment_id")`) {
		t.Fatal("install schema should preserve each issued payment method for switchable checkout")
	}

	updateRaw, err := os.ReadFile(filepath.Join(root, "database", "update.pgsql.sql"))
	if err != nil {
		t.Fatalf("read update schema: %v", err)
	}
	updateSQL := string(updateRaw)
	for _, fragment := range []string{`ADD COLUMN IF NOT EXISTS "checkout_result"`, `ADD COLUMN IF NOT EXISTS "checkout_claim"`, `ADD COLUMN IF NOT EXISTS "checkout_claim_expires_at"`, `ADD COLUMN IF NOT EXISTS "checkout_fingerprint"`, `("type" = 9 OR "period" = 'deposit')`, `"commission_status" IN (0, 1)`, `"commission_balance" = 0`} {
		if !strings.Contains(updateSQL, fragment) {
			t.Fatalf("update schema should close unpaid legacy deposit commissions: missing %q", fragment)
		}
	}
	for _, fragment := range []string{`CREATE TABLE IF NOT EXISTS "v2_order_payment_attempt"`, `ON CONFLICT ("order_id", "payment_id") DO NOTHING`} {
		if !strings.Contains(updateSQL, fragment) {
			t.Fatalf("update schema should preserve switchable payment attempts: missing %q", fragment)
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

func TestSplitSQLStatementsHonorsQuotedSemicolons(t *testing.T) {
	t.Parallel()

	raw := `INSERT INTO sample(value, label) VALUES ('semi;colon and it''s fine', "odd;""name");
SELECT 'tail';`
	statements := splitSQLStatements(raw)
	if len(statements) != 2 {
		t.Fatalf("splitSQLStatements() returned %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], `'semi;colon and it''s fine'`) || !strings.Contains(statements[0], `"odd;""name"`) {
		t.Fatalf("first statement lost quoted content: %q", statements[0])
	}
	if statements[1] != "SELECT 'tail';" {
		t.Fatalf("second statement = %q, want %q", statements[1], "SELECT 'tail';")
	}
}

func TestSplitSQLStatementsIgnoresCommentSemicolons(t *testing.T) {
	t.Parallel()

	raw := `-- leading comment ;
SELECT/* outer comment ;
  /* nested comment ; */
  still in outer ; */ 1; -- trailing comment ;
/* statement separator ; */ SELECT 2;`
	statements := splitSQLStatements(raw)
	if len(statements) != 2 {
		t.Fatalf("splitSQLStatements() returned %d statements, want 2: %#v", len(statements), statements)
	}
	if normalized := strings.Join(strings.Fields(statements[0]), " "); normalized != "SELECT 1;" {
		t.Fatalf("first normalized statement = %q, want %q", normalized, "SELECT 1;")
	}
	if normalized := strings.Join(strings.Fields(statements[1]), " "); normalized != "SELECT 2;" {
		t.Fatalf("second normalized statement = %q, want %q", normalized, "SELECT 2;")
	}
}

func TestSplitSQLStatementsHonorsPostgresDollarQuotesAndParameters(t *testing.T) {
	t.Parallel()

	raw := `SELECT $1;
DO $func_1$
BEGIN
  PERFORM 'inside;';
  PERFORM $$different dollar tag;$$;
END
$func_1$;
SELECT $2;`
	statements := splitSQLStatements(raw)
	if len(statements) != 3 {
		t.Fatalf("splitSQLStatements() returned %d statements, want 3: %#v", len(statements), statements)
	}
	if statements[0] != "SELECT $1;" || statements[2] != "SELECT $2;" {
		t.Fatalf("PostgreSQL parameters must not be treated as dollar-quote tags: %#v", statements)
	}
	if !strings.HasPrefix(statements[1], "DO $func_1$") || !strings.HasSuffix(statements[1], "$func_1$;") {
		t.Fatalf("tagged dollar-quoted block was split or truncated: %q", statements[1])
	}
	if !strings.Contains(statements[1], "PERFORM 'inside;';") || !strings.Contains(statements[1], "PERFORM $$different dollar tag;$$;") {
		t.Fatalf("tagged dollar-quoted block lost its body: %q", statements[1])
	}
}

func TestSplitSQLStatementsHonorsEmptyPostgresDollarQuoteTag(t *testing.T) {
	t.Parallel()

	raw := `CREATE FUNCTION sample() RETURNS void AS $$
BEGIN
  PERFORM 1;
END;
$$ LANGUAGE plpgsql;
SELECT 1`
	statements := splitSQLStatements(raw)
	if len(statements) != 2 {
		t.Fatalf("splitSQLStatements() returned %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.HasSuffix(statements[0], "$$ LANGUAGE plpgsql;") {
		t.Fatalf("empty-tag dollar-quoted block was split or truncated: %q", statements[0])
	}
	if statements[1] != "SELECT 1" {
		t.Fatalf("unterminated final statement = %q, want %q", statements[1], "SELECT 1")
	}
}

func TestSplitSQLStatementsHonorsEscapeStringsAndDollarQuoteBoundaries(t *testing.T) {
	t.Parallel()

	raw := `SELECT E'escaped\';semi;colon';
SELECT identifier$tag$part;
SELECT 3;`
	statements := splitSQLStatements(raw)
	if len(statements) != 3 {
		t.Fatalf("splitSQLStatements() returned %d statements, want 3: %#v", len(statements), statements)
	}
	if statements[0] != `SELECT E'escaped\';semi;colon';` {
		t.Fatalf("escape string was split or truncated: %q", statements[0])
	}
	if statements[1] != `SELECT identifier$tag$part;` {
		t.Fatalf("dollar sign inside identifier was treated as a quote: %q", statements[1])
	}
}

func TestSplitSQLStatementsKeepsClientEntrySplitMigrationBlocksWhole(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRootFromTestFile(t), "database", "update.pgsql.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var blocks []string
	for _, statement := range splitSQLStatements(string(raw)) {
		if strings.Contains(statement, "$client_entry_split$") {
			blocks = append(blocks, statement)
		}
	}
	if len(blocks) != 6 {
		t.Fatalf("found %d client-entry split migration statements, want 6: %#v", len(blocks), blocks)
	}
	for idx, block := range blocks {
		for _, fragment := range []string{
			"DO $client_entry_split$",
			"BEGIN",
			"ALTER TABLE",
			"EXCEPTION",
			"WHEN duplicate_object OR duplicate_table THEN NULL;",
			"END",
		} {
			if !strings.Contains(block, fragment) {
				t.Errorf("client-entry split block %d is incomplete; missing %q in %q", idx+1, fragment, block)
			}
		}
		if strings.Count(block, "$client_entry_split$") != 2 || !strings.HasSuffix(block, "$client_entry_split$;") {
			t.Errorf("client-entry split block %d has invalid dollar-quote boundaries: %q", idx+1, block)
		}
	}
}

func TestExecSQLFileReturnsErrorWhenAnyStatementFails(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	path := filepath.Join(t.TempDir(), "partial-failure.sql")
	if err := os.WriteFile(path, []byte("SELECT 1; SELECT broken;"), 0o600); err != nil {
		t.Fatalf("write SQL fixture: %v", err)
	}
	mock.ExpectExec(regexp.QuoteMeta("SELECT 1;")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT broken;")).WillReturnError(errors.New("synthetic SQL failure"))

	ok, failed, err := execSQLFile(context.Background(), db, path)
	if ok != 1 || failed != 1 {
		t.Fatalf("execSQLFile() counts = (%d, %d), want (1, 1)", ok, failed)
	}
	if err == nil || !strings.Contains(err.Error(), "1 of 2 SQL statements failed") {
		t.Fatalf("execSQLFile() error = %v, want partial-failure error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
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
