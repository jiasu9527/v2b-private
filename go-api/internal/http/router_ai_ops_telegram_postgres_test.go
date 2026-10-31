package httpapi

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"forest/go-api/internal/telegram"
)

type telegramHTTPFixtureTransport struct {
	mu    sync.Mutex
	texts []string
}

func (*telegramHTTPFixtureTransport) Identity() (string, bool) { return "42", true }
func (f *telegramHTTPFixtureTransport) Send(_ context.Context, _ int64, text string, _ [][]aiops.TelegramButton) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return int64(len(f.texts)), nil
}
func (f *telegramHTTPFixtureTransport) Edit(ctx context.Context, chat, _ int64, text string, buttons [][]aiops.TelegramButton) error {
	_, err := f.Send(ctx, chat, text, buttons)
	return err
}
func (f *telegramHTTPFixtureTransport) contains(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sent := range f.texts {
		if strings.Contains(sent, text) {
			return true
		}
	}
	return false
}

// Real HTTP authentication -> persistent bridge -> model tool -> panel write ->
// Telegram outbox. Only the model and Telegram network are local test doubles.
func TestAITelegramHTTPPostgresEndToEnd(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_HTTP_TEST_DSN")
	if dsn == "" {
		t.Skip("requires empty disposable FOREST_AIOPS_HTTP_TEST_DSN")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("APP_KEY", "")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_user`).Scan(&count); err != nil || count != 0 {
		t.Fatal("requires empty test database", count, err)
	}
	var actor int64
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES('tg-http@example.test','fixture','fixture','tg-http-fixture',1,1,1) RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	title := "TG integration " + fmt.Sprint(time.Now().UnixNano())
	defer db.Exec(`DELETE FROM v2_notice WHERE title=$1`, title)
	var calls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := calls.Add(1)
		message := map[string]any{"role": "assistant", "content": "TG fixture completed"}
		if step == 1 {
			args, _ := json.Marshal(map[string]any{"operation": "notices.save", "parameters": map[string]any{"title": title, "content": "telegram integration body", "tags": []string{"fixture"}}, "reason": "隔离测试通知"})
			message = map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: "tg-notice-once", Type: "function", Function: aiops.ToolFunction{Name: "propose_panel_operation", Arguments: string(args)}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer relay.Close()
	cfg := config.Config{AdminPath: "tg-test", AppKey: "telegram-http-app-key", TelegramBotEnable: true, TelegramBotToken: "42:fixture-token"}
	panel := admin.NewDBService(cfg, db)
	ai := aiops.NewService(cfg, db, panel)
	defer ai.Close()
	defer func() {
		_ = ai.ResetData(ctx, actor)
		_, _ = db.Exec(`DELETE FROM v2_ai_telegram_inbox WHERE actor_id=$1`, actor)
		_, _ = db.Exec(`DELETE FROM v2_ai_telegram_binding WHERE actor_id=$1`, actor)
	}()
	bridge := aiops.NewTelegramBridge(ai)
	transport := &telegramHTTPFixtureTransport{}
	if err = bridge.Start(ctx, transport); err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	bot := telegram.NewService(cfg, db).WithAIOps(bridge)
	router := NewRouter(cfg, WithAdminService(panel), WithAIOps(ai), WithAIOpsTelegram(bridge), WithTelegramService(bot), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: actor, IsAdmin: 1}}))
	if _, err = ai.SaveConfig(ctx, actor, aiops.ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "local-fixture", Models: aiops.DefaultModels}); err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.SaveConfig(ctx, actor, aiops.TelegramConfig{Enabled: true, DefaultModel: aiops.DefaultModels[0], DefaultAccessMode: "full", StreamUpdates: true}); err != nil {
		t.Fatal(err)
	}
	// Obtain the binding command through the same administrator endpoint as UI.
	req := httptest.NewRequest("POST", "/api/v1/tg-test/ai-ops/telegram/binding-code", strings.NewReader(fmt.Sprintf(`{"actor_id":%d}`, actor)))
	req.Header.Set("Authorization", "Bearer fixture")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var code struct {
		Data aiops.TelegramBindingCode `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &code) != nil || code.Data.Command == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	postUpdate := func(update int64, text string, verified bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"update_id": update, "message": map[string]any{"message_id": update, "from": map[string]any{"id": 555, "is_bot": false}, "chat": map[string]any{"id": 555, "type": "private"}, "text": text}})
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/guest/telegram/webhook?access_token=%x", md5.Sum([]byte(cfg.TelegramBotToken))), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if verified {
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", config.TelegramAIWebhookSecret(cfg.AppKey, cfg.TelegramBotToken))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	postUpdate(100, code.Data.Command, true)
	// A valid legacy URL token alone must never authorize AI natural language.
	postUpdate(101, "unverified fixture task", false)
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_inbox WHERE bot_id='42' AND update_id=101`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unverified input reached AI", count, err)
	}
	postUpdate(102, "请新建隔离测试通知", true)
	postUpdate(102, "请新建隔离测试通知", true)
	for deadline := time.Now().Add(15 * time.Second); !transport.contains("TG fixture completed"); {
		if time.Now().After(deadline) {
			t.Fatal("Telegram result not delivered")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_notice WHERE title=$1`, title).Scan(&count); err != nil || count != 1 {
		t.Fatal("panel write did not execute exactly once", count, err)
	}
	if calls.Load() != 2 {
		t.Fatal("duplicate model execution", calls.Load())
	}
	sessions, err := ai.ListSessions(ctx, actor)
	if err != nil || len(sessions) != 1 || sessions[0].Source != "telegram" {
		t.Fatal("browser session interop", sessions, err)
	}
	actions, err := ai.ListActions(ctx, actor, sessions[0].ID)
	if err != nil || len(actions) != 1 || actions[0].Status != "succeeded" {
		t.Fatal("full-access tool did not complete", actions, err)
	}
}
