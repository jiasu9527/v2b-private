package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func assertContextToolPairs(t *testing.T, messages []providerMessage) {
	t.Helper()
	pending := map[string]bool{}
	for _, message := range messages {
		if message.Role != "tool" && len(pending) != 0 {
			t.Fatalf("tool results interrupted by %s: %v", message.Role, pending)
		}
		if message.Role == "tool" {
			if !pending[message.ToolCallID] {
				t.Fatalf("orphan or duplicate tool result %s", message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
		}
		for _, call := range message.ToolCalls {
			if pending[call.ID] {
				t.Fatalf("duplicate pending call %s", call.ID)
			}
			pending[call.ID] = true
		}
	}
	if len(pending) != 0 {
		t.Fatalf("missing tool results: %v", pending)
	}
}

func TestAIContextPostgresRecoversOversizedHistory(t *testing.T) {
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
	type observedRequest struct {
		body []byte
		req  completionRequest
		err  error
	}
	requests := make(chan observedRequest, 8)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, (512<<10)+1))
		var req completionRequest
		if readErr == nil {
			readErr = json.Unmarshal(body, &req)
		}
		requests <- observedRequest{body: body, req: req, err: readErr}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"已继续分析，未重新执行操作"},"finish_reason":"stop"}]}`))
	}))
	defer relay.Close()
	s := NewService(config.Config{AppKey: "context-test-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, session, currentTurn, actionID int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1)RETURNING id`, "context-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "context-fixture-secret", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('large context',$1,$2,1,1)RETURNING id`, DefaultModels[0], actor).Scan(&session); err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, session)
	}()
	insertTurn := func(index int, status string) int64 {
		t.Helper()
		var id int64
		err := db.QueryRowContext(ctx, `INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)VALUES($1,$2,$3,'fixture',$4,$5,1,$6)RETURNING id`, session, actor, fmt.Sprintf("context-fixture-%d-%s", index, marker), DefaultModels[0], status, time.Now().Add(time.Minute).Unix()).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertMessage := func(turn int64, message providerMessage) {
		t.Helper()
		calls, _ := json.Marshal(message.ToolCalls)
		if len(message.ToolCalls) == 0 {
			calls = []byte("[]")
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO v2_ai_message(session_id,turn_id,role,content,tool_calls,tool_call_id,created_at)VALUES($1,$2,$3,$4,$5::JSONB,$6,1)`, session, turn, message.Role, message.Content, string(calls), message.ToolCallID); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 10; index++ {
		turn := insertTurn(index, "completed")
		callID := fmt.Sprintf("context-tool-%d", index)
		insertMessage(turn, providerMessage{Role: "user", Content: fmt.Sprintf("历史查询 %d", index)})
		insertMessage(turn, providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: callID, Type: "function", Function: ToolFunction{Name: "entry_health", Arguments: `{}`}}}})
		content, _ := json.Marshal(map[string]any{"fixture": index, "result": strings.Repeat("数据", 4600)})
		insertMessage(turn, providerMessage{Role: "tool", ToolCallID: callID, Content: string(content)})
		insertMessage(turn, providerMessage{Role: "assistant", Content: fmt.Sprintf("历史分析 %d", index)})
	}
	currentTurn = insertTurn(10, "running")
	question := "CURRENT-ADMIN-REQUEST：请核对中断任务状态，不要重新执行"
	insertMessage(currentTurn, providerMessage{Role: "user", Content: question})
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,payload,snapshot_hash,request_hash,reason,status,output,error,expires_at,created_at,updated_at)VALUES($1,$2,'context-action','ssh_command','{}','snapshot','request','fixture','unknown',$3,$4,$5,1,1)RETURNING id`, session, actor, "ACTION-OUTPUT-MARKER "+strings.Repeat("中转输出", 1500), "结果未知，禁止自动重试", time.Now().Add(time.Minute).Unix()).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	digest := func() string {
		t.Helper()
		var result string
		if err := db.QueryRowContext(ctx, `SELECT md5(COALESCE((SELECT string_agg(to_jsonb(m)::TEXT,'' ORDER BY id) FROM v2_ai_message m WHERE session_id=$1),'')) || md5(COALESCE((SELECT string_agg(to_jsonb(a)::TEXT,'' ORDER BY id) FROM v2_ai_action a WHERE session_id=$1),''))`, session).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := digest()
	messages, err := s.history(ctx, session, currentTurn)
	if err != nil {
		t.Fatalf("oversized history should be recoverable: %v", err)
	}
	raw, err := json.Marshal(messages)
	if err != nil || len(raw) > 240000 || !utf8.Valid(raw) || len(messages) >= 43 {
		t.Fatalf("history not bounded or invalid: bytes=%d messages=%d err=%v", len(raw), len(messages), err)
	}
	assertContextToolPairs(t, messages)
	assertCurrent := func(messages []providerMessage) {
		t.Helper()
		found := false
		for _, message := range messages {
			if message.Role == "user" && message.Content == question {
				found = true
			}
		}
		encoded, _ := json.Marshal(messages)
		if !found || !strings.Contains(string(encoded), "ACTION-OUTPUT-MARKER") || !strings.Contains(string(encoded), `\"status\":\"unknown\"`) || !strings.Contains(string(encoded), "禁止自动重试") || !strings.Contains(string(encoded), fmt.Sprintf(`\"action_id\":%d`, actionID)) {
			t.Fatalf("current request or action truth lost (question=%v)", found)
		}
	}
	assertCurrent(messages)
	if after := digest(); before != after {
		t.Fatal("history compaction changed persisted messages or actions")
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM v2_ai_turn WHERE id=$1`, currentTurn); err != nil {
		t.Fatal(err)
	}
	var oldCount int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_message WHERE session_id=$1`, session).Scan(&oldCount); err != nil || oldCount != 40 {
		t.Fatal("fixture history count", oldCount, err)
	}
	result, err := s.SendMessage(ctx, actor, SendMessageRequest{SessionID: session, Model: DefaultModels[0], Content: question, RequestID: "context-resume-" + marker})
	if err != nil {
		t.Fatal("same session cannot resume", err)
	}
	for {
		var status, problem string
		if err = db.QueryRowContext(ctx, `SELECT status,error FROM v2_ai_turn WHERE id=$1`, result.Turn.ID).Scan(&status, &problem); err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			if status != "completed" {
				t.Fatalf("resume failed: %s %s", status, problem)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("resume did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	observed := <-requests
	if observed.err != nil || len(observed.body) > 512<<10 {
		t.Fatal("invalid actual provider request", observed.err, len(observed.body))
	}
	assertContextToolPairs(t, observed.req.Messages)
	assertCurrent(observed.req.Messages)
	var persistedCount int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_message WHERE session_id=$1`, session).Scan(&persistedCount); err != nil || persistedCount != oldCount+2 {
		t.Fatal("resume must append without deleting existing history", persistedCount, err)
	}
	var status, output string
	if err = db.QueryRowContext(ctx, `SELECT status,output FROM v2_ai_action WHERE id=$1`, actionID).Scan(&status, &output); err != nil || status != "unknown" || !strings.Contains(output, "ACTION-OUTPUT-MARKER") {
		t.Fatal("context recovery altered action", status, err)
	}
	t.Run("full access refreshes completed and uncertain action truth", func(t *testing.T) {
		ids := []int64{}
		for index := 0; index < 2; index++ {
			var id int64
			if err := db.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,payload,snapshot_hash,request_hash,reason,status,expires_at,created_at,updated_at)VALUES($1,$2,$3,'ssh_command','{}','snapshot','request','fixture','pending',$4,1,1)RETURNING id`, session, actor, fmt.Sprintf("full-context-action-%d", index), time.Now().Add(time.Minute).Unix()).Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		messages := []providerMessage{{Role: "system", Content: "FULL ACCESS POLICY MUST REMAIN"}, {Role: "user", Content: "CURRENT FULL ACCESS TASK"}}
		messages, err := s.refreshActionContext(ctx, session, messages)
		if err != nil {
			t.Fatal(err)
		}
		original, _ := json.Marshal(messages)
		if _, err := db.ExecContext(ctx, `UPDATE v2_ai_action SET status='succeeded',output='EXECUTED-SUCCESS-MARKER' WHERE id=$1`, ids[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE v2_ai_action SET status='unknown',output='PARTIAL-OUTPUT-MARKER',error='断线，结果未知，禁止自动重试' WHERE id=$1`, ids[1]); err != nil {
			t.Fatal(err)
		}
		// Results from an earlier round may leave the context; the latest action
		// snapshot must still carry the actual successful/unknown statuses.
		messages = append(messages, providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "full-context-result", Type: "function", Function: ToolFunction{Name: "propose_ssh_command", Arguments: `{}`}}}}, providerMessage{Role: "tool", ToolCallID: "full-context-result", Content: `{"action_id":1,"status":"succeeded"}`})
		storedBefore := digest()
		refreshed, err := s.refreshActionContext(ctx, session, messages)
		if err != nil {
			t.Fatal(err)
		}
		if digest() != storedBefore {
			t.Fatal("refresh changed stored action/message records")
		}
		unchanged, _ := json.Marshal(messages[:len(messages)-2])
		if string(original) != string(unchanged) {
			t.Fatal("refresh mutated prior action snapshot")
		}
		assertContextToolPairs(t, refreshed)
		foundSnapshots := 0
		for _, message := range refreshed {
			if message.contextKind != "actions" {
				continue
			}
			foundSnapshots++
			if strings.Contains(message.Content, `"status":"pending"`) || !strings.Contains(message.Content, `"status":"succeeded"`) || !strings.Contains(message.Content, `"status":"unknown"`) || !strings.Contains(message.Content, "EXECUTED-SUCCESS-MARKER") || !strings.Contains(message.Content, "PARTIAL-OUTPUT-MARKER") {
				t.Fatal("stale action states retained", message.Content)
			}
		}
		if foundSnapshots != 1 || refreshed[0].Content != "FULL ACCESS POLICY MUST REMAIN" {
			t.Fatal("duplicated snapshots or lost policy", foundSnapshots)
		}
		if _, err := s.complete(ctx, DefaultModels[0], refreshed); err != nil {
			t.Fatal(err)
		}
		observed := <-requests
		assertContextToolPairs(t, observed.req.Messages)
		raw, _ := json.Marshal(observed.req.Messages)
		if observed.err != nil || !strings.Contains(string(raw), "CURRENT FULL ACCESS TASK") || !strings.Contains(string(raw), "EXECUTED-SUCCESS-MARKER") || !strings.Contains(string(raw), "PARTIAL-OUTPUT-MARKER") {
			t.Fatal("latest execution status missing from actual provider request", observed.err)
		}
	})

	t.Run("current tool rounds fit actual serialized request", func(t *testing.T) {
		current := []providerMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: "CURRENT-TOOL-REQUEST"}}
		for round := 0; round < 2; round++ {
			assistant := providerMessage{Role: "assistant"}
			results := []providerMessage{}
			for index := 0; index < 8; index++ {
				callID := fmt.Sprintf("round-%d-tool-%d", round, index)
				args := `{"query":"` + strings.Repeat("<", 2500) + `"}`
				assistant.ToolCalls = append(assistant.ToolCalls, ToolCall{ID: callID, Type: "function", Function: ToolFunction{Name: "user_lookup", Arguments: args}})
				content, _ := json.Marshal(map[string]string{"fixture": strings.Repeat("x", 28000)})
				results = append(results, providerMessage{Role: "tool", ToolCallID: callID, Content: string(content)})
			}
			current = append(current, assistant)
			current = append(current, results...)
		}
		unbounded, _ := json.Marshal(completionRequest{Model: DefaultModels[0], Messages: current, Tools: s.toolDefinitions(), ToolChoice: "auto", Stream: true, MaxTokens: 4096})
		if len(unbounded) <= 512<<10 {
			t.Fatalf("fixture must exceed provider request cap: %d", len(unbounded))
		}
		if _, err = s.complete(ctx, DefaultModels[0], current); err != nil {
			t.Fatal("tool accumulation should be compacted before provider request", err)
		}
		observed := <-requests
		if observed.err != nil || len(observed.body) > 512<<10 || !utf8.Valid(observed.body) {
			t.Fatal("unbounded current tool request", observed.err, len(observed.body))
		}
		assertContextToolPairs(t, observed.req.Messages)
		found := false
		for _, message := range observed.req.Messages {
			found = found || message.Role == "user" && message.Content == "CURRENT-TOOL-REQUEST"
		}
		if !found {
			t.Fatal("current administrator request was dropped with tool history")
		}
	})
}
