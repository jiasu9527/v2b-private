package user

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"forest/go-api/internal/config"
)

func TestPeekClientUserIDDoesNotConsumeOneTimeToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("otpn_dynamic-token").
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow("canonical-token", time.Now().Add(time.Hour).Unix()))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE token = \$1 LIMIT 1`).
		WithArgs("canonical-token").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(12)))

	service := NewDBService(config.Config{ShowSubscribeMethod: 1}, db)
	userID, err := service.PeekClientUserID(context.Background(), "dynamic-token")
	if err != nil {
		t.Fatalf("peek one-time token: %v", err)
	}
	if userID != 12 {
		t.Fatalf("expected user 12, got %d", userID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("peek unexpectedly mutated one-time token state: %v", err)
	}
}

func TestPeekClientUserIDUsesCachedTimedTokenWithoutMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("totp_timed-token").
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow("canonical-token", time.Now().Add(time.Hour).Unix()))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE token = \$1 LIMIT 1`).
		WithArgs("canonical-token").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(23)))

	service := NewDBService(config.Config{ShowSubscribeMethod: 2}, db)
	userID, err := service.PeekClientUserID(context.Background(), "timed-token")
	if err != nil {
		t.Fatalf("peek timed token: %v", err)
	}
	if userID != 23 {
		t.Fatalf("expected user 23, got %d", userID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected timed token queries: %v", err)
	}
}

func TestPeekClientUserIDRejectsMissingOneTimeToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("otpn_missing").
		WillReturnError(sql.ErrNoRows)

	service := NewDBService(config.Config{ShowSubscribeMethod: 1}, db)
	if _, err := service.PeekClientUserID(context.Background(), "missing"); err != ErrClientTokenInvalid {
		t.Fatalf("expected invalid token error, got %v", err)
	}
}
