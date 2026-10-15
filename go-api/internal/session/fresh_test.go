package session

import (
	"context"
	"errors"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
	"time"
)

func TestFreshAuthenticationRejectsRevokedSessionDespiteCachedIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{AppKey: "fresh-test-key"}
	cache := NewAuthCache(time.Minute)
	s := NewDBService(cfg, db).WithAuthCache(cache)
	token := signedAuthToken(t, cfg.AppKey, 7, "sid")
	cache.Store(token, "sid", &Identity{ID: 7, Email: "staff@example.test"})
	if _, err = s.Authenticate(context.Background(), token, false); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT u.id, u.email, u.is_admin, u.is_staff`).WithArgs(int64(7), "sid", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "admin", "staff"}))
	if _, err = s.AuthenticateFresh(context.Background(), "Bearer "+token, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked cached session accepted: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
