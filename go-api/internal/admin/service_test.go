package admin

import (
	"database/sql"
	"strings"
	"testing"

	"forest/go-api/internal/queue"
)

type stubQueueRuntime struct {
	snapshot queue.Snapshot
}

func (s stubQueueRuntime) Enqueue(string, string, queue.JobFunc) error {
	return nil
}

func (s stubQueueRuntime) Snapshot() queue.Snapshot {
	return s.snapshot
}

func TestDBServiceGetQueueStatsUsesQueueRuntimeSnapshot(t *testing.T) {
	service := (&DBService{db: &sql.DB{}}).WithQueueRuntime(stubQueueRuntime{
		snapshot: queue.Snapshot{
			Running:            true,
			Workers:            3,
			CurrentJobs:        7,
			ProcessedLastHour:  15,
			FailedLast7Days:    2,
			MaxRuntimeQueue:    "send_email_mass",
			MaxThroughputQueue: "send_email",
			Queues: []queue.QueueSnapshot{
				{Name: "send_email", Processes: 1, Length: 4, Wait: 9},
			},
		},
	})

	stats, err := service.GetQueueStats(t.Context())
	if err != nil {
		t.Fatalf("get queue stats: %v", err)
	}
	if !stats.Status || stats.Processes != 3 || stats.JobsPerMinute != 7 {
		t.Fatalf("unexpected queue stats: %#v", stats)
	}
	if stats.RecentJobs != 15 || stats.FailedJobs != 2 {
		t.Fatalf("unexpected queue counters: %#v", stats)
	}
	if stats.QueueWithMaxRuntime != "send_email_mass" || stats.QueueWithMaxThroughput != "send_email" {
		t.Fatalf("unexpected queue max values: %#v", stats)
	}
	if len(stats.Wait) != 1 || stats.Wait[0].Name != "send_email" || stats.Wait[0].Time != 9 {
		t.Fatalf("unexpected queue wait stats: %#v", stats.Wait)
	}
}

func TestDBServiceGetQueueWorkloadUsesQueueRuntimeSnapshot(t *testing.T) {
	service := (&DBService{db: &sql.DB{}}).WithQueueRuntime(stubQueueRuntime{
		snapshot: queue.Snapshot{
			Running: true,
			Queues: []queue.QueueSnapshot{
				{Name: "send_email", Processes: 2, Length: 5, Wait: 11},
				{Name: "send_email_mass", Processes: 1, Length: 3, Wait: 7},
			},
		},
	})

	workload, err := service.GetQueueWorkload(t.Context())
	if err != nil {
		t.Fatalf("get queue workload: %v", err)
	}
	if len(workload) < 6 {
		t.Fatalf("expected built-in queue workload rows, got %#v", workload)
	}

	byName := make(map[string]map[string]any, len(workload))
	for _, row := range workload {
		name, _ := row["name"].(string)
		byName[name] = row
	}

	if byName["send_email"]["processes"] != int64(2) || byName["send_email"]["length"] != int64(5) || byName["send_email"]["wait"] != int64(11) {
		t.Fatalf("unexpected send_email workload row: %#v", byName["send_email"])
	}
	if byName["send_email_mass"]["processes"] != int64(1) || byName["send_email_mass"]["length"] != int64(3) || byName["send_email_mass"]["wait"] != int64(7) {
		t.Fatalf("unexpected send_email_mass workload row: %#v", byName["send_email_mass"])
	}
}

func TestDBServiceGetQueueWorkloadIncludesKnownQueuesWhenIdle(t *testing.T) {
	service := (&DBService{db: &sql.DB{}}).WithQueueRuntime(stubQueueRuntime{
		snapshot: queue.Snapshot{
			Running: true,
		},
	})

	workload, err := service.GetQueueWorkload(t.Context())
	if err != nil {
		t.Fatalf("get queue workload: %v", err)
	}
	if len(workload) < 6 {
		t.Fatalf("expected built-in queue rows when idle, got %#v", workload)
	}

	byName := make(map[string]map[string]any, len(workload))
	for _, row := range workload {
		name, _ := row["name"].(string)
		byName[name] = row
	}

	for _, queueName := range []string{"order_handle", "send_email", "send_email_mass", "send_telegram", "stat", "traffic_fetch"} {
		row, ok := byName[queueName]
		if !ok {
			t.Fatalf("expected queue %q in idle workload: %#v", queueName, workload)
		}
		if row["processes"] != int64(0) || row["length"] != int64(0) || row["wait"] != int64(0) {
			t.Fatalf("expected idle queue row to be zeroed for %q, got %#v", queueName, row)
		}
	}
}

func TestAdminUserListJSONQueryUsesNonConflictingAlias(t *testing.T) {
	query := adminUserListJSONQuery("WHERE u.id = $1", "u.id", "ASC", 2, 3)

	if strings.Contains(query, "row_to_json(t)") {
		t.Fatalf("expected user list query to avoid row_to_json(t), got %s", query)
	}
	if !strings.Contains(query, "row_to_json(user_row)") {
		t.Fatalf("expected user list query to use user_row alias, got %s", query)
	}
}

func TestAdminUserJSONMapQueryUsesNonConflictingAlias(t *testing.T) {
	query := adminUserJSONMapQuery("SELECT * FROM v2_user WHERE id = $1 LIMIT 1")

	if strings.Contains(query, "row_to_json(t)") {
		t.Fatalf("expected user map query to avoid row_to_json(t), got %s", query)
	}
	if !strings.Contains(query, "row_to_json(user_row)") {
		t.Fatalf("expected user map query to use user_row alias, got %s", query)
	}
}
