package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

type fakeEntryMonitorController struct {
	start  func(context.Context, int64, int64, string) (int64, error)
	recent func(context.Context) (string, error)
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
	controller := &fakeEntryMonitorController{start: func(_ context.Context, userID, chatID int64, requestKey string) (int64, error) {
		if !acknowledged {
			t.Fatal("callback must be acknowledged before monitor work starts")
		}
		if userID != 9 || chatID != 123 || requestKey != "callback-1" {
			t.Fatalf("start args = user %d chat %d request key %q", userID, chatID, requestKey)
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
	var sent string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
		}
		sent = text
		return nil
	}
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-1", clientEntryMonitorRunCallback, 123)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !acknowledged || !strings.Contains(sent, "检测任务已启动") || strings.Contains(sent, "42") || strings.Contains(sent, "#") {
		t.Fatalf("acknowledged = %v, message = %q", acknowledged, sent)
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

func TestMonitorRunReplyKeyboardTextUsesStableMessageIdempotencyKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	var requestKeys []string
	controller := &fakeEntryMonitorController{start: func(_ context.Context, userID, chatID int64, requestKey string) (int64, error) {
		if userID != 9 || chatID != 123 {
			t.Fatalf("start args = user %d chat %d", userID, chatID)
		}
		requestKeys = append(requestKeys, requestKey)
		return 42, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error {
		t.Fatal("reply keyboard text must not use callback acknowledgement")
		return nil
	}
	var messages []string
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		if chatID != 123 {
			t.Fatalf("chatID = %d", chatID)
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
	// Telegram can retry the same webhook. Both deliveries must carry the same
	// request key so the controller can return the original run idempotently.
	for range 2 {
		if err := svc.HandleWebhook(context.Background(), payload); err != nil {
			t.Fatalf("HandleWebhook: %v", err)
		}
	}
	if len(requestKeys) != 2 || requestKeys[0] != "telegram-message:123:77" || requestKeys[1] != requestKeys[0] {
		t.Fatalf("request keys = %#v", requestKeys)
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "检测任务已启动") || strings.Contains(messages[0], "#") || strings.Contains(messages[0], "42") {
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
	svc.sendMessage = func(_ context.Context, _ int64, text string) error {
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
	controller := &fakeEntryMonitorController{start: func(_ context.Context, userID, chatID int64, requestKey string) (int64, error) {
		started = true
		if userID != 9 || chatID != 123 || requestKey != "callback-ack-failed" {
			t.Fatalf("start args = user %d chat %d request key %q", userID, chatID, requestKey)
		}
		return 42, nil
	}}
	svc := NewService(config.Config{}, db).WithEntryMonitorController(controller)
	svc.answerCallback = func(context.Context, string, string, bool) error {
		return errors.New("telegram response lost")
	}
	svc.sendMessage = func(context.Context, int64, string) error { return nil }
	mock.ExpectQuery(telegramOperatorQueryPattern).
		WithArgs(int64(123)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))

	if err := svc.HandleWebhook(context.Background(), monitorCallbackPayload("callback-ack-failed", clientEntryMonitorRunCallback, 123)); err != nil {
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
			"chat": map[string]any{"id": int64(-1001), "type": "group"},
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
			"chat": map[string]any{"id": telegramID, "type": "private"},
		},
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
