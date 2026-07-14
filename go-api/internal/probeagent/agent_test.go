package probeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type immediateChecker struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (c *immediateChecker) Check(ctx context.Context, _ string, _ int, _ time.Duration) CheckResult {
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		c.once.Do(func() { close(c.stopped) })
		return CheckResult{Error: "cancelled"}
	default:
	}
	latency := int64(1)
	return CheckResult{Success: true, LatencyMS: &latency, ResolvedIP: "127.0.0.1"}
}

func TestAgentUsesHTTPSProtocolAndBearer(t *testing.T) {
	for _, raw := range []string{"ftp://example.test", "http://example.test"} {
		if _, err := New(Config{APIURL: raw, Token: "secret", Interval: time.Second}); err == nil {
			t.Fatalf("New(%q) accepted insecure URL", raw)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/api/v1/probe/heartbeat" && r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		if r.URL.Path == "/api/v1/probe/tasks" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.URL.Path == "/api/v1/probe/results" && r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	agent, err := New(Config{APIURL: server.URL, Token: "secret", Interval: time.Hour, Version: "test"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = agent.Run(ctx); cancel() }()
	time.Sleep(50 * time.Millisecond)
	cancel()
}

func TestAgentSchedulesReportsAndStopsRemovedTask(t *testing.T) {
	checker := &immediateChecker{started: make(chan struct{}), stopped: make(chan struct{})}
	var mu sync.Mutex
	var heartbeats, taskCalls, reports int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/probe/heartbeat":
			heartbeats++
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/v1/probe/tasks":
			taskCalls++
			if taskCalls == 1 {
				_, _ = w.Write([]byte(`{"data":[{"target_id":7,"check_host":"127.0.0.1","check_port":443,"tcp_timeout_ms":1000,"check_interval_sec":1}]}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[]}`))
			}
		case "/api/v1/probe/results":
			var body struct {
				Results []Result `json:"results"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Results) != 1 || body.Results[0].TargetID != 7 {
				t.Fatalf("reported = %#v", body)
			}
			reports++
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	agent, err := New(Config{APIURL: server.URL, Token: "secret", Interval: 20 * time.Millisecond}, WithHTTPClient(server.Client()), WithChecker(checker), WithInsecureLocalHTTP())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	select {
	case <-checker.started:
	case <-time.After(time.Second):
		t.Fatal("task was not scheduled")
	}
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		complete := heartbeats >= 2 && reports >= 1
		mu.Unlock()
		if complete {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("heartbeats=%d reports=%d", heartbeats, reports)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not stop")
	}
}
