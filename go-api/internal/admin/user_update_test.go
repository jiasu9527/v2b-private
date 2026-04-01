package admin

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/session"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUpdateUserAllowsInviteUserOnlyUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := &DBService{db: db}

	mock.ExpectQuery(`SELECT email FROM v2_user WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("demo@example.com"))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE LOWER\(email\) = \$1 LIMIT 1`).
		WithArgs("owner@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectExec(`UPDATE v2_user SET invite_user_id = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(9), int64(7), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ok, err := svc.UpdateUser(context.Background(), UserUpdateRequest{
		ID: 9,
		Values: map[string]string{
			"invite_user_email": "owner@example.com",
		},
	})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if !ok {
		t.Fatal("expected update success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUpdateUserBannedInvalidatesAuthCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cache := session.NewAuthCache(time.Minute)
	cache.Store("token-1", "sess-1", &session.Identity{ID: 9, Email: "demo@example.com"})

	svc := &DBService{db: db, authCache: cache}

	mock.ExpectQuery(`SELECT email FROM v2_user WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("demo@example.com"))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE LOWER\(email\) = \$1 LIMIT 1`).
		WithArgs("demo@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET .* WHERE id = \$1`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ok, err := svc.UpdateUser(context.Background(), UserUpdateRequest{
		ID: 9,
		Values: map[string]string{
			"email":    "demo@example.com",
			"banned":   "1",
			"is_admin": "0",
			"is_staff": "0",
		},
	})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if !ok {
		t.Fatal("expected update success")
	}
	if _, ok := cache.Get("token-1"); ok {
		t.Fatal("expected auth cache to be invalidated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
