package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceUpdateManagedServerReplacesEntryGroupBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}

	mock.ExpectQuery(`SELECT 1 FROM "v2_server_vmess" WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(int64(1)))
	expectEnsureClientEntrySchema(mock)
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM v2_client_entry_group_member WHERE server_type = \$1 AND server_id = \$2`).
		WithArgs("vmess", int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT 1 FROM v2_client_entry_group WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(int64(1)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_member \(entry_group_id, server_type, server_id, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6\)`).
		WithArgs(int64(7), "vmess", int64(9), nil, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	updated, err := service.UpdateManagedServer(context.Background(), "vmess", int64(9), map[string]any{
		"entry_group_id": int64(7),
	})
	if err != nil {
		t.Fatalf("update managed server: %v", err)
	}
	if !updated {
		t.Fatalf("expected update success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceUpdateManagedServerClearsEntryGroupBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}

	mock.ExpectQuery(`SELECT 1 FROM "v2_server_vmess" WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(int64(1)))
	expectEnsureClientEntrySchema(mock)
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM v2_client_entry_group_member WHERE server_type = \$1 AND server_id = \$2`).
		WithArgs("vmess", int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	updated, err := service.UpdateManagedServer(context.Background(), "vmess", int64(9), map[string]any{
		"entry_group_id": nil,
	})
	if err != nil {
		t.Fatalf("update managed server: %v", err)
	}
	if !updated {
		t.Fatalf("expected update success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestNormalizeManagedServerUpdatePayloadRejectsInvalidEntryGroup(t *testing.T) {
	if _, err := normalizeManagedServerUpdatePayload(map[string]any{
		"entry_group_id": "abc",
	}); err == nil {
		t.Fatalf("expected invalid entry group error")
	}
}
