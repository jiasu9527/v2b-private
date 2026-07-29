package admin

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

type clientEntryMonitorProgressTestNotifier struct {
	err   error
	calls int
}

func (n *clientEntryMonitorProgressTestNotifier) NotifyAdmins(context.Context, string, bool) error {
	return nil
}

func (n *clientEntryMonitorProgressTestNotifier) EditChatMessage(context.Context, int64, int64, string) error {
	n.calls++
	return n.err
}

type captureClientEntryMonitorUnixArgument struct {
	value *int64
}

func (argument captureClientEntryMonitorUnixArgument) Match(value driver.Value) bool {
	unix, ok := value.(int64)
	if !ok {
		return false
	}
	*argument.value = unix
	return true
}

func expectClientEntryMonitorProgressClaim(mock sqlmock.Sqlmock, attempts int, authorized bool) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, requested_by_user_id, request_chat_id, progress_message_id, progress_attempts.*FROM v2_client_entry_monitor_run.*FOR UPDATE SKIP LOCKED`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "requested_by_user_id", "request_chat_id", "progress_message_id", "progress_attempts"}).
			AddRow(int64(91), int64(9), int64(12345), int64(456), attempts))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET progress_attempts = progress_attempts \+ 1, progress_next_attempt_at = \$2`).
		WithArgs(int64(91), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT EXISTS \(.*FROM v2_user.*WHERE id = \$1 AND telegram_id = \$2 AND banned = 0.*is_admin = 1 OR is_staff = 1`).
		WithArgs(int64(9), int64(12345)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(authorized))
}

func expectClientEntryMonitorProgressRun(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`(?s)SELECT id, policy_ids, expected_pairs, status, expected_results,.*FROM v2_client_entry_monitor_run WHERE id = \$1`).
		WithArgs(int64(91)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_ids", "expected_pairs", "status", "expected_results", "received_results",
			"progress_message_id", "progress_reported_results", "progress_reported_status", "progress_next_attempt_at", "progress_last_error",
			"started_at", "completed_at", "created_at",
		}).AddRow(int64(91), `[12]`, `[{"target_id":5,"probe_id":7,"target_version":2,"policy_id":12,"policy_name":"华东规则组","probe_name":"东京探针"}]`,
			"completed", int64(1), int64(1), int64(456), int64(0), "running", int64(0), "", int64(100), int64(101), int64(100)))
	mock.ExpectQuery(`(?s)SELECT result.id, result.policy_id, result.policy_name, result.target_id.*FROM v2_client_entry_monitor_run_result result.*WHERE result.run_id = \$1.*LIMIT \$2`).
		WithArgs(int64(91), clientEntryMonitorRunResultListLimit).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_id", "policy_name", "target_id", "target_name", "host", "port", "probe_id", "probe_name",
			"success", "latency_ms", "error", "resolved_ip", "reported_at",
		}).AddRow(int64(1), int64(12), "华东规则组", int64(5), "入口", "entry.example.com", int64(443), int64(7), "东京探针",
			int64(1), int64(12), "", "203.0.113.1", int64(101)))
}

func TestClientEntryMonitorProgressSuccessResetsAttempts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	notifier := &clientEntryMonitorProgressTestNotifier{}
	service := &DBService{db: db, dnsFailoverNotifier: notifier}
	expectClientEntryMonitorProgressClaim(mock, 2, true)
	expectClientEntryMonitorProgressRun(mock)
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET progress_reported_results = \$2, progress_reported_status = \$3,.*progress_attempts = 0, progress_next_attempt_at = 0`).
		WithArgs(int64(91), int64(1), "completed", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processed, err := service.notifyNextClientEntryMonitorProgress(context.Background(), conn)
	if err != nil || !processed {
		t.Fatalf("notify progress = (%v, %v)", processed, err)
	}
	if notifier.calls != 1 {
		t.Fatalf("edit calls = %d, want 1", notifier.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorProgressTemporaryFailureUsesExponentialBackoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	notifier := &clientEntryMonitorProgressTestNotifier{err: errors.New("telegram gateway timeout")}
	service := &DBService{db: db, dnsFailoverNotifier: notifier}
	const attempts = 3
	expectClientEntryMonitorProgressClaim(mock, attempts, true)
	expectClientEntryMonitorProgressRun(mock)
	var nextAttemptAt int64
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET progress_next_attempt_at = \$2, progress_last_error = \$3`).
		WithArgs(int64(91), captureClientEntryMonitorUnixArgument{value: &nextAttemptAt}, "telegram gateway timeout", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	startedAt := time.Now().Unix()

	processed, err := service.notifyNextClientEntryMonitorProgress(context.Background(), conn)
	if err != nil || !processed {
		t.Fatalf("notify progress = (%v, %v)", processed, err)
	}
	wantDelay := int64(dnsFailoverRetryDelay(attempts) / time.Second)
	if nextAttemptAt < startedAt+wantDelay || nextAttemptAt > time.Now().Unix()+wantDelay+1 {
		t.Fatalf("next attempt = %d, want about now + %ds", nextAttemptAt, wantDelay)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorProgressPermanentFailureStopsRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	const telegramError = "Bad Request: message to edit not found"
	notifier := &clientEntryMonitorProgressTestNotifier{err: errors.New(telegramError)}
	service := &DBService{db: db, dnsFailoverNotifier: notifier}
	expectClientEntryMonitorProgressClaim(mock, 1, true)
	expectClientEntryMonitorProgressRun(mock)
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET progress_message_id = NULL, progress_attempts = 0, progress_next_attempt_at = 0,.*progress_last_error = \$2`).
		WithArgs(int64(91), telegramError, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processed, err := service.notifyNextClientEntryMonitorProgress(context.Background(), conn)
	if err != nil || !processed {
		t.Fatalf("notify progress = (%v, %v)", processed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorProgressAuthorizationRevokedStopsRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	notifier := &clientEntryMonitorProgressTestNotifier{}
	service := &DBService{db: db, dnsFailoverNotifier: notifier}
	expectClientEntryMonitorProgressClaim(mock, 4, false)
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET progress_message_id = NULL, progress_attempts = 0, progress_next_attempt_at = 0`).
		WithArgs(int64(91), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processed, err := service.notifyNextClientEntryMonitorProgress(context.Background(), conn)
	if err != nil || !processed {
		t.Fatalf("notify progress = (%v, %v)", processed, err)
	}
	if notifier.calls != 0 {
		t.Fatalf("edit calls = %d, want 0", notifier.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPermanentClientEntryMonitorProgressErrors(t *testing.T) {
	for _, message := range []string{
		"Bad Request: message to edit not found",
		"Bad Request: message can't be edited",
		"Forbidden: bot was blocked by the user",
		"Bad Request: chat not found",
	} {
		if !isPermanentClientEntryMonitorProgressError(errors.New(message)) {
			t.Errorf("error %q was not classified as permanent", message)
		}
	}
	for _, message := range []string{"telegram gateway timeout", "connection reset by peer", "Too Many Requests: retry later"} {
		if isPermanentClientEntryMonitorProgressError(errors.New(message)) {
			t.Errorf("error %q was classified as permanent", message)
		}
	}
	if isPermanentClientEntryMonitorProgressError(nil) || isPermanentClientEntryMonitorProgressError(errors.New(strings.Repeat(" ", 3))) {
		t.Fatal("empty error was classified as permanent")
	}
}
