package postgres

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestSupportSchemaCreatesOnlyTransactionalWorkbenchState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		mock.ExpectBegin()
		for _, pattern := range []string{`SELECT pg_advisory_xact_lock`, `CREATE TABLE IF NOT EXISTS v2_support_request_limit`, `CREATE TABLE IF NOT EXISTS v2_support_entry_override`, `CREATE INDEX IF NOT EXISTS v2_support_entry_override_target_idx`, `CREATE TABLE IF NOT EXISTS v2_support_operation`, `CREATE INDEX IF NOT EXISTS v2_support_operation_actor_idx`, `CREATE INDEX IF NOT EXISTS v2_support_operation_created_idx`} {
			mock.ExpectExec(pattern).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectCommit()
		if err := EnsureSupportSchema(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSupportSchemaRollsBackPartialDDL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_support_request_limit`).WillReturnError(errors.New("DDL failure"))
	mock.ExpectRollback()
	if EnsureSupportSchema(context.Background(), db) == nil {
		t.Fatal("expected DDL error")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
