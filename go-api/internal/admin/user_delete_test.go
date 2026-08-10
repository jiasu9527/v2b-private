package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeleteUsersByIDListClearsInviteUserReferencesInOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_order WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET invite_user_id = NULL, updated_at = \$1 WHERE invite_user_id IN \(\$2\)`).
		WithArgs(sqlmock.AnyArg(), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`DELETE FROM v2_invite_code WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_ticket_message WHERE ticket_id IN \(SELECT id FROM v2_ticket WHERE user_id IN \(\$1\)\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_ticket WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET invite_user_id = NULL, updated_at = \$1 WHERE invite_user_id IN \(\$2\)`).
		WithArgs(sqlmock.AnyArg(), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_split_assignment WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_user_subscribe_activity WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_user WHERE id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	if err := deleteUsersByIDList(context.Background(), tx, []int64{9}, "删除失败"); err != nil {
		t.Fatalf("delete users: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
