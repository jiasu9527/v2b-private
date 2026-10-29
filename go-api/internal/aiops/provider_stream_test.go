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
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func streamFrame(delta any, finish any) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	return "data: " + string(raw) + "\n\n"
}

func streamTestService(t *testing.T, url string) (*Service, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(config.Config{AppKey: "stream-test"}, db, nil)
	cipher, err := s.encryptSecret("stream-secret")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT enabled,base_url,models::TEXT,api_key_cipher`).WillReturnRows(sqlmock.NewRows([]string{"enabled", "base_url", "models", "key", "reasoning_efforts", "fallback_relays", "failover_enabled", "failover_timeout_seconds"}).AddRow(true, url+"/v1", `["gpt-6.1-sol"]`, cipher, `{}`, `[]`, false, 30))
	t.Cleanup(func() {
		s.Close()
		db.Close()
	})
	return s, mock
}

func TestProviderStreamsPublicContentBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	finishServer := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Stream || req.Model != "gpt-6.1-sol" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.Header.Get("Authorization") != "Bearer stream-secret" {
			t.Error("stream request contract mismatch")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(w, ": heartbeat\r\n\r\n")
		fmt.Fprint(w, streamFrame(map[string]any{"role": "assistant", "content": "你好", "reasoning_content": "DO_NOT_SHOW_REASONING"}, nil))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, streamFrame(map[string]any{"content": "世界"}, nil))
		fmt.Fprint(w, streamFrame(map[string]any{}, "stop"))
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer finishServer()
	s, mock := streamTestService(t, server.URL)
	content := make(chan string, 5)
	type result struct {
		message providerMessage
		err     error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		msg, err := s.completeStream(ctx, "gpt-6.1-sol", []providerMessage{{Role: "user", Content: "test"}}, func(value string) error { content <- value; return nil })
		resultCh <- result{msg, err}
	}()
	select {
	case partial := <-content:
		if partial != "你好" {
			t.Fatalf("non-public content or bad partial: %q", partial)
		}
	case <-time.After(time.Second):
		t.Fatal("first content did not arrive while upstream was still open")
	}
	select {
	case <-resultCh:
		t.Fatal("returned before terminal stream markers")
	default:
	}
	finishServer()
	resultValue := <-resultCh
	if resultValue.err != nil || resultValue.message.Content != "你好世界" || len(resultValue.message.ToolCalls) != 0 {
		t.Fatalf("unexpected result: %+v %v", resultValue.message, resultValue.err)
	}
	if latest := <-content; latest != "你好世界" {
		t.Fatal("callback must contain accumulated content")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderStreamingJSONFallbackUsesOneRequest(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		// Deliberately omit Content-Type, like existing compatible relay mocks.
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"兼容 JSON"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	s, mock := streamTestService(t, server.URL)
	var drafts []string
	result, err := s.completeStream(context.Background(), "gpt-6.1-sol", nil, func(value string) error { drafts = append(drafts, value); return nil })
	if err != nil || result.Content != "兼容 JSON" || len(drafts) != 1 || drafts[0] != result.Content || count != 1 {
		t.Fatalf("fallback mismatch count=%d drafts=%v err=%v", count, drafts, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type oneByteReader struct{ io.Reader }

func TestProviderToolArgumentBudgetsForPanelOperations(t *testing.T) {
	for _, name := range []string{"panel_query", "propose_panel_operation", "propose_ssh_command"} {
		limit := providerToolArgumentLimit(name)
		for _, length := range []int{limit, limit + 1} {
			args := `{"data":"` + strings.Repeat("x", length-len(`{"data":""}`)) + `"}`
			call := ToolCall{ID: "fixture-call", Type: "function", Function: ToolFunction{Name: name, Arguments: args}}
			err := validateCompletion(providerMessage{Role: "assistant", ToolCalls: []ToolCall{call}})
			if (err == nil) != (length == limit) {
				t.Fatal(name, length, err)
			}
			if err != nil && !strings.Contains(err.Error(), "工具参数过长") {
				t.Fatal("unclear argument budget error", err)
			}
			// Deliberately finish the tool name after >16 KiB of arguments.
			input := streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]any{"name": name[:3], "arguments": args}}}}, nil)
			input += streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"name": name[3:]}}}}, nil) + streamFrame(map[string]any{}, "tool_calls") + "data: [DONE]\n\n"
			out, err := readCompletionStream(context.Background(), strings.NewReader(input), nil)
			if (err == nil) != (length == limit) {
				t.Fatal("stream budget", name, length, err)
			}
			if err == nil && (len(out.ToolCalls) != 1 || len(out.ToolCalls[0].Function.Arguments) != length) {
				t.Fatal("stream truncated arguments")
			}
			if err != nil && !strings.Contains(err.Error(), "工具参数过长") {
				t.Fatal(err)
			}
		}
	}
}

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestCompletionStreamAssemblesInterleavedToolsAndUnicode(t *testing.T) {
	first := streamFrame(map[string]any{"tool_calls": []any{
		map[string]any{"index": 1, "id": "call_", "type": "function", "function": map[string]any{"name": "entry_", "arguments": `{"email":"`}},
		map[string]any{"index": 0, "id": "call_0", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": "{"}},
	}}, nil)
	second := streamFrame(map[string]any{"content": "检查中", "reasoning": "PRIVATE", "tool_calls": []any{
		map[string]any{"index": 0, "function": map[string]any{"arguments": "}"}},
		map[string]any{"index": 1, "id": "1", "function": map[string]any{"name": "simulate", "arguments": `测试@example.test","ua":"Forest"}`}},
	}}, nil)
	input := "\ufeff: initial\n\n" + first + second + streamFrame(map[string]any{}, "tool_calls") + "data: [DONE]\n\n"
	var drafts []string
	result, err := readCompletionStream(context.Background(), oneByteReader{strings.NewReader(input)}, func(value string) error { drafts = append(drafts, value); return nil })
	if err != nil || len(result.ToolCalls) != 2 || result.ToolCalls[0].ID != "call_0" || result.ToolCalls[1].ID != "call_1" || result.ToolCalls[1].Function.Name != "entry_simulate" || result.ToolCalls[1].Function.Arguments != `{"email":"测试@example.test","ua":"Forest"}` || len(drafts) != 1 || drafts[0] != "检查中" {
		t.Fatalf("stream assembly: %+v drafts=%v err=%v", result, drafts, err)
	}
}

