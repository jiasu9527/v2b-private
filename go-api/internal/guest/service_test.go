package guest

import (
	"context"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPlansExposeTransferEnableAsGBForFrontend(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT \* FROM v2_plan WHERE "show" = 1 ORDER BY sort ASC NULLS LAST, id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "transfer_enable", "show"}).
			AddRow(int64(1), "轻量", int64(214748364800), int64(1)).
			AddRow(int64(2), "Legacy", int64(50), int64(1)))

	service := NewDBService(config.Config{}, db)
	plans, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	if plans[0]["transfer_enable"] != int64(200) {
		t.Fatalf("expected byte value converted to 200GB, got %#v", plans[0]["transfer_enable"])
	}
	if plans[1]["transfer_enable"] != int64(50) {
		t.Fatalf("expected legacy GB value preserved, got %#v", plans[1]["transfer_enable"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

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
