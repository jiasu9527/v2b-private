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
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestAIStreamDraftPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewService(config.Config{AppKey: "stream-test-only"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1)RETURNING id`, "stream-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	defer s.ResetData(ctx, actor)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Stream {
			t.Error("missing streaming request")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好\"}}]}\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"，正在检查\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer relay.Close()
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "fixture-only", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	run := func(request string) SendMessageResult {
		t.Helper()
		v, e := s.SendMessage(ctx, actor, SendMessageRequest{Content: "fixture", Model: DefaultModels[0], RequestID: request + marker})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	first := run("stream-finish-")
	<-started
	waitDraft := func(id int64) Draft {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			d, e := s.GetDraft(ctx, actor, id)
			if e != nil {
				t.Fatal(e)
			}
			if d.Content == "你好" {
				return d
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no live draft before upstream completes")
		return Draft{}
	}
	draft := waitDraft(first.Session.ID)
	if draft.Round != 1 || draft.Version <= 0 || draft.Status != "running" {
		t.Fatalf("bad draft %#v", draft)
	}
	// A fresh Service instance reads the same live draft (no process-local broker).
	other := NewService(config.Config{AppKey: "stream-test-only"}, db, nil)
	defer other.Close()
	secondView, e := other.GetDraft(ctx, actor, first.Session.ID)
	if e != nil || secondView.Content != draft.Content {
		t.Fatal("cross instance draft", e)
	}
	detail, e := s.GetSession(ctx, actor, first.Session.ID)
	if e != nil || len(detail.Messages) != 1 || detail.Turns[0].DraftContent != "你好" {
		t.Fatal("draft incorrectly appended as final message", e)
	}
	// Cancel another in-flight stream by deleting its session, without reviving it.
	deleted := run("stream-delete-")
	<-started
	waitDraft(deleted.Session.ID)
	if e = s.DeleteSession(ctx, actor, deleted.Session.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.updateDraft(ctx, actor, deleted.Session.ID, deleted.Turn.ID, 1, "late"); !errors.Is(e, ErrConflict) {
		t.Fatal("late draft update", e)
	}
	once.Do(func() { close(release) })
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		detail, e = s.GetSession(ctx, actor, first.Session.ID)
		if e != nil {
			t.Fatal(e)
		}
		if detail.Session.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if detail.Session.Status == "running" || len(detail.Messages) != 2 || detail.Messages[1].Content != "你好，正在检查" || detail.Turns[0].DraftContent != "" || detail.Turns[0].DraftVersion <= draft.Version {
		t.Fatal("final stream not atomically published/cleared")
	}
	if _, e = s.GetSession(ctx, actor, deleted.Session.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("deleted stream resurrected", e)
	}
	var drafts, unexpected int
	if e = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_turn WHERE session_id=$1 AND draft_content<>''`, first.Session.ID).Scan(&drafts); e != nil || drafts != 0 {
		t.Fatal("leftover draft", e)
	}
	if e = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE session_id=$1 AND role='assistant'`, first.Session.ID).Scan(&unexpected); e != nil || unexpected != 1 {
		t.Fatal("per-token message records", e)
	}
	if strings.Contains(detail.Messages[1].Content, "fixture-only") {
		t.Fatal("secret exposed")
	}
	s.Close()
}

func TestAIStreamSessionSnapshotPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Keep the reader on one identifiable PostgreSQL backend. No worker or
	// upstream request participates in this message/draft publication race.
	readerDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer readerDB.Close()
	readerDB.SetMaxOpenConns(1)
	s := NewService(config.Config{}, readerDB, nil)
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var readerPID int
	if err = readerDB.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&readerPID); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var actor, sessionID, turnID int64
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES($1,'unused','fixture',$2,1,1,1) RETURNING id`, "snapshot-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	now := time.Now().Unix()
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_session(title,model,created_by,status,created_at,updated_at) VALUES('snapshot fixture',$1,$2,'running',$3,$3) RETURNING id`, DefaultModels[0], actor, now).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, sessionID)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at,draft_content,draft_round,draft_version) VALUES($1,$2,$3,'fixture',$4,'running',$5,$6,'live draft',1,1) RETURNING id`, sessionID, actor, "snapshot-"+marker, DefaultModels[0], now, now+60).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO v2_ai_message(session_id,turn_id,role,content,created_at) VALUES($1,$2,'user','fixture prompt',$3)`, sessionID, turnID, now); err != nil {
		t.Fatal(err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.ExecContext(ctx, `LOCK TABLE v2_ai_message IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		detail SessionDetail
		err    error
	}
	read := make(chan result, 1)
	go func() {
		detail, err := s.GetSession(ctx, actor, sessionID)
		read <- result{detail: detail, err: err}
	}()
	// Waiting for the actual messages SELECT, rather than a sleep, proves the
	// read transaction has already taken its initial snapshot before publication.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid=$1 AND wait_event_type='Lock' AND query LIKE 'SELECT id,session_id,turn_id,role,content,%FROM v2_ai_message%')`, readerPID).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case outcome := <-read:
			t.Fatalf("reader finished before the controlled publication: %v", outcome.err)
		case <-ctx.Done():
			t.Fatal("reader did not block on the messages SELECT before timeout")
		case <-ticker.C:
		}
	}
	if _, err = writer.ExecContext(ctx, `INSERT INTO v2_ai_message(session_id,turn_id,role,content,created_at) VALUES($1,$2,'assistant','final answer',$3)`, sessionID, turnID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ExecContext(ctx, `UPDATE v2_ai_turn SET draft_content='',draft_version=draft_version+1 WHERE id=$1`, turnID); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	var first SessionDetail
	select {
	case outcome := <-read:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		first = outcome.detail
	case <-ctx.Done():
		t.Fatal("snapshot reader did not finish after publication")
	}
	if len(first.Messages) != 1 || first.Messages[0].Role != "user" || first.Messages[0].Content != "fixture prompt" || len(first.Turns) != 1 || first.Turns[0].DraftContent != "live draft" || first.Turns[0].DraftVersion != 1 {
		t.Fatalf("mixed message/draft snapshots: messages=%#v turns=%#v", first.Messages, first.Turns)
	}
	second, err := s.GetSession(ctx, actor, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || second.Messages[1].Role != "assistant" || second.Messages[1].Content != "final answer" || len(second.Turns) != 1 || second.Turns[0].DraftContent != "" || second.Turns[0].DraftVersion != 2 {
		t.Fatalf("new snapshot did not see atomic publication: messages=%#v turns=%#v", second.Messages, second.Turns)
	}
}
