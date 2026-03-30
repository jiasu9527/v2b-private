package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/queue"
)

type captureQueue struct {
	queueNames []string
	jobNames   []string
	runNow     bool
}

func (c *captureQueue) Enqueue(queueName, jobName string, fn queue.JobFunc) error {
	c.queueNames = append(c.queueNames, queueName)
	c.jobNames = append(c.jobNames, jobName)
	if c.runNow {
		return fn(context.Background())
	}
	return nil
}

func (c *captureQueue) Snapshot() queue.Snapshot {
	return queue.Snapshot{}
}

func TestDispatchUserMailJobsEnqueuesSingleBatchJob(t *testing.T) {
	service := (&DBService{}).WithQueueRuntime(&captureQueue{runNow: true})
	var sent []string
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
		sent = append(sent, to)
		return nil
	}

	err := service.dispatchUserMailJobs(
		context.Background(),
		[]string{"a@example.com", "b@example.com"},
		"Notice",
		"Hello",
		bulkMailConfig{host: "127.0.0.1", port: 25, from: "noreply@example.com", fromName: "Forest", appName: "Forest", template: "default"},
	)
	if err != nil {
		t.Fatalf("dispatch user mail jobs: %v", err)
	}

	q := service.jobs.(*captureQueue)
	if len(q.queueNames) != 1 || q.queueNames[0] != "send_email_mass" {
		t.Fatalf("unexpected queue names: %#v", q.queueNames)
	}
	if len(sent) != 2 || sent[0] != "a@example.com" || sent[1] != "b@example.com" {
		t.Fatalf("unexpected sent emails: %#v", sent)
	}
}

func TestDispatchUserMailJobsWaitsBetweenEmails(t *testing.T) {
	service := (&DBService{}).WithQueueRuntime(&captureQueue{runNow: true})
	var sent []string
	var waits []string
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
		sent = append(sent, to)
		return nil
	}
	service.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d.String())
		return nil
	}

	err := service.dispatchUserMailJobs(
		context.Background(),
		[]string{"a@example.com", "b@example.com", "c@example.com"},
		"Notice",
		"Hello",
		bulkMailConfig{host: "127.0.0.1", port: 25, from: "noreply@example.com", fromName: "Forest", appName: "Forest", template: "default", bulkIntervalSeconds: 2},
	)
	if err != nil {
		t.Fatalf("dispatch user mail jobs: %v", err)
	}
	if len(sent) != 3 {
		t.Fatalf("unexpected sent emails: %#v", sent)
	}
	if len(waits) != 2 || waits[0] != "2s" || waits[1] != "2s" {
		t.Fatalf("unexpected wait durations: %#v", waits)
	}
}

func TestDispatchUserMailJobsRendersNotifyTemplate(t *testing.T) {
	oldRoot := adminProjectRoot
	adminProjectRoot = t.TempDir()
	defer func() { adminProjectRoot = oldRoot }()

	if err := os.MkdirAll(filepath.Join(adminProjectRoot, "resources", "views", "mail", "forest-v2"), 0o755); err != nil {
		t.Fatalf("mkdir mail template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adminProjectRoot, "resources", "views", "mail", "forest-v2", "notify.blade.php"), []byte(`<html><body><h1>{{$name}}</h1><div>{!! nl2br(e($content)) !!}</div></body></html>`), 0o644); err != nil {
		t.Fatalf("write notify template: %v", err)
	}

	service := (&DBService{}).WithQueueRuntime(&captureQueue{runNow: true})
	var body string
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, renderedBody string) error {
		body = renderedBody
		return nil
	}

	err := service.dispatchUserMailJobs(
		context.Background(),
		[]string{"a@example.com"},
		"Notice",
		"Line1\nLine2",
		bulkMailConfig{host: "127.0.0.1", port: 25, from: "noreply@example.com", fromName: "Forest", appName: "Forest", template: "forest-v2"},
	)
	if err != nil {
		t.Fatalf("dispatch user mail jobs: %v", err)
	}
	if !strings.Contains(body, "<html>") || !strings.Contains(body, "Line1<br>Line2") {
		t.Fatalf("expected rendered notify template body, got %q", body)
	}
}
