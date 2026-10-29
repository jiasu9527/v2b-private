package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func streamCompatDelta(index any, id, name, arguments string) map[string]any {
	delta := map[string]any{"function": map[string]any{"name": name, "arguments": arguments}}
	if index != nil {
		delta["index"] = index
	}
	if id != "" {
		delta["id"], delta["type"] = id, "function"
	}
	return delta
}

func streamCompatBody(frames ...[]any) string {
	var body strings.Builder
	for _, calls := range frames {
		body.WriteString(streamFrame(map[string]any{"tool_calls": calls}, nil))
	}
	body.WriteString(streamFrame(map[string]any{}, "tool_calls"))
	body.WriteString("data: [DONE]\n\n")
	return body.String()
}

func TestCompletionStreamCompatibleSingleToolIndex(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index any
	}{
		{"explicit_nine", 9},
		{"starts_at_one", 1},
		{"missing_index_single", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := streamCompatBody([]any{streamCompatDelta(tc.index, "fixture-call", "groups_list", `{}`)})
			out, err := readCompletionStream(context.Background(), strings.NewReader(body), nil)
			if err != nil || len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "fixture-call" || out.ToolCalls[0].Function.Name != "groups_list" || out.ToolCalls[0].Function.Arguments != `{}` {
				t.Fatalf("valid compatible index rejected: %+v err=%v", out, err)
			}
		})
	}
}

func TestCompletionStreamCompatibleSparseIndexesOrdered(t *testing.T) {
	body := streamCompatBody(
		[]any{streamCompatDelta(90, "call-90", "groups_", `{`), streamCompatDelta(1, "call-1", "plans_", `{`), streamCompatDelta(9, "call-9", "servers_", `{`)},
		[]any{streamCompatDelta(9, "", "list", `}`), streamCompatDelta(90, "", "list", `}`), streamCompatDelta(1, "", "list", `}`)},
	)
	out, err := readCompletionStream(context.Background(), strings.NewReader(body), nil)
	if err != nil || len(out.ToolCalls) != 3 {
		t.Fatalf("sparse indexes rejected: %+v %v", out, err)
	}
	for position, expected := range []struct {
		index int
		name  string
	}{{1, "plans_list"}, {9, "servers_list"}, {90, "groups_list"}} {
		call := out.ToolCalls[position]
		if call.ID != fmt.Sprintf("call-%d", expected.index) || call.Function.Name != expected.name || call.Function.Arguments != `{}` {
			t.Fatalf("index ordering/assembly changed: %+v", out.ToolCalls)
		}
	}
}

