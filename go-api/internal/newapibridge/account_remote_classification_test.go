package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkspaceRemoteFailureClassificationAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		raw    string
		want   error
	}{
		{"observed_pool_shortage", 503, `{"error":{"message":"号池当前没有可用账号 PRIVATE_SECRET https://provider.example.test"}}`, ErrWorkspaceCapacity},
		{"envelope_pool_shortage", 503, `{"message":"号池当前没有可用账号 PRIVATE_SECRET"}`, ErrWorkspaceCapacity},
		{"structured_capacity", 503, `{"error":{"code":"account_pool_exhausted","message":"PRIVATE_SECRET"}}`, ErrWorkspaceCapacity},
		{"unknown_503", 503, `{"error":{"message":"PRIVATE_SECRET"}}`, ErrRemoteUnavailable},
		{"html_502", 502, `<html>PRIVATE_SECRET</html>`, ErrRemoteUnavailable},
		{"unknown_500", 500, `{"error":{"code":"server_error","message":"PRIVATE_SECRET"}}`, ErrRemoteUnavailable},
		{"unknown_400", 400, `{"error":{"message":"PRIVATE_SECRET"}}`, ErrWorkspaceInvalidRequest},
		{"unknown_422", 422, `{"error":{"code":"validation_error","message":"PRIVATE_SECRET"}}`, ErrWorkspaceInvalidRequest},
		{"unrelated_context_code", 400, `{"error":{"code":"invalid_request_context","message":"PRIVATE_SECRET"}}`, ErrWorkspaceInvalidRequest},
		{"known_context_message", 400, `{"error":{"message":"maximum context length exceeded PRIVATE_SECRET"}}`, ErrWorkspaceContext},
		{"known_short_context_message", 400, `{"error":{"message":"context length exceeded PRIVATE_SECRET"}}`, ErrWorkspaceContext},
		{"vague_context_message", 400, `{"error":{"message":"invalid request context PRIVATE_SECRET"}}`, ErrWorkspaceInvalidRequest},
		{"structured_context", 400, `{"error":{"code":"context_length_exceeded","message":"PRIVATE_SECRET"}}`, ErrWorkspaceContext},
		{"security_context_first", 400, `{"code":"SECURITY_CONTEXT_INVALID","error":{"code":"context_length_exceeded","message":"PRIVATE_SECRET"}}`, ErrWorkspaceVerification},
		{"channel_not_capacity", 503, `{"error":{"code":"channel_not_found","message":"PRIVATE_SECRET"}}`, ErrWorkspaceModel},
		{"capacity_text_not_400", 400, `{"message":"号池当前没有可用账号 PRIVATE_SECRET"}`, ErrWorkspaceInvalidRequest},
		{"auth_unchanged", 401, `{"message":"PRIVATE_SECRET"}`, ErrWorkspaceNotConnected},
		{"quota_unchanged", 402, `{"message":"PRIVATE_SECRET"}`, ErrWorkspaceQuota},
		{"rate_limit_unchanged", 429, `{"message":"PRIVATE_SECRET"}`, ErrWorkspaceRateLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := workspaceRemoteError(tc.status, []byte(tc.raw))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			view, _ := json.Marshal(chatError(fmt.Errorf("PRIVATE_SECRET: %w", err)))
			if strings.Contains(err.Error(), "PRIVATE_SECRET") || strings.Contains(string(view), "PRIVATE_SECRET") || strings.Contains(string(view), "provider.example.test") {
				t.Fatalf("upstream data leaked: %s / %v", view, err)
			}
		})
	}
}

func TestWorkspaceChatOpeningErrorCodesAndOutcomes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		failed bool
	}{
		{ErrWorkspaceCapacity, "model_capacity_unavailable", true},
		{ErrWorkspaceInvalidRequest, "upstream_invalid_request", true},
		{ErrRemoteUnavailable, "upstream_unavailable", false},
		{ErrWorkspaceResponse, "upstream_invalid_response", false},
		{context.DeadlineExceeded, "upstream_unavailable", false},
		{context.Canceled, "cancelled", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := fmt.Errorf("PRIVATE_SECRET: %w", tc.err)
			view := chatError(err)
			if view["code"] != tc.code || chatOpenFailedBeforeGeneration(err) != tc.failed || strings.Contains(view["message"].(string), "PRIVATE_SECRET") {
				t.Fatalf("wrong classification: %v, before generation=%v", view, chatOpenFailedBeforeGeneration(err))
			}
		})
	}
}

type workspaceErrorTransport func(*http.Request) (*http.Response, error)

