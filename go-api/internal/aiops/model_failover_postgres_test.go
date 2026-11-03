package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelFailoverPostgresContinuesWithoutReplayingMutation(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	var primary, backup atomic.Int32
	title := fmt.Sprintf("MODEL_FAILOVER_%d", time.Now().UnixNano())
	t.Cleanup(func() { f.db.Exec(`DELETE FROM v2_ai_knowledge WHERE title=$1`, title) })
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primary.Add(1) > 1 {
			w.WriteHeader(401)
			return
		}
		call := knowledgeFlowCall("write-once", "knowledge_create", map[string]any{"title": title, "summary": "verified test", "content": "verified test fact", "category": "server", "source_note": "local fixture"})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: []ToolCall{call}}, "finish_reason": "tool_calls"}}})
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := backup.Add(1)
		var req completionRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != DefaultModels[1] || req.ReasoningEffort != "" {
			t.Error("wrong backup request")
		}
		if result := knowledgeFlowResult(t, req, "write-once"); result == nil {
			t.Error("missing prior write result")
		}
		if n == 1 {
			call := knowledgeFlowCall("read-after-switch", "knowledge_search", map[string]any{"query": title})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: []ToolCall{call}}, "finish_reason": "tool_calls"}}})
			return
		}
		knowledgeFlowResult(t, req, "read-after-switch")
		fmt.Fprint(w, failoverTestReply)
	}))
	defer second.Close()
	enabled := true
	models := []string{DefaultModels[1]}
	endpoints := map[string]ModelEndpointSaveRequest{
		DefaultModels[0]: {BaseURL: first.URL + "/v1", APIKey: "first-key"},
		DefaultModels[1]: {BaseURL: second.URL + "/v1", APIKey: "second-key"},
	}
	req := ConfigSaveRequest{Enabled: true, Models: DefaultModels, BaseURL: "https://fixture.example.test/v1", FailoverEnabled: &enabled, FallbackModels: &models, ModelEndpoints: &endpoints}
	if _, err := f.s.SaveConfig(ctx, f.admin, req); err != nil {
		t.Fatal(err)
	}
	cfg, err := f.s.GetConfig(ctx, f.admin)
	if err != nil || !reflect.DeepEqual(cfg.FallbackModels, models) {
		t.Fatal("config round trip", err)
	}
	req.FallbackModels = nil
	if _, err = f.s.SaveConfig(ctx, f.admin, req); err != nil {
		t.Fatal("preserve", err)
	}
	started, err := f.s.SendMessage(ctx, f.admin, SendMessageRequest{Model: DefaultModels[0], Content: "保存已经验证的运维知识", RequestID: title, AccessMode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	turn := continuousWait(t, f, started.Turn.ID)
	var actualModel string
	f.db.QueryRow(`SELECT model FROM v2_ai_turn WHERE id=$1`, turn.ID).Scan(&actualModel)
	if turn.Status != "completed" || actualModel != DefaultModels[1] || primary.Load() != 2 || backup.Load() != 2 {
		t.Fatalf("turn %+v requests %d/%d", turn, primary.Load(), backup.Load())
	}
	var count int
	f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_knowledge WHERE title=$1`, title).Scan(&count)
	if count != 1 {
		t.Fatalf("mutation executed %d times", count)
	}
	detail, err := f.s.GetSession(ctx, f.admin, started.Session.ID)
	if err != nil || detail.Session.Model != DefaultModels[0] {
		t.Fatal("changed user selected model", err)
	}
	empty := []string{}
	req.FallbackModels = &empty
	if _, err = f.s.SaveConfig(ctx, f.admin, req); err != nil {
		t.Fatal(err)
	}
	cfg, err = f.s.GetConfig(ctx, f.admin)
	if err != nil || len(cfg.FallbackModels) != 0 {
		t.Fatal("clear", err)
	}
}
