package probeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type immediateChecker struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

type blockingCheckerCall struct {
	host    string
	port    int
	timeout time.Duration
}

type blockingChecker struct {
	calls chan blockingCheckerCall
}

func (c *blockingChecker) Check(ctx context.Context, host string, port int, timeout time.Duration) CheckResult {
	select {
	case c.calls <- blockingCheckerCall{host: host, port: port, timeout: timeout}:
	case <-ctx.Done():
		return CheckResult{Error: ctx.Err().Error()}
	}
	<-ctx.Done()
	return CheckResult{Error: ctx.Err().Error()}
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

func TestAgentFlushPreservesEveryResultInOriginalOrder(t *testing.T) {
	var reported []Result
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/probe/results" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Results []Result `json:"results"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		reported = append(reported, body.Results...)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	agent, err := New(
		Config{APIURL: server.URL, Token: "secret", Interval: time.Second},
		WithHTTPClient(server.Client()),
		WithInsecureLocalHTTP(),
	)
	if err != nil {
		t.Fatal(err)
	}
	latency := int64(1)
	pending := []Result{
		{ResultID: "target-7-old", TargetID: 7, Success: true, LatencyMS: &latency},
		{ResultID: "target-8-old", TargetID: 8, Success: true, LatencyMS: &latency},
		{ResultID: "target-7-latest", TargetID: 7, Success: false, Error: "timeout"},
		{ResultID: "target-8-latest", TargetID: 8, Success: false, Error: "refused"},
	}

	remaining := agent.flush(context.Background(), pending)
	if len(remaining) != 0 {
		t.Fatalf("remaining = %#v", remaining)
	}
	if len(reported) != len(pending) {
		t.Fatalf("reported = %#v", reported)
	}
	for index := range pending {
		if reported[index].ResultID != pending[index].ResultID || reported[index].TargetID != pending[index].TargetID {
			t.Fatalf("reported[%d] = %#v, want %#v", index, reported[index], pending[index])
		}
	}
}

func TestAgentCopiesManualRunAndTargetVersionIntoProbeResult(t *testing.T) {
	checker := &immediateChecker{started: make(chan struct{}), stopped: make(chan struct{})}
	agent, err := New(
		Config{APIURL: "http://127.0.0.1", Token: "secret", Interval: time.Second},
		WithChecker(checker),
		WithInsecureLocalHTTP(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.runTask(ctx, Task{
		TargetID: 7, RunID: 91, TargetVersion: 4, CheckHost: "127.0.0.1", CheckPort: 443,
		TCPTimeoutMS: 1000, CheckIntervalSec: 60,
	})
	select {
	case result := <-agent.results:
		if result.TargetID != 7 || result.RunID != 91 || result.TargetVersion != 4 {
			t.Fatalf("result = %#v", result)
		}
		cancel()
	case <-time.After(time.Second):
		t.Fatal("probe result was not produced")
	}
}

func TestAgentLogsEachOperationOutageAndRecoveryOnce(t *testing.T) {
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	defer func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	}()
	log.SetFlags(0)
	log.SetPrefix("")

	tests := []struct {
		name   string
		path   string
		invoke func(context.Context, *Agent) error
	}{
		{
			name: "heartbeat",
			path: "/api/v1/probe/heartbeat",
			invoke: func(ctx context.Context, agent *Agent) error {
				return agent.heartbeat(ctx)
			},
		},
		{
			name: "tasks",
			path: "/api/v1/probe/tasks",
			invoke: func(ctx context.Context, agent *Agent) error {
				_, err := agent.tasks(ctx)
				return err
			},
		},
		{
			name: "report",
			path: "/api/v1/probe/results",
			invoke: func(ctx context.Context, agent *Agent) error {
				latency := int64(1)
				return agent.report(ctx, []Result{{ResultID: "result-1", TargetID: 7, Success: true, LatencyMS: &latency}})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Fatalf("path = %q", r.URL.Path)
				}
				calls++
				if calls <= 2 {
					http.Error(w, "temporary outage", http.StatusServiceUnavailable)
					return
				}
				if test.name == "tasks" {
					_, _ = w.Write([]byte(`{"data":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{}}`))
			}))
			defer server.Close()
			agent, err := New(
				Config{APIURL: server.URL, Token: "secret", Interval: time.Second},
				WithHTTPClient(server.Client()),
				WithInsecureLocalHTTP(),
			)
			if err != nil {
				t.Fatal(err)
			}
			for call := 1; call <= 4; call++ {
				err := test.invoke(context.Background(), agent)
				if call <= 2 && err == nil {
					t.Fatalf("call %d unexpectedly succeeded", call)
				}
				if call > 2 && err != nil {
					t.Fatalf("call %d failed: %v", call, err)
				}
			}
			output := logs.String()
			failureMarker := "probe " + test.name + " failed:"
			recoveryMarker := "probe " + test.name + " recovered"
			if count := strings.Count(output, failureMarker); count != 1 {
				t.Fatalf("failure log count = %d; logs:\n%s", count, output)
			}
			if count := strings.Count(output, recoveryMarker); count != 1 {
				t.Fatalf("recovery log count = %d; logs:\n%s", count, output)
			}
			if !strings.Contains(output, "temporary outage") {
				t.Fatalf("response detail missing from logs:\n%s", output)
			}
		})
	}
}

