package user

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
)

func TestChangePasswordInvalidatesAuthCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cache := session.NewAuthCache(time.Minute)
	cache.Store("token-1", "sess-1", &session.Identity{ID: 9, Email: "demo@example.com"})

	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	svc := &DBService{cfg: config.Config{}, db: db, authCache: cache}

	mock.ExpectQuery(`SELECT password, password_algo, password_salt`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"password", "password_algo", "password_salt"}).AddRow(string(hash), nil, nil))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE v2_user`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ok, err := svc.ChangePassword(context.Background(), 9, "old-password", "new-password")
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if !ok {
		t.Fatal("expected password change success")
	}
	if _, ok := cache.Get("token-1"); ok {
		t.Fatal("expected auth cache to be invalidated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
