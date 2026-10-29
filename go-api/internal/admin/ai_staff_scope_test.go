package admin

import (
	"context"
	"regexp"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAIStaffScopeMutationAtomicPredicates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewDBService(config.Config{}, db)
	ctx := WithAIStaffScope(context.Background())
	mock.ExpectExec(`UPDATE v2_user\s+SET token = \$2, uuid = \$3, updated_at = \$4\s+WHERE id = \$1 AND is_admin=0 AND is_staff=0`).WithArgs(int64(4), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	if ok, err := s.ResetUserSecret(ctx, 4); err == nil || ok {
		t.Fatal("promoted user reset was accepted")
	}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET banned = $2, updated_at = $3 WHERE id = $1 AND is_admin=0 AND is_staff=0`)).WithArgs(int64(4), int64(1), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if ok, err := s.SetUserBanned(ctx, 4, 1); err == nil || ok {
		t.Fatal("promoted user ban was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAIStaffScopeDeleteLocksRolesBeforeAnyRelatedDeletion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := WithAIStaffScope(context.Background())
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id,is_admin,is_staff FROM v2_user WHERE id IN ($1,$2) ORDER BY id FOR UPDATE`)).WithArgs(int64(4), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "is_admin", "is_staff"}).AddRow(4, 0, 0).AddRow(5, 1, 0))
	if err := deleteUsersByIDList(ctx, tx, []int64{4, 5}, "delete failed"); err == nil {
		t.Fatal("delete ignored concurrently promoted target")
	}
	mock.ExpectRollback()
	tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
