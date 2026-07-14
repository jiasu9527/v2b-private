package probeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const maxResultBatch = 500

type Config struct {
	APIURL   string
	Token    string
	Interval time.Duration
	Version  string
}
type fileConfig struct {
	APIURL   string `json:"api_url"`
	Token    string `json:"token"`
	Interval int64  `json:"interval"`
	Version  string `json:"version"`
}
type Task struct {
	TargetID         int64  `json:"target_id"`
	CheckHost        string `json:"check_host"`
	CheckPort        int    `json:"check_port"`
	TCPTimeoutMS     int64  `json:"tcp_timeout_ms"`
	CheckIntervalSec int64  `json:"check_interval_sec"`
}
type Result struct {
	ResultID   string `json:"result_id"`
	TargetID   int64  `json:"target_id"`
	Success    bool   `json:"success"`
	LatencyMS  *int64 `json:"latency_ms"`
	Error      string `json:"error"`
	ResolvedIP string `json:"resolved_ip"`
}
type Checker interface {
	Check(context.Context, string, int, time.Duration) CheckResult
}
type Option func(*Agent)
type Agent struct {
	cfg            Config
	client         *http.Client
	checker        Checker
	allowLocalHTTP bool
	results        chan Result
}

func LoadConfig(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("stat probe config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return Config{}, errors.New("probe config must be a regular 0600 file")
	}
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Config{}, fmt.Errorf("read probe config: %w", err)
	}
	var raw fileConfig
	if err := json.Unmarshal(body, &raw); err != nil {
		return Config{}, fmt.Errorf("decode probe config: %w", err)
	}
	return Config{APIURL: raw.APIURL, Token: raw.Token, Interval: time.Duration(raw.Interval) * time.Second, Version: raw.Version}, nil
}
func WithHTTPClient(client *http.Client) Option { return func(a *Agent) { a.client = client } }
func WithChecker(checker Checker) Option        { return func(a *Agent) { a.checker = checker } }
func WithInsecureLocalHTTP() Option             { return func(a *Agent) { a.allowLocalHTTP = true } }
func New(cfg Config, options ...Option) (*Agent, error) {
	a := &Agent{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, checker: TCPChecker{}, results: make(chan Result, maxResultBatch)}
	for _, option := range options {
		option(a)
	}
	if a.client == nil {
		return nil, errors.New("http client is required")
	}
	if a.checker == nil {
		return nil, errors.New("tcp checker is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("probe token is required")
	}
	if cfg.Interval <= 0 {
		return nil, errors.New("probe interval must be positive")
	}
	u, err := url.Parse(cfg.APIURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid probe api_url")
	}
	if u.Scheme != "https" && !(a.allowLocalHTTP && u.Scheme == "http" && isLocalHost(u.Hostname())) {
		return nil, errors.New("probe api_url must use https")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("probe api_url must use http or https")
	}
	return a, nil
}
func isLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (a *Agent) Run(ctx context.Context) error {
	workers := map[int64]context.CancelFunc{}
	var workerWG sync.WaitGroup
	defer func() {
		for _, cancel := range workers {
			cancel()
		}
		workerWG.Wait()
	}()
	refresh := func() {
		if err := a.heartbeat(ctx); err != nil {
			return
		}
		tasks, err := a.tasks(ctx)
		if err != nil {
			return
		}
		next := make(map[int64]Task, len(tasks))
		for _, task := range tasks {
			if task.TargetID > 0 {
				next[task.TargetID] = task
			}
		}
		for id, cancel := range workers {
			if _, ok := next[id]; !ok {
				cancel()
				delete(workers, id)
			}
		}
		for id, task := range next {
			if _, exists := workers[id]; exists {
				continue
			}
			taskCtx, cancel := context.WithCancel(ctx)
			workers[id] = cancel
			workerWG.Add(1)
			go func(task Task) { defer workerWG.Done(); a.runTask(taskCtx, task) }(task)
		}
	}
	refresh()
	heartbeatTicker := time.NewTicker(a.cfg.Interval)
	defer heartbeatTicker.Stop()
	flushTicker := time.NewTicker(minDuration(a.cfg.Interval, 100*time.Millisecond))
	defer flushTicker.Stop()
	var pending []Result
	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-a.results:
			pending = append(pending, result)
		case <-heartbeatTicker.C:
			refresh()
			pending = a.flush(ctx, pending)
		case <-flushTicker.C:
			pending = a.flush(ctx, pending)
		}
	}
}
func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
func (a *Agent) runTask(ctx context.Context, task Task) {
	interval := time.Duration(task.CheckIntervalSec) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	timeout := time.Duration(task.TCPTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	for {
		result := a.checker.Check(ctx, task.CheckHost, task.CheckPort, timeout)
		if ctx.Err() != nil {
			return
		}
		report := Result{ResultID: fmt.Sprintf("%d-%d", task.TargetID, time.Now().UnixNano()), TargetID: task.TargetID, Success: result.Success, LatencyMS: result.LatencyMS, Error: result.Error, ResolvedIP: result.ResolvedIP}
		select {
		case a.results <- report:
		case <-ctx.Done():
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (a *Agent) flush(ctx context.Context, pending []Result) []Result {
	for len(pending) > 0 {
		size := len(pending)
		if size > maxResultBatch {
			size = maxResultBatch
		}
		if err := a.report(ctx, pending[:size]); err != nil {
			return pending
		}
		pending = pending[size:]
	}
	return pending
}
func (a *Agent) heartbeat(ctx context.Context) error {
	return a.request(ctx, http.MethodPost, "/api/v1/probe/heartbeat", map[string]string{"version": a.cfg.Version, "arch": runtime.GOARCH}, nil)
}
func (a *Agent) tasks(ctx context.Context) ([]Task, error) {
	var payload struct {
		Data []Task `json:"data"`
	}
	err := a.request(ctx, http.MethodGet, "/api/v1/probe/tasks", nil, &payload)
	return payload.Data, err
}
func (a *Agent) report(ctx context.Context, results []Result) error {
	return a.request(ctx, http.MethodPost, "/api/v1/probe/results", map[string][]Result{"results": results}, nil)
}
func (a *Agent) request(ctx context.Context, method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := strings.TrimRight(a.cfg.APIURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("probe api %s: %s", path, response.Status)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}
