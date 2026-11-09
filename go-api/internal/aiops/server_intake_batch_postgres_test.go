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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Every test owns an isolated search_path, including its user/config tables.
// The legacy-schema test must never alter a caller's shared tables.
func sshBatchDBFixture(t *testing.T) (*sql.DB, *Service) {
	t.Helper()
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("ssh_batch_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error("remove isolated schema:", err)
		}
		base.Close()
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE v2_user(id BIGINT PRIMARY KEY,is_admin INTEGER NOT NULL DEFAULT 0,is_staff INTEGER NOT NULL DEFAULT 0,banned INTEGER NOT NULL DEFAULT 0); INSERT INTO v2_user(id,is_admin,is_staff)VALUES(1,1,0),(2,0,1),(3,0,1),(4,0,0)`); err != nil {
		t.Fatal(err)
	}
	s := NewService(config.Config{AppKey: "ssh-batch-isolated-fixture"}, db, admin.NewDBService(config.Config{}, db))
	t.Cleanup(s.Close)
	if err = s.ensureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, s
}

func sshBatchSessionTurn(t *testing.T, db *sql.DB, actor int64) (int64, int64) {
	t.Helper()
	var session, turn int64
	now := time.Now().Unix()
	if err := db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('batch','fixture',$1,$2,$2)RETURNING id`, actor, now).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)VALUES($1,$2,$3,'fixture','fixture','completed',$4,$5)RETURNING id`, session, actor, fmt.Sprintf("batch-%d", time.Now().UnixNano()), now, now+3600).Scan(&turn); err != nil {
		t.Fatal(err)
	}
	return session, turn
}

func TestSSHIntakeBatchPostgresLegacyMigrationPreservesCiphertext(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx := context.Background()
	session, turn := sshBatchSessionTurn(t, db, 1)
	// Recreate only the previous constraint shape in this test's own schema.
	if _, err := db.Exec(`ALTER TABLE v2_ai_server_intake DROP COLUMN item_index CASCADE; ALTER TABLE v2_ai_server_intake ADD CONSTRAINT v2_ai_server_intake_turn_id_key UNIQUE(turn_id)`); err != nil {
		t.Fatal(err)
	}
	cipher, err := s.encryptSecret("fixture-old-secret")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = db.QueryRow(`INSERT INTO v2_ai_server_intake(session_id,turn_id,actor_id,name,host,port,username,password_cipher,created_at,expires_at)VALUES($1,$2,1,'legacy','192.0.2.1',22,'root',$3,1,9999999999)RETURNING id`, session, turn, cipher).Scan(&id); err != nil {
		t.Fatal(err)
	}
	upgrade := NewService(s.cfg, db, s.panel)
	defer upgrade.Close()
	if err = upgrade.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var saved string
	var index int
	if err = db.QueryRow(`SELECT password_cipher,item_index FROM v2_ai_server_intake WHERE id=$1`, id).Scan(&saved, &index); err != nil || saved != cipher || index != 1 {
		t.Fatal("legacy intake changed during upgrade", err)
	}
	// A second item now fits the same turn; a repeated item index still fails.
	insert := `INSERT INTO v2_ai_server_intake(session_id,turn_id,actor_id,item_index,name,host,port,username,password_cipher,created_at,expires_at)VALUES($1,$2,1,2,'second','192.0.2.2',22,'root',$3,1,9999999999)`
	if _, err = db.Exec(insert, session, turn, cipher); err != nil {
		t.Fatal("one-intake constraint survived migration", err)
	}
	if _, err = db.Exec(insert, session, turn, cipher); err == nil {
		t.Fatal("turn/item uniqueness was lost")
	}
	// A fresh service reruns the migration safely over an already-batched turn.
	repeat := NewService(s.cfg, db, s.panel)
	defer repeat.Close()
	if err = repeat.ensureSchema(ctx); err != nil {
		t.Fatal("migration is not replayable", err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, turn).Scan(&count); err != nil || count != 2 {
		t.Fatal("migration replay lost records", count, err)
	}
}

func TestSSHIntakeBatchPostgresAtomicStoreAndPagedOwnerProjection(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx := context.Background()
	session, turn := sshBatchSessionTurn(t, db, 2)
	otherSession, otherTurn := sshBatchSessionTurn(t, db, 3)
	inputs := make([]SSHRegistrationInput, maxSSHIntakeBatchSize)
	for i := range inputs {
		inputs[i] = SSHRegistrationInput{Name: fmt.Sprintf("%02d", i) + strings.Repeat("服", 126), Host: fmt.Sprintf("%02d.", i) + strings.Repeat(strings.Repeat("h", 60)+".", 3) + "example.test", Port: 22, Username: "root", Password: fmt.Sprintf("batch-only-secret-%02d", i)}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.storeSSHIntakesTx(ctx, tx, 2, session, turn, inputs)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, `"total":50`) || strings.Contains(summary, "batch-only-secret") || strings.Contains(summary, "password_cipher") {
		t.Fatal("batch receipt lost count or exposed credentials")
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, turn).Scan(&count); err != nil || count != 50 {
		t.Fatal("batch was not fully persisted", count, err)
	}
	for i := range inputs {
		var cipher string
		var created, expires int64
		if err = db.QueryRow(`SELECT password_cipher,created_at,expires_at FROM v2_ai_server_intake WHERE turn_id=$1 AND item_index=$2`, turn, i+1).Scan(&cipher, &created, &expires); err != nil {
			t.Fatal(err)
		}
		plain, err := s.decryptSecret(cipher)
		if err != nil || plain != inputs[i].Password || cipher == plain || expires-created != 86400 {
			t.Fatal("per-record encryption/expiry failed", err)
		}
	}
	// Failing the second insert must not leave the first item after rollback.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := []SSHRegistrationInput{inputs[0], inputs[1]}
	bad[1].Port = 70000
	if _, err = s.storeSSHIntakesTx(ctx, tx, 3, otherSession, otherTurn, bad); err == nil {
		tx.Rollback()
		t.Fatal("invalid second row unexpectedly persisted")
	}
	tx.Rollback()
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, otherTurn).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed batch left a partial intake", count, err)
	}
	if _, err = db.Exec(`UPDATE v2_ai_server_intake SET registered_server_id=123456 WHERE turn_id=$1 AND item_index=1;`, turn); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE v2_ai_server_intake SET expires_at=1 WHERE turn_id=$1 AND item_index=2`, turn); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,target_id,payload,snapshot_hash,request_hash,reason,expires_at,created_at,updated_at)SELECT session_id,actor_id,'batch-pending','server_register',id,'{}','fixture','fixture','fixture',$2,1,1 FROM v2_ai_server_intake WHERE turn_id=$1 AND item_index=3`, turn, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	call := func(actor, sid int64, args map[string]any) (ToolOutcome, error) {
		raw, _ := json.Marshal(args)
		return s.callTool(ctx, actor, sid, ToolCall{Function: ToolFunction{Name: "ssh_intakes_list", Arguments: string(raw)}})
	}
	type page struct {
		TurnID    int64             `json:"turn_id"`
		Total     int               `json:"total"`
		Items     []sshIntakePublic `json:"items"`
		Next      *int              `json:"next_offset"`
		Truncated bool              `json:"truncated"`
	}
	seen := map[int64]bool{}
	offset := 0
	for {
		result, err := call(2, session, map[string]any{"turn_id": turn, "offset": offset, "limit": 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Content) >= 32000 || strings.Contains(result.Content, "batch-only-secret") || strings.Contains(result.Content, "password_cipher") || strings.Contains(result.Content, "v1:") {
			t.Fatal("tool projection leaked credentials or exceeded chat cap")
		}
		var p page
		if json.Unmarshal([]byte(result.Content), &p) != nil || p.Total != 50 || p.TurnID != turn || len(p.Items) == 0 {
			t.Fatal("invalid batch page")
		}
		if offset == 0 && (!p.Truncated || len(p.Items) >= 50 || p.Items[0].State != "registered" || p.Items[1].State != "expired" || p.Items[2].State != "pending" || p.Items[3].State != "unregistered") {
			t.Fatal("byte-cap pagination or progress state missing")
		}
		for _, item := range p.Items {
			if seen[item.IntakeID] {
				t.Fatal("duplicate pagination item")
			}
			seen[item.IntakeID] = true
		}
		if !p.Truncated {
			if p.Next != nil {
				t.Fatal("terminal page still has a cursor")
			}
			break
		}
		if p.Next == nil || *p.Next != offset+len(p.Items) {
			t.Fatal("cursor did not track actual byte-limited page size")
		}
		offset = *p.Next
	}
	if len(seen) != 50 {
		t.Fatal("pagination omitted records")
	}
	if _, err = call(2, session, map[string]any{"offset": 1}); !errors.Is(err, ErrValidation) {
		t.Fatal("unstable implicit batch pagination accepted", err)
	}
	if _, err = call(3, session, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("another staff member could read the session", err)
	}
	if _, err = call(4, session, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user could list intakes", err)
	}
	for _, query := range []struct{ actor, session, turn int64 }{{1, session, turn}, {3, otherSession, turn}, {2, session, otherTurn}} {
		result, err := call(query.actor, query.session, map[string]any{"turn_id": query.turn})
		var p page
		if err != nil || json.Unmarshal([]byte(result.Content), &p) != nil || p.Total != 0 || len(p.Items) != 0 {
			t.Fatal("actor/session/turn projection boundary failed", err)
		}
	}
}

func TestSSHIntakeBatchPostgresSendIdempotencyAndNoSecretRelay(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var calls atomic.Int32
	var leaked atomic.Bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), "batch-send-secret") {
			leaked.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"已接收批量资料，尚未登记服务器。"}}]}`)
	}))
	defer relay.Close()
	if _, err := s.SaveConfig(ctx, 1, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "test-relay", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	req := SendMessageRequest{Model: DefaultModels[0], RequestID: "batch-send-idempotency", Content: "帮我添加这两台服务器\n192.0.2.10 root batch-send-secret-A\n192.0.2.11:2222 admin batch-send-secret-B"}
	sent, err := s.SendMessage(ctx, 1, req)
	if err != nil {
		t.Fatal(err)
	}
	for {
		var state, problem string
		if err := db.QueryRowContext(ctx, `SELECT status,error FROM v2_ai_turn WHERE id=$1`, sent.Turn.ID).Scan(&state, &problem); err != nil {
			t.Fatal(err)
		}
		if state != "running" {
			if state != "completed" {
				t.Fatal("batch message failed", state, problem)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	replay, err := s.SendMessage(ctx, 1, req)
	if err != nil || replay.Turn.ID != sent.Turn.ID || calls.Load() != 1 || leaked.Load() {
		t.Fatal("replay repeated request or leaked a batch credential", err)
	}
	req.Content = strings.Replace(req.Content, "secret-B", "secret-C", 1)
	if _, err = s.SendMessage(ctx, 1, req); !errors.Is(err, ErrConflict) {
		t.Fatal("changing only the second password did not conflict", err)
	}
	detail, err := s.GetSession(ctx, 1, sent.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), "batch-send-secret") {
		t.Fatal("batch credentials leaked into session title/messages")
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1`, sent.Turn.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("idempotent send changed intake count", count, err)
	}
	bad := SendMessageRequest{Model: DefaultModels[0], RequestID: "batch-send-invalid", Content: "192.0.2.20 root batch-send-secret-D\n192.0.2.21\nroot"}
	if _, err = s.SendMessage(ctx, 1, bad); !errors.Is(err, ErrValidation) {
		t.Fatal("incomplete second server was not rejected", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_turn WHERE request_id=$1`, bad.RequestID).Scan(&count); err != nil || count != 0 || calls.Load() != 1 {
		t.Fatal("invalid batch persisted or reached relay", count, err)
	}
}
