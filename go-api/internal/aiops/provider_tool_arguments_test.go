package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func argumentTestCall(name string, size int) ToolCall {
	return ToolCall{ID: "oversized", Type: "function", Function: ToolFunction{Name: name, Arguments: `{"q":"` + strings.Repeat("x", size-8) + `"}`}}
}

func argumentTestRequest(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(completionRequest{
		Model: "model", MaxTokens: initialCompletionTokens, ReasoningEffort: "high", Stream: true, ToolChoice: "auto",
		Tools: (&Service{}).toolDefinitions(),
		Messages: []providerMessage{
			{Role: "system", Content: "Keep original constraints"},
			{Role: "user", Content: "Check the specified user's entry status"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "already-done", Type: "function", Function: ToolFunction{Name: "panel_status", Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: "already-done", Content: `{"action_id":123,"execution_state":"completed"}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func argumentTestWrite(w http.ResponseWriter, calls []ToolCall, streaming bool) {
	if !streaming {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: calls}, "finish_reason": "tool_calls"}}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, streamFrame(map[string]any{"content": "Draft from rejected attempt"}, nil))
	for i, call := range calls {
		fmt.Fprint(w, streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": call.ID, "type": call.Type, "function": call.Function}}}, nil))
	}
	fmt.Fprint(w, streamFrame(map[string]any{}, "tool_calls")+"data: [DONE]\n\n")
}

func TestProviderToolArgumentErrorBoundariesAndSafeMetadata(t *testing.T) {
	for _, name := range []string{"entry_simulate", "panel_query", "knowledge_create"} {
		limit := providerToolArgumentLimit(name)
		if err := validateCompletion(providerMessage{ToolCalls: []ToolCall{argumentTestCall(name, limit)}}); err != nil {
			t.Fatalf("boundary %s: %v", name, err)
		}
		err := validateCompletion(providerMessage{ToolCalls: []ToolCall{argumentTestCall(name, limit+1)}})
		var sizeErr *providerToolArgumentError
		if !errors.As(err, &sizeErr) || sizeErr.ToolName != name || sizeErr.Bytes != limit+1 || sizeErr.Limit != limit || canSwitchRelay(err) || canRetryRelay(err) {
			t.Fatalf("wrong typed size error: %v", err)
		}
	}
	secret := "UNTRUSTED_KEY_OR_INSTRUCTION"
	call := argumentTestCall(secret, 16001)
	call.Function.Arguments = `{"q":"` + strings.Repeat("中", 6000) + secret + `"}`
	err := validateCompletion(providerMessage{ToolCalls: []ToolCall{call}})
	var sizeErr *providerToolArgumentError
	if !errors.As(err, &sizeErr) || sizeErr.ToolName != "unknown" || sizeErr.Bytes != len(call.Function.Arguments) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "中") {
		t.Fatalf("unsafe or non-byte diagnostic: %v", err)
	}
	for _, tool := range (&Service{}).toolDefinitions() {
		if !providerDiagnosticToolNames()[tool.Function.Name] {
			t.Fatalf("registered name missing: %s", tool.Function.Name)
		}
	}
}

func TestProviderToolArgumentCorrectionPreservesHistoryAndSettings(t *testing.T) {
	payload := argumentTestRequest(t)
	original := append([]byte(nil), payload...)
	sizeErr := &providerToolArgumentError{ToolName: "entry_simulate", Bytes: 16001, Limit: 16000}
	corrected, ok := correctProviderToolArguments(payload, sizeErr)
	if !ok || !reflect.DeepEqual(payload, original) {
		t.Fatal("correction failed or mutated original payload")
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(payload, &before)
	_ = json.Unmarshal(corrected, &after)
	var beforeMessages, afterMessages []providerMessage
	_ = json.Unmarshal(before["messages"], &beforeMessages)
	_ = json.Unmarshal(after["messages"], &afterMessages)
	if len(afterMessages) != len(beforeMessages)+1 || !reflect.DeepEqual(beforeMessages, afterMessages[:len(beforeMessages)]) {
		t.Fatal("original history or completed tool results changed")
	}
	last := afterMessages[len(afterMessages)-1]
	if last.Role != "system" || len(last.ToolCalls) != 0 || last.ToolCallID != "" || !strings.Contains(last.Content, "本次回复中的全部工具均未执行") || !strings.Contains(last.Content, "禁止重新执行已完成动作") || !strings.Contains(last.Content, "分页") {
		t.Fatalf("invalid correction guidance: %+v", last)
	}
	delete(before, "messages")
	delete(after, "messages")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("request settings changed")
	}
	for _, invalid := range [][]byte{[]byte(`{}`), []byte(`{"messages":null}`), []byte(`{"messages":"bad"}`), []byte(`null`), []byte(`{`)} {
		if _, ok := correctProviderToolArguments(invalid, sizeErr); ok {
			t.Fatal("corrected an invalid/missing message history")
		}
	}
	var oversized map[string]json.RawMessage
	_ = json.Unmarshal(payload, &oversized)
	oversized["padding"], _ = json.Marshal(strings.Repeat("x", maxProviderRequestBytes))
	huge, _ := json.Marshal(oversized)
	if _, ok := correctProviderToolArguments(huge, sizeErr); ok {
		t.Fatal("correction bypassed total request budget")
	}
}

func TestProviderToolArgumentCorrectionCompleteBatchOnce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		streaming bool
		size      int
	}{
		{"json-final-limit", false, 16001},
		{"sse-final-limit", true, 16001},
		{"sse-assembly-limit", true, 32769},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload, _ := io.ReadAll(r.Body)
				requests = append(requests, payload)
				if len(requests) == 1 {
					validButDiscarded := ToolCall{ID: "must-not-execute", Type: "function", Function: ToolFunction{Name: "panel_status", Arguments: `{}`}}
					argumentTestWrite(w, []ToolCall{validButDiscarded, argumentTestCall("entry_simulate", tc.size)}, tc.streaming)
					return
				}
				argumentTestWrite(w, []ToolCall{{ID: "corrected", Type: "function", Function: ToolFunction{Name: "entry_simulate", Arguments: `{"email":"user@example.test","ua":""}`}}}, false)
			}))
			defer server.Close()
			s := failoverTestService(t)
			endpoint := failoverTestEndpoint(t, s, "primary", server.URL, "TEST_PRIVATE_KEY")
			payload := argumentTestRequest(t)
			var drafts []string
			out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), []RelayEndpoint{endpoint}, "model", payload, func(value string) error { drafts = append(drafts, value); return nil })
			if err != nil || len(requests) != 2 || len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "corrected" || s.relayCoolingDown(relayFingerprint(endpoint, "model")) {
				t.Fatalf("correction failed: err=%v requests=%d out=%+v", err, len(requests), out)
			}
			if tc.streaming && !reflect.DeepEqual(drafts, []string{"Draft from rejected attempt", ""}) {
				t.Fatalf("rejected draft not cleared: %v", drafts)
			}
			var first, second completionRequest
			_ = json.Unmarshal(requests[0], &first)
			_ = json.Unmarshal(requests[1], &second)
			if len(second.Messages) != len(first.Messages)+1 || !reflect.DeepEqual(first.Messages, second.Messages[:len(first.Messages)]) || strings.Contains(string(requests[1]), "must-not-execute") || strings.Contains(string(requests[1]), "TEST_PRIVATE_KEY") {
				t.Fatal("replayed rejected batch, lost history or leaked credentials")
			}
		})
	}
}

func TestProviderToolArgumentCorrectionStopsAfterOneAndDoesNotFailover(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			var counts [2]atomic.Int32
			servers := make([]*httptest.Server, 2)
			for i := range servers {
				servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					counts[i].Add(1)
					argumentTestWrite(w, []ToolCall{argumentTestCall("entry_simulate", 32769)}, streaming)
				}))
				defer servers[i].Close()
			}
			s := failoverTestService(t)
			endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", servers[0].URL, "key"), failoverTestEndpoint(t, s, "backup", servers[1].URL, "key")}
			out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "model", argumentTestRequest(t), nil)
			if !isProviderToolArgumentError(err) || canSwitchRelay(err) || !strings.Contains(err.Error(), "已尝试精简参数一次") || len(out.ToolCalls) != 0 || counts[0].Load() != 2 || counts[1].Load() != 0 {
				t.Fatalf("unbounded/reclassified retry: err=%v out=%+v requests=%d/%d", err, out, counts[0].Load(), counts[1].Load())
			}
			for _, endpoint := range endpoints {
				if s.relayCoolingDown(relayFingerprint(endpoint, "model")) {
					t.Fatal("argument size error cooled down a relay")
				}
			}
		})
	}
}

func TestProviderToolArgumentCorrectionBudgetSharedAcrossRelays(t *testing.T) {
	var primaryCount, backupCount atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCount.Add(1) == 1 {
			argumentTestWrite(w, []ToolCall{argumentTestCall("entry_simulate", 16001)}, false)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCount.Add(1)
		var request completionRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.Messages) != 5 || !strings.Contains(request.Messages[4].Content, "后端参数校验反馈") {
			t.Error("lost correction on genuine availability failover")
		}
		argumentTestWrite(w, []ToolCall{argumentTestCall("entry_simulate", 16001)}, false)
	}))
	defer backup.Close()
	s := failoverTestService(t)
	endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", primary.URL, "key"), failoverTestEndpoint(t, s, "backup", backup.URL, "key")}
	_, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "model", argumentTestRequest(t), nil)
	if !isProviderToolArgumentError(err) || primaryCount.Load() != 2 || backupCount.Load() != 1 || s.relayCoolingDown(relayFingerprint(endpoints[1], "model")) {
		t.Fatalf("correction budget reset across relays: %v %d/%d", err, primaryCount.Load(), backupCount.Load())
	}
}

func TestProviderToolArgumentCorrectionHonorsCancelAndDraftFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "draft-reset-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				argumentTestWrite(w, []ToolCall{argumentTestCall("entry_simulate", 32769)}, true)
			}))
			defer server.Close()
			s := failoverTestService(t)
			endpoint := failoverTestEndpoint(t, s, "primary", server.URL, "key")
			out, err := s.completeWithRelays(ctx, failoverTestConfig(), []RelayEndpoint{endpoint}, "model", argumentTestRequest(t), func(value string) error {
				if mode == "cancel" {
					cancel()
				}
				if value == "" && mode == "draft-reset-failure" {
					return errors.New("PRIVATE_DATABASE_ERROR")
				}
				return nil
			})
			if err == nil || requests.Load() != 1 || len(out.ToolCalls) != 0 || strings.Contains(err.Error(), "PRIVATE_DATABASE_ERROR") {
				t.Fatalf("retried canceled/persistence failure: %v %d %+v", err, requests.Load(), out)
			}
		})
	}
}

func TestProviderToolArgumentCorrectionBudgetSharedAcrossModels(t *testing.T) {
	s := failoverTestService(t)
	cfg := failoverTestConfig()
	cfg.Models = []string{"first", "second"}
	cfg.ReadyModels = cfg.Models
	cfg.FallbackModels = []string{"second"}
	cfg.modelEndpoints = map[string]modelEndpoint{}
	var counts [2]atomic.Int32
	for i, model := range cfg.Models {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := counts[i].Add(1)
			var request completionRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			if i == 0 && attempt > 1 {
				// Auth/config failures can switch models but are not retried
				// at the same relay, keeping the assertion deterministic.
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if i == 1 && (len(request.Messages) != 5 || !strings.Contains(request.Messages[4].Content, "后端参数校验反馈")) {
				t.Error("lost correction guidance after model failover")
			}
			argumentTestWrite(w, []ToolCall{argumentTestCall("entry_simulate", 16001)}, false)
		}))
		defer server.Close()
		endpoint := failoverTestEndpoint(t, s, model, server.URL, "key")
		cfg.modelEndpoints[model] = modelEndpoint{BaseURL: endpoint.BaseURL, APIKeyCipher: endpoint.APIKeyCipher}
	}
	var request completionRequest
	_ = json.Unmarshal(argumentTestRequest(t), &request)
	request.Model = "first"
	out, err := s.completeWithModelFailover(context.Background(), cfg, "", nil, "first", request, nil)
	if !isProviderToolArgumentError(err) || len(out.ToolCalls) != 0 || counts[0].Load() != 2 || counts[1].Load() != 1 {
		t.Fatalf("correction budget reset across models: %v %d/%d", err, counts[0].Load(), counts[1].Load())
	}
}

func TestProviderToolArgumentErrorClassificationAtRelay(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			argumentTestWrite(w, []ToolCall{argumentTestCall("entry_simulate", 32769)}, streaming)
		}))
		s := failoverTestService(t)
		endpoint := failoverTestEndpoint(t, s, "primary", server.URL, "key")
		out, err := s.completeAtRelay(context.Background(), endpoint, argumentTestRequest(t), time.Second, nil)
		server.Close()
		if !isProviderToolArgumentError(err) || canSwitchRelay(err) || len(out.ToolCalls) != 0 {
			t.Fatalf("size error escaped as relay outage: %v %+v", err, out)
		}
	}
}
