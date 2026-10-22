package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cfgpkg "forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func migrationMailTestConfig(t *testing.T, interval int64) string {
	t.Helper()
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"email_host":          "smtp.example.test",
		"email_port":          2525,
		"email_username":      "mail-user",
		"email_password":      "smtp-secret",
		"email_from_address":  "notify@example.test",
		"email_bulk_interval": interval,
		"email_template":      "migration-fixture",
		"app_name":            "Target site",
	})
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })
	return root
}

func TestMigrationMailSettingsUsesExistingBulkConfiguration(t *testing.T) {
	root := migrationMailTestConfig(t, 5)
	service := &DBService{}
	interval, configured, err := service.MigrationMailSettings()
	if err != nil || interval != 5 || !configured {
		t.Fatalf("settings: interval=%d configured=%v err=%v", interval, configured, err)
	}

	writeAdminJSONFixture(t, root, map[string]any{"email_bulk_interval": 0})
	interval, configured, err = service.MigrationMailSettings()
	if err != nil || interval != 0 || configured {
		t.Fatalf("empty settings: interval=%d configured=%v err=%v", interval, configured, err)
	}
	service.cfg = cfgpkg.Config{MailHost: "runtime.example.test"}
	_, configured, err = service.MigrationMailSettings()
	if err != nil || !configured {
		t.Fatalf("runtime SMTP fallback: configured=%v err=%v", configured, err)
	}
}

func TestPrepareMigrationMailDefersSendingAndSnapshotsFiveSecondInterval(t *testing.T) {
	root := migrationMailTestConfig(t, 5)
	service := (&DBService{}).WithQueueRuntime(&captureQueue{runNow: true})
	var sent []string
	var waits []time.Duration
	var outcomes []bool
	var callbackContexts []context.Context
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
		if host != "smtp.example.test" || port != 2525 || username != "mail-user" || password != "smtp-secret" {
			t.Error("SMTP configuration snapshot changed")
		}
		sent = append(sent, to)
		return nil
	}
	service.sleep = func(_ context.Context, duration time.Duration) error {
		waits = append(waits, duration)
		return nil
	}
	emails := []string{" a@example.test ", "", "b@example.test", "c@example.test"}
	interval, run, err := service.PrepareMigrationMail(context.Background(), emails, "Notice", "Hello", func(ctx context.Context, ok bool) error {
		outcomes = append(outcomes, ok)
		callbackContexts = append(callbackContexts, ctx)
		return nil
	})
	if err != nil || run == nil || interval != 5 {
		t.Fatalf("prepare: interval=%d run=%v err=%v", interval, run != nil, err)
	}
	if len(sent) != 0 || len(service.jobs.(*captureQueue).queueNames) != 0 || len(outcomes) != 0 {
		t.Fatal("preparation must not send, enqueue, or record results")
	}
	// Changing caller storage or the setting after preparation must not alter
	// this batch's captured recipients or pacing.
	emails[0] = "unrelated@example.test"
	writeAdminJSONFixture(t, root, map[string]any{"email_bulk_interval": 0})
	jobCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := run(jobCtx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(sent, []string{"a@example.test", "b@example.test", "c@example.test"}) {
		t.Fatalf("recipients: %#v", sent)
	}
	if !reflect.DeepEqual(waits, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Fatalf("expected exactly two 5-second waits, got %#v", waits)
	}
	if !reflect.DeepEqual(outcomes, []bool{true, true, true}) {
		t.Fatalf("results: %#v", outcomes)
	}
	for _, got := range callbackContexts {
		if got != jobCtx {
			t.Fatal("callback must receive the job context")
		}
	}
}

