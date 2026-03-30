package telegram

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

type fakeTelegramAdminService struct {
	lastReply admin.TicketReplyRequest
	err       error
}

func (f *fakeTelegramAdminService) ReplyTicket(_ context.Context, req admin.TicketReplyRequest) (bool, error) {
	f.lastReply = req
	return true, f.err
}

func TestHandleWebhookBindCommandBindsTelegramUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{AppName: "forest-go", AppURL: "https://site.example", TelegramBotEnable: true, TelegramBotToken: "bot-token"}, db)
	svc = svc.WithUserResolver(func(context.Context, string) (int64, error) { return 9, nil })

	var sentText string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("expected chat id 123, got %d", chatID)
		}
		sentText = text
		return nil
	}

	mock.ExpectQuery(`SELECT telegram_id FROM v2_user WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"telegram_id"}).AddRow(sql.NullInt64{}))
	mock.ExpectExec(`UPDATE v2_user SET telegram_id = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(9), int64(123), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = svc.HandleWebhook(context.Background(), map[string]any{
		"message": map[string]any{
			"text":       "/bind https://site.example/api/v1/client/subscribe?token=sub-token",
			"message_id": 1,
			"chat": map[string]any{
				"id":   123,
				"type": "private",
			},
		},
	})
	if err != nil {
		t.Fatalf("handle webhook: %v", err)
	}
	if !strings.Contains(sentText, "绑定成功") {
		t.Fatalf("expected bind success message, got %q", sentText)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandleWebhookTrafficCommandSendsUsageSummary(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{AppName: "forest-go", AppURL: "https://site.example", TelegramBotEnable: true, TelegramBotToken: "bot-token"}, db)
	var sentText string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("expected chat id 123, got %d", chatID)
		}
		sentText = text
		return nil
	}

	mock.ExpectQuery(`SELECT email, transfer_enable, u, d FROM v2_user WHERE telegram_id = \$1 LIMIT 1`).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"email", "transfer_enable", "u", "d"}).AddRow("user@example.com", int64(1073741824), int64(107374182), int64(214748364)))

	err = svc.HandleWebhook(context.Background(), map[string]any{
		"message": map[string]any{
			"text":       "/traffic",
			"message_id": 1,
			"chat": map[string]any{
				"id":   123,
				"type": "private",
			},
		},
	})
	if err != nil {
		t.Fatalf("handle webhook: %v", err)
	}
	if !strings.Contains(sentText, "剩余流量") {
		t.Fatalf("expected traffic summary, got %q", sentText)
	}
}

func TestHandleWebhookUnbindCommandClearsTelegramBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, db)
	var sentText string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("expected chat id 123, got %d", chatID)
		}
		sentText = text
		return nil
	}

	mock.ExpectQuery(`SELECT id FROM v2_user WHERE telegram_id = \$1 LIMIT 1`).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`UPDATE v2_user SET telegram_id = NULL, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = svc.HandleWebhook(context.Background(), map[string]any{
		"message": map[string]any{
			"text":       "/unbind",
			"message_id": 1,
			"chat": map[string]any{
				"id":   123,
				"type": "private",
			},
		},
	})
	if err != nil {
		t.Fatalf("handle webhook: %v", err)
	}
	if !strings.Contains(sentText, "解绑成功") {
		t.Fatalf("expected unbind success message, got %q", sentText)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandleWebhookGetLatestURLCommandSendsConfiguredURL(t *testing.T) {
	svc := NewService(config.Config{AppName: "Forest", AppURL: "https://site.example", TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil)
	var sentText string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("expected chat id 123, got %d", chatID)
		}
		sentText = text
		return nil
	}

	err := svc.HandleWebhook(context.Background(), map[string]any{
		"message": map[string]any{
			"text":       "/getlatesturl",
			"message_id": 1,
			"chat": map[string]any{
				"id":   123,
				"type": "private",
			},
		},
	})
	if err != nil {
		t.Fatalf("handle webhook: %v", err)
	}
	if !strings.Contains(sentText, "https://site.example") || !strings.Contains(sentText, "Forest") {
		t.Fatalf("expected latest url message, got %q", sentText)
	}
}

func TestHandleWebhookReplyMessageUsesAdminService(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	adminService := &fakeTelegramAdminService{}
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, db).WithAdminService(adminService)
	svc.sendMessage = func(context.Context, int64, string) error { return nil }

	mock.ExpectQuery(`SELECT id, email, is_admin, is_staff FROM v2_user WHERE telegram_id = \$1 LIMIT 1`).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(7), "admin@example.com", int64(1), int64(0)))

	err = svc.HandleWebhook(context.Background(), map[string]any{
		"message": map[string]any{
			"text":       "handled",
			"message_id": 1,
			"chat": map[string]any{
				"id":   123,
				"type": "private",
			},
			"reply_to_message": map[string]any{
				"text": "#21 ticket",
			},
		},
	})
	if err != nil {
		t.Fatalf("handle webhook: %v", err)
	}
	if adminService.lastReply.ID != 21 || adminService.lastReply.AdminID != 7 || adminService.lastReply.Message != "handled" {
		t.Fatalf("unexpected ticket reply request: %#v", adminService.lastReply)
	}
}

func TestHandleWebhookChatJoinRequestApprovesAvailableUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, db)
	var approvedChatID, approvedUserID int64
	svc.approveJoin = func(_ context.Context, chatID, userID int64) error {
		approvedChatID = chatID
		approvedUserID = userID
		return nil
	}
	svc.declineJoin = func(context.Context, int64, int64) error { t.Fatal("did not expect decline"); return nil }

	mock.ExpectQuery(`SELECT id, banned, transfer_enable, u, d, expired_at FROM v2_user WHERE telegram_id = \$1 LIMIT 1`).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "banned", "transfer_enable", "u", "d", "expired_at"}).AddRow(int64(9), int64(0), int64(1073741824), int64(1), int64(1), sql.NullInt64{}))

	err = svc.HandleWebhook(context.Background(), map[string]any{
		"chat_join_request": map[string]any{
			"chat": map[string]any{"id": 555},
			"from": map[string]any{"id": 123},
		},
	})
	if err != nil {
		t.Fatalf("handle webhook: %v", err)
	}
	if approvedChatID != 555 || approvedUserID != 123 {
		t.Fatalf("unexpected approve call: chat=%d user=%d", approvedChatID, approvedUserID)
	}
}
