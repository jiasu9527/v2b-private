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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

const failoverTestReply = `{"choices":[{"message":{"role":"assistant","content":"备用回复"},"finish_reason":"stop"}]}`

func failoverTestService(t *testing.T) *Service {
	t.Helper()
	s := NewService(config.Config{AppKey: "failover-test-app-key"}, nil, nil)
	t.Cleanup(s.Close)
	return s
}

func failoverTestEndpoint(t *testing.T, s *Service, id, url, key string) RelayEndpoint {
	t.Helper()
	cipher, err := s.encryptSecret(key)
	if err != nil {
		t.Fatal(err)
	}
	return RelayEndpoint{ID: id, Name: id, BaseURL: url + "/v1", APIKeyCipher: cipher, Enabled: true}
}

func failoverTestConfig() Config {
	return Config{Enabled: true, FailoverEnabled: true, FailoverTimeoutSeconds: 10}
}

func TestProviderFailoverKeepsExactRequestAndSeparateKeys(t *testing.T) {
	var bodies [2]string
	var counts [2]atomic.Int32
	servers := make([]*httptest.Server, 2)
	for i := range servers {
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[i].Add(1)
			body, _ := io.ReadAll(r.Body)
			bodies[i] = string(body)
			if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != fmt.Sprintf("Bearer relay-key-%d", i) {
				t.Error("relay path or credential isolation violated")
			}
			if i == 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"error":{"message":"SECRET_RAW_UPSTREAM_BODY"}}`)
				return
			}
			fmt.Fprint(w, failoverTestReply)
		}))
		defer servers[i].Close()
	}
	s := failoverTestService(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.db = db
	primary := failoverTestEndpoint(t, s, "primary", servers[0].URL, "relay-key-0")
	fallback := failoverTestEndpoint(t, s, "backup", servers[1].URL, "relay-key-1")
	backups, _ := json.Marshal([]RelayEndpoint{fallback})
	mock.ExpectQuery(`SELECT enabled,base_url,models::TEXT,api_key_cipher`).WillReturnRows(sqlmock.NewRows([]string{"enabled", "base_url", "models", "key", "reasoning_efforts", "fallback_relays", "failover_enabled", "failover_timeout_seconds"}).AddRow(true, primary.BaseURL, `["gpt-6.1-sol"]`, primary.APIKeyCipher, `{}`, string(backups), true, 10))
	out, err := s.completeStreamWithReasoning(context.Background(), "gpt-6.1-sol", "high", []providerMessage{{Role: "user", Content: "保留上下文"}}, nil)
	if err != nil || out.Content != "备用回复" || counts[0].Load() != 1 || counts[1].Load() != 1 || bodies[0] != bodies[1] {
		t.Fatalf("failover did not preserve request: %v counts %d/%d", err, counts[0].Load(), counts[1].Load())
	}
	var request completionRequest
	if json.Unmarshal([]byte(bodies[1]), &request) != nil || request.Model != "gpt-6.1-sol" || request.ReasoningEffort != "high" || len(request.Messages) != 1 || request.Messages[0].Content != "保留上下文" {
		t.Fatalf("model, reasoning, or messages changed: %+v", request)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderFailoverErrorEligibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		switches bool
	}{
		{"auth", 401, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"forbidden", 403, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"endpoint", 404, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"timeout", 408, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"rate", 429, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"upstream", 502, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"model", 400, `{"error":{"code":"model_not_available"}}`, true},
		{"quota", 400, `{"error":{"code":"insufficient_quota"}}`, true},
		{"invalid_json", 200, `SECRET_RAW_UPSTREAM_BODY`, true},
		{"parameter_400", 400, `{"error":{"message":"SECRET_RAW_UPSTREAM_BODY"}}`, false},
		{"parameter_422", 422, `{"error":{"message":"SECRET_RAW_UPSTREAM_BODY"}}`, false},
		{"reasoning", 400, `{"error":{"param":"reasoning_effort"}}`, false},
		{"redirect", 307, `{"error":{"code":"insufficient_quota"}}`, false},
		{"other", 418, `SECRET_RAW_UPSTREAM_BODY`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var primaryCount, backupCount atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCount.Add(1)
				fmt.Fprint(w, failoverTestReply)
			}))
			defer backup.Close()
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCount.Add(1)
				w.Header().Set("Location", backup.URL)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer primary.Close()
			s := failoverTestService(t)
			endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", primary.URL, "key-0"), failoverTestEndpoint(t, s, "backup", backup.URL, "key-1")}
			out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "model", []byte(`{}`), nil)
			if (err == nil) != tc.switches || primaryCount.Load() != 1 || (backupCount.Load() == 1) != tc.switches {
				t.Fatalf("bad failover eligibility: out=%+v err=%v requests=%d/%d", out, err, primaryCount.Load(), backupCount.Load())
			}
			if err != nil && (strings.Contains(err.Error(), "SECRET_RAW") || strings.Contains(err.Error(), "key-0")) {
				t.Fatal("unsafe provider error", err)
			}
		})
	}
}

func TestProviderFailoverPartialStreamDiscardsToolsAndClearsDraft(t *testing.T) {
	partial := streamFrame(map[string]any{"content": "失败站部分内容", "tool_calls": []any{map[string]any{"index": 0, "id": "failed_call", "type": "function", "function": map[string]any{"name": "propose_ssh_command", "arguments": `{"command":"`}}}}, nil)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, partial)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame(map[string]any{"content": "备用完整回复", "tool_calls": []any{map[string]any{"index": 0, "id": "valid_call", "type": "function", "function": map[string]any{"name": "panel_status", "arguments": `{}`}}}}, nil))
		fmt.Fprint(w, streamFrame(map[string]any{}, "tool_calls")+"data: [DONE]\n\n")
	}))
	defer backup.Close()
	s := failoverTestService(t)
	endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", primary.URL, "key-0"), failoverTestEndpoint(t, s, "backup", backup.URL, "key-1")}
	var drafts []string
	out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "model", []byte(`{}`), func(value string) error { drafts = append(drafts, value); return nil })
	if err != nil || !reflect.DeepEqual(drafts, []string{"失败站部分内容", "", "备用完整回复"}) || len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "valid_call" || out.Content != "备用完整回复" {
		t.Fatalf("partial attempts mixed: out=%+v drafts=%v err=%v", out, drafts, err)
	}
}

