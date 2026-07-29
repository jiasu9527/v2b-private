package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

type fakeEntryMonitorController struct {
	start            func(context.Context, int64, int64, string) (int64, error)
	startWithMessage func(context.Context, []int64, int64, int64, int64, string) (int64, error)
	options          []admin.ClientEntryMonitorRunOption
	recent           func(context.Context) (string, error)
	recentImage      func(context.Context) ([]byte, string, error)
}

func (f *fakeEntryMonitorController) ListClientEntryMonitorRunOptions(context.Context) ([]admin.ClientEntryMonitorRunOption, error) {
	if f == nil {
		return nil, nil
	}
	return f.options, nil
}

func (f *fakeEntryMonitorController) StartClientEntryMonitorRunForPoliciesWithMessage(ctx context.Context, policyIDs []int64, userID, chatID, messageID int64, requestKey string) (int64, error) {
	if f == nil || f.startWithMessage == nil {
		return 0, nil
	}
	return f.startWithMessage(ctx, policyIDs, userID, chatID, messageID, requestKey)
}

const telegramOperatorQueryPattern = `(?s)SELECT id.*FROM v2_user.*WHERE telegram_id = \$1 AND banned = 0 AND \(is_admin = 1 OR is_staff = 1\).*ORDER BY id ASC.*LIMIT 1`

func (f *fakeEntryMonitorController) StartClientEntryMonitorRun(ctx context.Context, userID, chatID int64, requestKey string) (int64, error) {
	if f == nil || f.start == nil {
		return 0, nil
	}
	return f.start(ctx, userID, chatID, requestKey)
}

func (f *fakeEntryMonitorController) RecentClientEntryMonitorReport(ctx context.Context) (string, error) {
	if f == nil || f.recent == nil {
		return "", nil
	}
	return f.recent(ctx)
}

func (f *fakeEntryMonitorController) RecentClientEntryMonitorReportImage(ctx context.Context) ([]byte, string, error) {
	if f == nil || f.recentImage == nil {
		return nil, "", nil
	}
	return f.recentImage(ctx)
}

type telegramRoundTripFunc func(*http.Request) (*http.Response, error)

