package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

type fakeAIController func(context.Context, aiops.TelegramInput) (bool, error)

func (f fakeAIController) HandleUpdate(ctx context.Context, input aiops.TelegramInput) (bool, error) {
	return f(ctx, input)
}

func aiWebhookMessage(text string) map[string]any {
	return map[string]any{
		"update_id": int64(600),
		"message": map[string]any{
			"message_id": int64(200), "text": text,
			"chat": map[string]any{"id": int64(123), "type": "private"},
			"from": map[string]any{"id": int64(123), "is_bot": false},
		},
	}
}

func aiWebhookCallback(data string) map[string]any {
	p := aiWebhookMessage("")
	m := p["message"]
	delete(p, "message")
	p["callback_query"] = map[string]any{
		"id": "callback-fixture", "data": data, "message": m,
		"from": map[string]any{"id": int64(123), "is_bot": false},
	}
	return p
}

func aiTelegramService() *Service {
	return NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "98765:fixture-token", AppURL: "https://example.test"}, nil)
}

func TestAIWebhookReceivesOriginalTextAndRepliesToAIMessages(t *testing.T) {
	s := aiTelegramService()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.db = db
	text := "SSH password line with trailing space \n"
	called := false
	s.WithAIOps(fakeAIController(func(_ context.Context, in aiops.TelegramInput) (bool, error) {
		called = true
		if in.BotID != "98765" || in.UpdateID != 600 || in.FromID != 123 || in.ChatID != 123 || in.MessageID != 200 || in.Text != text || in.Callback != "" || !in.IsPrivate {
			t.Fatalf("incorrect input: %+v", in)
		}
		return true, nil
	}))
	s.adminService = &fakeTelegramAdminService{}
	p := aiWebhookMessage(text)
	p["message"].(map[string]any)["reply_to_message"] = map[string]any{"text": "#123"}
	if err := s.HandleWebhook(WithVerifiedAIWebhook(context.Background()), p); err != nil || !called {
		t.Fatal("AI must handle before legacy ticket reply", called, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAIWebhookRequiresVerifiedHeaderAndPrivateSender(t *testing.T) {
	cases := map[string]func(map[string]any){
		"group":              func(p map[string]any) { p["message"].(map[string]any)["chat"].(map[string]any)["type"] = "group" },
		"other sender":       func(p map[string]any) { p["message"].(map[string]any)["from"].(map[string]any)["id"] = 124 },
		"bot":                func(p map[string]any) { p["message"].(map[string]any)["from"].(map[string]any)["is_bot"] = true },
		"missing from":       func(p map[string]any) { delete(p["message"].(map[string]any), "from") },
		"missing bot marker": func(p map[string]any) { delete(p["message"].(map[string]any)["from"].(map[string]any), "is_bot") },
		"missing update":     func(p map[string]any) { delete(p, "update_id") },
		"fraction update":    func(p map[string]any) { p["update_id"] = 1.5 },
		"negative update":    func(p map[string]any) { p["update_id"] = -1 },
		"zero message":       func(p map[string]any) { p["message"].(map[string]any)["message_id"] = 0 },
		"fraction sender":    func(p map[string]any) { p["message"].(map[string]any)["from"].(map[string]any)["id"] = 123.5 },
		"oversize sender": func(p map[string]any) {
			p["message"].(map[string]any)["from"].(map[string]any)["id"] = float64(9007199254740992)
		},
		"edited":  func(p map[string]any) { p["edited_message"] = p["message"]; delete(p, "message") },
		"channel": func(p map[string]any) { p["channel_post"] = p["message"]; delete(p, "message") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := aiTelegramService().WithAIOps(fakeAIController(func(context.Context, aiops.TelegramInput) (bool, error) {
				t.Fatal("untrusted update reached AI")
				return false, nil
			}))
			p := aiWebhookMessage("/ai")
			mutate(p)
			if err := s.HandleWebhook(WithVerifiedAIWebhook(context.Background()), p); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, callback := range []bool{false, true} {
		s := aiTelegramService().WithAIOps(fakeAIController(func(context.Context, aiops.TelegramInput) (bool, error) {
			t.Fatal("legacy webhook reached AI")
			return false, nil
		}))
		var sends int
		s.client = &http.Client{Transport: telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			sends++
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p["text"] != "请管理员在后台更新 Telegram Webhook 后使用 AI 运维" {
				t.Fatal("legacy webhook revealed AI details", p)
			}
			return aiHTTPResponse(200, `{"ok":true,"result":{"message_id":44}}`), nil
		})}
		s.answerCallback = func(context.Context, string, string, bool) error { return nil }
		p := aiWebhookMessage("/ai_bind secret-code")
		if callback {
			p = aiWebhookCallback("ai:stop:44")
		}
		if err := s.HandleWebhook(context.Background(), p); err != nil || sends != 1 {
			t.Fatal("legacy webhook must give fixed upgrade hint", sends, err)
		}
	}
}

func TestAIWebhookPreservesLegacyDispatchAndErrors(t *testing.T) {
	s := aiTelegramService()
	var aiCalls, legacyCalls, acknowledgments int
	s.WithAIOps(fakeAIController(func(context.Context, aiops.TelegramInput) (bool, error) { aiCalls++; return false, nil }))
	s.sendMessage = func(context.Context, int64, string) error { legacyCalls++; return nil }
	s.answerCallback = func(context.Context, string, string, bool) error { acknowledgments++; return nil }
	ctx := WithVerifiedAIWebhook(context.Background())
	if err := s.HandleWebhook(ctx, aiWebhookMessage("/getlatesturl")); err != nil || aiCalls != 0 || legacyCalls != 1 {
		t.Fatal("unhandled command lost legacy route", aiCalls, legacyCalls, err)
	}
	if err := s.HandleWebhook(ctx, aiWebhookCallback("legacy:unknown")); err != nil || aiCalls != 0 || acknowledgments != 1 {
		t.Fatal("legacy callback intercepted", aiCalls, acknowledgments, err)
	}
	want := errors.New("durable inbox unavailable")
	s.WithAIOps(fakeAIController(func(_ context.Context, in aiops.TelegramInput) (bool, error) {
		if in.Callback != "ai:stop:1" || in.Text != "" {
			t.Fatal(in)
		}
		return true, want
	}))
	if err := s.HandleWebhook(ctx, aiWebhookCallback("ai:stop:1")); !errors.Is(err, want) || acknowledgments != 2 {
		t.Fatal("AI callback must acknowledge once and preserve error", acknowledgments, err)
	}
}

func TestAIWebhookPreservesRecognizableTicketRepliesAndMonitorButtons(t *testing.T) {
	s := aiTelegramService().WithAIOps(fakeAIController(func(context.Context, aiops.TelegramInput) (bool, error) {
		t.Fatal("explicit legacy action reached AI")
		return true, nil
	}))
	ctx := WithVerifiedAIWebhook(context.Background())
	for _, text := range []string{clientEntryMonitorRunButtonText, clientEntryMonitorRecentButtonText, clientEntryManualAutoSplitText, "/bind subscription", "/traffic"} {
		if handled, err := s.handleAIWebhook(ctx, aiWebhookMessage(text)); handled || err != nil {
			t.Fatal("legacy menu action intercepted", text, handled, err)
		}
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.db = db
	adminService := &fakeTelegramAdminService{}
	s.WithAdminService(adminService)
	s.sendMessage = func(context.Context, int64, string) error { return nil }
	mock.ExpectQuery(`SELECT id, email, is_admin, is_staff FROM v2_user WHERE telegram_id = \$1 AND banned = 0 AND is_admin = 1 LIMIT 1`).WithArgs(int64(123)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(7), "admin@example.test", 1, 0))
	payload := aiWebhookMessage("已经帮你处理好了")
	payload["message"].(map[string]any)["reply_to_message"] = map[string]any{"text": "工单提醒 #21\n用户消息"}
	if err := s.HandleWebhook(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if adminService.lastReply.ID != 21 || adminService.lastReply.Message != "已经帮你处理好了" {
		t.Fatal("ticket reply lost", adminService.lastReply)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func aiHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestAITransportPlainTextMessageIdentityAndEdit(t *testing.T) {
	s := aiTelegramService()
	transport := s.AITransport()
	if identity, ok := transport.Identity(); !ok || identity != "98765" {
		t.Fatal(identity, ok)
	}
	var calls int
	s.client = &http.Client{Transport: telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		if p["chat_id"] != float64(123) || p["text"] != "**literal** <b>literal</b>" || p["parse_mode"] != nil || p["entities"] != nil || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("message not literal plain text", p)
		}
		if calls == 1 && (r.URL.Path != "/bot98765:fixture-token/sendMessage" || p["message_id"] != nil) {
			t.Fatal(r.URL.Path, p)
		}
		if calls == 2 && (r.URL.Path != "/bot98765:fixture-token/editMessageText" || p["message_id"] != float64(66)) {
			t.Fatal(r.URL.Path, p)
		}
		if calls <= 2 && p["disable_notification"] != nil {
			t.Fatal("ordinary Send/Edit changed notification behavior", p)
		}
		if calls == 3 && (r.URL.Path != "/bot98765:fixture-token/sendMessage" || p["disable_notification"] != true || p["message_id"] != nil) {
			t.Fatal("silent send missing notification flag", p)
		}
		return aiHTTPResponse(200, `{"ok":true,"result":{"message_id":66}}`), nil
	})}
	buttons := [][]aiops.TelegramButton{{{Text: "停止", Data: "ai:stop:1"}}}
	id, err := transport.Send(context.Background(), 123, "**literal** <b>literal</b>", buttons)
	if err != nil || id != 66 {
		t.Fatal(id, err)
	}
	if err := transport.Edit(context.Background(), 123, id, "**literal** <b>literal</b>", nil); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
	silent, ok := transport.(interface {
		SendSilent(context.Context, int64, string, [][]aiops.TelegramButton) (int64, error)
	})
	if !ok {
		t.Fatal("missing optional silent transport")
	}
	if id, err := silent.SendSilent(context.Background(), 123, "**literal** <b>literal</b>", nil); err != nil || id != 66 || calls != 3 {
		t.Fatal("silent send failed", id, calls, err)
	}
	s.cfg.TelegramBotEnable = false
	if _, ok := transport.Identity(); ok {
		t.Fatal("disabled bot has identity")
	}
	if _, err := transport.Send(context.Background(), 123, "disabled", nil); err == nil || calls != 3 {
		t.Fatal("disabled bot sent", calls, err)
	}
}

func TestAITransportSafeErrorsAndRateLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		retry   time.Duration
		success bool
	}{
		{"429", 429, `{"ok":false,"error_code":429,"description":"secret-token","parameters":{"retry_after":17}}`, 17 * time.Second, false},
		{"429 bad JSON", 429, `invalid secret-token`, time.Second, false},
		{"not modified", 400, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`, 0, true},
		{"5xx", 503, `secret-token`, 0, false},
		{"invalid message", 200, `{"ok":true,"result":true}`, 0, false},
		{"network", 0, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := aiTelegramService()
			s.client = &http.Client{Transport: telegramRoundTripFunc(func(*http.Request) (*http.Response, error) {
				if tc.status == 0 {
					return nil, errors.New("secret-token in endpoint")
				}
				return aiHTTPResponse(tc.status, tc.body), nil
			})}
			err := s.AITransport().Edit(context.Background(), 123, 44, "draft", nil)
			if tc.success {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var safe *AITransportError
			if !errors.As(err, &safe) || strings.Contains(err.Error(), "secret-token") || safe.RetryAfter() != tc.retry {
				t.Fatal("unsafe error or lost retry_after", err)
			}
		})
	}
}

func TestAITransportRejectsInvalidDestinationAndPayload(t *testing.T) {
	s := aiTelegramService()
	s.client = &http.Client{Transport: telegramRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid request sent"); return nil, nil })}
	for _, id := range []int64{0, -123} {
		if _, err := s.AITransport().Send(context.Background(), id, "text", nil); err == nil {
			t.Fatal("invalid destination accepted", id)
		}
	}
	for _, text := range []string{"", " ", strings.Repeat("😀", 2049), string([]byte{0xff})} {
		if _, err := s.AITransport().Send(context.Background(), 123, text, nil); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	for _, token := range []string{"", "bot-token", "abc:secret", "0:secret", "123:secret/path", "123:secret?x=y"} {
		s.cfg.TelegramBotToken = token
		if _, ok := s.AITransport().Identity(); ok {
			t.Fatal("invalid token identity accepted")
		}
	}
}

func TestAITransportDoesNotFollowRedirectOrLeakCancellationURL(t *testing.T) {
	s := aiTelegramService()
	var calls int
	s.client = &http.Client{Transport: telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := aiHTTPResponse(http.StatusTemporaryRedirect, `{"ok":false}`)
		resp.Header.Set("Location", "https://other.example.test/collect")
		return resp, nil
	})}
	if _, err := s.AITransport().Send(context.Background(), 123, "private operational text", nil); err == nil || calls != 1 {
		t.Fatal("AI message followed redirect", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.client = &http.Client{Transport: telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}
	if _, err := s.AITransport().Send(ctx, 123, "text", nil); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "fixture-token") {
		t.Fatal("cancellation leaked endpoint or lost cancellation identity", err)
	}
}

func TestAIWebhookCallbackAcknowledgementFailureDoesNotReplayDispatch(t *testing.T) {
	s := aiTelegramService()
	var calls, acknowledgments int
	s.WithAIOps(fakeAIController(func(context.Context, aiops.TelegramInput) (bool, error) {
		calls++
		return true, nil
	}))
	s.answerCallback = func(context.Context, string, string, bool) error {
		acknowledgments++
		return errors.New("lost acknowledgement")
	}
	if err := s.HandleWebhook(WithVerifiedAIWebhook(context.Background()), aiWebhookCallback("ai:stop:42")); err != nil || calls != 1 || acknowledgments != 1 {
		t.Fatal("ack error lost or replayed business task", calls, acknowledgments, err)
	}
}
