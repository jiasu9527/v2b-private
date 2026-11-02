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

func TestProviderHealthyReasoningAndToolStreamRenewsIdleWindow(t *testing.T) {
	// Scale the configured 30s idle window to 1s. A stream longer than that
	// window must finish if reasoning/tool bytes keep arriving, even without
	// any public content to trigger the UI draft callback.
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for range 8 {
			fmt.Fprint(w, streamFrame(map[string]any{"reasoning_content": "checking"}, nil))
			w.(http.Flusher).Flush()
			select {
			case <-time.After(180 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, streamFrame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "complete_call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": `{}`}}}}, nil))
		fmt.Fprint(w, streamFrame(map[string]any{}, "tool_calls")+"data: [DONE]\n\n")
	}))
	defer server.Close()
	s := failoverTestService(t)
	endpoint := failoverTestEndpoint(t, s, "primary", server.URL, "test-key")
	cfg := failoverTestConfig()
	cfg.FailoverTimeoutSeconds = 1
	started := time.Now()
	out, err := s.completeWithRelays(context.Background(), cfg, []RelayEndpoint{endpoint}, "model", []byte(`{}`), nil)
	if err != nil || len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "complete_call" || count.Load() != 1 || time.Since(started) < time.Second {
		t.Fatalf("healthy long stream was cut off/retried: out=%+v err=%v attempts=%d elapsed=%v", out, err, count.Load(), time.Since(started))
	}
}

func TestProviderFirstBodyGraceIsSeparateFromStreamIdleWindow(t *testing.T) {
	s := failoverTestService(t)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		// Early headers must not consume the first-body inference grace.
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-time.After(1250 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, streamFrame(map[string]any{"content": "inference finished"}, "stop")+"data: [DONE]\n\n")
	}))
	defer server.Close()
	endpoint := failoverTestEndpoint(t, s, "primary", server.URL, "key-0")
	cfg := failoverTestConfig()
	cfg.FailoverTimeoutSeconds = 1
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := s.completeWithRelays(ctx, cfg, []RelayEndpoint{endpoint}, "model", []byte(`{}`), nil)
	if err != nil || out.Content != "inference finished" || count.Load() != 1 {
		t.Fatalf("inference grace ended at idle deadline: %+v %v requests=%d", out, err, count.Load())
	}
}