func TestProviderFailoverDoesNotRetryCanceledOrPersistenceErrors(t *testing.T) {
	for _, stage := range []string{"canceled_before_request", "canceled_during_response", "content_callback", "clear_callback"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var primaryCount, backupCount atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCount.Add(1)
				if stage == "canceled_during_response" {
					cancel()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, streamFrame(map[string]any{"content": "部分内容"}, nil))
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCount.Add(1)
				fmt.Fprint(w, failoverTestReply)
			}))
			defer backup.Close()
			s := failoverTestService(t)
			endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", primary.URL, "key-0"), failoverTestEndpoint(t, s, "backup", backup.URL, "key-1")}
			if stage == "canceled_before_request" {
				cancel()
			}
			out, err := s.completeWithRelays(ctx, failoverTestConfig(), endpoints, "model", []byte(`{}`), func(value string) error {
				if stage == "content_callback" || (stage == "clear_callback" && value == "") {
					return errors.New("SECRET_DATABASE_ERROR")
				}
				return nil
			})
			if err == nil || backupCount.Load() != 0 || len(out.ToolCalls) != 0 || strings.Contains(err.Error(), "SECRET_") {
				t.Fatalf("should not retry cancellation/persistence: out=%+v err=%v backup requests=%d", out, err, backupCount.Load())
			}
			if stage == "canceled_before_request" && primaryCount.Load() != 0 {
				t.Fatal("canceled request reached primary")
			}
		})
	}
}

type providerClientFunc func(*http.Request) (*http.Response, error)

func (fn providerClientFunc) Do(req *http.Request) (*http.Response, error) { return fn(req) }

func TestProviderFailoverTransportTimeoutAndAttemptDeadline(t *testing.T) {
	s := failoverTestService(t)
	endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", "https://primary.example", "key-0"), failoverTestEndpoint(t, s, "backup", "https://backup.example", "key-1")}
	count := 0
	s.httpClient = providerClientFunc(func(req *http.Request) (*http.Response, error) {
		count++
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
			t.Error("configured per-attempt deadline not applied")
		}
		if count == 1 {
			return nil, fmt.Errorf("SECRET_TRANSPORT_DATA: %w", context.DeadlineExceeded)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(failoverTestReply))}, nil
	})
	out, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "model", []byte(`{}`), nil)
	if err != nil || out.Content != "备用回复" || count != 2 {
		t.Fatalf("timeout not failed over safely: %+v %v %d", out, err, count)
	}
}

func TestProviderFailoverRealAttemptTimeoutCancelsStreamAndUsesBackup(t *testing.T) {
	primaryDone := make(chan struct{})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(primaryDone)
		// Drain the request before streaming so net/http can observe disconnects
		// while the handler is waiting for the response deadline.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame(map[string]any{"content": "超时站部分内容"}, nil))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			t.Error("timed-out request did not close upstream stream")
		}
	}))
	defer primary.Close()
	var backupCount atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCount.Add(1)
		fmt.Fprint(w, failoverTestReply)
	}))
	defer backup.Close()
	s := failoverTestService(t)
	endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", primary.URL, "key-0"), failoverTestEndpoint(t, s, "backup", backup.URL, "key-1")}
	cfg := failoverTestConfig()
	// Production SaveConfig enforces 10–90 seconds. Exercise real expiration
	// through the internal helper without making the suite wait ten seconds.
	cfg.FailoverTimeoutSeconds = 1
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var drafts []string
	out, err := s.completeWithRelays(ctx, cfg, endpoints, "model", []byte(`{}`), func(value string) error { drafts = append(drafts, value); return nil })
	if err != nil || ctx.Err() != nil || out.Content != "备用回复" || backupCount.Load() != 1 || !reflect.DeepEqual(drafts, []string{"超时站部分内容", "", "备用回复"}) {
		t.Fatalf("real attempt timeout failed: out=%+v drafts=%v err=%v parent=%v", out, drafts, err, ctx.Err())
	}
	select {
	case <-primaryDone:
	case <-time.After(time.Second):
		t.Fatal("primary response handler did not stop after timeout")
	}
}

