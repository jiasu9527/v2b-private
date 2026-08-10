package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectCurrentClientEntrySchema(mock sqlmock.Sqlmock, columnsExist bool, legacyColumnsExist bool) {
	for _, tablePattern := range []string{
		`CREATE TABLE IF NOT EXISTS v2_client_entry_group`,
		`CREATE TABLE IF NOT EXISTS v2_client_entry_group_member`,
		`CREATE TABLE IF NOT EXISTS v2_client_entry_group_ip`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy \(.*mode varchar\(16\) NOT NULL DEFAULT 'standard'.*snapshot_from BIGINT DEFAULT NULL.*snapshot_to BIGINT DEFAULT NULL`,
		`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_member`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_user_subscribe_activity \(.*user_id INTEGER NOT NULL.*last_subscribe_at BIGINT NOT NULL.*PRIMARY KEY \(user_id\)`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_split_group \(.*policy_id INTEGER NOT NULL.*parent_id BIGINT DEFAULT NULL.*path varchar\(255\) NOT NULL DEFAULT ''.*entry_host varchar\(255\) NOT NULL DEFAULT ''.*global_sort BIGINT DEFAULT NULL.*FOREIGN KEY \(policy_id\).*ON DELETE CASCADE.*FOREIGN KEY \(parent_id\).*ON DELETE CASCADE`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_split_assignment \(.*policy_id INTEGER NOT NULL.*user_id INTEGER NOT NULL.*group_id BIGINT NOT NULL.*UNIQUE \(policy_id, user_id\).*FOREIGN KEY \(policy_id\).*ON DELETE CASCADE.*FOREIGN KEY \(group_id\).*ON DELETE CASCADE`,
	} {
		mock.ExpectExec(tablePattern).
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
		{"v2_client_entry_user_policy", "mode", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN mode varchar\(16\) NOT NULL DEFAULT 'standard'`},
		{"v2_client_entry_user_policy", "conditions", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN conditions text NOT NULL DEFAULT '\[\]'`},
		{"v2_client_entry_user_policy", "entry_host", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN entry_host varchar\(255\) NOT NULL DEFAULT ''`},
		{"v2_client_entry_user_policy", "resolve_entry_host", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN resolve_entry_host SMALLINT NOT NULL DEFAULT 0`},
		{"v2_client_entry_user_policy", "extra_nodes", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN extra_nodes text NOT NULL DEFAULT '\[\]'`},
		{"v2_client_entry_user_policy", "extra_nodes_position", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN extra_nodes_position varchar\(16\) NOT NULL DEFAULT 'after'`},
		{"v2_client_entry_user_policy", "snapshot_from", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN snapshot_from BIGINT DEFAULT NULL`},
		{"v2_client_entry_user_policy", "snapshot_to", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN snapshot_to BIGINT DEFAULT NULL`},
		{"v2_client_entry_user_policy_split_group", "global_sort", `ALTER TABLE v2_client_entry_user_policy_split_group ADD COLUMN global_sort BIGINT DEFAULT NULL`},
		{"v2_server_shadowsocks", "client_entry_only", `ALTER TABLE v2_server_shadowsocks ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_vmess", "client_entry_only", `ALTER TABLE v2_server_vmess ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_vless", "client_entry_only", `ALTER TABLE v2_server_vless ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_trojan", "client_entry_only", `ALTER TABLE v2_server_trojan ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_tuic", "client_entry_only", `ALTER TABLE v2_server_tuic ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_hysteria", "client_entry_only", `ALTER TABLE v2_server_hysteria ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_anytls", "client_entry_only", `ALTER TABLE v2_server_anytls ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
		{"v2_server_v2node", "client_entry_only", `ALTER TABLE v2_server_v2node ADD COLUMN IF NOT EXISTS client_entry_only SMALLINT NOT NULL DEFAULT 0`},
	}
	for _, column := range columns {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(column.table, column.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(columnsExist))
		if !columnsExist {
			mock.ExpectExec(column.stmt).WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	mock.ExpectExec(`WITH needs_backfill AS MATERIALIZED`).WillReturnResult(sqlmock.NewResult(0, 0))
	legacyDefaults := []struct {
		column       string
		defaultValue string
	}{
		{column: "email", defaultValue: `''`},
		{column: "entry_group_id", defaultValue: `0`},
		{column: "server_type", defaultValue: `''`},
		{column: "server_id", defaultValue: `0`},
	}
	for _, legacyColumn := range legacyDefaults {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs("v2_client_entry_user_policy", legacyColumn.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(legacyColumnsExist))
		if legacyColumnsExist {
			mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN ` + legacyColumn.column + ` SET DEFAULT ` + legacyColumn.defaultValue)).
				WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	if legacyColumnsExist {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_user`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_email`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_user_policy_user .*SELECT id, lower\(trim\(email\)\).*ON CONFLICT DO NOTHING`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_user_policy_member .*SELECT id, server_type, server_id.*ON CONFLICT DO NOTHING`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, constraint := range []struct {
		table      string
		name       string
		definition string
	}{
		{"v2_client_entry_user_policy_split_group", "uniq_v2_client_entry_user_policy_split_group_path", "UNIQUE (policy_id, path)"},
		{"v2_client_entry_user_policy_split_group", "fk_v2_client_entry_user_policy_split_group_policy", "FOREIGN KEY (policy_id) REFERENCES v2_client_entry_user_policy(id) ON DELETE CASCADE"},
		{"v2_client_entry_user_policy_split_group", "fk_v2_client_entry_user_policy_split_group_parent", "FOREIGN KEY (parent_id) REFERENCES v2_client_entry_user_policy_split_group(id) ON DELETE CASCADE"},
		{"v2_client_entry_user_policy_split_assignment", "uniq_v2_client_entry_user_policy_split_assignment_policy_user", "UNIQUE (policy_id, user_id)"},
		{"v2_client_entry_user_policy_split_assignment", "fk_v2_client_entry_user_policy_split_assignment_policy", "FOREIGN KEY (policy_id) REFERENCES v2_client_entry_user_policy(id) ON DELETE CASCADE"},
		{"v2_client_entry_user_policy_split_assignment", "fk_v2_client_entry_user_policy_split_assignment_group", "FOREIGN KEY (group_id) REFERENCES v2_client_entry_user_policy_split_group(id) ON DELETE CASCADE"},
	} {
		alter := "ALTER TABLE " + constraint.table + " ADD CONSTRAINT " + constraint.name + " " + constraint.definition
		mock.ExpectExec(`(?s)DO .*` + regexp.QuoteMeta(alter) + `.*duplicate_object OR duplicate_table`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, index := range []string{
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_member_group ON v2_client_entry_group_member\(entry_group_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_ip_group ON v2_client_entry_group_ip\(entry_group_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_sort ON v2_client_entry_user_policy\(sort, id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_policy ON v2_client_entry_user_policy_member\(policy_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_server ON v2_client_entry_user_policy_member\(server_type, server_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_user_subscribe_activity_last_subscribe_at ON v2_user_subscribe_activity\(last_subscribe_at\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_split_group_policy ON v2_client_entry_user_policy_split_group\(policy_id, sort, id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_split_group_global_sort ON v2_client_entry_user_policy_split_group\(global_sort, id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_split_group_parent ON v2_client_entry_user_policy_split_group\(parent_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_split_assignment_group ON v2_client_entry_user_policy_split_assignment\(group_id\)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_split_assignment_user ON v2_client_entry_user_policy_split_assignment\(user_id, policy_id\)`,
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
	expectCurrentClientEntrySchema(mock, false, false)

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
	expectCurrentClientEntrySchema(mock, true, false)

	if err := EnsureClientEntrySchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureClientEntrySchema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnsureClientEntrySchemaUnblocksLegacyNotNullPolicyColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectCurrentClientEntrySchema(mock, true, true)

	if err := EnsureClientEntrySchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureClientEntrySchema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
