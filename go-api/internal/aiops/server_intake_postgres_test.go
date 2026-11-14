package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestSSHIntakePostgresFreeFormMessageGoesToAIAndStagesEncrypted(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	password := "  fixture-intake-password  "
	phrase := "fixture-intake-key-phrase"
	key := intakeTestKey(t, phrase)
	original := "请整理这些机器，格式我不改：192.0.2.10 root " + password + "\n第二台 192.0.2.11 admin 私钥：\n" + key + "\n口令：" + phrase
	stageArgs, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"host": "192.0.2.10", "username": "root", "password": password},
		map[string]any{"host": "192.0.2.11", "username": "admin", "private_key": key, "passphrase": phrase},
	}})
	var calls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad completion request")
			w.WriteHeader(400)
			return
		}
		n := calls.Add(1)
		answer := providerMessage{Role: "assistant", Content: "已处理当前请求"}
		if n == 3 {
			for _, message := range req.Messages {
				if strings.Contains(message.Content, strings.TrimSpace(password)) || strings.Contains(message.Content, phrase) || strings.Contains(message.Content, key) {
					t.Error("older SSH paste was sent to a later model turn")
				}
			}
		}
		if n == 1 || n == 5 {
			found := false
			for _, message := range req.Messages {
				if message.Role == "user" && message.Content == original {
					found = true
				}
			}
			if !found {
				t.Error("original arbitrary-format message did not reach AI")
			}
			answer.Content = ""
			answer.ToolCalls = []ToolCall{{ID: "stage", Type: "function", Function: ToolFunction{Name: "ssh_server_stage", Arguments: string(stageArgs)}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": answer}}})
	}))
	defer relay.Close()
	s := NewService(config.Config{AppKey: "intake-postgres-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err := s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1)RETURNING id`, "intake-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		db.Exec(`DELETE FROM v2_ai_session WHERE created_by=$1`, actor)
		db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
		db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	}()
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "relay-test-key", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	wait := func(turn int64) {
		t.Helper()
		for {
			var status, problem string
			if err := db.QueryRowContext(ctx, `SELECT status,error FROM v2_ai_turn WHERE id=$1`, turn).Scan(&status, &problem); err != nil {
				t.Fatal(err)
			}
			if status != "running" {
				if status != "completed" {
					t.Fatalf("turn status=%s error=%s", status, problem)
				}
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	req := SendMessageRequest{Model: DefaultModels[0], Content: original, RequestID: "intake-" + marker}
	result, err := s.SendMessage(ctx, actor, req)
	if err != nil {
		t.Fatal(err)
	}
	wait(result.Turn.ID)
	if calls.Load() != 2 {
		t.Fatal("stage flow did not finish", calls.Load())
	}
	detail, err := s.GetSession(ctx, actor, result.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(detail)
	for _, secret := range []string{strings.TrimSpace(password), phrase, key, strings.Split(key, "\n")[1]} {
		if strings.Contains(string(public), secret) {
			t.Fatal("SSH secret leaked into public session")
		}
	}
	var storedContent, messageCipher, hash string
	if err = db.QueryRowContext(ctx, `SELECT content,content_cipher FROM v2_ai_message WHERE turn_id=$1 AND role='user'`, result.Turn.ID).Scan(&storedContent, &messageCipher); err != nil || storedContent != sshDirectHistoryNotice || messageCipher == "" {
		t.Fatal("free-form message not stored as encrypted, redacted content", err)
	}
	if plain, e := s.decryptSecret(messageCipher); e != nil || plain != original {
		t.Fatal("encrypted message changed original bytes", e)
	}
	if err = db.QueryRowContext(ctx, `SELECT request_hash FROM v2_ai_turn WHERE id=$1`, result.Turn.ID).Scan(&hash); err != nil || hash != requestHash(req) {
		t.Fatal("idempotency hash did not use original request", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT id,password_cipher,private_key_cipher,key_passphrase_cipher FROM v2_ai_server_intake WHERE turn_id=$1 ORDER BY item_index`, result.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var pw, private, passphrase string
		if err = rows.Scan(&id, &pw, &private, &passphrase); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
		if len(ids) == 1 {
			if got, e := s.decryptSecret(pw); e != nil || got != password || private != "" {
				rows.Close()
				t.Fatal("password stage did not preserve exact bytes", e)
			}
		} else {
			gotKey, e1 := s.decryptSecret(private)
			gotPhrase, e2 := s.decryptSecret(passphrase)
			if e1 != nil || e2 != nil || gotKey != key || gotPhrase != phrase || pw != "" {
				rows.Close()
				t.Fatal("private-key stage mismatch", e1, e2)
			}
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatal("stage did not preserve both servers", len(ids))
	}
	var leaked bool
	if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_message WHERE turn_id=$1 AND (strpos(content,$2)>0 OR strpos(tool_calls::TEXT,$2)>0))`, result.Turn.ID, strings.TrimSpace(password)).Scan(&leaked); err != nil || leaked {
		t.Fatal("stage tool arguments leaked in message table", err)
	}
	replay, err := s.SendMessage(ctx, actor, req)
	if err != nil || replay.Turn.ID != result.Turn.ID || calls.Load() != 2 {
		t.Fatal("idempotent replay repeated model or stage tool", err)
	}
	changed := req
	changed.Content = strings.ReplaceAll(req.Content, password, "changed-fixture-password")
	if _, err = s.SendMessage(ctx, actor, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed credential with reused request id must conflict", err)
	}
	normal, err := s.SendMessage(ctx, actor, SendMessageRequest{SessionID: result.Session.ID, Model: DefaultModels[0], Content: "继续检查正常任务", RequestID: "normal-" + marker})
	if err != nil {
		t.Fatal(err)
	}
	wait(normal.Turn.ID)
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, normal.Turn.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("ordinary message created an intake", count, err)
	}
	incomplete := SendMessageRequest{Model: DefaultModels[0], Content: "| 密码 | fixture-intake-password |", RequestID: "incomplete-" + marker}
	accepted, err := s.SendMessage(ctx, actor, incomplete)
	if err != nil {
		t.Fatal("arbitrary format was blocked before AI", err)
	}
	wait(accepted.Turn.ID)
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, accepted.Turn.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("incomplete message unexpectedly staged a server", count, err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM v2_ai_turn WHERE id=$1`, result.Turn.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE id=$1`, ids[0]).Scan(&count); err != nil || count != 0 {
		t.Fatal("turn deletion did not cascade staged credentials", count, err)
	}
	req.RequestID = "cascade-" + marker
	second, err := s.SendMessage(ctx, actor, req)
	if err != nil {
		t.Fatal(err)
	}
	wait(second.Turn.ID)
	if err = s.DeleteSession(ctx, actor, second.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE session_id=$1`, second.Session.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("session deletion did not cascade staged credentials", count, err)
	}
}
