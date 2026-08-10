package user

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRecordClientEntrySubscribeActivityPersistsEverySuccessfulFetch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	mock.ExpectExec(`INSERT INTO v2_user_subscribe_activity`).
		WithArgs(int64(7), int64(1000)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_user_subscribe_activity`).
		WithArgs(int64(7), int64(1030)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_user_subscribe_activity`).
		WithArgs(int64(7), int64(1060)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	service.recordClientEntrySubscribeActivity(context.Background(), 7, 1000)
	service.recordClientEntrySubscribeActivity(context.Background(), 7, 1030)
	service.recordClientEntrySubscribeActivity(context.Background(), 7, 1060)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRecordClientEntrySubscribeActivityRetriesAfterWriteFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	mock.ExpectExec(`INSERT INTO v2_user_subscribe_activity`).
		WithArgs(int64(9), int64(2000)).
		WillReturnError(errors.New("temporary database error"))
	mock.ExpectExec(`INSERT INTO v2_user_subscribe_activity`).
		WithArgs(int64(9), int64(2001)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	service.recordClientEntrySubscribeActivity(context.Background(), 9, 2000)
	service.recordClientEntrySubscribeActivity(context.Background(), 9, 2001)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