func (f workspaceErrorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkspaceRequestTransportSanitizedAndCancellationPreserved(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		s := &Service{client: &http.Client{Transport: workspaceErrorTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if canceled {
				cancel()
				return nil, context.Canceled
			}
			return nil, errors.New("PRIVATE_SECRET transport failure")
		})}}
		_, _, err := s.workspaceRequest(ctx, Settings{APIURL: "https://fixture.example.test"}, nil, http.MethodGet, "/api/user/self", nil, "")
		cancel()
		want := ErrRemoteUnavailable
		if canceled {
			want = context.Canceled
		}
		if !errors.Is(err, want) || calls != 1 || strings.Contains(err.Error(), "PRIVATE_SECRET") {
			t.Fatalf("transport: %v, calls=%d", err, calls)
		}
	}
}

func TestChatPostgresUnavailableOpeningIsUnknownAndNeverRetried(t *testing.T) {
	s := chatFixture(t)
	for _, failure := range []error{ErrRemoteUnavailable, ErrWorkspaceResponse, context.DeadlineExceeded, context.Canceled} {
		c := chatCreateFixture(t, s, 1)
		calls := 0
		req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: fmt.Sprintf("unknown-opening-%d", c.ID), Content: "hello"}
		open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
			calls++
			return nil, failure
		}
		emit := func(string, any) error { return nil }
		if err := s.sendChatWithOpener(context.Background(), 1, "fixture", req, emit, open); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		receipt, err := s.GetChatRequest(context.Background(), 1, req.ClientRequestID)
		if err != nil || !receipt.Found || receipt.Status != "unknown" {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
		if err = s.sendChatWithOpener(context.Background(), 1, "fixture", req, emit, open); err != nil || calls != 1 {
			t.Fatalf("replayed upstream: %d %v", calls, err)
		}
	}
}

func TestWorkspacePostgresChatFailuresUseSafeEventsWithoutReplay(t *testing.T) {
	s, _ := workspacePostgresFixture(t)
	connectWorkspace(t, s)
	base := s.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	for _, tc := range []struct {
		name, contentType, body, code, outcome string
		status                                 int
		failure                                error
		transport, canceled                    bool
	}{
		{name: "capacity", status: 503, contentType: "application/json", body: `{"error":{"message":"号池当前没有可用账号 PRIVATE_SECRET"}}`, failure: ErrWorkspaceCapacity, code: "model_capacity_unavailable", outcome: "failed"},
		{name: "generic_503", status: 503, contentType: "application/json", body: `{"error":{"message":"PRIVATE_SECRET"}}`, failure: ErrRemoteUnavailable, code: "upstream_unavailable", outcome: "unknown"},
		{name: "non_stream_200", status: 200, contentType: "application/json", body: `{"choices":[{"message":{"content":"PRIVATE_SECRET"}}]}`, failure: ErrWorkspaceResponse, code: "upstream_invalid_response", outcome: "unknown"},
		{name: "invalid_request", status: 400, contentType: "application/json", body: `{"error":{"code":"invalid_parameter","message":"PRIVATE_SECRET"}}`, failure: ErrWorkspaceInvalidRequest, code: "upstream_invalid_request", outcome: "failed"},
		{name: "transport", transport: true, failure: ErrRemoteUnavailable, code: "upstream_unavailable", outcome: "unknown"},
		{name: "canceled", transport: true, canceled: true, failure: context.Canceled, code: "cancelled", outcome: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := s.CreateConversation(context.Background(), 1, ConversationCreateRequest{Mode: "chat", Model: "test-model"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			s.client.Transport = workspaceErrorTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/pg/chat/completions" {
					return base.RoundTrip(r)
				}
				calls++
				if tc.transport {
					if tc.canceled {
						cancel()
						return nil, context.Canceled
					}
					return nil, errors.New("PRIVATE_SECRET transport failure")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "remote-error-" + tc.name, Content: "hello"}
			code, outcome := "", ""
			emit := func(event string, data any) error {
				raw, _ := json.Marshal(data)
				if strings.Contains(string(raw), "PRIVATE_SECRET") {
					t.Fatalf("upstream leaked in %s: %s", event, raw)
				}
				if event == "error" {
					code = data.(map[string]any)["code"].(string)
				}
				if event == "done" {
					outcome = data.(map[string]any)["status"].(string)
				}
				return nil
			}
			if err := s.SendChat(ctx, 1, "fixture", req, emit); !errors.Is(err, tc.failure) {
				t.Fatalf("got %v, want %v", err, tc.failure)
			}
			if code != tc.code || outcome != tc.outcome {
				t.Fatalf("code %q / outcome %q", code, outcome)
			}
			receipt, err := s.GetChatRequest(context.Background(), 1, req.ClientRequestID)
			if err != nil || !receipt.Found || receipt.Status != tc.outcome {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			if err := s.SendChat(context.Background(), 1, "fixture", req, emit); err != nil || calls != 1 {
				t.Fatalf("remote replay: calls=%d, err=%v", calls, err)
			}
		})
	}
}