func TestCompletionStreamCompatibleMissingIndexAssociation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		frames    [][]any
		ids       []string
		arguments []string
	}{
		{
			name:   "single anonymous continuation",
			frames: [][]any{{streamCompatDelta(nil, "single", "groups_list", `{`)}, {streamCompatDelta(nil, "", "", `}`)}},
			ids:    []string{"single"}, arguments: []string{`{}`},
		},
		{
			name: "explicit then missing index with exact ID",
			frames: [][]any{
				{streamCompatDelta(21, "first", "groups_list", `{"limit":`), streamCompatDelta(88, "second", "groups_list", `{"limit":`)},
				{streamCompatDelta(nil, "second", "", `2}`), streamCompatDelta(nil, "first", "", `1}`)},
			},
			ids: []string{"first", "second"}, arguments: []string{`{"limit":1}`, `{"limit":2}`},
		},
		{
			name: "multiple omitted indexes with distinct IDs",
			frames: [][]any{
				{streamCompatDelta(nil, "first", "groups_list", `{"limit":`), streamCompatDelta(nil, "second", "groups_list", `{"limit":`)},
				{streamCompatDelta(nil, "second", "", `2}`), streamCompatDelta(nil, "first", "", `1}`)},
			},
			ids: []string{"first", "second"}, arguments: []string{`{"limit":1}`, `{"limit":2}`},
		},
		{
			name:   "single explicit index with omitted index continuation",
			frames: [][]any{{streamCompatDelta(999, "single", "groups_list", `{`)}, {streamCompatDelta(nil, "", "", `}`)}},
			ids:    []string{"single"}, arguments: []string{`{}`},
		},
		{
			name: "repeated full metadata and argument fragments",
			frames: [][]any{
				{streamCompatDelta(9, "single", "groups_list", `{"query":"`)},
				{streamCompatDelta(9, "single", "groups_list", `same`)},
				{streamCompatDelta(9, "single", "groups_list", `same`)},
				{streamCompatDelta(9, "single", "groups_list", `"}`)},
			},
			ids: []string{"single"}, arguments: []string{`{"query":"samesame"}`},
		},
		{
			name: "similar prefix ID fragments are appended verbatim",
			frames: [][]any{
				{streamCompatDelta(9, "a", "groups_", `{`)},
				{streamCompatDelta(9, "ab", "list", `}`)},
			},
			ids: []string{"aab"}, arguments: []string{`{}`},
		},
		{
			name: "mixed indexed and unindexed preserve first appearance",
			frames: [][]any{
				{streamCompatDelta(90, "first", "groups_list", `{`), streamCompatDelta(nil, "second", "groups_list", `{`), streamCompatDelta(1, "third", "groups_list", `{`)},
				{streamCompatDelta(nil, "third", "", `}`), streamCompatDelta(nil, "second", "", `}`), streamCompatDelta(nil, "first", "", `}`)},
			},
			ids: []string{"first", "second", "third"}, arguments: []string{`{}`, `{}`, `{}`},
		},
		{
			name: "omitted first index binds later by exact ID",
			frames: [][]any{
				{streamCompatDelta(nil, "late-index", "groups_list", `{`), streamCompatDelta(1, "first-index", "groups_list", `{`)},
				{streamCompatDelta(9, "late-index", "", `}`), streamCompatDelta(1, "", "", `}`)},
			},
			ids: []string{"first-index", "late-index"}, arguments: []string{`{}`, `{}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := readCompletionStream(context.Background(), strings.NewReader(streamCompatBody(tc.frames...)), nil)
			if err != nil || len(out.ToolCalls) != len(tc.ids) {
				t.Fatalf("unambiguous omitted indexes rejected: %+v %v", out, err)
			}
			for i, id := range tc.ids {
				if out.ToolCalls[i].ID != id || out.ToolCalls[i].Function.Name != "groups_list" || out.ToolCalls[i].Function.Arguments != tc.arguments[i] {
					t.Fatalf("tool fragments associated with wrong call: %+v", out.ToolCalls)
				}
			}
		})
	}
}

func TestCompletionStreamRejectsAmbiguousIndexCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]any
	}{
		{"negative index", [][]any{{streamCompatDelta(-1, "bad", "groups_list", `{}`)}}},
		{"fractional index", [][]any{{streamCompatDelta(1.5, "bad", "groups_list", `{}`)}}},
		{"string index", [][]any{{streamCompatDelta("9", "bad", "groups_list", `{}`)}}},
		{"missing index unknown ID without name", [][]any{{streamCompatDelta(nil, "unknown-call", "", `{}`)}}},
		{"anonymous multiple initial calls", [][]any{{streamCompatDelta(nil, "", "groups_list", `{}`), streamCompatDelta(nil, "", "plans_list", `{}`)}}},
		{"anonymous multiple deltas cannot attach to one existing call", [][]any{
			{streamCompatDelta(9, "single", "groups_list", `{"query":"`)},
			{streamCompatDelta(nil, "", "", `part`), streamCompatDelta(nil, "", "", `"}`)},
		}},
		{"anonymous continuation among two calls", [][]any{
			{streamCompatDelta(12, "first", "groups_list", `{`), streamCompatDelta(16, "second", "groups_list", `{`)},
			{streamCompatDelta(nil, "", "", `}`)},
		}},
		{"anonymous pair continuation among two calls", [][]any{
			{streamCompatDelta(12, "first", "groups_list", `{`), streamCompatDelta(16, "second", "groups_list", `{`)},
			{streamCompatDelta(nil, "", "", `}`), streamCompatDelta(nil, "", "", `}`)},
		}},
		{"same ID at two explicit indexes", [][]any{{streamCompatDelta(12, "same", "groups_list", `{}`), streamCompatDelta(16, "same", "groups_list", `{}`)}}},
		{"swapped IDs contradict two existing explicit indexes", [][]any{
			{streamCompatDelta(12, "operation-a", "propose_panel_operation", `{"operation":"notices.save","parameters":{`), streamCompatDelta(16, "operation-b", "propose_panel_operation", `{"operation":"notices.save","parameters":{`)},
			{streamCompatDelta(12, "operation-b", "", `},"reason":"fixture a"}`), streamCompatDelta(16, "operation-a", "", `},"reason":"fixture b"}`)},
		}},
		{"partial ID cannot choose among two calls", [][]any{
			{streamCompatDelta(12, "call-one", "groups_list", `{`), streamCompatDelta(16, "call-two", "groups_list", `{`)},
			{streamCompatDelta(nil, "call-", "", `}`)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := streamFrame(map[string]any{"content": "SECRET_RAW_RESPONSE"}, nil) + streamCompatBody(tc.frames...)
			out, err := readCompletionStream(context.Background(), strings.NewReader(body), nil)
			if err == nil || !reflect.DeepEqual(out, providerMessage{}) || strings.Contains(err.Error(), "SECRET_RAW_RESPONSE") {
				t.Fatalf("ambiguous stream returned executable or unsafe output: %+v %v", out, err)
			}
		})
	}
}

func TestProviderCompatibleMoreThanEightToolCalls(t *testing.T) {
	var calls []ToolCall
	var frames [][]any
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("fixture-read-%d", i)
		calls = append(calls, ToolCall{ID: id, Type: "function", Function: ToolFunction{Name: "groups_list", Arguments: `{}`}})
		frames = append(frames, []any{streamCompatDelta(100+i*3, id, "groups_list", `{}`)})
	}
	for _, tc := range []struct{ name, contentType, body string }{
		{"stream", "text/event-stream", streamCompatBody(frames...)},
		{"json", "application/json", func() string {
			raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: calls}, "finish_reason": "tool_calls"}}})
			return string(raw)
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			s, mock := streamTestService(t, server.URL)
			out, err := s.completeStream(context.Background(), "gpt-6.1-sol", []providerMessage{{Role: "user", Content: "read-only fixture"}}, nil)
			if err != nil || !reflect.DeepEqual(out.ToolCalls, calls) || requests.Load() != 1 {
				t.Fatalf("bounded twelve-tool %s response rejected: %+v %v count=%d", tc.name, out, err, requests.Load())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderCompatibleActualToolCallCountBoundary(t *testing.T) {
	for _, count := range []int{64, 65} {
		var calls []ToolCall
		var deltas []any
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("fixture-boundary-%d", i)
			calls = append(calls, ToolCall{ID: id, Type: "function", Function: ToolFunction{Name: "groups_list", Arguments: `{}`}})
			deltas = append(deltas, streamCompatDelta(1000000+i*100, id, "groups_list", `{}`))
		}
		jsonBody, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: calls}, "finish_reason": "tool_calls"}}})
		for _, tc := range []struct{ kind, contentType, body string }{
			{"stream", "text/event-stream", streamCompatBody(deltas)},
			{"json", "application/json", string(jsonBody)},
		} {
			t.Run(fmt.Sprintf("%s_%d_actual_calls", tc.kind, count), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", tc.contentType)
					fmt.Fprint(w, tc.body)
				}))
				defer server.Close()
				s, mock := streamTestService(t, server.URL)
				out, err := s.completeStream(context.Background(), "gpt-6.1-sol", nil, nil)
				if count == 64 {
					if err != nil || !reflect.DeepEqual(out.ToolCalls, calls) {
						t.Fatalf("64 calls within budget rejected: count=%d err=%v", len(out.ToolCalls), err)
					}
				} else if err == nil || !reflect.DeepEqual(out, providerMessage{}) {
					t.Fatalf("oversized call batch returned executable output: count=%d err=%v", len(out.ToolCalls), err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProviderCompatibleStreamFailoverDiscardsAmbiguousTools(t *testing.T) {
	bad := streamFrame(map[string]any{"content": "failed draft"}, nil) + streamCompatBody(
		[]any{streamCompatDelta(21, "failed-one", "groups_list", `{`), streamCompatDelta(25, "failed-two", "groups_list", `{`)},
		[]any{streamCompatDelta(nil, "", "", `}`)},
	)
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, bad)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame(map[string]any{"content": "backup draft"}, nil)+streamCompatBody([]any{streamCompatDelta(99, "backup-safe-read", "groups_list", `{}`)}))
	}))
	defer backup.Close()
	s := failoverTestService(t)
	endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", primary.URL, "primary-key"), failoverTestEndpoint(t, s, "backup", backup.URL, "backup-key")}
	var drafts []string
	out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "gpt-6.1-sol", []byte(`{}`), func(value string) error { drafts = append(drafts, value); return nil })
	if err != nil || primaryCalls.Load() != 1 || backupCalls.Load() != 1 || len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "backup-safe-read" || !reflect.DeepEqual(drafts, []string{"failed draft", "", "backup draft"}) {
		t.Fatalf("failed tools leaked or compatible backup rejected: %+v %v drafts=%v requests=%d/%d", out, err, drafts, primaryCalls.Load(), backupCalls.Load())
	}
}