func TestProviderSingleRelayRecoversTransientCompletionOnly(t *testing.T) {
	for _, failure := range []string{"http503", "truncated_stream", "whitespace_stream", "whitespace_json"} {
		t.Run(failure, func(t *testing.T) {
			s := failoverTestService(t)
			endpoint := failoverTestEndpoint(t, s, "primary", "https://primary.example", "fixture-key")
			count := 0
			var payloads []string
			s.httpClient = providerClientFunc(func(req *http.Request) (*http.Response, error) {
				count++
				payload, _ := io.ReadAll(req.Body)
				payloads = append(payloads, string(payload))
				status, header, body := 200, http.Header{}, failoverTestReply
				if count == 1 {
					switch failure {
					case "http503":
						status, body = 503, `{"error":{"message":"PRIVATE_ERROR"}}`
					case "truncated_stream":
						header.Set("Content-Type", "text/event-stream")
						body = streamFrame(map[string]any{"content": "未完成草稿", "tool_calls": []any{map[string]any{"index": 0, "id": "never_dispatch", "type": "function", "function": map[string]any{"name": "propose_ssh_command", "arguments": `{"command":"`}}}}, nil)
					case "whitespace_stream":
						header.Set("Content-Type", "text/event-stream")
						body = streamFrame(map[string]any{"content": " \n\t"}, "stop") + "data: [DONE]\n\n"
					case "whitespace_json":
						body = `{"choices":[{"message":{"content":" \n\t"},"finish_reason":"stop"}]}`
					}
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			cfg := failoverTestConfig()
			cfg.FailoverEnabled = false
			var drafts []string
			out, err := s.completeWithRelays(context.Background(), cfg, []RelayEndpoint{endpoint}, "model", []byte(`{"messages":[{"role":"user","content":"test"}]}`), func(value string) error { drafts = append(drafts, value); return nil })
			if err != nil || count != 2 || out.Content != "备用回复" || len(out.ToolCalls) != 0 || payloads[0] != payloads[1] {
				t.Fatalf("single relay completion recovery: %+v %v requests=%d", out, err, count)
			}
			if failure == "truncated_stream" && !reflect.DeepEqual(drafts, []string{"未完成草稿", "", "备用回复"}) {
				t.Fatalf("partial tool/text leaked into resumed response: %v", drafts)
			}
		})
	}
}

func TestProviderDoesNotRepeatPermanentFailuresAtSameRelay(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"authentication", `{"error":{}}`, 401},
		{"quota", `{"error":{"code":"insufficient_quota"}}`, 429},
		{"invalid_arguments", `{"error":{}}`, 400},
		{"malformed_json", `{`, 200},
		{"finish_mismatch", `{"choices":[{"message":{"content":"pretending tool call"},"finish_reason":"tool_calls"}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := failoverTestService(t)
			endpoint := failoverTestEndpoint(t, s, "primary", "https://primary.example", "fixture-key")
			count := 0
			s.httpClient = providerClientFunc(func(*http.Request) (*http.Response, error) {
				count++
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), []RelayEndpoint{endpoint}, "model", []byte(`{}`), nil)
			if err == nil || count != 1 || len(out.ToolCalls) != 0 || out.Content != "" {
				t.Fatalf("permanent response should not repeat: %+v %v requests=%d", out, err, count)
			}
		})
	}
}

func TestProviderOutputLimitExpandsBudgetWithoutDispatchingTruncatedTools(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, succeeds := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/succeeds=%v", stream, succeeds), func(t *testing.T) {
				s := failoverTestService(t)
				endpoint := failoverTestEndpoint(t, s, "primary", "https://primary.example", "fixture-key")
				var budgets []int
				s.httpClient = providerClientFunc(func(req *http.Request) (*http.Response, error) {
					var request completionRequest
					if json.NewDecoder(req.Body).Decode(&request) != nil || request.Model != "fixture-model" || request.ReasoningEffort != "high" || len(request.Messages) != 1 || request.Messages[0].Content != "unchanged" {
						t.Fatal("context/model/reasoning changed during length recovery")
					}
					budgets = append(budgets, request.MaxTokens)
					finish, args := "length", `{"unfinished":`
					if succeeds && request.MaxTokens == maxCompletionTokens {
						finish, args = "tool_calls", `{}`
					}
					header := http.Header{}
					var body string
					if stream {
						header.Set("Content-Type", "text/event-stream")
						body = streamFrame(map[string]any{"content": "处理中", "tool_calls": []any{map[string]any{"index": 0, "id": "fixture-call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": args}}}}, nil) + streamFrame(map[string]any{}, finish) + "data: [DONE]\n\n"
					} else {
						encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "处理中", "tool_calls": []ToolCall{{ID: "fixture-call", Type: "function", Function: ToolFunction{Name: "panel_status", Arguments: args}}}}, "finish_reason": finish}}})
						body = string(encoded)
					}
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				request, _ := json.Marshal(completionRequest{Model: "fixture-model", ReasoningEffort: "high", Messages: []providerMessage{{Role: "user", Content: "unchanged"}}, MaxTokens: initialCompletionTokens, Stream: true})
				var drafts []string
				out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), []RelayEndpoint{endpoint}, "fixture-model", request, func(value string) error { drafts = append(drafts, value); return nil })
				if !reflect.DeepEqual(budgets, []int{4096, 8192, 16384}) || (err == nil) != succeeds || s.relayCoolingDown(relayFingerprint(endpoint, "fixture-model")) {
					t.Fatalf("length retry should be bounded and not cool healthy relay: budgets=%v err=%v", budgets, err)
				}
				if succeeds && (len(out.ToolCalls) != 1 || out.ToolCalls[0].Function.Arguments != `{}`) {
					t.Fatalf("wrong complete tool: %+v", out)
				}
				if !succeeds && (!errors.Is(err, errProviderOutputLimit) || len(out.ToolCalls) != 0 || out.Content != "") {
					t.Fatalf("truncated tool escaped bounded retries: %+v %v", out, err)
				}
				if stream && !reflect.DeepEqual(drafts, []string{"处理中", "", "处理中", "", "处理中"}) {
					t.Fatalf("length drafts were not cleared: %v", drafts)
				}
			})
		}
	}
}

func TestProviderCancellationInterruptsCooldownAndRetryWaits(t *testing.T) {
	for _, stage := range []string{"cooldown", "backoff"} {
		t.Run(stage, func(t *testing.T) {
			s := failoverTestService(t)
			endpoint := failoverTestEndpoint(t, s, "primary", "https://primary.example", "fixture-key")
			count := 0
			s.httpClient = providerClientFunc(func(*http.Request) (*http.Response, error) {
				count++
				return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})
			if stage == "cooldown" {
				s.markRelayCoolingDown(relayFingerprint(endpoint, "model"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			started := time.Now()
			out, err := s.completeWithRelays(ctx, failoverTestConfig(), []RelayEndpoint{endpoint}, "model", []byte(`{}`), nil)
			want := 1
			if stage == "cooldown" {
				want = 0
			}
			if !errors.Is(err, errProviderCanceled) || time.Since(started) > time.Second || count != want || len(out.ToolCalls) != 0 {
				t.Fatalf("cancel did not stop wait: %+v err=%v requests=%d", out, err, count)
			}
		})
	}
}

func TestProviderIdleWatchRenewsAndCancels(t *testing.T) {
	ctx, watch := newProviderIdleContext(context.Background(), 100*time.Millisecond)
	defer watch.close()
	for range 4 {
		time.Sleep(40 * time.Millisecond)
		watch.touch()
		if ctx.Err() != nil {
			t.Fatal("active reader was canceled")
		}
	}
	select {
	case <-ctx.Done():
		if !watch.expired() {
			t.Fatal("idle expiry not classified")
		}
	case <-time.After(time.Second):
		t.Fatal("idle reader did not time out")
	}
}

func TestProviderFirstResponseGraceExpiresOrBecomesIdleWindow(t *testing.T) {
	for _, firstByte := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_byte=%v", firstByte), func(t *testing.T) {
			ctx, watch := newProviderResponseContext(context.Background(), 300*time.Millisecond, 50*time.Millisecond)
			defer watch.close()
			started := time.Now()
			time.Sleep(100 * time.Millisecond)
			if ctx.Err() != nil {
				t.Fatal("first-byte grace incorrectly used idle window")
			}
			if firstByte {
				watch.touch()
			}
			select {
			case <-ctx.Done():
				elapsed := time.Since(started)
				if !watch.expired() || (firstByte && elapsed > 250*time.Millisecond) || (!firstByte && elapsed < 280*time.Millisecond) {
					t.Fatalf("wrong phase timeout: firstByte=%v elapsed=%v", firstByte, elapsed)
				}
			case <-time.After(time.Second):
				t.Fatal("response watchdog did not expire")
			}
		})
	}
}
