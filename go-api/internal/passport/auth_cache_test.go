package passport

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRemoveAllSessionsInvalidatesAuthCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cache := session.NewAuthCache(time.Minute)
	cache.Store("token-1", "sess-1", &session.Identity{ID: 9, Email: "demo@example.com"})

	svc := NewDBServiceWithConfig(config.Config{}, db).WithAuthCache(cache)

	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := svc.removeAllSessions(context.Background(), 9); err != nil {
		t.Fatalf("remove all sessions: %v", err)
	}
	if _, ok := cache.Get("token-1"); ok {
		t.Fatal("expected auth cache to be invalidated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
