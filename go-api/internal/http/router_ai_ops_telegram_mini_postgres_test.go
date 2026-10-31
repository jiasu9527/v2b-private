package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func miniHTTPSignedFixture(tgID int64) string {
	v := url.Values{"auth_date": {strconv.FormatInt(time.Now().Unix(), 10)}, "query_id": {"local-fixture"}, "user": {fmt.Sprintf(`{"id":%d,"first_name":"Fixture"}`, tgID)}}
	fields := []string{}
	for key, values := range v {
		fields = append(fields, key+"="+values[0])
	}
	sort.Strings(fields)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte("42:fixture-token"))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(fields, "\n")))
	v.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return v.Encode()
}

func TestTelegramMiniHTTPPostgresEndToEnd(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_HTTP_TEST_DSN")
	if dsn == "" {
		t.Skip("requires empty disposable FOREST_AIOPS_HTTP_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_user`).Scan(&count); err != nil || count != 0 {
		t.Fatal("requires empty disposable database", count, err)
	}
	var actor int64
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES('mini-http@example.test','fixture','fixture','mini-http-fixture',1,1,1)RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	title := "Mini HTTP " + fmt.Sprint(time.Now().UnixNano())
	defer db.Exec(`DELETE FROM v2_notice WHERE title=$1`, title)
	var calls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := map[string]any{"role": "assistant", "content": "mini fixture completed"}
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"operation": "notices.save", "parameters": map[string]any{"title": title, "content": "mini isolated body", "tags": []string{"fixture"}}, "reason": "mini隔离测试"})
			message = map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: "mini-write-once", Type: "function", Function: aiops.ToolFunction{Name: "propose_panel_operation", Arguments: string(args)}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer relay.Close()
	cfg := config.Config{AdminPath: "test", AppKey: "mini-http-app-key"}
	panel := admin.NewDBService(cfg, db)
	ai := aiops.NewService(cfg, db, panel)
	defer ai.Close()
	defer func() {
		_ = ai.ResetData(ctx, actor)
		_, _ = db.Exec(`DELETE FROM v2_ai_telegram_mini_request WHERE actor_id=$1`, actor)
		_, _ = db.Exec(`DELETE FROM v2_ai_telegram_inbox WHERE actor_id=$1`, actor)
		_, _ = db.Exec(`DELETE FROM v2_ai_telegram_binding WHERE actor_id=$1`, actor)
	}()
	bridge := aiops.NewTelegramBridge(ai)
	router := NewRouter(cfg, WithAIOps(ai), WithAIOpsTelegram(bridge), WithAdminService(panel), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: actor, IsAdmin: 1}}))
	transport := &telegramHTTPFixtureTransport{}
	if err = bridge.Start(ctx, transport); err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	if _, err = ai.SaveConfig(ctx, actor, aiops.ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "mini-fixture", Models: aiops.DefaultModels}); err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.SaveConfig(ctx, actor, aiops.TelegramConfig{Enabled: true, DefaultModel: aiops.DefaultModels[0], DefaultAccessMode: "full"}); err != nil {
		t.Fatal(err)
	}
	code, err := bridge.CreateBindingCode(ctx, actor, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.HandleUpdate(ctx, aiops.TelegramInput{BotID: "42", UpdateID: 400, ChatID: 555, FromID: 555, IsPrivate: true, Text: code.Command}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token string, input any, want int, result any) {
		t.Helper()
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/api/v1/ai-ops-mini/"+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
		if result != nil {
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || json.Unmarshal(envelope.Data, result) != nil {
				t.Fatal("bad response", w.Body.String())
			}
		}
	}
	var auth aiops.MiniAuthResult
	request("POST", "auth", "", map[string]string{"init_data": miniHTTPSignedFixture(555)}, 200, &auth)
	if auth.User.ID != actor || auth.Token == "" {
		t.Fatal("wrong identity", auth.User)
	}
	input := aiops.SendMessageRequest{Content: "新建隔离通知", Model: aiops.DefaultModels[0], RequestID: "mini-http-send-fixture", AccessMode: "full"}
	var sent, duplicate aiops.SendMessageResult
	request("POST", "chat/send", auth.Token, input, 202, &sent)
	request("POST", "chat/send", auth.Token, input, 202, &duplicate)
	if sent.Turn.ID != duplicate.Turn.ID || sent.Session.Source != "telegram" {
		t.Fatal("not idempotent or missing source", sent, duplicate)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		detail, e := ai.GetSession(ctx, actor, sent.Session.ID)
		if e != nil {
			t.Fatal(e)
		}
		if detail.Session.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mini task timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatal("duplicate model execution", calls.Load())
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_notice WHERE title=$1`, title).Scan(&count); err != nil || count != 1 {
		t.Fatal("write count", count, err)
	}
	var detail aiops.SessionDetail
	request("GET", fmt.Sprintf("session?id=%d", sent.Session.ID), auth.Token, nil, 200, &detail)
	if len(detail.Messages) == 0 {
		t.Fatal("mini cannot see result")
	}
	// Scoped mutations including deletion execute under the binding guard but
	// must not deadlock when deleteData takes its normal Telegram/AI locks.
	request("POST", "session/delete", auth.Token, map[string]any{"id": sent.Session.ID, "confirm": true}, 200, nil)
	request("POST", "chat/send", auth.Token, input, 403, nil)
	if err = bridge.RevokeBinding(ctx, actor, actor); err != nil {
		t.Fatal(err)
	}
	request("GET", "identity", auth.Token, nil, 401, nil)
	request("POST", "chat/send", auth.Token, input, 401, nil)
	if calls.Load() != 2 {
		t.Fatal("revocation or deletion re-executed model")
	}
}
