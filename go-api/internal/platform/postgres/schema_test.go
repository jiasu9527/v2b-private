package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectCurrentClientEntrySchema(mock sqlmock.Sqlmock, columnsExist bool) {
	for _, table := range []string{
		"v2_client_entry_group",
		"v2_client_entry_group_member",
		"v2_client_entry_group_ip",
		"v2_client_entry_user_policy",
		"v2_client_entry_user_policy_member",
	} {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + table).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	columns := []struct {
		table  string
		column string
		stmt   string
	}{
		{"v2_client_entry_group", "remote_enabled", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_enabled SMALLINT NOT NULL DEFAULT 0`},
		{"v2_client_entry_group", "remote_host", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_host varchar\(255\) NOT NULL DEFAULT ''`},
		{"v2_client_entry_group", "remote_ssh_port", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_ssh_port INTEGER NOT NULL DEFAULT 22`},
		{"v2_client_entry_group", "remote_ssh_user", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_ssh_user varchar\(255\) NOT NULL DEFAULT ''`},
		{"v2_client_entry_group", "remote_ssh_password", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_ssh_password text NOT NULL DEFAULT ''`},
		{"v2_client_entry_group", "remote_group_ref", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_group_ref varchar\(255\) NOT NULL DEFAULT ''`},
		{"v2_client_entry_group", "remote_exclude_names", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_exclude_names text NOT NULL DEFAULT '\[\]'`},
		{"v2_client_entry_group", "remote_refresh_sec", `ALTER TABLE v2_client_entry_group ADD COLUMN remote_refresh_sec INTEGER NOT NULL DEFAULT 300`},
		{"v2_client_entry_user_policy", "name", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN name varchar\(255\) NOT NULL DEFAULT ''`},
		{"v2_client_entry_user_policy", "sort", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN sort INTEGER NOT NULL DEFAULT 0`},
		{"v2_client_entry_user_policy", "action", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN action varchar\(16\) NOT NULL DEFAULT 'override'`},
		{"v2_client_entry_user_policy", "conditions", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN conditions text NOT NULL DEFAULT '\[\]'`},
		{"v2_client_entry_user_policy", "entry_host", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN entry_host varchar\(255\) NOT NULL DEFAULT ''`},
	}
	for _, column := range columns {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(column.table, column.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(columnsExist))
		if !columnsExist {
			mock.ExpectExec(column.stmt).WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	for _, legacyColumn := range []string{"email", "server_type"} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs("v2_client_entry_user_policy", legacyColumn).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}
	for _, index := range []string{
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_member_group ON v2_client_entry_group_member\(entry_group_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_ip_group ON v2_client_entry_group_ip\(entry_group_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_sort ON v2_client_entry_user_policy\(sort, id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_policy ON v2_client_entry_user_policy_member\(policy_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_server ON v2_client_entry_user_policy_member\(server_type, server_id\)`,
	} {
		mock.ExpectExec(index).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func TestEnsureClientEntrySchemaCreatesCurrentTablesAndColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectCurrentClientEntrySchema(mock, false)

	if err := EnsureClientEntrySchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureClientEntrySchema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnsureClientEntrySchemaDoesNotCreateRetiredEmailTableOnFreshSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectCurrentClientEntrySchema(mock, true)

	if err := EnsureClientEntrySchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureClientEntrySchema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