func TestProviderFailoverCoolingDownRecoveryAndConfigChanges(t *testing.T) {
	s := failoverTestService(t)
	endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", "https://primary.example", "key-0"), failoverTestEndpoint(t, s, "backup", "https://backup.example", "key-1")}
	primaryHealthy := false
	primaryCount, backupCount := 0, 0
	s.httpClient = providerClientFunc(func(req *http.Request) (*http.Response, error) {
		status, body := 200, failoverTestReply
		if req.URL.Host == "primary.example" {
			primaryCount++
			if !primaryHealthy {
				status, body = 503, `{"error":{}}`
			}
		} else {
			backupCount++
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	complete := func(model string) {
		t.Helper()
		if _, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, model, []byte(`{}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	complete("model-a")
	complete("model-a")
	if primaryCount != 1 || backupCount != 2 {
		t.Fatal("cooldown did not skip failed primary")
	}
	complete("model-b")
	if primaryCount != 2 {
		t.Fatal("model-specific failure suppressed another model")
	}
	primaryHealthy = true
	s.relayHealthMu.Lock()
	s.relayCooldowns[relayFingerprint(endpoints[0], "model-a")] = time.Now().Add(-time.Second)
	s.relayHealthMu.Unlock()
	complete("model-a")
	if primaryCount != 3 || backupCount != 3 {
		t.Fatal("expired cooldown did not recover to primary")
	}
	// Editing credentials immediately bypasses old failures for the same model.
	endpoints[0] = failoverTestEndpoint(t, s, "primary", "https://primary.example", "replacement-key")
	complete("model-b")
	if primaryCount != 4 || backupCount != 3 {
		t.Fatal("new credential inherited old circuit state")
	}
	oldURLKey := relayFingerprint(endpoints[0], "model-b")
	s.markRelayCoolingDown(oldURLKey)
	endpoints[0].BaseURL += "/new-path"
	complete("model-b")
	if primaryCount != 5 || backupCount != 3 {
		t.Fatal("new URL inherited old circuit state")
	}
	for _, endpoint := range endpoints {
		s.markRelayCoolingDown(relayFingerprint(endpoint, "model-c"))
	}
	if _, err := s.completeWithRelays(context.Background(), failoverTestConfig(), endpoints, "model-c", []byte(`{}`), nil); err == nil || !strings.Contains(err.Error(), "冷却") || primaryCount != 5 || backupCount != 3 {
		t.Fatalf("all-cooling request should stop before transport: %v", err)
	}
}

func TestProviderFailoverDisabledAndCompletedRequestsNeverUseBackup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		status  int
	}{
		{"disabled_failure", false, 503},
		{"enabled_success", true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := failoverTestService(t)
			endpoints := []RelayEndpoint{failoverTestEndpoint(t, s, "primary", "https://primary.example", "key-0"), failoverTestEndpoint(t, s, "backup", "https://backup.example", "key-1")}
			count := 0
			s.httpClient = providerClientFunc(func(req *http.Request) (*http.Response, error) {
				count++
				if req.URL.Host != "primary.example" {
					t.Fatal("backup must not be used")
				}
				if _, ok := req.Context().Deadline(); !tc.enabled && ok {
					t.Fatal("disabled failover changed legacy request timeout")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(failoverTestReply))}, nil
			})
			cfg := failoverTestConfig()
			cfg.FailoverEnabled = tc.enabled
			_, err := s.completeWithRelays(context.Background(), cfg, endpoints, "model", []byte(`{}`), nil)
			if (err == nil) != (tc.status == 200) || count != 1 {
				t.Fatal("unexpected attempts", err, count)
			}
		})
	}
}

func TestProviderRelayHealthConcurrentAccess(t *testing.T) {
	s := failoverTestService(t)
	endpoint := failoverTestEndpoint(t, s, "primary", "https://primary.example", "key-0")
	key := relayFingerprint(endpoint, "model")
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 50 {
				s.markRelayCoolingDown(key)
				s.relayCoolingDown(key)
				s.clearRelayCooldown(key)
			}
		}()
	}
	group.Wait()
}
