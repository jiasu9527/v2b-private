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
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestSSHIntakePostgresSendMessageEncryptsBeforePersistenceAndRelay(t *testing.T) {
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
	requests := make(chan completionRequest, 8)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests <- req
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"已读取连接目标，凭据仅保留在后端"}}]}`))
	}))
	defer relay.Close()
	s := NewService(config.Config{AppKey: "intake-postgres-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
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
	password := "  fixture-intake-password  "
	phrase := "fixture-intake-key-phrase"
	key := intakeTestKey(t, phrase)
	encodedPassword, _ := json.Marshal(password)
	content := "请检查服务，再检查重复资料 " + password + "\nhost: 192.0.2.10\nusername: root\npassword: " + string(encodedPassword) + "\npassphrase: " + phrase + "\n" + key
	req := SendMessageRequest{Model: DefaultModels[0], Content: content, RequestID: "intake-" + marker}
	result, err := s.SendMessage(ctx, actor, req)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets := func(text string) {
		t.Helper()
		for _, secret := range []string{strings.TrimSpace(password), phrase, key, strings.Split(key, "\n")[1]} {
			if strings.Contains(text, secret) {
				t.Fatal("SSH credential exposed in a public field or relay message")
			}
		}
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
	wait(result.Turn.ID)
	select {
	case observed := <-requests:
		found := false
		for _, message := range observed.Messages {
			assertNoSecrets(message.Content)
			if message.Role == "user" {
				found = strings.Contains(message.Content, `"intake_id":`) && strings.Contains(message.Content, `"host":"192.0.2.10"`) && strings.Contains(message.Content, "请检查服务")
			}
		}
		if !found {
			t.Fatal("task text or safe intake metadata missing from relay")
		}
	default:
		t.Fatal("relay request missing")
	}
	detail, err := s.GetSession(ctx, actor, result.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(detail.Session.Title)
	for _, message := range detail.Messages {
		assertNoSecrets(message.Content)
	}
	var intakeID, storedActor, storedSession, created, expires int64
	var passwordCipher, keyCipher, phraseCipher, fingerprint string
	if err = db.QueryRowContext(ctx, `SELECT id,actor_id,session_id,password_cipher,private_key_cipher,key_passphrase_cipher,created_at,expires_at FROM v2_ai_server_intake WHERE turn_id=$1`, result.Turn.ID).Scan(&intakeID, &storedActor, &storedSession, &passwordCipher, &keyCipher, &phraseCipher, &created, &expires); err != nil {
		t.Fatal(err)
	}
	if intakeID <= 0 || storedActor != actor || storedSession != result.Session.ID || expires-created != 86400 {
		t.Fatal("intake actor/session/expiry incorrect")
	}
	for _, pair := range [][2]string{{passwordCipher, password}, {keyCipher, key}, {phraseCipher, phrase}} {
		if pair[0] == pair[1] || !strings.HasPrefix(pair[0], "v1:") {
			t.Fatal("credential persisted without encryption")
		}
		plain, err := s.decryptSecret(pair[0])
		if err != nil || plain != pair[1] {
			t.Fatal("encrypted credential does not preserve original bytes")
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT request_hash FROM v2_ai_turn WHERE id=$1`, result.Turn.ID).Scan(&fingerprint); err != nil || fingerprint != requestHash(req) {
		t.Fatal("idempotency hash must use the original secret-sensitive request", err)
	}
	retry, err := s.SendMessage(ctx, actor, req)
	if err != nil || retry.Turn.ID != result.Turn.ID {
		t.Fatal("same request was not idempotent", err)
	}
	changed := req
	changed.Content = strings.ReplaceAll(req.Content, password, "changed-fixture-password")
	if _, err = s.SendMessage(ctx, actor, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed credential with reused request id must conflict", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE session_id=$1`, result.Session.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry created duplicate intake", count, err)
	}
	normal, err := s.SendMessage(ctx, actor, SendMessageRequest{SessionID: result.Session.ID, Model: DefaultModels[0], Content: "继续检查正常任务", RequestID: "normal-" + marker})
	if err != nil {
		t.Fatal(err)
	}
	wait(normal.Turn.ID)
	<-requests
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, normal.Turn.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("ordinary message created an intake", count, err)
	}
	invalid := SendMessageRequest{Model: DefaultModels[0], Content: "| 密码 | fixture-intake-password |", RequestID: "invalid-" + marker}
	if _, err = s.SendMessage(ctx, actor, invalid); !errors.Is(err, ErrSSHIntakeInvalid) {
		t.Fatal("unsupported credential format should be rejected before persistence", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_turn WHERE actor_id=$1 AND request_id=$2`, actor, invalid.RequestID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected credentials persisted a turn", count, err)
	}
	select {
	case <-requests:
		t.Fatal("rejected or idempotent input caused a relay request")
	default:
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM v2_ai_turn WHERE id=$1`, result.Turn.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE id=$1`, intakeID).Scan(&count); err != nil || count != 0 {
		t.Fatal("turn deletion did not cascade intake", count, err)
	}
	req.RequestID = "cascade-" + marker
	second, err := s.SendMessage(ctx, actor, req)
	if err != nil {
		t.Fatal(err)
	}
	wait(second.Turn.ID)
	<-requests
	if err = s.DeleteSession(ctx, actor, second.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE session_id=$1`, second.Session.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("session deletion did not cascade intake", count, err)
	}
}
