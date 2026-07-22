package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeleteManagedServerCleansClientEntryRuleMembers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	mock.ExpectQuery(`SELECT 1 FROM "v2_server_vmess" WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_member WHERE server_type = \$1 AND server_id = \$2`).
		WithArgs("vmess", int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`DELETE FROM v2_client_entry_group_member WHERE server_type = \$1 AND server_id = \$2`).
		WithArgs("vmess", int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "v2_server_vmess" WHERE id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ok, err := service.DeleteManagedServer(context.Background(), "vmess", 9)
	if err != nil || !ok {
		t.Fatalf("delete managed server: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
