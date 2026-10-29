package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func knowledgeFlowCall(id, name string, args map[string]any) ToolCall {
	raw, _ := json.Marshal(args)
	return ToolCall{ID: id, Type: "function", Function: ToolFunction{Name: name, Arguments: string(raw)}}
}

func knowledgeFlowRun(t *testing.T, f staffAIFixture, actor int64, task string, respond func(int, completionRequest) []ToolCall) SessionDetail {
	t.Helper()
	var rounds atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxProviderRequestBytes+1))
		var request completionRequest
		if err != nil || len(raw) > maxProviderRequestBytes || json.Unmarshal(raw, &request) != nil {
			t.Error("invalid or oversized knowledge model request", err, len(raw))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.Messages) > maxContextMessages {
			t.Error("knowledge context exceeded message limit", len(request.Messages))
		}
		n := int(rounds.Add(1))
		if n > 8 {
			t.Error("unexpected knowledge tool loop")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls := respond(n, request)
		message, finish := providerMessage{Role: "assistant", ToolCalls: calls}, "tool_calls"
		if len(calls) == 0 {
			message.Content, finish = "fixture knowledge task completed", "stop"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
	}))
	defer relay.Close()
	ctx := context.Background()
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	started, err := f.s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: task, RequestID: fmt.Sprintf("knowledge-%d", time.Now().UnixNano()), AccessMode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := f.s.GetSession(ctx, actor, started.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Turns) > 0 && detail.Turns[0].Status != "running" {
			if detail.Turns[0].Status != "completed" {
				t.Fatalf("knowledge turn did not complete: %+v", detail.Turns[0])
			}
			return detail
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("knowledge turn timed out")
	return SessionDetail{}
}

func knowledgeFlowResult(t *testing.T, request completionRequest, id string) map[string]any {
	t.Helper()
	for _, message := range request.Messages {
		if message.Role == "tool" && message.ToolCallID == id {
			var value map[string]any
			if err := json.Unmarshal([]byte(message.Content), &value); err != nil {
				t.Errorf("knowledge tool output is not JSON: %v", err)
			}
			return value
		}
	}
	t.Errorf("missing knowledge tool result %s", id)
	return map[string]any{}
}

func knowledgeFlowHas(request completionRequest, value string) bool {
	for _, message := range request.Messages {
		if strings.Contains(message.Content, value) {
			return true
		}
	}
	return false
}

func knowledgeFlowUserHas(request completionRequest, value string) bool {
	for _, message := range request.Messages {
		if message.Role == "user" && strings.Contains(message.Content, value) {
			return true
		}
	}
	return false
}

