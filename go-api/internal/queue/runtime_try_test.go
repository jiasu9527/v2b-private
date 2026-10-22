package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeTryEnqueueRejectsUnavailableStoppedAndNilJob(t *testing.T) {
	job := func(context.Context) error { return nil }
	var missing *Runtime
	if err := missing.TryEnqueue("send_email_mass", "mail", job); err == nil || err.Error() != "queue runtime unavailable" {
		t.Fatalf("nil runtime: %v", err)
	}
	rt := NewRuntime(1, 1)
	if err := rt.TryEnqueue("send_email_mass", "mail", job); err == nil || err.Error() != "queue runtime not started" {
		t.Fatalf("stopped runtime: %v", err)
	}
	if err := rt.TryEnqueue("send_email_mass", "mail", nil); err == nil || err.Error() != "queue job is nil" {
		t.Fatalf("nil job: %v", err)
	}
	if snapshot := rt.Snapshot(); snapshot.CurrentJobs != 0 || len(snapshot.Queues) != 0 {
		t.Fatalf("rejected jobs affected snapshot: %#v", snapshot)
	}
}

func TestRuntimeTryEnqueueIsNonblockingAndDoesNotTrackRejectedJobs(t *testing.T) {
	rt := NewRuntime(1, 1)
	rt.Start()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		rt.Shutdown(context.Background())
	})
	if err := rt.TryEnqueue("", "active", func(context.Context) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted job did not reach worker")
	}
	var acceptedRan, rejectedRan atomic.Bool
	if err := rt.TryEnqueue("send_email_mass", "buffered", func(context.Context) error {
		acceptedRan.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("available buffer: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		result <- rt.TryEnqueue("rejected_queue", "overflow", func(context.Context) error {
			rejectedRan.Store(true)
			return nil
		})
	}()
	select {
	case err := <-result:
		if err == nil || err.Error() != "queue runtime full" {
			t.Fatalf("full queue error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TryEnqueue blocked on a full queue")
	}
	snapshot := rt.Snapshot()
	if snapshot.CurrentJobs != 2 || len(snapshot.Queues) != 2 {
		t.Fatalf("unexpected tracked jobs: %#v", snapshot)
	}
	for _, queue := range snapshot.Queues {
		switch queue.Name {
		case "default":
			if queue.Processes != 1 || queue.Length != 0 {
				t.Fatalf("active job: %#v", queue)
			}
		case "send_email_mass":
			if queue.Processes != 0 || queue.Length != 1 {
				t.Fatalf("buffered job: %#v", queue)
			}
		default:
			t.Fatalf("unexpected rejected queue in snapshot: %#v", queue)
		}
	}
	if acceptedRan.Load() || rejectedRan.Load() {
		t.Fatal("buffered/rejected job executed synchronously")
	}
	releaseOnce.Do(func() { close(release) })
	rt.Shutdown(context.Background())
	if !acceptedRan.Load() || rejectedRan.Load() {
		t.Fatalf("accepted=%v rejected=%v", acceptedRan.Load(), rejectedRan.Load())
	}
	if snapshot := rt.Snapshot(); snapshot.CurrentJobs != 0 || snapshot.ProcessedLastHour != 2 {
		t.Fatalf("completed snapshot: %#v", snapshot)
	}
	if err := rt.TryEnqueue("send_email_mass", "after_shutdown", func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted job after shutdown")
	}
}

func TestRuntimeTryEnqueueConcurrentShutdown(t *testing.T) {
	for round := 0; round < 30; round++ {
		rt := NewRuntime(1, 16)
		rt.Start()
		workerStarted := make(chan struct{})
		if err := rt.TryEnqueue("send_email_mass", "blocker", func(ctx context.Context) error {
			close(workerStarted)
			<-ctx.Done()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-workerStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not start")
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var accepted, executed atomic.Int64
		for sender := 0; sender < 8; sender++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for attempt := 0; attempt < 40; attempt++ {
					err := rt.TryEnqueue("send_email_mass", "race", func(context.Context) error {
						executed.Add(1)
						return nil
					})
					if err == nil {
						accepted.Add(1)
					} else if err.Error() != "queue runtime full" && err.Error() != "queue runtime not started" {
						t.Errorf("unexpected queue error: %v", err)
					}
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rt.Shutdown(context.Background())
		}()
		close(start)
		wg.Wait()
		if accepted.Load() != executed.Load() {
			t.Fatalf("round %d: accepted=%d executed=%d", round, accepted.Load(), executed.Load())
		}
		if snapshot := rt.Snapshot(); snapshot.Running || snapshot.CurrentJobs != 0 || snapshot.ProcessedLastHour != accepted.Load()+1 {
			t.Fatalf("round %d: snapshot %#v", round, snapshot)
		}
	}
}
