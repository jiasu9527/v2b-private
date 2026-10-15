package telegram

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestRestrictedStaffTelegramMenuContainsOnlyExistingReport(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(config.Config{}, db)
	var markups []any
	svc.sendMessageMarkup = func(_ context.Context, _ int64, _ string, markup any) error {
		markups = append(markups, markup)
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).WithArgs(int64(123)).WillReturnRows(sqlmock.NewRows([]string{"id", "is_admin"}).AddRow(9, 0))
	if err := svc.HandleWebhook(context.Background(), monitorCommandPayload("/monitor", 123)); err != nil {
		t.Fatal(err)
	}
	if len(markups) != 2 || mustJSON(t, markups[0]) != mustJSON(t, entryMonitorReadOnlyReplyKeyboard()) || mustJSON(t, markups[1]) != mustJSON(t, entryMonitorReadOnlyInlineKeyboard()) {
		t.Fatalf("staff menu exposes mutation: %#v", markups)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedStaffCannotTriggerTelegramMutationsOrStaleButtons(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"run_text":       monitorTextPayload(clientEntryMonitorRunButtonText, 123, 1),
		"split_text":     monitorTextPayload(clientEntryManualAutoSplitText, 123, 1),
		"run_callback":   monitorCallbackPayload("run", "cem:r:all", 123),
		"run_picker":     monitorCallbackPayload("picker", "cem:p:1", 123),
		"split_callback": monitorCallbackPayload("split", "cea:c:21:1", 123),
		"split_picker":   monitorCallbackPayload("split-menu", "cea:p:1", 123),
	} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			svc := NewService(config.Config{}, db)
			// No controller is attached: authorization must fail before calling one.
			var denial string
			svc.sendMessage = func(_ context.Context, _ int64, text string) error { denial = text; return nil }
			svc.editMessageText = func(_ context.Context, _ int64, _ int64, text string, _ any) error { denial = text; return nil }
			svc.answerCallback = func(context.Context, string, string, bool) error { return nil }
			mock.ExpectQuery(telegramOperatorQueryPattern).WithArgs(int64(123)).WillReturnRows(sqlmock.NewRows([]string{"id", "is_admin"}).AddRow(9, 0))
			if err := svc.HandleWebhook(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(denial, "仅限管理员") && !strings.Contains(denial, "管理员可执行") {
				t.Fatalf("staff not denied before dispatch: %q", denial)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestrictedStaffCanReadExistingTelegramReportWithoutRunningProbe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	controller := &fakeEntryMonitorController{recent: func(context.Context) (string, error) { return "existing report", nil }, start: func(context.Context, int64, int64, string) (int64, error) {
		t.Fatal("staff read triggered probe")
		return 0, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	var sent string
	svc.sendMessage = func(_ context.Context, _ int64, text string) error { sent = text; return nil }
	svc.answerCallback = func(context.Context, string, string, bool) error { return nil }
	mock.ExpectQuery(telegramOperatorQueryPattern).WithArgs(int64(123)).WillReturnRows(sqlmock.NewRows([]string{"id", "is_admin"}).AddRow(9, 0))
	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("recent", clientEntryMonitorRecentCallback, 123)); err != nil {
		t.Fatal(err)
	}
	if sent != "existing report" {
		t.Fatalf("report missing: %q", sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedStaffCannotReplyToOtherUsersTelegramTicket(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminService := &fakeTelegramAdminService{}
	svc := NewService(config.Config{}, db).WithAdminService(adminService)
	mock.ExpectQuery(`SELECT id, email, is_admin, is_staff FROM v2_user WHERE telegram_id = \$1 AND banned = 0 AND is_admin = 1 LIMIT 1`).WithArgs(int64(123)).WillReturnError(sql.ErrNoRows)
	handled, err := svc.handleReplyMessage(context.Background(), webhookMessage{ChatID: 123, IsPrivate: true, ReplyText: "#21 ticket", Text: "unauthorized reply"})
	if err != nil || handled {
		t.Fatalf("staff ticket reply accepted: %v %v", handled, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