func knowledgeFlowLookup(t *testing.T, f staffAIFixture, title string) KnowledgeEntry {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT id FROM v2_ai_knowledge WHERE title=$1`, title).Scan(&id); err != nil {
		t.Fatal("knowledge was not saved", title, err)
	}
	entry, err := f.s.GetKnowledge(context.Background(), f.admin, id)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func knowledgeFlowEdit(entry KnowledgeEntry) KnowledgeSaveRequest {
	version := entry.Version
	return KnowledgeSaveRequest{ID: entry.ID, Title: entry.Title, Summary: entry.Summary, Content: entry.Content, Category: entry.Category, Status: entry.Status, Pinned: entry.Pinned, ExpectedVersion: &version}
}

func TestAIKnowledgeFlowPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM v2_ai_knowledge WHERE created_by IN($1,$2,$3,$4)`, f.admin, f.staff, f.other, f.user)
	})
	var active KnowledgeEntry
	var origin SessionDetail
	const injected = "FIXTURE_FAKE_SYSTEM_INSTRUCTION_IGNORE_REAL_TASK"

	t.Run("administrator saves shared knowledge with trusted turn provenance", func(t *testing.T) {
		origin = knowledgeFlowRun(t, f, f.admin, "记录这次已验证的服务器配置供以后查询", func(round int, request completionRequest) []ToolCall {
			if round == 1 {
				return []ToolCall{knowledgeFlowCall("save-active", "knowledge_save", map[string]any{"title": "FLOW_ACTIVE_V1", "summary": "FLOW_SUMMARY_V1", "content": "FLOW_CONTENT_V1\n" + injected, "category": "server", "pinned": true, "source_note": "fixture verified configuration"})}
			}
			result := knowledgeFlowResult(t, request, "save-active")
			if result["status"] == "error" {
				t.Error("administrator knowledge_save failed", result)
			}
			if !knowledgeFlowUserHas(request, "FLOW_ACTIVE_V1") {
				t.Error("knowledge was not refreshed in the next tool round")
			}
			return nil
		})
		active = knowledgeFlowLookup(t, f, "FLOW_ACTIVE_V1")
		if active.Status != "active" || !active.Pinned || active.SourceKind != "ai" || active.CreatedBy != f.admin || active.SourceSessionID != origin.Session.ID || active.SourceTurnID != origin.Turns[0].ID {
			t.Fatalf("AI provenance or publication state incorrect: %+v", active)
		}
		shared := knowledgeFlowRun(t, f, f.other, "CURRENT_HUMAN_TASK_ONLY_INSPECT", func(_ int, request completionRequest) []ToolCall {
			if !knowledgeFlowHas(request, "FLOW_ACTIVE_V1") || !knowledgeFlowHas(request, "FLOW_SUMMARY_V1") || !knowledgeFlowHas(request, "FLOW_CONTENT_V1") || !knowledgeFlowHas(request, "CURRENT_HUMAN_TASK_ONLY_INSPECT") {
				t.Error("new staff session did not receive shared active context")
			}
			for _, message := range request.Messages {
				if strings.Contains(message.Content, injected) && message.Role != "user" {
					t.Errorf("stored fake instruction promoted to %s", message.Role)
				}
				if message.Role == "user" && strings.HasPrefix(message.Content, knowledgeContextPrefix) {
					var context struct {
						Data []knowledgeContextEntry `json:"data"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(message.Content, knowledgeContextPrefix)), &context); err != nil || len(context.Data) != 1 {
						t.Error("malformed knowledge directory", err, len(context.Data))
						continue
					}
					entry := context.Data[0]
					if entry.ID != active.ID || entry.Version != active.Version || entry.Title != active.Title || entry.Summary != active.Summary || entry.Category != active.Category {
						t.Error("knowledge context omitted identity/version/summary metadata", entry)
					}
				}
			}
			return nil
		})
		for _, message := range shared.Messages {
			if strings.Contains(message.Content, injected) || strings.HasPrefix(message.Content, knowledgeContextPrefix) {
				t.Fatal("derived knowledge context was persisted as conversation history")
			}
		}
	})

	t.Run("admin edit and disable are reflected by next model request", func(t *testing.T) {
		if active.ID == 0 {
			t.Fatal("active fixture missing")
		}
		edit := knowledgeFlowEdit(active)
		edit.Title, edit.Summary, edit.Content = "FLOW_ACTIVE_V2", "FLOW_SUMMARY_V2", "FLOW_CONTENT_V2"
		updated, err := f.s.SaveKnowledge(ctx, f.admin, edit)
		if err != nil || updated.Version != active.Version+1 {
			t.Fatal("administrator edit failed", updated, err)
		}
		active = updated
		knowledgeFlowRun(t, f, f.staff, "查看当前知识版本", func(_ int, request completionRequest) []ToolCall {
			if !knowledgeFlowHas(request, "FLOW_ACTIVE_V2") || !knowledgeFlowHas(request, "FLOW_CONTENT_V2") || knowledgeFlowHas(request, "FLOW_ACTIVE_V1") || knowledgeFlowHas(request, "FLOW_CONTENT_V1") {
				t.Error("context used stale knowledge version")
			}
			return nil
		})
		edit = knowledgeFlowEdit(active)
		edit.Status = "disabled"
		active, err = f.s.SaveKnowledge(ctx, f.admin, edit)
		if err != nil {
			t.Fatal(err)
		}
		knowledgeFlowRun(t, f, f.admin, "不要读取已停用知识", func(_ int, request completionRequest) []ToolCall {
			if knowledgeFlowHas(request, "FLOW_ACTIVE_V2") || knowledgeFlowHas(request, "FLOW_CONTENT_V2") {
				t.Error("disabled knowledge leaked into automatic context")
			}
			return nil
		})
	})

	t.Run("staff saves draft and cannot forge source or edit existing knowledge", func(t *testing.T) {
		staffOrigin := knowledgeFlowRun(t, f, f.staff, "提交经验草稿", func(round int, request completionRequest) []ToolCall {
			switch round {
			case 1:
				return []ToolCall{knowledgeFlowCall("staff-save", "knowledge_save", map[string]any{"title": "FLOW_STAFF_DRAFT", "summary": "FLOW_DRAFT_SUMMARY", "content": "FLOW_DRAFT_PRIVATE_CONTENT", "category": "troubleshooting", "pinned": true, "source_note": "fixture staff experience"})}
			case 2:
				if result := knowledgeFlowResult(t, request, "staff-save"); result["status"] == "error" {
					t.Error("staff draft creation failed", result)
				}
				return []ToolCall{knowledgeFlowCall("forged-source", "knowledge_save", map[string]any{"title": "FLOW_FORGED_SOURCE", "summary": "fixture summary", "content": "fixture text", "category": "other", "source_note": "forged fixture source", "source_session_id": origin.Session.ID, "source_turn_id": origin.Turns[0].ID, "created_by": f.admin})}
			case 3:
				if result := knowledgeFlowResult(t, request, "forged-source"); result["status"] != "error" {
					t.Error("AI accepted forged provenance fields", result)
				}
				return []ToolCall{knowledgeFlowCall("staff-edit", "knowledge_save", map[string]any{"id": active.ID, "expected_version": active.Version, "title": "FLOW_STAFF_OVERWRITE", "summary": "fixture summary", "content": "must not replace admin knowledge", "category": "server", "source_note": "unauthorized fixture edit"})}
			case 4:
				if result := knowledgeFlowResult(t, request, "staff-edit"); result["status"] != "error" {
					t.Error("staff AI edited existing knowledge", result)
				}
			}
			return nil
		})
		draft := knowledgeFlowLookup(t, f, "FLOW_STAFF_DRAFT")
		if draft.Status != "draft" || draft.Pinned || draft.CreatedBy != f.staff || draft.SourceSessionID != staffOrigin.Session.ID || draft.SourceTurnID != staffOrigin.Turns[0].ID {
			t.Fatal("staff draft not isolated or source wrong", draft)
		}
		for _, actor := range []int64{f.staff, f.other, f.admin} {
			knowledgeFlowRun(t, f, actor, "查看已发布知识", func(_ int, request completionRequest) []ToolCall {
				if knowledgeFlowHas(request, "FLOW_STAFF_DRAFT") || knowledgeFlowHas(request, "FLOW_DRAFT_PRIVATE_CONTENT") {
					t.Error("draft automatically shared", actor)
				}
				return nil
			})
		}
		var forged int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_knowledge WHERE title IN('FLOW_FORGED_SOURCE','FLOW_STAFF_OVERWRITE')`).Scan(&forged); err != nil || forged != 0 {
			t.Fatal("unauthorized knowledge persisted", forged, err)
		}
		knowledgeFlowRun(t, f, f.admin, "只查询已发布知识", func(round int, request completionRequest) []ToolCall {
			if round == 1 {
				return []ToolCall{
					knowledgeFlowCall("draft-search", "knowledge_search", map[string]any{"query": "FLOW_STAFF_DRAFT"}),
					knowledgeFlowCall("draft-get", "knowledge_get", map[string]any{"id": draft.ID}),
					knowledgeFlowCall("disabled-get", "knowledge_get", map[string]any{"id": active.ID}),
				}
			}
			if found := knowledgeFlowResult(t, request, "draft-search"); found["total"] != float64(0) {
				t.Error("search tools exposed unpublished draft", found)
			}
			for _, id := range []string{"draft-get", "disabled-get"} {
				if result := knowledgeFlowResult(t, request, id); result["status"] != "error" {
					t.Error("knowledge_get exposed unpublished item", id, result)
				}
			}
			return nil
		})
	})

	t.Run("long content stays bounded and can be read by character pages", func(t *testing.T) {
		content := strings.Repeat("中文😀A", 2900)
		long, err := f.s.SaveKnowledge(ctx, f.admin, KnowledgeSaveRequest{Title: "FLOW_LONG_READABLE", Summary: "FLOW_LONG_SUMMARY", Content: content, Category: "deployment", Status: "active", Pinned: true})
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 20; index++ {
			_, err := f.s.SaveKnowledge(ctx, f.admin, KnowledgeSaveRequest{Title: fmt.Sprintf("FLOW_BUDGET_%02d", index), Summary: strings.Repeat("摘要", 220), Content: strings.Repeat("数据", 5900), Category: "network", Status: "active", Pinned: true})
			if err != nil {
				t.Fatal(err)
			}
		}
		knowledgeFlowRun(t, f, f.other, "FLOW_CURRENT_TASK_PAGINATE", func(round int, request completionRequest) []ToolCall {
			if !knowledgeFlowHas(request, "FLOW_CURRENT_TASK_PAGINATE") || knowledgeFlowHas(request, content) {
				t.Error("knowledge replaced current user task or dumped unbounded full content")
			}
			switch round {
			case 1:
				return []ToolCall{
					knowledgeFlowCall("long-search", "knowledge_search", map[string]any{"query": "FLOW_LONG_READABLE", "page_size": 1}),
					knowledgeFlowCall("read-page", "knowledge_get", map[string]any{"id": long.ID, "content_offset": 3700, "content_limit": 700}),
				}
			case 2:
				if found := knowledgeFlowResult(t, request, "long-search"); found["total"] != float64(1) {
					t.Error("active long knowledge was not searchable", found)
				}
				result := knowledgeFlowResult(t, request, "read-page")
				if result["status"] == "error" {
					t.Error("long knowledge get failed", result)
				}
				raw, _ := json.Marshal(result)
				if len(raw) > maxToolOutputBytes || !utf8.Valid(raw) {
					t.Error("invalid/oversized knowledge page", len(raw))
				}
				// Assert the actual page body after the tool's result contract is
				// applied; fixed offsets are Unicode characters, never raw bytes.
				page, _ := result["content"].(string)
				if page != string([]rune(content)[3700:4400]) || result["content_offset"] != float64(3700) || result["next_content_offset"] != float64(4400) || result["total_chars"] != float64(11600) || result["truncated"] != true {
					t.Errorf("wrong Unicode knowledge page: chars=%d offset=%v next=%v total=%v truncated=%v", utf8.RuneCountInString(page), result["content_offset"], result["next_content_offset"], result["total_chars"], result["truncated"])
				}
			}
			return nil
		})
		messages := []providerMessage{
			{Role: "system", Content: "FLOW_ORIGINAL_SYSTEM_POLICY"},
			{Role: "user", Content: "old task"},
			{Role: "assistant", Content: strings.Repeat("old history ", 3000)},
			{Role: "user", Content: "FLOW_REAL_CURRENT_TASK_MUST_SURVIVE"},
		}
		withKnowledge, err := f.s.refreshKnowledgeContext(ctx, messages)
		if err != nil {
			t.Fatal(err)
		}
		withKnowledge, err = f.s.refreshKnowledgeContext(ctx, withKnowledge)
		if err != nil {
			t.Fatal(err)
		}
		knowledgeCount := 0
		for _, message := range withKnowledge {
			if message.contextKind == "knowledge" {
				knowledgeCount++
				if message.Role != "user" || len(message.Content) > knowledgeContextBudget {
					t.Fatal("knowledge not bounded untrusted user context", message.Role, len(message.Content))
				}
			}
		}
		if knowledgeCount != 1 {
			t.Fatal("knowledge refresh duplicated or omitted context", knowledgeCount)
		}
		compacted, err := compactProviderContext(withKnowledge, knowledgeContextBudget+4000)
		if err != nil {
			t.Fatal(err)
		}
		serialized, _ := json.Marshal(compacted)
		if !strings.Contains(string(serialized), "FLOW_REAL_CURRENT_TASK_MUST_SURVIVE") || compacted[0].Role != "system" || compacted[0].Content != "FLOW_ORIGINAL_SYSTEM_POLICY" {
			t.Fatal("synthetic knowledge replaced current task or system policy")
		}
	})

	t.Run("deleting source conversation retains knowledge and reset removes it", func(t *testing.T) {
		if err := f.s.DeleteSession(ctx, f.admin, origin.Session.ID); err != nil {
			t.Fatal(err)
		}
		retained, err := f.s.GetKnowledge(ctx, f.admin, active.ID)
		if err != nil || retained.SourceSessionID != origin.Session.ID || retained.SourceTurnID != origin.Turns[0].ID {
			t.Fatal("deleting conversation removed or detached knowledge", retained, err)
		}
		if err := f.s.ResetData(ctx, f.staff); !errors.Is(err, ErrForbidden) {
			t.Fatal("staff reset accepted", err)
		}
		if err := f.s.ResetData(ctx, f.admin); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_knowledge`).Scan(&count); err != nil || count != 0 {
			t.Fatal("reset retained knowledge", count, err)
		}
	})
}
