package guest

import (
	"context"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInvitePreviewTrimsPaddedCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT code, user_id FROM v2_invite_code WHERE code = \$1 AND status = 0 LIMIT 1`).
		WithArgs("ABCD1234").
		WillReturnRows(sqlmock.NewRows([]string{"code", "user_id"}).AddRow("ABCD1234                        ", int64(7)))
	mock.ExpectQuery(`SELECT status, expired_at FROM v2_invite_campaign WHERE user_id = \$1 AND invite_code = \$2 AND status IN \(0, 1\) ORDER BY id DESC LIMIT 1`).
		WithArgs(int64(7), "ABCD1234").
		WillReturnRows(sqlmock.NewRows([]string{"status", "expired_at"}))

	service := NewDBService(config.Config{}, db)
	payload, err := service.InvitePreview(context.Background(), "ABCD1234")
	if err != nil {
		t.Fatalf("invite preview: %v", err)
	}
	if payload["code"] != "ABCD1234" {
		t.Fatalf("expected trimmed code, got %#v", payload["code"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