func TestPrepareMigrationMailZeroIntervalContinuesAfterSMTPFailuresAndUsesExistingLog(t *testing.T) {
	migrationMailTestConfig(t, 0)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	var calls int
	var outcomes []bool
	smtpErr := errors.New("SMTP failure containing smtp-secret")
	for _, item := range []struct {
		email string
		err   any
	}{{"a@example.test", smtpErr.Error()}, {"b@example.test", nil}, {"c@example.test", smtpErr.Error()}} {
		mock.ExpectExec("INSERT INTO v2_mail_log").WithArgs(item.email, "Notice", "notify", item.err, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
		calls++
		if calls == 2 {
			return nil
		}
		return smtpErr
	}
	service.sleep = func(context.Context, time.Duration) error {
		t.Fatal("zero interval must not wait")
		return nil
	}
	interval, run, err := service.PrepareMigrationMail(context.Background(), []string{"a@example.test", "b@example.test", "c@example.test"}, "Notice", "Hello", func(_ context.Context, success bool) error {
		outcomes = append(outcomes, success)
		return nil
	})
	if err != nil || interval != 0 {
		t.Fatalf("prepare: interval=%d err=%v", interval, err)
	}
	err = run(context.Background())
	if err == nil || strings.Contains(err.Error(), "smtp-secret") || err.Error() != "迁移通知邮件未全部发送完成" {
		t.Fatalf("expected fixed safe error, got %v", err)
	}
	if calls != 3 || !reflect.DeepEqual(outcomes, []bool{false, true, false}) {
		t.Fatalf("calls=%d results=%#v", calls, outcomes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareMigrationMailStopsOnResultPersistenceFailure(t *testing.T) {
	migrationMailTestConfig(t, 5)
	service := &DBService{}
	var calls, results int
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
		calls++
		return nil
	}
	service.sleep = func(context.Context, time.Duration) error {
		t.Fatal("must stop before waiting after callback failure")
		return nil
	}
	_, run, err := service.PrepareMigrationMail(context.Background(), []string{"a@example.test", "b@example.test"}, "Notice", "Hello", func(context.Context, bool) error {
		results++
		return errors.New("sensitive database failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	err = run(context.Background())
	if err == nil || strings.Contains(err.Error(), "sensitive") || calls != 1 || results != 1 {
		t.Fatalf("calls=%d callbacks=%d err=%v", calls, results, err)
	}
}

func TestPrepareMigrationMailCancellation(t *testing.T) {
	for _, mode := range []string{"prepare", "before_run", "before_next", "during_wait"} {
		t.Run(mode, func(t *testing.T) {
			interval := int64(0)
			if mode == "during_wait" {
				interval = 5
			}
			migrationMailTestConfig(t, interval)
			service := &DBService{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls, callbacks int
			service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
				calls++
				if mode == "before_next" {
					cancel()
				}
				return nil
			}
			service.sleep = func(ctx context.Context, d time.Duration) error {
				cancel()
				return ctx.Err()
			}
			if mode == "prepare" {
				cancel()
			}
			_, run, err := service.PrepareMigrationMail(ctx, []string{"a@example.test", "b@example.test"}, "Notice", "Hello", func(context.Context, bool) error {
				callbacks++
				return nil
			})
			if mode == "prepare" {
				if !errors.Is(err, context.Canceled) || run != nil {
					t.Fatalf("prepare cancellation: run=%v err=%v", run != nil, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before_run" {
				cancel()
			}
			err = run(ctx)
			wantCalls := 1
			if mode == "before_run" {
				wantCalls = 0
			}
			if !errors.Is(err, context.Canceled) || calls != wantCalls || callbacks != wantCalls {
				t.Fatalf("calls=%d callbacks=%d err=%v", calls, callbacks, err)
			}
		})
	}
}

func TestPrepareMigrationMailRendersNotifyTemplate(t *testing.T) {
	root := migrationMailTestConfig(t, 0)
	dir := filepath.Join(root, "resources", "views", "mail", "migration-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notify.blade.php"), []byte(`<html><h1>{{$name}}</h1><p>{!! nl2br(e($content)) !!}</p></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &DBService{}
	var rendered string
	service.mailSender = func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
		rendered = body
		return nil
	}
	_, run, err := service.PrepareMigrationMail(context.Background(), []string{"a@example.test"}, "Notice", "Line1\n<Line2>", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "<h1>Target site</h1>") || !strings.Contains(rendered, "Line1<br>&lt;Line2&gt;") {
		t.Fatalf("unexpected notify body: %q", rendered)
	}
}

func TestPrepareMigrationMailRejectsUnconfiguredSMTPAndInvalidRequests(t *testing.T) {
	root := migrationMailTestConfig(t, 0)
	service := &DBService{}
	for _, tc := range []struct {
		name    string
		emails  []string
		subject string
		content string
	}{
		{"empty_recipients", []string{" "}, "Notice", "Hello"},
		{"empty_subject", []string{"a@example.test"}, " ", "Hello"},
		{"empty_content", []string{"a@example.test"}, "Notice", " "},
		{"header_injection", []string{"a@example.test"}, "Notice\r\nBcc: attacker@example.test", "Hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, run, err := service.PrepareMigrationMail(context.Background(), tc.emails, tc.subject, tc.content, nil)
			if err == nil || run != nil {
				t.Fatalf("run=%v err=%v", run != nil, err)
			}
		})
	}
	writeAdminJSONFixture(t, root, map[string]any{"email_bulk_interval": 5})
	_, run, err := service.PrepareMigrationMail(context.Background(), []string{"a@example.test"}, "Notice", "Hello", nil)
	if err == nil || err.Error() != "邮件服务未配置" || run != nil {
		t.Fatalf("unconfigured SMTP: run=%v err=%v", run != nil, err)
	}
}

func TestMigrationMailConfigErrorIsRedacted(t *testing.T) {
	root := migrationMailTestConfig(t, 5)
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), []byte(`{"email_password": "smtp-secret", invalid}`), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &DBService{}
	_, _, settingsErr := service.MigrationMailSettings()
	_, run, prepareErr := service.PrepareMigrationMail(context.Background(), []string{"a@example.test"}, "Notice", "Hello", nil)
	for _, err := range []error{settingsErr, prepareErr} {
		if err == nil || err.Error() != "读取邮件设置失败" {
			t.Fatalf("expected fixed safe error, got %v", err)
		}
	}
	if run != nil {
		t.Fatal("invalid config must not prepare a runnable job")
	}
}