func TestCompletionStreamRejectsIncompleteInvalidOrOversizedTools(t *testing.T) {
	tool := streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_0", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": `{}`}}}}, nil)
	finish := streamFrame(map[string]any{}, "tool_calls")
	done := "data: [DONE]\n\n"
	for _, test := range []struct{ name, body string }{
		{"missing done", tool + finish},
		{"missing finish", tool + done},
		{"length limit", tool + streamFrame(map[string]any{}, "length") + done},
		{"wrong finish", tool + streamFrame(map[string]any{}, "stop") + done},
		{"content after finish", tool + finish + streamFrame(map[string]any{"content": "extra"}, nil) + done},
		{"raw error", "data: {\"error\":{\"message\":\"SECRET_CREDENTIAL\"}}\n\n"},
		{"event error", "event: error\ndata: SECRET_CREDENTIAL\n\n"},
		{"invalid JSON", "data: SECRET_CREDENTIAL\n\n"},
		{"invalid argument JSON", streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": "{"}}}}, nil) + finish + done},
		{"ninth tool", streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 8, "id": "call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": `{}`}}}}, nil) + finish + done},
		{"gap tool", streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 1, "id": "call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": `{}`}}}}, nil) + finish + done},
		{"oversized arguments", streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": strings.Repeat(" ", 16001)}}}}, nil) + finish + done},
		{"oversized content", streamFrame(map[string]any{"content": strings.Repeat("x", 32001)}, nil) + streamFrame(map[string]any{}, "stop") + done},
		{"oversized event", "data: " + strings.Repeat(" ", maxCompletionEventBytes+2) + "\n\n"},
		{"oversized wire", strings.Repeat(": heartbeat\n", maxCompletionStreamBytes/12+1) + tool + finish + done},
		{"wrong role", streamFrame(map[string]any{"role": "system", "content": "x"}, nil) + streamFrame(map[string]any{}, "stop") + done},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := readCompletionStream(context.Background(), strings.NewReader(test.body), nil)
			if err == nil || strings.Contains(err.Error(), "SECRET_CREDENTIAL") || len(result.ToolCalls) != 0 || result.Content != "" {
				t.Fatalf("invalid stream yielded executable output: %+v err=%v", result, err)
			}
		})
	}
}

func TestCompletionStreamCancellationAndCallbackFailureAreSafe(t *testing.T) {
	body := streamFrame(map[string]any{"content": "hello"}, nil) + streamFrame(map[string]any{}, "stop") + "data: [DONE]\n\n"
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		result, err := readCompletionStream(ctx, strings.NewReader(body), func(string) error { return errors.New("SECRET_CREDENTIAL database error") })
		cancel()
		if err == nil || result.Content != "" || strings.Contains(err.Error(), "SECRET_CREDENTIAL") {
			t.Fatalf("cancelled=%v result=%+v err=%v", cancelled, result, err)
		}
	}
}

func TestCompletionStreamMultilineCRLFAndNoFinalBlankLine(t *testing.T) {
	body := "event: message\r\nid: 1\r\ndata: {\"choices\":[{\"index\":0,\r\ndata: \"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\r\n\r\n" + streamFrame(map[string]any{}, "stop") + "data: [DONE]"
	result, err := readCompletionStream(context.Background(), strings.NewReader(body), nil)
	if err != nil || result.Content != "ok" {
		t.Fatalf("multiline CRLF: %+v %v", result, err)
	}
}
