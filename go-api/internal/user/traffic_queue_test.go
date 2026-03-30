package user

import (
	"context"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/queue"
)

func TestQueueTrafficReportEnqueuesTrafficAndStatJobs(t *testing.T) {
	service := NewDBService(config.Config{}, nil).WithQueueRuntime(&captureTrafficQueue{})

	err := service.QueueTrafficReport(context.Background(), TrafficReport{
		ServerID:   7,
		ServerType: "vmess",
		ServerRate: 1.5,
		Traffic: map[int64]TrafficUsage{
			11: {U: 10, D: 20},
			12: {U: 30, D: 40},
		},
	})
	if err != nil {
		t.Fatalf("queue traffic report: %v", err)
	}

	q := service.jobs.(*captureTrafficQueue)
	if len(q.queueNames) != 2 || q.queueNames[0] != "traffic_fetch" || q.queueNames[1] != "stat" {
		t.Fatalf("unexpected queue names: %#v", q.queueNames)
	}
}

type captureTrafficQueue struct {
	queueNames []string
}

func (c *captureTrafficQueue) Enqueue(queueName, jobName string, fn queue.JobFunc) error {
	c.queueNames = append(c.queueNames, queueName)
	return nil
}

func (c *captureTrafficQueue) Snapshot() queue.Snapshot {
	return queue.Snapshot{}
}
