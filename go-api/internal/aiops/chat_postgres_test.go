package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAIChatPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("use disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var mu sync.Mutex
	calls := 0
	mode := "ok"
	seenModels := []string{}
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	var lastMessages []providerMessage
	var pendingPolicy int64
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		calls++
		current := mode
		seenModels = append(seenModels, req.Model)
		lastMessages = req.Messages
		mu.Unlock()
		started <- struct{}{}
		if current == "slow" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if current == "pending" {
			args, _ := json.Marshal(map[string]any{"kind": "policy", "target_id": pendingPolicy, "host": "after.example.test", "reason": "测试待确认"})
			response := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []ToolCall{
				{ID: "reused-id", Type: "function", Function: ToolFunction{Name: "propose_entry_host", Arguments: string(args)}},
				{ID: "after-pending", Type: "function", Function: ToolFunction{Name: "panel_status", Arguments: "{}"}},
			}}}}}
			json.NewEncoder(w).Encode(response)
			return
		}
		if current == "tools" {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"reused-id","type":"function","function":{"name":"not_a_tool","arguments":"{}"}}]}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"完成分析，未修改任何配置"}}]}`))
	}))
	defer relay.Close()
	s := NewService(config.Config{AppKey: "test-ai-key"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_session`).Scan(&count); err != nil || count != 0 {
		t.Fatal("use empty database")
	}
	var actor int64
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','test',$2,1,1,1)RETURNING id`, fmt.Sprintf("ai-%d@example.test", time.Now().UnixNano()), fmt.Sprint(time.Now().UnixNano())).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM v2_user WHERE id=$1`, actor)
	defer s.ResetData(ctx, actor)
	cfg, err := s.GetConfig(ctx, actor)
	if err != nil || cfg.Enabled || len(cfg.Models) != 4 {
		t.Fatalf("default %#v %v", cfg, err)
	}
	cfg, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", Models: DefaultModels, APIKey: "test-provider-key"})
	if err != nil || !cfg.HasAPIKey {
		t.Fatal(err)
	}
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://different.test/v1", Models: DefaultModels}); !errors.Is(err, ErrValidation) {
		t.Fatalf("key inherited by new host %v", err)
	}
	wait := func(id int64) SessionDetail {
		t.Helper()
		for i := 0; i < 200; i++ {
			v, err := s.GetSession(ctx, actor, id)
			if err != nil {
				t.Fatal(err)
			}
			if v.Session.Status != "running" {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn timeout")
		return SessionDetail{}
	}
	req := SendMessageRequest{Model: DefaultModels[1], Content: "查询状态", RequestID: "request-first-0001"}
	result, err := s.SendMessage(ctx, actor, req)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.SendMessage(ctx, actor, req)
	if err != nil || retry.Turn.ID != result.Turn.ID {
		t.Fatalf("idempotency %v", err)
	}
	detail := wait(result.Session.ID)
	if len(detail.Messages) != 2 || detail.Turns[0].Status != "completed" {
		t.Fatalf("detail %#v", detail)
	}
	mu.Lock()
	if calls != 1 || seenModels[0] != DefaultModels[1] {
		t.Fatal("duplicate charge/model mapping")
	}
	mu.Unlock()
	// An executing action holds this row. A new message must fail fast before
	// making any provider request, rather than queue behind a remote operation.
	lockedSession, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.requireSessionTx(ctx, lockedSession, result.Session.ID); err != nil {
		lockedSession.Rollback()
		t.Fatal(err)
	}
	lockedCtx, lockedCancel := context.WithTimeout(ctx, time.Second)
	lockedStart := time.Now()
	_, lockedErr := s.SendMessage(lockedCtx, actor, SendMessageRequest{SessionID: result.Session.ID, Model: DefaultModels[1], Content: "locked", RequestID: "request-session-locked"})
	lockedElapsed := time.Since(lockedStart)
	lockedCancel()
	lockedSession.Rollback()
	if !errors.Is(lockedErr, ErrBusy) || lockedElapsed >= time.Second {
		t.Fatalf("locked session must reject fast: elapsed=%s error=%v", lockedElapsed, lockedErr)
	}
	mu.Lock()
	if calls != 1 {
		t.Errorf("locked session caused a provider request: %d", calls)
	}
	mu.Unlock()
	var lockedTurns int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_turn WHERE request_id='request-session-locked'`).Scan(&lockedTurns); err != nil || lockedTurns != 0 {
		t.Fatalf("locked session inserted a turn: %d %v", lockedTurns, err)
	}
	for i, m := range DefaultModels {
		req = SendMessageRequest{SessionID: result.Session.ID, Model: m, Content: "继续分析", RequestID: fmt.Sprintf("request-model-%d", i)}
		if _, err = s.SendMessage(ctx, actor, req); err != nil {
			t.Fatal(err)
		}
		wait(result.Session.ID)
	}

	if _, err := s.panel.ListClientEntryUserPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,created_at,updated_at)VALUES('ai-chat-pending-test',10,'override','[]','before.example.test',1,1)RETURNING id`).Scan(&pendingPolicy); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id=$1`, pendingPolicy)
	mu.Lock()
	mode = "pending"
	mu.Unlock()
	pendingResult, err := s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "提案入口切换", AccessMode: "standard", RequestID: "request-pending-0001"})
	if err != nil {
		t.Fatal(err)
	}
	pendingDetail := wait(pendingResult.Session.ID)
	if pendingDetail.Turns[0].Status != "pending" || len(pendingDetail.Messages) != 5 {
		t.Fatalf("pending pairing %#v", pendingDetail)
	}
	if pendingDetail.Messages[2].ToolCallID != "reused-id" || pendingDetail.Messages[3].ToolCallID != "after-pending" || !strings.Contains(pendingDetail.Messages[3].Content, "skipped") {
		t.Fatal("pending batch calls not paired/skipped")
	}
	var unchanged string
	if err = db.QueryRowContext(ctx, `SELECT entry_host FROM v2_client_entry_user_policy WHERE id=$1`, pendingPolicy).Scan(&unchanged); err != nil || unchanged != "before.example.test" {
		t.Fatal("proposal executed without approval")
	}
	actions, err := s.ListActions(ctx, actor, pendingResult.Session.ID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("pending actions %#v %v", actions, err)
	}
	if _, err = s.RejectAction(ctx, actor, pendingResult.Session.ID, actions[0].ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	mode = "ok"
	mu.Unlock()
	if _, err = s.SendMessage(ctx, actor, SendMessageRequest{SessionID: pendingResult.Session.ID, Model: DefaultModels[0], Content: "继续分析执行结果", RequestID: "request-summary-0001"}); err != nil {
		t.Fatal(err)
	}
	wait(pendingResult.Session.ID)
	mu.Lock()
	hasSummary := false
	for _, m := range lastMessages {
		if strings.Contains(m.Content, "系统提供的动作状态数据") && strings.Contains(m.Content, "rejected") {
			hasSummary = true
		}
	}
	mu.Unlock()
	if !hasSummary {
		t.Fatal("action results absent from history")
	}
	// Reused provider tool IDs in a later turn must not reuse the previous action.
	mu.Lock()
	mode = "pending"
	mu.Unlock()
	if _, err = s.SendMessage(ctx, actor, SendMessageRequest{SessionID: pendingResult.Session.ID, Model: DefaultModels[0], Content: "再次提案", RequestID: "request-pending-0002"}); err != nil {
		t.Fatal(err)
	}
	wait(pendingResult.Session.ID)
	actions, err = s.ListActions(ctx, actor, pendingResult.Session.ID)
	if err != nil || len(actions) != 2 {
		t.Fatalf("tool call collision %#v %v", actions, err)
	}
	mu.Lock()
	mode = "slow"
	mu.Unlock()
	for len(started) > 0 {
		<-started
	}
	slow, err := s.SendMessage(ctx, actor, SendMessageRequest{SessionID: result.Session.ID, Model: DefaultModels[0], Content: "等待", RequestID: "request-slow-delete"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = s.SendMessage(ctx, actor, SendMessageRequest{SessionID: result.Session.ID, Model: DefaultModels[0], Content: "并发", RequestID: "request-concurrent"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("same session concurrent %v", err)
	}
	if err = s.DeleteSession(ctx, actor, slow.Session.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	if _, err = s.GetSession(ctx, actor, slow.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted resurrected %v", err)
	}
	mu.Lock()
	mode = "tools"
	mu.Unlock()
	tools, err := s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "测试工具配对", AccessMode: "standard", RequestID: "request-tools-0001"})
	if err != nil {
		t.Fatal(err)
	}
	detail = wait(tools.Session.ID)
	if detail.Turns[0].Status != "failed" {
		t.Fatalf("loop unbounded %#v", detail.Turns)
	}
	for i, m := range detail.Messages {
		if len(m.ToolCalls) > 0 {
			if i+1 >= len(detail.Messages) || detail.Messages[i+1].ToolCallID != m.ToolCalls[0].ID {
				t.Fatal("unmatched call")
			}
		}
	}
	mu.Lock()
	mode = "ok"
	mu.Unlock()
	if _, err = s.SendMessage(ctx, actor, SendMessageRequest{SessionID: tools.Session.ID, Model: DefaultModels[0], Content: "继续", RequestID: "request-recovery-0001"}); err != nil {
		t.Fatal(err)
	}
	wait(tools.Session.ID)
	mu.Lock()
	for _, m := range lastMessages {
		if len(m.ToolCalls) > 0 {
			t.Fatal("failed turn included")
		}
	}
	mu.Unlock()
	if err = s.ResetData(ctx, actor); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.GetConfig(ctx, actor)
	if err != nil || cfg.HasAPIKey || cfg.Enabled {
		t.Fatalf("reset cfg %#v %v", cfg, err)
	}
	sessions, err := s.ListSessions(ctx, actor)
	if err != nil || len(sessions) != 0 {
		t.Fatal("reset sessions")
	}
	var raw string
	if err = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT api_key_cipher FROM v2_ai_config WHERE id=1),'')`).Scan(&raw); err != nil || strings.Contains(raw, "provider-key") {
		t.Fatal("key leak")
	}

	// Shared leases can belong to an operation in another session; cleanup may
	// not wait for that remote execution even if its own action precheck is empty.
	idleSession, err := s.CreateSession(ctx, actor, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = lockAIState(ctx, lease); err != nil {
		lease.Rollback()
		t.Fatal(err)
	}
	leaseCtx, leaseCancel := context.WithTimeout(ctx, time.Second)
	err = s.DeleteSession(leaseCtx, actor, idleSession.ID)
	leaseCancel()
	lease.Rollback()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("shared lease must reject cleanup immediately: %v", err)
	}
	if err = s.DeleteSession(ctx, actor, idleSession.ID); err != nil {
		t.Fatal(err)
	}

	// A crash can leave a claimed action behind. Fresh running operations block
	// deletion, but actions beyond the execution lease must not block it forever.
	for _, operation := range []string{"delete", "clear", "reset"} {
		staleSession, err := s.CreateSession(ctx, actor, CreateSessionRequest{Model: DefaultModels[0]})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		var actionID int64
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,payload,snapshot_hash,request_hash,reason,status,expires_at,claimed_at,created_at,updated_at)VALUES($1,$2,$3,'ssh','{}','snapshot','request','crash regression','running',$4,$4,$4,$4)RETURNING id`, staleSession.ID, actor, "stale-action-"+operation, now).Scan(&actionID); err != nil {
			t.Fatal(err)
		}
		cleanup := func() error {
			switch operation {
			case "delete":
				return s.DeleteSession(ctx, actor, staleSession.ID)
			case "clear":
				return s.ClearSessions(ctx, actor)
			default:
				return s.ResetData(ctx, actor)
			}
		}
		if err = cleanup(); !errors.Is(err, ErrBusy) {
			t.Fatalf("%s must reject fresh running action: %v", operation, err)
		}
		if _, err = db.ExecContext(ctx, `UPDATE v2_ai_action SET claimed_at=$2 WHERE id=$1`, actionID, now-181); err != nil {
			t.Fatal(err)
		}
		if err = cleanup(); err != nil {
			t.Fatalf("%s blocked by crashed old action: %v", operation, err)
		}
		if _, err = s.GetSession(ctx, actor, staleSession.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s failed to remove stale session: %v", operation, err)
		}
	}

	// Four global workers, no fifth provider charge, Close cancels all workers.
	cfg, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", Models: DefaultModels, APIKey: "test-provider-key"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	mode = "slow"
	release = make(chan struct{})
	mu.Unlock()
	for len(started) > 0 {
		<-started
	}
	active := []SendMessageResult{}
	for i := 0; i < 4; i++ {
		v, err := s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "并发上限测试", RequestID: fmt.Sprintf("global-workers-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		active = append(active, v)
		<-started
	}
	if _, err = s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "第五个", RequestID: "global-workers-overflow"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("worker limit %v", err)
	}
	resetDone := make(chan error, 1)
	go func() { resetDone <- s.ResetData(ctx, actor) }()
	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reset deadlock")
	}
	closeDone := make(chan struct{})
	go func() { s.Close(); close(closeDone) }()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close deadlock")
	}
	for _, v := range active {
		if _, err := s.GetSession(ctx, actor, v.Session.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("reset resurrected %v", err)
		}
	}
	if _, err = s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "closed", RequestID: "request-after-close"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("send after Close %v", err)
	}
	t.Log("verified config crypto/exact models/request idempotency/async turns/one running per session/tool pairing/6-loop bound/delete no resurrection/reset")
}
