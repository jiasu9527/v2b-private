package admin

import (
	"context"
	"testing"

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

func TestDispatchUserMailJobsEnqueuesEachEmail(t *testing.T) {
	service := (&DBService{}).WithQueueRuntime(&captureQueue{runNow: true})
	var sent []string
	service.mailSender = func(host string, port int, encryption, username, password, from, to, subject, body string) error {
		sent = append(sent, to)
		return nil
	}

	err := service.dispatchUserMailJobs(
		context.Background(),
		[]string{"a@example.com", "b@example.com"},
		"Notice",
		"Hello",
		bulkMailConfig{host: "127.0.0.1", port: 25, from: "noreply@example.com"},
	)
	if err != nil {
		t.Fatalf("dispatch user mail jobs: %v", err)
	}

	q := service.jobs.(*captureQueue)
	if len(q.queueNames) != 2 || q.queueNames[0] != "send_email_mass" || q.queueNames[1] != "send_email_mass" {
		t.Fatalf("unexpected queue names: %#v", q.queueNames)
	}
	if len(sent) != 2 || sent[0] != "a@example.com" || sent[1] != "b@example.com" {
		t.Fatalf("unexpected sent emails: %#v", sent)
	}
}
