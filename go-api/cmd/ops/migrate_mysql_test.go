package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLegacyMySQLConfigFromEnv(t *testing.T) {
	cfg, err := legacyMySQLConfigFromEnv(map[string]string{
		"DB_CONNECTION": "mysql",
		"DB_HOST":       "mysql.internal",
		"DB_PORT":       "3307",
		"DB_DATABASE":   "forest_legacy",
		"DB_USERNAME":   "legacy_user",
		"DB_PASSWORD":   "legacy_pass",
	})
	if err != nil {
		t.Fatalf("legacyMySQLConfigFromEnv: %v", err)
	}

	if cfg.Host != "mysql.internal" || cfg.Port != "3307" || cfg.Database != "forest_legacy" || cfg.Username != "legacy_user" || cfg.Password != "legacy_pass" {
		t.Fatalf("unexpected config: %#v", cfg)
	}

	dsn := cfg.DSN()
	for _, fragment := range []string{
		"legacy_user:legacy_pass@tcp(mysql.internal:3307)/forest_legacy",
		"charset=utf8mb4",
		"parseTime=true",
		"multiStatements=true",
	} {
		if !regexp.MustCompile(regexp.QuoteMeta(fragment)).MatchString(dsn) {
			t.Fatalf("expected dsn %q to contain %q", dsn, fragment)
		}
	}
}

func TestCopyTableRowsConvertsValuesAndResetsSequence(t *testing.T) {
	ctx := context.Background()

	sourceDB, sourceMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new source mock: %v", err)
	}
	defer sourceDB.Close()

	targetDB, targetMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new target mock: %v", err)
	}
	defer targetDB.Close()

	plan := tableCopyPlan{
		Table: "v2_user",
		Columns: []tableColumnPlan{
			{Name: "id", Cast: "integer"},
			{Name: "email", Cast: "text"},
			{Name: "balance", Cast: "integer"},
		},
		IdentityColumn: "id",
	}

	sourceRows := sqlmock.NewRows([]string{"id", "email", "balance"}).
		AddRow([]byte("7"), []byte("demo@example.com"), int64(1200))
	sourceMock.ExpectQuery(regexp.QuoteMeta("SELECT `id`, `email`, `balance` FROM `v2_user`")).
		WillReturnRows(sourceRows)

	targetMock.ExpectBegin()
	targetMock.ExpectPrepare(regexp.QuoteMeta(`INSERT INTO "v2_user" ("id", "email", "balance") VALUES ($1::integer, $2::text, $3::integer)`))
	targetMock.ExpectExec(regexp.QuoteMeta(`INSERT INTO "v2_user" ("id", "email", "balance") VALUES ($1::integer, $2::text, $3::integer)`)).
		WithArgs(int64(7), "demo@example.com", int64(1200)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	targetMock.ExpectCommit()
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT setval(pg_get_serial_sequence('public.v2_user', 'id'), COALESCE(MAX("id"), 1), COUNT(*) > 0) FROM "v2_user"`)).
		WillReturnRows(sqlmock.NewRows([]string{"setval"}).AddRow(int64(7)))

	rowsCopied, err := copyTableRows(ctx, sourceDB, targetDB, plan)
	if err != nil {
		t.Fatalf("copyTableRows: %v", err)
	}
	if rowsCopied != 1 {
		t.Fatalf("expected 1 copied row, got %d", rowsCopied)
	}

	if err := sourceMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("source expectations: %v", err)
	}
	if err := targetMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("target expectations: %v", err)
	}
}
