package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureClientEntrySchemaCreatesMissingTablesAndColumns(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group_ip`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_user`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, item := range []struct {
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
		{"v2_client_entry_user_policy", "email", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN email varchar\(255\) NOT NULL DEFAULT ''`},
		{"v2_client_entry_user_policy", "entry_host", `ALTER TABLE v2_client_entry_user_policy ADD COLUMN entry_host varchar\(255\) NOT NULL DEFAULT ''`},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectExec(item.stmt).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN entry_group_id SET DEFAULT 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN server_type SET DEFAULT ''`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN server_id SET DEFAULT 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_member_group ON v2_client_entry_group_member\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_ip_group ON v2_client_entry_group_ip\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_server ON v2_client_entry_user_policy\(server_type, server_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_email ON v2_client_entry_user_policy_user\(lower\(email\)\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_policy ON v2_client_entry_user_policy_user\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_policy ON v2_client_entry_user_policy_member\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_user`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := EnsureClientEntrySchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureClientEntrySchema: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnsureClientEntrySchemaSkipsExistingColumns(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group_ip`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_user`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, item := range []struct {
		table  string
		column string
	}{
		{"v2_client_entry_group", "remote_enabled"},
		{"v2_client_entry_group", "remote_host"},
		{"v2_client_entry_group", "remote_ssh_port"},
		{"v2_client_entry_group", "remote_ssh_user"},
		{"v2_client_entry_group", "remote_ssh_password"},
		{"v2_client_entry_group", "remote_group_ref"},
		{"v2_client_entry_group", "remote_exclude_names"},
		{"v2_client_entry_group", "remote_refresh_sec"},
		{"v2_client_entry_user_policy", "email"},
		{"v2_client_entry_user_policy", "entry_host"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}

	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN entry_group_id SET DEFAULT 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN server_type SET DEFAULT ''`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN server_id SET DEFAULT 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_member_group ON v2_client_entry_group_member\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_ip_group ON v2_client_entry_group_ip\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_server ON v2_client_entry_user_policy\(server_type, server_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_email ON v2_client_entry_user_policy_user\(lower\(email\)\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_policy ON v2_client_entry_user_policy_user\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_policy ON v2_client_entry_user_policy_member\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_user`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := EnsureClientEntrySchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureClientEntrySchema: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
