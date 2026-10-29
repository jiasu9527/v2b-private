package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Inspect the actual HTTP body, including whether strict was explicitly sent.
// Decoding only completionRequest would lose the distinction between an
// omitted field and a false default in the provider's tool declaration.
func knowledgeContractAssertWire(t *testing.T, raw []byte) {
	t.Helper()
	var request struct {
		Tools []struct {
			Function struct {
				Name       string          `json:"name"`
				Strict     json.RawMessage `json:"strict"`
				Parameters struct {
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Error("could not decode actual provider tool declaration", err)
		return
	}
	found := map[string]bool{}
	for _, tool := range request.Tools {
		if tool.Function.Name != "knowledge_save" && tool.Function.Name != "knowledge_create" {
			continue
		}
		found[tool.Function.Name] = true
		if string(tool.Function.Strict) != "false" {
			t.Errorf("%s must explicitly send strict:false, got %q", tool.Function.Name, tool.Function.Strict)
		}
		for _, required := range tool.Function.Parameters.Required {
			if required == "id" || required == "expected_version" {
				t.Errorf("%s incorrectly requires new-entry update identity %s", tool.Function.Name, required)
			}
		}
		for _, field := range []string{"id", "expected_version"} {
			_, exists := tool.Function.Parameters.Properties[field]
			if tool.Function.Name == "knowledge_create" && exists {
				t.Errorf("create-only tool must not advertise %s", field)
			}
			if tool.Function.Name == "knowledge_save" && !exists {
				t.Errorf("compatible save tool lost optional update field %s", field)
			}
		}
	}
	if !found["knowledge_save"] || !found["knowledge_create"] {
		t.Error("actual model request is missing save/create declarations", found)
	}
}

func knowledgeContractRun(t *testing.T, f staffAIFixture, actor int64, respond func(int, completionRequest) []ToolCall) SessionDetail {
	t.Helper()
	var rounds atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxProviderRequestBytes+1))
		var request completionRequest
		if err != nil || len(raw) > maxProviderRequestBytes || json.Unmarshal(raw, &request) != nil {
			t.Error("invalid provider request", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		knowledgeContractAssertWire(t, raw)
		round := int(rounds.Add(1))
		if round > 5 {
			t.Error("unexpected model loop in knowledge contract test")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls := respond(round, request)
		message := providerMessage{Role: "assistant", ToolCalls: calls}
		finish := "tool_calls"
		if len(calls) == 0 {
			message.Content, finish = "fixture knowledge contract complete", "stop"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
	}))
	defer relay.Close()
	ctx := context.Background()
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	started, err := f.s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "按当前要求记录独立知识，不要覆盖现有架构条目", RequestID: fmt.Sprintf("knowledge-contract-%d", time.Now().UnixNano()), AccessMode: "full"})
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
				t.Fatalf("knowledge contract turn failed: %+v", detail.Turns[0])
			}
			return detail
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("knowledge contract turn timed out")
	return SessionDetail{}
}

func knowledgeContractArgs(title, content string) map[string]any {
	return map[string]any{"title": title, "summary": "fixture independently verified fact", "content": content, "category": "architecture", "source_note": "fixture current operator request"}
}

func TestAIKnowledgeContractPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM v2_ai_knowledge WHERE created_by IN($1,$2,$3,$4)`, f.admin, f.staff, f.other, f.user)
	})
	const originalBody = "CONTRACT_ORIGINAL_ARCHITECTURE_BODY_MUST_NEVER_CHANGE"
	architecture, err := f.s.SaveKnowledge(ctx, f.admin, KnowledgeSaveRequest{Title: "CONTRACT_ARCHITECTURE_V5", Summary: "original architecture", Content: originalBody, Category: "architecture", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for architecture.Version < 5 {
		edit := knowledgeFlowEdit(architecture)
		edit.Summary = fmt.Sprintf("original architecture revision %d", architecture.Version+1)
		architecture, err = f.s.SaveKnowledge(ctx, f.admin, edit)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertOriginal := func(t *testing.T) {
		t.Helper()
		current, err := f.s.GetKnowledge(ctx, f.admin, architecture.ID)
		if err != nil || current.Version != 5 || current.Content != originalBody || current.Title != architecture.Title {
			t.Fatalf("unrelated version-five architecture changed: %+v err=%v", current, err)
		}
	}

	for _, tool := range []string{"knowledge_save", "knowledge_create"} {
		for _, role := range []string{"admin", "staff"} {
			t.Run(tool+" creates with entirely omitted identity for "+role, func(t *testing.T) {
				actor, status := f.admin, "active"
				if role == "staff" {
					actor, status = f.staff, "draft"
				}
				title := "CONTRACT_CREATE_" + tool + "_" + role
				var savedID int64
				detail := knowledgeContractRun(t, f, actor, func(round int, request completionRequest) []ToolCall {
					if round == 1 {
						// No id/expected_version keys at all, including no null/zero.
						return []ToolCall{knowledgeFlowCall("create-without-identity", tool, knowledgeContractArgs(title, "CONTRACT_SEPARATE_NEW_FACT"))}
					}
					result := knowledgeFlowResult(t, request, "create-without-identity")
					id, _ := result["id"].(float64)
					savedID = int64(id)
					if savedID <= 0 || savedID == architecture.ID || result["version"] != float64(1) || result["status"] != status {
						t.Error("omitted identity did not create a new version-one entry", result)
					}
					return nil
				})
				created, err := f.s.GetKnowledge(ctx, f.admin, savedID)
				if err != nil || created.Title != title || created.Version != 1 || created.Status != status || created.CreatedBy != actor || created.SourceSessionID != detail.Session.ID || created.SourceTurnID != detail.Turns[0].ID {
					t.Fatal("new knowledge not persisted independently", created, err)
				}
				assertOriginal(t)
			})
		}
	}

	t.Run("partial zero and null identities are rejected instead of becoming create", func(t *testing.T) {
		cases := []struct {
			name, tool string
			fields     map[string]any
		}{
			{"id-only", "knowledge_save", map[string]any{"id": architecture.ID}},
			{"version-only", "knowledge_save", map[string]any{"expected_version": 1}},
			{"zero-id-only", "knowledge_save", map[string]any{"id": 0}},
			{"zero-version-only", "knowledge_save", map[string]any{"expected_version": 0}},
			{"both-zero", "knowledge_save", map[string]any{"id": 0, "expected_version": 0}},
			{"zero-id-positive-version", "knowledge_save", map[string]any{"id": 0, "expected_version": 1}},
			{"valid-id-zero-version", "knowledge_save", map[string]any{"id": architecture.ID, "expected_version": 0}},
			{"null-id-only", "knowledge_save", map[string]any{"id": nil}},
			{"null-version-only", "knowledge_save", map[string]any{"expected_version": nil}},
			{"both-null", "knowledge_save", map[string]any{"id": nil, "expected_version": nil}},
			{"null-id-positive-version", "knowledge_save", map[string]any{"id": nil, "expected_version": 1}},
			{"valid-id-null-version", "knowledge_save", map[string]any{"id": architecture.ID, "expected_version": nil}},
			{"create-id-prohibited", "knowledge_create", map[string]any{"id": architecture.ID}},
			{"create-version-prohibited", "knowledge_create", map[string]any{"expected_version": 5}},
			{"create-pair-prohibited", "knowledge_create", map[string]any{"id": architecture.ID, "expected_version": 5}},
		}
		var before int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_knowledge`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		knowledgeContractRun(t, f, f.admin, func(round int, request completionRequest) []ToolCall {
			if round == 1 {
				calls := make([]ToolCall, 0, len(cases))
				for _, tc := range cases {
					args := knowledgeContractArgs("CONTRACT_INVALID_"+tc.name, "MUST_NOT_SAVE_INVALID_IDENTITY")
					for key, value := range tc.fields {
						args[key] = value
					}
					calls = append(calls, knowledgeFlowCall(tc.name, tc.tool, args))
				}
				return calls
			}
			for _, tc := range cases {
				result := knowledgeFlowResult(t, request, tc.name)
				if result["status"] != "error" || (result["code"] != "invalid_arguments" && result["code"] != "knowledge_write_identity") {
					t.Error("invalid identity accepted or misclassified", tc.name, result)
				}
			}
			return nil
		})
		var after int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_knowledge`).Scan(&after); err != nil || after != before {
			t.Fatal("invalid identity unexpectedly created knowledge", before, after, err)
		}
		assertOriginal(t)
	})

	t.Run("old architecture version conflicts and explicit CAS update remains usable", func(t *testing.T) {
		target, err := f.s.SaveKnowledge(ctx, f.admin, KnowledgeSaveRequest{Title: "CONTRACT_EDIT_TARGET", Summary: "fixture separate target", Content: "CONTRACT_EDIT_BEFORE", Category: "network", Status: "active"})
		if err != nil {
			t.Fatal(err)
		}
		knowledgeContractRun(t, f, f.admin, func(round int, request completionRequest) []ToolCall {
			switch round {
			case 1:
				stale := knowledgeContractArgs("CONTRACT_ARCHITECTURE_STALE_OVERWRITE", "MUST_NOT_OVERWRITE_ARCHITECTURE")
				stale["id"], stale["expected_version"] = architecture.ID, 1
				update := knowledgeContractArgs(target.Title, "CONTRACT_EDIT_AFTER")
				update["id"], update["expected_version"] = target.ID, target.Version
				return []ToolCall{knowledgeFlowCall("stale-architecture", "knowledge_save", stale), knowledgeFlowCall("valid-update", "knowledge_save", update)}
			case 2:
				stale := knowledgeFlowResult(t, request, "stale-architecture")
				if stale["status"] != "error" || stale["code"] != "knowledge_version_conflict" {
					t.Error("stale architecture version not rejected", stale)
				}
				updated := knowledgeFlowResult(t, request, "valid-update")
				if updated["id"] != float64(target.ID) || updated["version"] != float64(2) || updated["status"] != "active" {
					t.Error("legitimate pair could not update CAS", updated)
				}
				args := knowledgeContractArgs(target.Title, "MUST_NOT_OVERWRITE_NEW_VERSION")
				args["id"], args["expected_version"] = target.ID, 1
				return []ToolCall{knowledgeFlowCall("stale-target", "knowledge_save", args)}
			case 3:
				if stale := knowledgeFlowResult(t, request, "stale-target"); stale["status"] != "error" || stale["code"] != "knowledge_version_conflict" {
					t.Error("stale target version not rejected", stale)
				}
				return []ToolCall{knowledgeFlowCall("read-updated", "knowledge_get", map[string]any{"id": target.ID})}
			case 4:
				current := knowledgeFlowResult(t, request, "read-updated")
				if current["version"] != float64(2) || current["content"] != "CONTRACT_EDIT_AFTER" {
					t.Error("CAS conflict changed latest knowledge", current)
				}
			}
			return nil
		})
		current, err := f.s.GetKnowledge(ctx, f.admin, target.ID)
		if err != nil || current.Version != 2 || current.Content != "CONTRACT_EDIT_AFTER" {
			t.Fatal("persisted CAS result wrong", current, err)
		}
		assertOriginal(t)
	})

	t.Run("wrong update retry is blocked and switches to independent create", func(t *testing.T) {
		var createdID int64
		args := knowledgeContractArgs("CONTRACT_MISTAKEN_UPDATE", "CONTRACT_MISTAKEN_CONTENT")
		args["id"], args["expected_version"] = architecture.ID, 1
		knowledgeContractRun(t, f, f.admin, func(round int, request completionRequest) []ToolCall {
			switch round {
			case 1:
				return []ToolCall{knowledgeFlowCall("wrong-update", "knowledge_save", args)}
			case 2:
				result := knowledgeFlowResult(t, request, "wrong-update")
				if result["status"] != "error" || result["code"] != "knowledge_version_conflict" {
					t.Error("original stale update did not produce version conflict", result)
				}
				return []ToolCall{knowledgeFlowCall("repeat-wrong-update", "knowledge_save", args)}
			case 3:
				result := knowledgeFlowResult(t, request, "repeat-wrong-update")
				if result["status"] != "error" || result["code"] != "knowledge_conflict_repeated" {
					t.Error("same failed update was submitted again", result)
				}
				return []ToolCall{knowledgeFlowCall("correct-new-entry", "knowledge_create", knowledgeContractArgs("CONTRACT_CORRECT_NEW_ENTRY", "CONTRACT_DISTINCT_FACT"))}
			case 4:
				result := knowledgeFlowResult(t, request, "correct-new-entry")
				id, _ := result["id"].(float64)
				createdID = int64(id)
				if createdID <= 0 || createdID == architecture.ID || result["version"] != float64(1) || result["status"] != "active" {
					t.Error("recovery did not independently create the requested entry", result)
				}
			}
			return nil
		})
		created, err := f.s.GetKnowledge(ctx, f.admin, createdID)
		if err != nil || created.Content != "CONTRACT_DISTINCT_FACT" || created.Version != 1 {
			t.Fatal("new entry recovery missing", created, err)
		}
		assertOriginal(t)
	})
}
