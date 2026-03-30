package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeReportsRunningAndTracksQueuedWork(t *testing.T) {
	rt := NewRuntime(1, 8)
	if snapshot := rt.Snapshot(); snapshot.Running {
		t.Fatalf("expected runtime stopped before start")
	}

	rt.Start()
	defer rt.Shutdown(context.Background())

	block := make(chan struct{})
	if err := rt.Enqueue("send_email", "job-1", func(context.Context) error {
		<-block
		return nil
	}); err != nil {
		t.Fatalf("enqueue first job: %v", err)
	}
	if err := rt.Enqueue("send_email", "job-2", func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("enqueue second job: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot := rt.Snapshot()
		if snapshot.Running && snapshot.Workers == 1 && snapshot.CurrentJobs >= 1 && len(snapshot.Queues) == 1 {
			queue := snapshot.Queues[0]
			if queue.Processes != 1 || queue.Length != 1 {
				if time.Now().After(deadline) {
					t.Fatalf("timed out waiting for active and pending job split, got %#v", snapshot)
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if queue.Name != "send_email" {
				t.Fatalf("expected send_email queue, got %q", queue.Name)
			}
			close(block)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for queue snapshot, got %#v", rt.Snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimeTracksProcessedAndFailedJobs(t *testing.T) {
	rt := NewRuntime(1, 4)
	rt.Start()
	defer rt.Shutdown(context.Background())

	done := make(chan struct{}, 2)
	if err := rt.Enqueue("send_email", "ok", func(context.Context) error {
		done <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("enqueue ok job: %v", err)
	}
	if err := rt.Enqueue("send_email_mass", "fail", func(context.Context) error {
		done <- struct{}{}
		return errors.New("boom")
	}); err != nil {
		t.Fatalf("enqueue fail job: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(done) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(done) < 2 {
		t.Fatalf("timed out waiting for queued jobs to finish")
	}

	snapshot := rt.Snapshot()
	if snapshot.ProcessedLastHour != 1 {
		t.Fatalf("expected one processed job, got %d", snapshot.ProcessedLastHour)
	}
	if snapshot.FailedLast7Days != 1 {
		t.Fatalf("expected one failed job, got %d", snapshot.FailedLast7Days)
	}
	if snapshot.MaxThroughputQueue != "send_email" {
		t.Fatalf("expected max throughput queue send_email, got %q", snapshot.MaxThroughputQueue)
	}
	if snapshot.MaxRuntimeQueue == "" {
		t.Fatalf("expected max runtime queue to be set")
	}
}
