package main

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestApplyUpdateCompatFixesIgnoresOwnerOnlyIndexErrors(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_runtime_kv (SQLSTATE 42501)"))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_auth_session (SQLSTATE 42501)"))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE v2_server_v2node ADD COLUMN send_through varchar\(255\) DEFAULT NULL`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_server_v2node (SQLSTATE 42501)"))

	for _, item := range []struct {
		table  string
		column string
	}{
		{table: "v2_user", column: "expired_at"},
		{table: "v2_invite_code", column: "code"},
		{table: "v2_invite_campaign", column: "invite_code"},
		{table: "v2_invite_campaign_record", column: "invite_code"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestApplyUpdateCompatFixesTrimsExistingInviteColumns(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_user", "expired_at").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_user SET expired_at = NULL WHERE expired_at IS NOT NULL AND expired_at <= 0`).
		WillReturnResult(sqlmock.NewResult(0, 4))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_invite_code", "code").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_invite_code SET code = BTRIM\(code\)`).
		WillReturnResult(sqlmock.NewResult(0, 2))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_invite_campaign", "invite_code").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_invite_campaign SET invite_code = CASE WHEN invite_code IS NULL THEN NULL ELSE BTRIM\(invite_code\) END`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_invite_campaign_record", "invite_code").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_invite_campaign_record SET invite_code = BTRIM\(invite_code\)`).
		WillReturnResult(sqlmock.NewResult(0, 3))

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestApplyUpdateCompatFixesAddsV2nodeSendThroughColumnWhenMissing(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE v2_server_v2node ADD COLUMN send_through varchar\(255\) DEFAULT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, item := range []struct {
		table  string
		column string
	}{
		{table: "v2_user", column: "expired_at"},
		{table: "v2_invite_code", column: "code"},
		{table: "v2_invite_campaign", column: "invite_code"},
		{table: "v2_invite_campaign_record", column: "invite_code"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
