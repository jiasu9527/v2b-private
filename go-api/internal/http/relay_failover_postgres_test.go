package httpapi

import (
	"context"
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
)

// Exercise a real tool mutation, then lose the next upstream stream. Switching
// providers must preserve the result, discard the unfinished tool, and clear the
// old draft even when its last update is within the normal 600ms throttle.
func TestRelayFailoverPostgresPreservesExecutedOperations(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_FAILOVER_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_FAILOVER_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_user`).Scan(&count); err != nil || count != 0 {
		t.Fatal("requires empty test database", err)
	}
	var actor int64
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES('relay-flow@example.test','fixture','fixture','fixture',1,1,1) RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	title := "Relay failover fixture"
	defer db.Exec(`DELETE FROM v2_notice WHERE title=$1`, title)
	cfg := config.Config{AdminPath: "relay-test", AppKey: "isolated-relay-flow-key"}
	panel := admin.NewDBService(cfg, db)
	ai := aiops.NewService(cfg, db, panel)
	defer ai.ResetData(ctx, actor)
	defer ai.Close()
	NewRouter(cfg, WithAIOps(ai), WithAdminService(panel))

	type message struct {
		Role       string           `json:"role"`
		Content    string           `json:"content"`
		ToolCallID string           `json:"tool_call_id"`
		ToolCalls  []aiops.ToolCall `json:"tool_calls"`
	}
	type request struct {
		Model           string    `json:"model"`
		ReasoningEffort string    `json:"reasoning_effort"`
		Messages        []message `json:"messages"`
	}
	decode := func(r *http.Request, key string) request {
		t.Helper()
		var req request
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != aiops.DefaultModels[0] || req.ReasoningEffort != "high" || r.Header.Get("Authorization") != "Bearer "+key || r.URL.Path != "/v1/chat/completions" {
			t.Error("relay request changed model, reasoning, endpoint or credential")
		}
		return req
	}
	frame := func(content string) string {
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}}})
		return "data: " + string(raw) + "\n\n"
	}
	var primaryCalls, backupCalls atomic.Int32
	release := make(chan struct{})
	backupStarted := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var oldDraftVersion atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decode(r, "primary-fixture-key")
		n := primaryCalls.Add(1)
		if n == 1 {
			args, _ := json.Marshal(map[string]any{"operation": "notices.save", "parameters": map[string]any{"title": title, "content": "exactly once", "tags": []string{}}, "reason": "isolated failover test"})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: "write-once", Type: "function", Function: aiops.ToolFunction{Name: "propose_panel_operation", Arguments: string(args)}}}}}}})
			return
		}
		if n != 2 {
			t.Error("completed work was replayed")
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, frame("主站未完成"))
		// This proposed second write is deliberately unfinished and must never run.
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"incomplete-write","type":"function","function":{"name":"propose_panel_operation","arguments":"{"}}]}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var draft string
			var version int64
			e := db.QueryRow(`SELECT draft_content,draft_version FROM v2_ai_turn WHERE actor_id=$1 ORDER BY id DESC LIMIT 1`, actor).Scan(&draft, &version)
			if e == nil && draft == "主站未完成" {
				oldDraftVersion.Store(version)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("primary stream draft not persisted")
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decode(r, "backup-fixture-key")
		backupCalls.Add(1)
		toolResults := 0
		for _, m := range req.Messages {
			if m.Role == "tool" && m.ToolCallID == "write-once" {
				toolResults++
			}
			if strings.Contains(m.Content, "主站未完成") {
				t.Error("unfinished content passed to backup")
			}
			for _, call := range m.ToolCalls {
				if call.ID == "incomplete-write" {
					t.Error("partial tool passed to backup")
				}
			}
		}
		if toolResults != 1 {
			t.Error("backup lost completed tool result", toolResults)
		}
		var draft string
		var version int64
		if e := db.QueryRow(`SELECT draft_content,draft_version FROM v2_ai_turn WHERE actor_id=$1 ORDER BY id DESC LIMIT 1`, actor).Scan(&draft, &version); e != nil || draft != "" || oldDraftVersion.Load() == 0 || version <= oldDraftVersion.Load() {
			t.Error("relay switch did not clear draft before backup", draft, version, e)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, frame("备用处理中"))
		w.(http.Flusher).Flush()
		close(backupStarted)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, frame("，完成"))
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer backup.Close()
	defer unblock()
	enabled, timeout := true, 10
	relays := []aiops.RelaySaveRequest{{Name: "备用测试站", BaseURL: backup.URL, Enabled: true, APIKey: "backup-fixture-key"}}
	efforts := map[string]string{aiops.DefaultModels[0]: "high"}
	if _, err = ai.SaveConfig(ctx, actor, aiops.ConfigSaveRequest{Enabled: true, BaseURL: primary.URL, APIKey: "primary-fixture-key", Models: aiops.DefaultModels, ReasoningEfforts: &efforts, FailoverEnabled: &enabled, FailoverTimeoutSeconds: &timeout, FallbackRelays: &relays}); err != nil {
		t.Fatal(err)
	}
	session, err := ai.CreateSession(ctx, actor, aiops.CreateSessionRequest{Model: aiops.DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := ai.SendMessage(ctx, actor, aiops.SendMessageRequest{SessionID: session.ID, Model: aiops.DefaultModels[0], Content: "创建隔离测试公告并报告结果", RequestID: "relay-failover-flow", AccessMode: "full", AccessVersion: &session.AccessVersion})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backupStarted:
	case <-time.After(8 * time.Second):
		detail, _ := ai.GetSession(ctx, actor, sent.Session.ID)
		t.Fatalf("backup did not start: %+v", detail)
	}
	replacementSeen := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		draft, e := ai.GetDraft(ctx, actor, session.ID)
		if e != nil {
			t.Fatal(e)
		}
		if draft.Content == "备用处理中" {
			replacementSeen = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !replacementSeen {
		t.Error("backup first content was throttled")
	}
	unblock()
	var detail aiops.SessionDetail
	for deadline := time.Now().Add(5 * time.Second); ; {
		detail, err = ai.GetSession(ctx, actor, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Session.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Status != "completed" || detail.Turns[0].DraftContent != "" {
		t.Fatalf("turn incomplete: %+v", detail.Turns)
	}
	if got := detail.Messages[len(detail.Messages)-1].Content; got != "备用处理中，完成" {
		t.Fatal("merged partial replies", got)
	}
	actions, err := ai.ListActions(ctx, actor, session.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != "succeeded" {
		t.Fatal("operation replayed or failed", actions, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_notice WHERE title=$1`, title).Scan(&count); err != nil || count != 1 {
		t.Fatal("write count", count, err)
	}
	if primaryCalls.Load() != 2 || backupCalls.Load() != 1 {
		t.Fatal("unexpected relay attempts", primaryCalls.Load(), backupCalls.Load())
	}
}