func TestAgentRestartsWorkerWhenTaskChanges(t *testing.T) {
	type taskPayload struct {
		TargetID         int64  `json:"target_id"`
		GroupID          int64  `json:"group_id"`
		CheckHost        string `json:"check_host"`
		CheckPort        int    `json:"check_port"`
		TCPTimeoutMS     int64  `json:"tcp_timeout_ms"`
		CheckIntervalSec int64  `json:"check_interval_sec"`
	}
	base := taskPayload{
		TargetID:         7,
		GroupID:          11,
		CheckHost:        "primary.example.test",
		CheckPort:        443,
		TCPTimeoutMS:     1000,
		CheckIntervalSec: 30,
	}
	tests := []struct {
		name   string
		mutate func(*taskPayload)
	}{
		{name: "group", mutate: func(task *taskPayload) { task.GroupID = 12 }},
		{name: "host", mutate: func(task *taskPayload) { task.CheckHost = "replacement.example.test" }},
		{name: "port", mutate: func(task *taskPayload) { task.CheckPort = 8443 }},
		{name: "timeout", mutate: func(task *taskPayload) { task.TCPTimeoutMS = 2500 }},
		{name: "interval", mutate: func(task *taskPayload) { task.CheckIntervalSec = 60 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			var mu sync.Mutex
			taskCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/probe/heartbeat":
					_, _ = w.Write([]byte(`{"data":{}}`))
				case "/api/v1/probe/tasks":
					mu.Lock()
					taskCalls++
					call := taskCalls
					mu.Unlock()
					task := changed
					if call == 1 {
						task = base
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []taskPayload{task}})
				default:
					http.Error(w, "unexpected path", http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			checker := &blockingChecker{calls: make(chan blockingCheckerCall, 2)}
			agent, err := New(
				Config{APIURL: server.URL, Token: "secret", Interval: 10 * time.Millisecond},
				WithHTTPClient(server.Client()),
				WithChecker(checker),
				WithInsecureLocalHTTP(),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- agent.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("agent stopped with error: %v", err)
					}
				case <-time.After(time.Second):
					t.Error("agent did not stop")
				}
			})

			select {
			case first := <-checker.calls:
				if first.host != base.CheckHost || first.port != base.CheckPort || first.timeout != time.Second {
					t.Fatalf("initial check = %#v", first)
				}
			case <-time.After(time.Second):
				t.Fatal("initial worker did not start")
			}
			select {
			case replacement := <-checker.calls:
				// A second blocked Check call proves that the old worker was cancelled
				// and a replacement was started for the changed task.
				wantTimeout := time.Duration(changed.TCPTimeoutMS) * time.Millisecond
				if replacement.host != changed.CheckHost || replacement.port != changed.CheckPort || replacement.timeout != wantTimeout {
					t.Fatalf("replacement check = %#v; want host=%q port=%d timeout=%s", replacement, changed.CheckHost, changed.CheckPort, wantTimeout)
				}
			case <-time.After(time.Second):
				t.Fatal("changed task did not restart worker")
			}
		})
	}
}

func TestAgentKeepsWorkerWhenTaskIsUnchanged(t *testing.T) {
	task := map[string]any{
		"target_id":          7,
		"group_id":           11,
		"check_host":         "primary.example.test",
		"check_port":         443,
		"tcp_timeout_ms":     1000,
		"check_interval_sec": 30,
	}
	refreshed := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	taskCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/probe/heartbeat":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/v1/probe/tasks":
			mu.Lock()
			taskCalls++
			if taskCalls >= 3 {
				once.Do(func() { close(refreshed) })
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{task}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	checker := &blockingChecker{calls: make(chan blockingCheckerCall, 2)}
	agent, err := New(
		Config{APIURL: server.URL, Token: "secret", Interval: 10 * time.Millisecond},
		WithHTTPClient(server.Client()),
		WithChecker(checker),
		WithInsecureLocalHTTP(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("agent did not stop")
		}
	})

	select {
	case <-checker.calls:
	case <-time.After(time.Second):
		t.Fatal("initial worker did not start")
	}
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("tasks were not refreshed")
	}
	select {
	case call := <-checker.calls:
		t.Fatalf("unchanged task restarted worker: %#v", call)
	case <-time.After(50 * time.Millisecond):
	}
}
