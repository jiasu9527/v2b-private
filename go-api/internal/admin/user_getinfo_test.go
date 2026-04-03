package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetUserInfoByIDReturnsDeletedPlaceholderWhenUserMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := &DBService{db: db}

	mock.ExpectQuery(`SELECT row_to_json\(user_row\)`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"row_to_json"}))

	user, err := svc.GetUserInfoByID(context.Background(), 9)
	if err != nil {
		t.Fatalf("get user info: %v", err)
	}
	if user == nil {
		t.Fatal("expected deleted placeholder, got nil")
	}
	if user["id"] != int64(9) {
		t.Fatalf("expected placeholder id 9, got %#v", user["id"])
	}
	if user["email"] != "已删除用户 #9" {
		t.Fatalf("expected deleted placeholder email, got %#v", user["email"])
	}
	if user["deleted_user"] != int64(1) {
		t.Fatalf("expected deleted_user flag, got %#v", user["deleted_user"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
