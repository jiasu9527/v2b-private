package background

import (
	"context"
	"testing"

	"forest/go-api/internal/queue"
)

type captureQueue struct {
	queueNames []string
	jobNames   []string
	runNow     bool
	snapshot   queue.Snapshot
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
	return c.snapshot
}

type fakeHeartbeater struct {
	calls int
}

func (f *fakeHeartbeater) TouchScheduleHeartbeat(context.Context) error {
	f.calls++
	return nil
}

type fakeOrderProcessor struct {
	calls int
}

func (f *fakeOrderProcessor) HandlePendingOrders(context.Context) error {
	f.calls++
	return nil
}

type fakeStatProcessor struct {
	calls int
}

func (f *fakeStatProcessor) RefreshLegacyStats(context.Context) error {
	f.calls++
	return nil
}

func TestRunnerRunOnceEnqueuesOrderHandleAndStatJobs(t *testing.T) {
	q := &captureQueue{runNow: true}
	heartbeat := &fakeHeartbeater{}
	orders := &fakeOrderProcessor{}
	stats := &fakeStatProcessor{}

	runner := NewRunner(q, heartbeat, orders, stats)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}

	if heartbeat.calls != 1 {
		t.Fatalf("expected one heartbeat, got %d", heartbeat.calls)
	}
	if orders.calls != 1 {
		t.Fatalf("expected one order sweep, got %d", orders.calls)
	}
	if stats.calls != 1 {
		t.Fatalf("expected one stat refresh, got %d", stats.calls)
	}
	if len(q.queueNames) != 2 || q.queueNames[0] != "order_handle" || q.queueNames[1] != "stat_refresh" {
		t.Fatalf("unexpected queue names: %#v", q.queueNames)
	}
}

func TestRunnerRunOnceSkipsBusyQueuesButStillRefreshesDailyStats(t *testing.T) {
	q := &captureQueue{
		runNow: true,
		snapshot: queue.Snapshot{
			Queues: []queue.QueueSnapshot{
				{Name: "order_handle", Length: 1},
				{Name: "stat", Processes: 1},
			},
		},
	}
	heartbeat := &fakeHeartbeater{}
	orders := &fakeOrderProcessor{}
	stats := &fakeStatProcessor{}

	runner := NewRunner(q, heartbeat, orders, stats)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once: %v", err)
	}

	if heartbeat.calls != 1 {
		t.Fatalf("expected one heartbeat, got %d", heartbeat.calls)
	}
	if orders.calls != 0 {
		t.Fatalf("expected order sweep skipped, got %d", orders.calls)
	}
	if stats.calls != 1 {
		t.Fatalf("expected stat refresh to run on dedicated queue, got %d", stats.calls)
	}
	if len(q.queueNames) != 1 || q.queueNames[0] != "stat_refresh" {
		t.Fatalf("expected only stat refresh queue job, got %#v", q.queueNames)
	}
}
