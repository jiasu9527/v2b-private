package admin

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfgpkg "forest/go-api/internal/config"
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
	if stats.QueueWithMaxRuntime != "邮件群发队列" || stats.QueueWithMaxThroughput != "邮件队列" {
		t.Fatalf("unexpected queue max values: %#v", stats)
	}
	if len(stats.Wait) != 1 || stats.Wait[0].Name != "邮件队列" || stats.Wait[0].Time != 9 {
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
	if len(workload) < 7 {
		t.Fatalf("expected built-in queue workload rows, got %#v", workload)
	}

	byQueue := make(map[string]map[string]any, len(workload))
	for _, row := range workload {
		name, _ := row["queue"].(string)
		byQueue[name] = row
	}

	if byQueue["send_email"]["name"] != "邮件队列" || byQueue["send_email"]["processes"] != int64(2) || byQueue["send_email"]["length"] != int64(5) || byQueue["send_email"]["wait"] != int64(11) {
		t.Fatalf("unexpected send_email workload row: %#v", byQueue["send_email"])
	}
	if byQueue["send_email_mass"]["name"] != "邮件群发队列" || byQueue["send_email_mass"]["processes"] != int64(1) || byQueue["send_email_mass"]["length"] != int64(3) || byQueue["send_email_mass"]["wait"] != int64(7) {
		t.Fatalf("unexpected send_email_mass workload row: %#v", byQueue["send_email_mass"])
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
	if len(workload) < 7 {
		t.Fatalf("expected built-in queue rows when idle, got %#v", workload)
	}

	byQueue := make(map[string]map[string]any, len(workload))
	for _, row := range workload {
		name, _ := row["queue"].(string)
		byQueue[name] = row
	}

	for _, queueName := range []string{"order_handle", "send_email", "send_email_mass", "send_telegram", "stat", "stat_refresh", "maintenance_cleanup", "traffic_fetch"} {
		row, ok := byQueue[queueName]
		if !ok {
			t.Fatalf("expected queue %q in idle workload: %#v", queueName, workload)
		}
		if row["processes"] != int64(0) || row["length"] != int64(0) || row["wait"] != int64(0) {
			t.Fatalf("expected idle queue row to be zeroed for %q, got %#v", queueName, row)
		}
	}
	if byQueue["maintenance_cleanup"]["name"] != "自动清理队列" {
		t.Fatalf("expected localized maintenance queue name, got %#v", byQueue["maintenance_cleanup"])
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

func TestBuildAdminUserSubscribeURLUsesRuntimeConfig(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"subscribe_url":         "https://old.example.com",
		"subscribe_path":        "/old-sub",
		"show_subscribe_method": 0,
	})
	mustMkdirAll(t, filepath.Join(root, "go-api"))

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()
	if err := os.Chdir(filepath.Join(root, "go-api")); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	cfg := cfgpkg.Load()
	runtimeState := cfgpkg.NewRuntimeState(cfg)
	service := (&DBService{cfg: cfg}).WithRuntimeConfig(runtimeState)

	writeAdminJSONFixture(t, root, map[string]any{
		"subscribe_url":         "https://sub.example.com",
		"subscribe_path":        "/custom-sub",
		"show_subscribe_method": 0,
	})
	runtimeState.Reload()

	got, err := service.buildAdminUserSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build admin subscribe url: %v", err)
	}
	want := "https://sub.example.com/custom-sub?token=token-1"
	if got != want {
		t.Fatalf("expected runtime subscribe url %q, got %q", want, got)
	}
}

func TestBuildAdminUserSubscribeURLTrimsTrailingSlashForQueryToken(t *testing.T) {
	service := &DBService{cfg: cfgpkg.Config{
		SubscribeURL:  "https://sub.example.com",
		SubscribePath: "/custom-sub/",
	}}

	got, err := service.buildAdminUserSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build admin subscribe url: %v", err)
	}
	want := "https://sub.example.com/custom-sub?token=token-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildAdminUserSubscribeURLCanPutTokenInPath(t *testing.T) {
	service := &DBService{cfg: cfgpkg.Config{
		SubscribeURL:         "https://sub.example.com",
		SubscribePath:        "/custom-sub",
		SubscribeTokenInPath: true,
	}}

	got, err := service.buildAdminUserSubscribeURL(context.Background(), 1, "token-1")
	if err != nil {
		t.Fatalf("build admin subscribe url: %v", err)
	}
	want := "https://sub.example.com/custom-sub/token-1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if strings.Contains(got, "?token=") {
		t.Fatalf("expected admin subscribe url to omit token query, got %q", got)
	}
}

func TestNotifyURLUsesRuntimeConfig(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"app_url": "https://old.example.com",
	})
	mustMkdirAll(t, filepath.Join(root, "go-api"))

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()
	if err := os.Chdir(filepath.Join(root, "go-api")); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	cfg := cfgpkg.Load()
	runtimeState := cfgpkg.NewRuntimeState(cfg)
	service := (&DBService{cfg: cfg}).WithRuntimeConfig(runtimeState)

	writeAdminJSONFixture(t, root, map[string]any{
		"app_url": "https://panel.example.com",
	})
	runtimeState.Reload()

	got := service.notifyURL("stripe", "pay-uuid", sql.NullString{})
	want := "https://panel.example.com/api/v1/guest/payment/notify/stripe/pay-uuid"
	if got != want {
		t.Fatalf("expected runtime notify url %q, got %q", want, got)
	}
}