func (f telegramRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMonitorCommandsShowPersistentAndInlineKeyboardsToBoundOperators(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
	}{
		{name: "admin start", command: "/start"},
		{name: "staff monitor", command: "/monitor@forest_bot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			svc := NewService(config.Config{}, db)
			type delivery struct {
				chatID int64
				text   string
				markup any
			}
			var deliveries []delivery
			svc.sendMessageMarkup = func(_ context.Context, chatID int64, text string, markup any) error {
				deliveries = append(deliveries, delivery{chatID: chatID, text: text, markup: markup})
				return nil
			}

			mock.ExpectQuery(telegramOperatorQueryPattern).
				WithArgs(int64(123)).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

			err = svc.HandleWebhook(context.Background(), monitorCommandPayload(test.command, 123))
			if err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			if len(deliveries) != 2 {
				t.Fatalf("deliveries = %#v", deliveries)
			}
			if deliveries[0].chatID != 123 || !strings.Contains(deliveries[0].text, "固定在输入框下方") {
				t.Fatalf("reply keyboard message = %#v", deliveries[0])
			}
			replyMarkup, ok := deliveries[0].markup.(replyKeyboardMarkup)
			if !ok {
				t.Fatalf("reply markup type = %T", deliveries[0].markup)
			}
			if !replyMarkup.IsPersistent || !replyMarkup.ResizeKeyboard || mustJSON(t, replyMarkup) != mustJSON(t, entryMonitorReplyKeyboard()) {
				t.Fatalf("reply keyboard = %s", mustJSON(t, replyMarkup))
			}
			if deliveries[1].chatID != 123 || deliveries[1].text != "快捷操作" {
				t.Fatalf("inline keyboard message = %#v", deliveries[1])
			}
			inlineMarkup, ok := deliveries[1].markup.(inlineKeyboardMarkup)
			if !ok {
				t.Fatalf("inline markup type = %T", deliveries[1].markup)
			}
			if mustJSON(t, inlineMarkup) != mustJSON(t, entryMonitorInlineKeyboard()) {
				t.Fatalf("inline keyboard = %s", mustJSON(t, inlineMarkup))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}

func TestMonitorCommandRejectsBoundRegularUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{}, db)
	var sent string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		sent = text
		return nil
	}
	svc.sendMessageMarkup = func(context.Context, int64, string, any) error {
		t.Fatal("regular user must not receive the monitor keyboard")
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnError(sql.ErrNoRows)

	if err := svc.HandleWebhook(context.Background(), monitorCommandPayload("/monitor", 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !strings.Contains(sent, "无权限") {
		t.Fatalf("denial message = %q", sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorCommandRejectsBannedOperator(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{}, db)
	var sent string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		sent = text
		return nil
	}
	svc.sendMessageMarkup = func(context.Context, int64, string, any) error {
		t.Fatal("banned operator must not receive the monitor keyboard")
		return nil
	}
	// The banned predicate makes a bound but banned admin/staff invisible to
	// the operator lookup, so PostgreSQL returns no rows.
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnError(sql.ErrNoRows)

	if err := svc.HandleWebhook(context.Background(), monitorCommandPayload("/monitor", 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !strings.Contains(sent, "无权限") {
		t.Fatalf("denial message = %q", sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorRunCallbackAcknowledgesAndStartsForAuthorizedStaff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	acknowledged := false
	controller := &fakeEntryMonitorController{options: []admin.ClientEntryMonitorRunOption{{PolicyID: 1, Name: "华东", TargetCount: 2}}, startWithMessage: func(_ context.Context, policyIDs []int64, userID, chatID, messageID int64, requestKey string) (int64, error) {
		if !acknowledged {
			t.Fatal("callback must be acknowledged before monitor work starts")
		}
		if len(policyIDs) != 1 || policyIDs[0] != 1 || userID != 9 || chatID != 123 || messageID != 1 || requestKey != "telegram-callback:123:1:cem:r:1" {
			t.Fatalf("start args = policies=%v user=%d chat=%d message=%d request=%q", policyIDs, userID, chatID, messageID, requestKey)
		}
		return 42, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(_ context.Context, callbackID, text string, showAlert bool) error {
		if callbackID != "callback-1" || text != "" || showAlert {
			t.Fatalf("callback answer = id %q text %q alert %v", callbackID, text, showAlert)
		}
		acknowledged = true
		return nil
	}
	var edited string
	svc.editMessageText = func(_ context.Context, chatID, messageID int64, text string, markup any) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		if messageID != 1 || mustJSON(t, markup) != mustJSON(t, entryMonitorEmptyInlineKeyboard()) {
			t.Fatalf("edit message=%d markup=%s", messageID, mustJSON(t, markup))
		}
		edited = text
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-1", "cem:r:1", 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !acknowledged || !strings.Contains(edited, "检测已启动") || strings.Contains(edited, "42") || strings.Contains(edited, "#") {
		t.Fatalf("acknowledged = %v, message = %q", acknowledged, edited)
	}
	svc.entryMonitorMenusMu.Lock()
	menu := svc.entryMonitorMenus["123:1"]
	svc.entryMonitorMenusMu.Unlock()
	if menu == nil || !menu.started {
		t.Fatal("started menu was not retained for callback idempotency")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorRecentCallbackAcknowledgesAndSplitsReport(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	plainReport := strings.Repeat("测", telegramMessageLimit+17)
	report := "**" + plainReport + "**"
	acknowledged := false
	controller := &fakeEntryMonitorController{recent: func(context.Context) (string, error) {
		if !acknowledged {
			t.Fatal("callback must be acknowledged before loading a report")
		}
		return report, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error {
		acknowledged = true
		return nil
	}
	var chunks []string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		chunks = append(chunks, text)
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-2", clientEntryMonitorRecentCallback, 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if len(chunks) != 2 || len([]rune(chunks[0])) > telegramMessageLimit || strings.Join(chunks, "") != plainReport {
		t.Fatalf("report chunks = lengths %v, joined matches = %v", runeLengths(chunks), strings.Join(chunks, "") == plainReport)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorRecentCallbackSendsImageWithInlineKeyboard(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	photo := []byte{0x89, 'P', 'N', 'G'}
	acknowledged := false
	controller := &fakeEntryMonitorController{recentImage: func(context.Context) ([]byte, string, error) {
		if !acknowledged {
			t.Fatal("callback must be acknowledged before rendering an image")
		}
		return photo, "近期入口检测", nil
	}, recent: func(context.Context) (string, error) {
		t.Fatal("text report must not be loaded when image rendering succeeds")
		return "", nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(_ context.Context, callbackID, text string, showAlert bool) error {
		if callbackID != "callback-image" || text != "" || showAlert {
			t.Fatalf("callback answer = id %q text %q alert %v", callbackID, text, showAlert)
		}
		acknowledged = true
		return nil
	}
	sent := false
	svc.sendPhoto = func(_ context.Context, chatID int64, gotPhoto []byte, caption string, markup any) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		if !bytes.Equal(gotPhoto, photo) || caption != "近期入口检测" {
			t.Fatalf("photo delivery = %x caption %q", gotPhoto, caption)
		}
		if mustJSON(t, markup) != mustJSON(t, entryMonitorInlineKeyboard()) {
			t.Fatalf("inline markup = %s", mustJSON(t, markup))
		}
		sent = true
		return nil
	}
	svc.sendMessage = func(context.Context, int64, string) error {
		t.Fatal("successful image delivery must not send a text fallback")
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-image", clientEntryMonitorRecentCallback, 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !sent {
		t.Fatal("recent image was not sent")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorPhotoRunCallbackOpensTextPickerForEditableProgress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	for range 3 {
		mock.ExpectQuery(telegramOperatorQueryPattern).
			WithArgs(int64(123)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	}

	started := 0
	controller := &fakeEntryMonitorController{
		options: []admin.ClientEntryMonitorRunOption{{PolicyID: 1, Name: "华东", TargetCount: 2}},
		startWithMessage: func(_ context.Context, policyIDs []int64, _, chatID, messageID int64, requestKey string) (int64, error) {
			if len(policyIDs) != 1 || policyIDs[0] != 1 || chatID != 123 || messageID != 99 || requestKey != "telegram-callback:123:99:cem:r:1" {
				t.Fatalf("start args = policies=%v chat=%d message=%d request=%q", policyIDs, chatID, messageID, requestKey)
			}
			started++
			return 1, nil
		},
	}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error { return nil }
	pickers := 0
	svc.sendMessageMarkup = func(_ context.Context, chatID int64, text string, markup any) error {
		if chatID != 123 || !strings.Contains(text, "选择要主动检测") || mustJSON(t, markup) != mustJSON(t, entryMonitorRulesKeyboardForTest()) {
			t.Fatalf("picker = chat=%d text=%q markup=%s", chatID, text, mustJSON(t, markup))
		}
		pickers++
		return nil
	}
	edits := 0
	svc.editMessageText = func(_ context.Context, chatID, messageID int64, text string, markup any) error {
		if chatID != 123 || messageID != 99 || !strings.Contains(text, "检测已启动") || mustJSON(t, markup) != mustJSON(t, entryMonitorEmptyInlineKeyboard()) {
			t.Fatalf("edit = chat=%d message=%d text=%q markup=%s", chatID, messageID, text, mustJSON(t, markup))
		}
		edits++
		return nil
	}

	photoPayload := monitorCallbackPayload("photo-run", clientEntryMonitorRunCallback, 123)
	photoMessage := photoPayload["callback_query"].(map[string]any)["message"].(map[string]any)
	delete(photoMessage, "text")
	photoMessage["photo"] = []any{map[string]any{"file_id": "report"}}
	for range 2 {
		if err := svc.HandleWebhook(context.Background(), photoPayload); err != nil {
			t.Fatalf("photo callback: %v", err)
		}
	}
	if pickers != 1 || edits != 0 || started != 0 {
		t.Fatalf("after photo callback pickers=%d edits=%d started=%d", pickers, edits, started)
	}

	textPayload := monitorCallbackPayload("text-rule", "cem:r:1", 123)
	textPayload["callback_query"].(map[string]any)["message"].(map[string]any)["message_id"] = int64(99)
	if err := svc.HandleWebhook(context.Background(), textPayload); err != nil {
		t.Fatalf("text picker callback: %v", err)
	}
	if pickers != 1 || edits != 1 || started != 1 {
		t.Fatalf("final pickers=%d edits=%d started=%d", pickers, edits, started)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorRunReplyKeyboardTextCreatesOnePickerPerWebhookMessage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	controller := &fakeEntryMonitorController{options: []admin.ClientEntryMonitorRunOption{{PolicyID: 1, Name: "华东", TargetCount: 2}}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error {
		t.Fatal("reply keyboard text must not use callback acknowledgement")
		return nil
	}
	var messages []string
	svc.sendMessageMarkup = func(_ context.Context, chatID int64, text string, markup any) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		if mustJSON(t, markup) != mustJSON(t, entryMonitorRulesKeyboardForTest()) {
			t.Fatalf("picker markup = %s", mustJSON(t, markup))
		}
		messages = append(messages, text)
		return nil
	}
	for range 2 {
		mock.ExpectQuery(telegramOperatorQueryPattern).
			WithArgs(int64(123)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	}

	payload := monitorTextPayload(clientEntryMonitorRunButtonText, 123, 77)
	for range 2 {
		if err := svc.HandleWebhook(context.Background(), payload); err != nil {
			t.Fatalf("HandleWebhook: %v", err)
		}
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "选择要主动检测") {
		t.Fatalf("messages = %#v", messages)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorRecentReplyKeyboardTextUsesSameAuthorizedAction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	recentCalled := false
	controller := &fakeEntryMonitorController{recent: func(context.Context) (string, error) {
		recentCalled = true
		return "**用户入口一键检测 #42**\n状态：已完成", nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	var sent string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		sent += text
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorTextPayload(clientEntryMonitorRecentButtonText, 123, 78)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !recentCalled || !strings.Contains(sent, "🧭 用户入口检测结果") || strings.Contains(sent, "**") || strings.Contains(sent, "#42") {
		t.Fatalf("recent called = %v, message = %q", recentCalled, sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMonitorReplyKeyboardTextRejectsUnauthorizedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{}, db).WithEntryMonitorController(&fakeEntryMonitorController{
		start: func(context.Context, int64, int64, string) (int64, error) {
			t.Fatal("unauthorized reply keyboard action must not start a run")
			return 0, nil
		},
	})
	var sent string
	svc.sendMessage = func(_ context.Context, _ int64, text string) error {
		sent = text
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnError(sql.ErrNoRows)

	if err := svc.HandleWebhook(context.Background(), monitorTextPayload(clientEntryMonitorRunButtonText, 123, 79)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !strings.Contains(sent, "无权限") {
		t.Fatalf("message = %q", sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMalformedOrGroupMonitorReplyTextDoesNotStartRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{}, db).WithEntryMonitorController(&fakeEntryMonitorController{
		start: func(context.Context, int64, int64, string) (int64, error) {
			t.Fatal("malformed or group message must not start a run")
			return 0, nil
		},
	})
	missingID := monitorTextPayload(clientEntryMonitorRunButtonText, 123, 0)
	if err := svc.HandleWebhook(context.Background(), missingID); err != nil {
		t.Fatalf("missing message id: %v", err)
	}
	group := monitorTextPayload(clientEntryMonitorRunButtonText, -1001, 80)
	group["message"].(map[string]any)["chat"].(map[string]any)["type"] = "group"
	if err := svc.HandleWebhook(context.Background(), group); err != nil {
		t.Fatalf("group message: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database access: %v", err)
	}
}

func TestMonitorCallbackRejectsUnauthorizedUserWithoutStarting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	controller := &fakeEntryMonitorController{start: func(context.Context, int64, int64, string) (int64, error) {
		t.Fatal("unauthorized user must not start a monitor run")
		return 0, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	answerCount := 0
	svc.answerCallback = func(context.Context, string, string, bool) error {
		answerCount++
		return nil
	}
	var sent string
	svc.editMessageText = func(_ context.Context, _, _ int64, text string, _ any) error {
		sent = text
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnError(sql.ErrNoRows)

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-3", clientEntryMonitorRunCallback, 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if answerCount != 1 || !strings.Contains(sent, "无权限") {
		t.Fatalf("answer count = %d, message = %q", answerCount, sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUnknownCallbackIsAcknowledgedWithoutDatabaseLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{}, db)
	answerCount := 0
	svc.answerCallback = func(context.Context, string, string, bool) error {
		answerCount++
		return nil
	}
	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-4", "other:action", 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if answerCount != 1 {
		t.Fatalf("answer count = %d", answerCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database access: %v", err)
	}
}

func TestMonitorCallbackStillStartsWhenAcknowledgementFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	started := false
	controller := &fakeEntryMonitorController{options: []admin.ClientEntryMonitorRunOption{{PolicyID: 1, Name: "华东", TargetCount: 2}}, startWithMessage: func(_ context.Context, policyIDs []int64, userID, chatID, messageID int64, requestKey string) (int64, error) {
		started = true
		if len(policyIDs) != 1 || policyIDs[0] != 1 || userID != 9 || chatID != 123 || messageID != 1 || requestKey != "telegram-callback:123:1:cem:r:1" {
			t.Fatalf("start args = policies=%v user=%d chat=%d message=%d request=%q", policyIDs, userID, chatID, messageID, requestKey)
		}
		return 42, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error {
		return errors.New("telegram response lost")
	}
	svc.editMessageText = func(context.Context, int64, int64, string, any) error { return nil }
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-ack-failed", "cem:r:1", 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !started {
		t.Fatal("callback action did not start after acknowledgement failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestForwardedMonitorCallbackCannotSendResultsToGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{}, db).WithEntryMonitorController(&fakeEntryMonitorController{
		start: func(context.Context, int64, int64, string) (int64, error) {
			t.Fatal("group callback must not start a monitor run")
			return 0, nil
		},
	})
	answerCount := 0
	svc.answerCallback = func(context.Context, string, string, bool) error {
		answerCount++
		return nil
	}
	payload := map[string]any{"callback_query": map[string]any{
		"id":   "callback-group",
		"data": clientEntryMonitorRunCallback,
		"from": map[string]any{"id": int64(123)},
		"message": map[string]any{
			"message_id": int64(1),
			"chat":       map[string]any{"id": int64(-1001), "type": "group"},
		},
	}}
	if err := svc.HandleWebhook(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if answerCount != 1 {
		t.Fatalf("answer count = %d", answerCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database access: %v", err)
	}
}

func TestDirectNotifierNotifyChatAndAdminsSplitLongMessages(t *testing.T) {
	message := strings.Repeat("a", 3000) + "\n" + strings.Repeat("界", 2000)
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil)
	requestedIncludeStaff := false
	svc.resolveRecipients = func(_ context.Context, includeStaff bool) ([]int64, error) {
		requestedIncludeStaff = includeStaff
		return []int64{22}, nil
	}
	type delivery struct {
		chatID int64
		text   string
	}
	var deliveries []delivery
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		deliveries = append(deliveries, delivery{chatID: chatID, text: text})
		return nil
	}

	if err := svc.DirectNotifier().NotifyChat(context.Background(), 11, message); err != nil {
		t.Fatalf("NotifyChat: %v", err)
	}
	if len(deliveries) != 2 || deliveries[0].chatID != 11 || strings.Join([]string{deliveries[0].text, deliveries[1].text}, "") != message {
		t.Fatalf("NotifyChat deliveries = %#v", deliveries)
	}
	deliveries = nil
	if err := svc.DirectNotifier().NotifyAdmins(context.Background(), message, true); err != nil {
		t.Fatalf("NotifyAdmins: %v", err)
	}
	if !requestedIncludeStaff || len(deliveries) != 2 || deliveries[0].chatID != 22 || strings.Join([]string{deliveries[0].text, deliveries[1].text}, "") != message {
		t.Fatalf("NotifyAdmins includeStaff=%v deliveries=%#v", requestedIncludeStaff, deliveries)
	}
	for _, item := range deliveries {
		if len([]rune(item.text)) > telegramMessageLimit {
			t.Fatalf("chunk length = %d", len([]rune(item.text)))
		}
	}
}

func TestDirectNotifierIncludesBoundAdminsAndStaff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, db)
	var chats []int64
	svc.sendMessage = func(_ context.Context, chatID int64, _ string) error {
		chats = append(chats, chatID)
		return nil
	}
	mock.ExpectQuery(`(?s)SELECT telegram_id.*FROM v2_user.*telegram_id IS NOT NULL.*banned = 0.*\(is_admin = 1 OR is_staff = 1\).*ORDER BY id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"telegram_id"}).AddRow(int64(11)).AddRow(int64(22)))

	if err := svc.DirectNotifier().NotifyAdmins(context.Background(), "入口告警", true); err != nil {
		t.Fatalf("NotifyAdmins: %v", err)
	}
	if len(chats) != 2 || chats[0] != 11 || chats[1] != 22 {
		t.Fatalf("recipient chats = %#v", chats)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestTelegramSendMessageEncodesInlineKeyboardMarkup(t *testing.T) {
	svc := NewService(config.Config{TelegramBotToken: "bot-token"}, nil)
	called := false
	svc.client = &http.Client{Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		if request.URL.Path != "/botbot-token/sendMessage" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if request.Form.Get("chat_id") != "123" || request.Form.Get("text") != "用户入口检测" {
			t.Fatalf("form = %#v", request.Form)
		}
		if _, exists := request.Form["parse_mode"]; exists {
			t.Fatalf("plain-text message must not set parse_mode: %#v", request.Form)
		}
		var markup inlineKeyboardMarkup
		if err := json.Unmarshal([]byte(request.Form.Get("reply_markup")), &markup); err != nil {
			t.Fatalf("decode reply_markup: %v", err)
		}
		if mustJSON(t, markup) != mustJSON(t, entryMonitorInlineKeyboard()) {
			t.Fatalf("markup = %s", mustJSON(t, markup))
		}
		return telegramOKResponse(), nil
	})}

	if err := svc.sendMessageWithMarkupNow(context.Background(), 123, "**用户入口检测**", entryMonitorInlineKeyboard()); err != nil {
		t.Fatalf("sendMessageWithMarkupNow: %v", err)
	}
	if !called {
		t.Fatal("telegram API was not called")
	}
}

func TestTelegramSendPhotoEncodesMultipartPayload(t *testing.T) {
	svc := NewService(config.Config{TelegramBotToken: "bot-token"}, nil)
	wantPhoto := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	called := false
	svc.client = &http.Client{Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		if request.Method != http.MethodPost || request.URL.Path != "/botbot-token/sendPhoto" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Fatalf("content type = %q", request.Header.Get("Content-Type"))
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if request.FormValue("chat_id") != "123" || request.FormValue("caption") != "入口检测" {
			t.Fatalf("form values = %#v", request.Form)
		}
		var markup inlineKeyboardMarkup
		if err := json.Unmarshal([]byte(request.FormValue("reply_markup")), &markup); err != nil {
			t.Fatalf("decode reply_markup: %v", err)
		}
		if mustJSON(t, markup) != mustJSON(t, entryMonitorInlineKeyboard()) {
			t.Fatalf("markup = %s", mustJSON(t, markup))
		}
		file, header, err := request.FormFile("photo")
		if err != nil {
			t.Fatalf("FormFile(photo): %v", err)
		}
		defer file.Close()
		if header.Filename != "entry-monitor.png" {
			t.Fatalf("photo filename = %q", header.Filename)
		}
		gotPhoto, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read photo: %v", err)
		}
		if !bytes.Equal(gotPhoto, wantPhoto) {
			t.Fatalf("photo bytes = %x, want %x", gotPhoto, wantPhoto)
		}
		return telegramOKResponse(), nil
	})}

	if err := svc.sendPhotoWithMarkupNow(context.Background(), 123, wantPhoto, "**入口检测**", entryMonitorInlineKeyboard()); err != nil {
		t.Fatalf("sendPhotoWithMarkupNow: %v", err)
	}
	if !called {
		t.Fatal("telegram sendPhoto API was not called")
	}
}

func TestDirectNotifierNotifyChatImageUsesPhotoSender(t *testing.T) {
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil)
	wantPhoto := []byte{1, 2, 3}
	called := false
	svc.sendPhoto = func(_ context.Context, chatID int64, photo []byte, caption string, markup any) error {
		if chatID != 456 || !bytes.Equal(photo, wantPhoto) || caption != "检测报告" || markup != nil {
			t.Fatalf("image delivery = chat %d photo %v caption %q markup %#v", chatID, photo, caption, markup)
		}
		called = true
		return nil
	}

	if err := svc.DirectNotifier().NotifyChatImage(context.Background(), 456, wantPhoto, "检测报告"); err != nil {
		t.Fatalf("NotifyChatImage: %v", err)
	}
	if !called {
		t.Fatal("DirectNotifier did not call sendPhoto")
	}
}

func TestDirectNotifierEditChatMessageClearsInlineKeyboard(t *testing.T) {
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil)
	called := false
	svc.editMessageText = func(_ context.Context, chatID, messageID int64, text string, markup any) error {
		if chatID != 456 || messageID != 78 || text != "进度" || mustJSON(t, markup) != mustJSON(t, entryMonitorEmptyInlineKeyboard()) {
			t.Fatalf("edit delivery = chat=%d message=%d text=%q markup=%s", chatID, messageID, text, mustJSON(t, markup))
		}
		called = true
		return nil
	}
	if err := svc.DirectNotifier().EditChatMessage(context.Background(), 456, 78, "进度"); err != nil {
		t.Fatalf("EditChatMessage: %v", err)
	}
	if !called {
		t.Fatal("DirectNotifier did not edit the progress message")
	}
}

func TestTelegramSendMessageEncodesPersistentReplyKeyboardMarkup(t *testing.T) {
	svc := NewService(config.Config{TelegramBotToken: "bot-token"}, nil)
	svc.client = &http.Client{Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/botbot-token/sendMessage" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if request.Form.Get("chat_id") != "123" || request.Form.Get("text") != "快捷菜单" {
			t.Fatalf("form = %#v", request.Form)
		}
		if _, exists := request.Form["parse_mode"]; exists {
			t.Fatalf("plain-text message must not set parse_mode: %#v", request.Form)
		}
		var markup replyKeyboardMarkup
		if err := json.Unmarshal([]byte(request.Form.Get("reply_markup")), &markup); err != nil {
			t.Fatalf("decode reply_markup: %v", err)
		}
		if !markup.IsPersistent || !markup.ResizeKeyboard || markup.InputFieldPlaceholder != "请选择入口检测操作" {
			t.Fatalf("reply keyboard options = %#v", markup)
		}
		if mustJSON(t, markup) != mustJSON(t, entryMonitorReplyKeyboard()) {
			t.Fatalf("markup = %s", mustJSON(t, markup))
		}
		return telegramOKResponse(), nil
	})}

	if err := svc.sendMessageWithMarkupNow(context.Background(), 123, "快捷菜单", entryMonitorReplyKeyboard()); err != nil {
		t.Fatalf("sendMessageWithMarkupNow: %v", err)
	}
}

func TestTelegramAnswerCallbackQueryUsesDedicatedMethod(t *testing.T) {
	svc := NewService(config.Config{TelegramBotToken: "bot-token"}, nil)
	svc.client = &http.Client{Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/botbot-token/answerCallbackQuery" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if request.Form.Get("callback_query_id") != "callback-5" || request.Form.Get("show_alert") != "false" {
			t.Fatalf("form = %#v", request.Form)
		}
		return telegramOKResponse(), nil
	})}
	if err := svc.answerCallbackQueryNow(context.Background(), "callback-5", "", false); err != nil {
		t.Fatalf("answerCallbackQueryNow: %v", err)
	}
}

func TestEntryMonitorRulePickerPaginatesEightOptionsAndUsesShortCallbacks(t *testing.T) {
	options := make([]admin.ClientEntryMonitorRunOption, 0, 9)
	for id := int64(1); id <= 9; id++ {
		options = append(options, admin.ClientEntryMonitorRunOption{PolicyID: id, Name: "规则", TargetCount: id})
	}
	controller := &fakeEntryMonitorController{options: options}
	text, keyboard, err := entryMonitorRulesPage(context.Background(), controller, 2)
	if err != nil || !strings.Contains(text, "第 2/2 页") {
		t.Fatalf("page = %q err=%v", text, err)
	}
	if len(keyboard.InlineKeyboard) != 3 || keyboard.InlineKeyboard[0][0].CallbackData != "cem:r:9" || keyboard.InlineKeyboard[1][0].CallbackData != "cem:r:all" || keyboard.InlineKeyboard[2][0].CallbackData != "cem:p:1" {
		t.Fatalf("keyboard = %#v", keyboard)
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if len(button.CallbackData) > 64 {
				t.Fatalf("callback too long: %q", button.CallbackData)
			}
		}
	}
	ids, label := selectedEntryMonitorRuleOptions(options, clientEntryMonitorCallback{RunAll: true})
	if len(ids) != 9 || ids[0] != 1 || ids[8] != 9 || label != "全部 9 个规则组" {
		t.Fatalf("all selection = ids=%v label=%q", ids, label)
	}
}

func TestEntryMonitorRuleCallbacksRejectInvalidValuesAndRequireMessageID(t *testing.T) {
	for _, value := range []string{"cem:p:0", "cem:p:1000", "cem:r:0", "cem:r:-1", "cem:r:bad", "cem:x:1", "client_entry_monitor:run"} {
		if _, ok := parseClientEntryMonitorCallback(value); ok {
			t.Fatalf("invalid callback accepted: %q", value)
		}
	}
	if got, ok := parseClientEntryMonitorCallback("cem:r:42"); !ok || got.PolicyID != 42 {
		t.Fatalf("valid callback = %#v ok=%v", got, ok)
	}
	payload := monitorCallbackPayload("missing-message", "cem:p:1", 123)
	delete(payload["callback_query"].(map[string]any)["message"].(map[string]any), "message_id")
	if _, ok := parseWebhookCallbackQuery(payload["callback_query"].(map[string]any)); ok {
		t.Fatal("callback without message_id was accepted")
	}
}

func TestEntryMonitorMenuLockPreventsQueuedRuleFromOverwritingStartedRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	for range 2 {
		mock.ExpectQuery(telegramOperatorQueryPattern).WithArgs(int64(123)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	}

	started := make(chan struct{})
	continueStart := make(chan struct{})
	controller := &fakeEntryMonitorController{
		options: []admin.ClientEntryMonitorRunOption{{PolicyID: 1, Name: "华东", TargetCount: 1}, {PolicyID: 2, Name: "华南", TargetCount: 1}},
		startWithMessage: func(context.Context, []int64, int64, int64, int64, string) (int64, error) {
			close(started)
			<-continueStart
			return 1, nil
		},
	}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error { return nil }
	var editsMu sync.Mutex
	var edits []string
	svc.editMessageText = func(_ context.Context, _, _ int64, text string, _ any) error {
		editsMu.Lock()
		edits = append(edits, text)
		editsMu.Unlock()
		return nil
	}

	runDone := make(chan error, 1)
	go func() {
		runDone <- svc.HandleWebhook(context.Background(), monitorCallbackPayload("run", "cem:r:1", 123))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("run did not reach start")
	}
	ruleDone := make(chan error, 1)
	go func() {
		ruleDone <- svc.HandleWebhook(context.Background(), monitorCallbackPayload("other-rule", "cem:r:2", 123))
	}()
	close(continueStart)
	if err := <-runDone; err != nil {
		t.Fatalf("run callback: %v", err)
	}
	if err := <-ruleDone; err != nil {
		t.Fatalf("second rule callback: %v", err)
	}
	editsMu.Lock()
	defer editsMu.Unlock()
	if len(edits) != 1 || !strings.Contains(edits[0], "检测已启动") {
		t.Fatalf("edits = %#v", edits)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEntryMonitorMenuLockDeduplicatesRetryAndExpires(t *testing.T) {
	svc := NewService(config.Config{}, nil)
	key, state := svc.lockEntryMonitorMenu(123, 7)
	svc.unlockEntryMonitorMenu(key, state, true)

	queued := make(chan bool, 1)
	go func() {
		queuedKey, queuedState := svc.lockEntryMonitorMenu(123, 7)
		queued <- queuedState.started
		svc.unlockEntryMonitorMenu(queuedKey, queuedState, false)
	}()
	select {
	case started := <-queued:
		if !started {
			t.Fatal("retry did not observe started menu state")
		}
	case <-time.After(time.Second):
		t.Fatal("retry did not acquire menu lock")
	}

	oldRetention := entryMonitorMenuRetention
	entryMonitorMenuRetention = 5 * time.Millisecond
	defer func() { entryMonitorMenuRetention = oldRetention }()
	key, state = svc.lockEntryMonitorMenu(124, 8)
	svc.unlockEntryMonitorMenu(key, state, true)
	deadline := time.Now().Add(time.Second)
	for {
		svc.entryMonitorMenusMu.Lock()
		_, exists := svc.entryMonitorMenus["124:8"]
		svc.entryMonitorMenusMu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired menu state was retained")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEntryMonitorRunCallbackRetryStartsOnlyOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	for range 2 {
		mock.ExpectQuery(telegramOperatorQueryPattern).WithArgs(int64(123)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	}
	starts := 0
	controller := &fakeEntryMonitorController{
		options: []admin.ClientEntryMonitorRunOption{{PolicyID: 1, Name: "华东", TargetCount: 1}},
		startWithMessage: func(context.Context, []int64, int64, int64, int64, string) (int64, error) {
			starts++
			return 1, nil
		},
	}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error { return nil }
	edits := 0
	svc.editMessageText = func(context.Context, int64, int64, string, any) error {
		edits++
		return nil
	}
	payload := monitorCallbackPayload("retry", "cem:r:1", 123)
	for range 2 {
		if err := svc.HandleWebhook(context.Background(), payload); err != nil {
			t.Fatalf("HandleWebhook: %v", err)
		}
	}
	if starts != 1 || edits != 1 {
		t.Fatalf("starts=%d edits=%d", starts, edits)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestTelegramEditMessageTreatsNotModifiedAsSuccess(t *testing.T) {
	svc := NewService(config.Config{TelegramBotToken: "bot-token"}, nil)
	svc.client = &http.Client{Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/botbot-token/editMessageText" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"Bad Request: message is not modified"}`))}, nil
	})}
	if err := svc.editMessageTextNow(context.Background(), 123, 77, "unchanged", entryMonitorEmptyInlineKeyboard()); err != nil {
		t.Fatalf("editMessageTextNow: %v", err)
	}
}

func monitorCommandPayload(command string, chatID int64) map[string]any {
	return map[string]any{"message": map[string]any{
		"message_id": int64(1),
		"text":       command,
		"chat":       map[string]any{"id": chatID, "type": "private"},
	}}
}

func monitorTextPayload(text string, chatID, messageID int64) map[string]any {
	return map[string]any{"message": map[string]any{
		"message_id": messageID,
		"text":       text,
		"chat":       map[string]any{"id": chatID, "type": "private"},
	}}
}

func monitorCallbackPayload(callbackID, data string, telegramID int64) map[string]any {
	return map[string]any{"callback_query": map[string]any{
		"id":   callbackID,
		"data": data,
		"from": map[string]any{"id": telegramID},
		"message": map[string]any{
			"message_id": int64(1),
			"text":       "入口检测菜单",
			"chat":       map[string]any{"id": telegramID, "type": "private"},
		},
	}}
}

func entryMonitorRulesKeyboardForTest() inlineKeyboardMarkup {
	return inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{
		{{Text: "华东 · 2 个目标", CallbackData: "cem:r:1"}},
		{{Text: "检测全部（1 组）", CallbackData: "cem:r:all"}},
	}}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(encoded)
}

func runeLengths(values []string) []int {
	result := make([]int, len(values))
	for index, value := range values {
		result[index] = len([]rune(value))
	}
	return result
}

func telegramOKResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}
}
