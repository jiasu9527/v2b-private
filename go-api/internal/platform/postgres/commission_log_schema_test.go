package postgres

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestCommissionLogSchemaIsAdditiveAndRetryable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		mock.ExpectBegin()
		for _, pattern := range []string{`SELECT pg_advisory_xact_lock`, `ALTER TABLE v2_commission_log ADD COLUMN IF NOT EXISTS deleted_at`, `ALTER TABLE v2_commission_log ADD COLUMN IF NOT EXISTS deleted_by`, `ALTER TABLE v2_commission_log ADD COLUMN IF NOT EXISTS delete_reason`, `ALTER TABLE v2_commission_log ADD COLUMN IF NOT EXISTS visibility_version`, `CREATE INDEX IF NOT EXISTS idx_v2_commission_log_visibility_inviter_time`, `CREATE INDEX IF NOT EXISTS idx_v2_commission_log_trade`, `CREATE TABLE IF NOT EXISTS v2_commission_log_preview`, `CREATE INDEX IF NOT EXISTS idx_v2_commission_log_preview_expiry`, `CREATE TABLE IF NOT EXISTS v2_commission_log_operation`} {
			mock.ExpectExec(pattern).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectCommit()
		if err = EnsureCommissionLogSchema(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestCommissionLogSchemaRollbackOnFailure(t *testing.T) {
	if EnsureCommissionLogSchema(context.Background(), nil) == nil {
		t.Fatal("nil db")
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_commission_log`).WillReturnError(errors.New("fixture DDL failure"))
	mock.ExpectRollback()
	if EnsureCommissionLogSchema(context.Background(), db) == nil {
		t.Fatal("expected error")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
