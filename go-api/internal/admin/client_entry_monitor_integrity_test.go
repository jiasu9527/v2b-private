package admin

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLockClientEntryMonitorRevisionRejectsStaleSave(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1 FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(8)))
	if err := lockClientEntryMonitorRevision(context.Background(), tx, 7); !errors.Is(err, ErrClientEntryMonitorRevisionConflict) {
		t.Fatalf("revision conflict error = %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestLockClientEntryMonitorRunStartIsSerializedAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryMonitorRunLockKey).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_monitor_run WHERE request_key = \$1`).
		WithArgs("telegram-callback-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))

	runID, exists, err := lockClientEntryMonitorRunStart(context.Background(), tx, "telegram-callback-1")
	if err != nil {
		t.Fatalf("lockClientEntryMonitorRunStart: %v", err)
	}
	if !exists || runID != 42 {
		t.Fatalf("existing run = (%d, %v), want (42, true)", runID, exists)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorStateFreshUsesTwoExpectedCycles(t *testing.T) {
	now := int64(100)
	for _, test := range []struct {
		name       string
		reportedAt sql.NullInt64
		want       bool
	}{
		{name: "fresh at inclusive cutoff", reportedAt: sql.NullInt64{Int64: 34, Valid: true}, want: true},
		{name: "stale before cutoff", reportedAt: sql.NullInt64{Int64: 33, Valid: true}, want: false},
		{name: "future timestamp", reportedAt: sql.NullInt64{Int64: 101, Valid: true}, want: false},
		{name: "missing timestamp", reportedAt: sql.NullInt64{}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := clientEntryMonitorStateFresh(test.reportedAt, now, 30, 3000); got != test.want {
				t.Fatalf("fresh = %v, want %v", got, test.want)
			}
		})
	}
}

func TestClientEntryMonitorRefreshRunsInitiallyAndWhenDirty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	service.clientEntryEnsureOnce.Do(func() {})

	expectEmptyClientEntryMonitorRefresh(mock)
	if err := service.refreshClientEntryMonitorTargetsIfDue(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if service.clientEntryMonitorAt.IsZero() || service.clientEntryMonitorDirty {
		t.Fatalf("refresh state = at %v dirty %v", service.clientEntryMonitorAt, service.clientEntryMonitorDirty)
	}
	if err := service.refreshClientEntryMonitorTargetsIfDue(context.Background()); err != nil {
		t.Fatalf("throttled refresh: %v", err)
	}

	service.markClientEntryMonitorTargetsDirty()
	expectEmptyClientEntryMonitorRefresh(mock)
	if err := service.refreshClientEntryMonitorTargetsIfDue(context.Background()); err != nil {
		t.Fatalf("dirty refresh: %v", err)
	}
	if service.clientEntryMonitorDirty {
		t.Fatal("dirty flag was not cleared after successful refresh")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorEndpointAndBindingChangesResetState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	mock.ExpectQuery(`(?s)SELECT id, host, port.*FROM v2_client_entry_monitor_target.*FOR UPDATE`).
		WithArgs(int64(3), "policy:42").
		WillReturnRows(sqlmock.NewRows([]string{"id", "host", "port"}).AddRow(int64(5), "old.example.com", int64(443)))
	mock.ExpectExec(`DELETE FROM v2_client_entry_monitor_state WHERE target_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	if err := resetClientEntryMonitorTargetState(context.Background(), tx, 3, "policy:42", "new.example.com", nil); err != nil {
		t.Fatalf("reset host state: %v", err)
	}
	port := int64(8443)
	mock.ExpectQuery(`(?s)SELECT id, host, port.*FROM v2_client_entry_monitor_target.*FOR UPDATE`).
		WithArgs(int64(3), "policy:42").
		WillReturnRows(sqlmock.NewRows([]string{"id", "host", "port"}).AddRow(int64(5), "new.example.com", int64(443)))
	mock.ExpectExec(`DELETE FROM v2_client_entry_monitor_state WHERE target_id = \$1`).
		WithArgs(int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := resetClientEntryMonitorTargetState(context.Background(), tx, 3, "policy:42", "new.example.com", &port); err != nil {
		t.Fatalf("reset endpoint state: %v", err)
	}
	mock.ExpectExec(`(?s)DELETE FROM v2_client_entry_monitor_state state.*NOT EXISTS.*v2_client_entry_monitor_probe binding`).
		WithArgs(int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := deleteUnboundClientEntryMonitorStates(context.Background(), tx, 3); err != nil {
		t.Fatalf("delete unbound state: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func expectEmptyClientEntryMonitorRefresh(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`(?s)SELECT p.id, p.name, p.sort, p.action, p.conditions, p.entry_host, p.enabled, p.remarks, p.created_at, p.updated_at.*FROM v2_client_entry_user_policy p`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort", "action", "conditions", "entry_host", "enabled", "remarks", "created_at", "updated_at"}))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, policy_id FROM v2_client_entry_monitor ORDER BY id FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id"}))
	mock.ExpectCommit()
}

type rejectingEntryMonitorNotifier struct {
	called bool
}

func (n *rejectingEntryMonitorNotifier) NotifyAdmins(context.Context, string, bool) error {
	n.called = true
	return nil
}

func (n *rejectingEntryMonitorNotifier) NotifyChat(context.Context, int64, string) error {
	n.called = true
	return nil
}

type splitEntryMonitorNotifier struct {
	chatIDs  []int64
	calls    []int64
	failures map[int64]error
}

func (n *splitEntryMonitorNotifier) ListAdminChats(context.Context, bool) ([]int64, error) {
	return append([]int64(nil), n.chatIDs...), nil
}

func (n *splitEntryMonitorNotifier) NotifyAdmins(context.Context, string, bool) error {
	return nil
}

func (n *splitEntryMonitorNotifier) NotifyChat(_ context.Context, chatID int64, _ string) error {
	n.calls = append(n.calls, chatID)
	if n.failures != nil {
		return n.failures[chatID]
	}
	return nil
}

func expectClientEntryMonitorEventClaim(mock sqlmock.Sqlmock, eventID int64, attempts int) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, message, notify_attempts.*FROM v2_client_entry_monitor_event.*FOR UPDATE SKIP LOCKED`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "message", "notify_attempts"}).AddRow(eventID, "entry alert", attempts))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event.*SET notify_attempts = notify_attempts \+ 1`).
		WithArgs(eventID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func expectClientEntryMonitorEventDeliverySync(mock sqlmock.Sqlmock, eventID int64, rows *sqlmock.Rows) {
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event_delivery`).
		WithArgs(eventID, int64(11), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event_delivery`).
		WithArgs(eventID, int64(22), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT chat_id, delivered_at, attempts, next_attempt_at, last_error.*FROM v2_client_entry_monitor_event_delivery.*FOR UPDATE`).
		WithArgs(eventID).
		WillReturnRows(rows)
	mock.ExpectCommit()
}

func expectClientEntryMonitorDeliveryClaim(mock sqlmock.Sqlmock, eventID, chatID int64) {
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event_delivery.*SET attempts = attempts \+ 1`).
		WithArgs(eventID, chatID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectClientEntryMonitorDeliveryFailure(mock sqlmock.Sqlmock, eventID, chatID int64) {
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event_delivery.*SET next_attempt_at =`).
		WithArgs(eventID, chatID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectClientEntryMonitorDeliverySuccess(mock sqlmock.Sqlmock, eventID, chatID int64) {
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event_delivery.*SET delivered_at =`).
		WithArgs(eventID, chatID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestClientEntryMonitorEventDeliveryRetriesOnlyFailedRecipient(t *testing.T) {
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

	notifier := &splitEntryMonitorNotifier{
		chatIDs:  []int64{11, 22},
		failures: map[int64]error{22: errors.New("chat 22 unavailable")},
	}
	service := &DBService{db: db, dnsFailoverNotifier: notifier}

	// The first pass succeeds for chat 11 and leaves chat 22 pending.
	expectClientEntryMonitorEventClaim(mock, 7, 0)
	firstDeliveryRows := sqlmock.NewRows([]string{"chat_id", "delivered_at", "attempts", "next_attempt_at", "last_error"})
	firstDeliveryRows.AddRow(int64(11), nil, 0, 0, "")
	firstDeliveryRows.AddRow(int64(22), nil, 0, 0, "")
	expectClientEntryMonitorEventDeliverySync(mock, 7, firstDeliveryRows)
	expectClientEntryMonitorDeliveryClaim(mock, 7, 11)
	expectClientEntryMonitorDeliverySuccess(mock, 7, 11)
	expectClientEntryMonitorDeliveryClaim(mock, 7, 22)
	expectClientEntryMonitorDeliveryFailure(mock, 7, 22)
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event.*SET notify_next_attempt_at =`).
		WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processed, err := service.notifyNextClientEntryMonitorEvent(context.Background(), conn)
	if err != nil {
		t.Fatalf("first event delivery: %v", err)
	}
	if !processed {
		t.Fatal("first event delivery was not processed")
	}
	if got, want := notifier.calls, []int64{11, 22}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first recipient calls = %#v, want %#v", got, want)
	}

	// On retry, chat 11 is already delivered and must not be sent again.
	notifier.failures = nil
	expectClientEntryMonitorEventClaim(mock, 7, 1)
	retryDeliveryRows := sqlmock.NewRows([]string{"chat_id", "delivered_at", "attempts", "next_attempt_at", "last_error"})
	retryDeliveryRows.AddRow(int64(11), int64(100), 1, 0, "")
	retryDeliveryRows.AddRow(int64(22), nil, 1, 0, "chat 22 unavailable")
	expectClientEntryMonitorEventDeliverySync(mock, 7, retryDeliveryRows)
	expectClientEntryMonitorDeliveryClaim(mock, 7, 22)
	expectClientEntryMonitorDeliverySuccess(mock, 7, 22)
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event.*SET notified_at = \$2`).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processed, err = service.notifyNextClientEntryMonitorEvent(context.Background(), conn)
	if err != nil {
		t.Fatalf("retry event delivery: %v", err)
	}
	if !processed {
		t.Fatal("retry event delivery was not processed")
	}
	if got, want := notifier.calls, []int64{11, 22, 22}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all recipient calls = %#v, want %#v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCompletedClientEntryMonitorRunDoesNotNotifyFormerOperator(t *testing.T) {
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
	notifier := &rejectingEntryMonitorNotifier{}
	service := &DBService{db: db, dnsFailoverNotifier: notifier}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, requested_by_user_id, request_chat_id, notify_attempts.*FROM v2_client_entry_monitor_run.*FOR UPDATE SKIP LOCKED`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "requested_by_user_id", "request_chat_id", "notify_attempts"}).
			AddRow(int64(91), int64(9), int64(12345), 0))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET notify_attempts = notify_attempts \+ 1`).
		WithArgs(int64(91), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT EXISTS \(.*FROM v2_user.*WHERE id = \$1 AND telegram_id = \$2 AND banned = 0.*is_admin = 1 OR is_staff = 1`).
		WithArgs(int64(9), int64(12345)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run.*SET notified_at = \$2`).
		WithArgs(int64(91), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processed, err := service.notifyNextClientEntryMonitorRun(context.Background(), conn)
	if err != nil {
		t.Fatalf("notifyNextClientEntryMonitorRun: %v", err)
	}
	if !processed {
		t.Fatal("unauthorized notification was not consumed")
	}
	if notifier.called {
		t.Fatal("former operator received a completed run notification")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorReportedAtFormatting(t *testing.T) {
	got := formatClientEntryMonitorReportedAt(time.Date(2026, time.July, 29, 12, 30, 0, 0, time.Local).Unix())
	if got == "" || got == "未知" {
		t.Fatalf("formatted reported time = %q", got)
	}
}

func TestClientEntryMonitorRunReportSummarizesAndCapsResults(t *testing.T) {
	results := make([]ClientEntryMonitorRunResult, 55)
	for index := range results {
		results[index] = ClientEntryMonitorRunResult{
			TargetName: "节点", Host: "entry.example.com", Port: 443,
			ProbeName: "探针", Success: index < 40, ReportedAt: 1,
		}
	}
	report := formatClientEntryMonitorRunReport(ClientEntryMonitorRun{
		ID: 9, Status: "timeout", ExpectedResults: 60, ReceivedResults: 55, Results: results,
	})
	for _, want := range []string{"正常：40", "异常：15", "未返回：5", "共 55 条结果，其余请在后台查看。"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q: %s", want, report)
		}
	}
	if got := strings.Count(report, "\n节点 ·"); got != 50 {
		t.Fatalf("visible result lines = %d, want 50", got)
	}
}

func TestListClientEntryMonitorRunsCapsEachRunResultSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`(?s)SELECT id, policy_ids, status, expected_results,.*FROM v2_client_entry_monitor_run\s+WHERE status = 'running' OR created_at >= \$1\s+ORDER BY id DESC LIMIT \$2`).
		WithArgs(cleanupCutoffHours(24), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_ids", "status", "expected_results", "received_results",
			"started_at", "completed_at", "created_at",
		}).AddRow(int64(91), `[12]`, "completed", int64(250), int64(250), int64(100), int64(101), int64(100)))

	resultRows := sqlmock.NewRows([]string{
		"id", "run_id", "target_id", "target_name", "host", "port", "probe_id", "probe_name",
		"success", "latency_ms", "error", "resolved_ip", "reported_at",
	})
	for index := int64(1); index <= clientEntryMonitorRunResultListLimit; index++ {
		resultRows.AddRow(index, int64(91), index, "入口", "entry.example.com", int64(443), int64(7), "探针",
			int64(1), int64(12), "", "203.0.113.1", int64(101))
	}
	mock.ExpectQuery(`(?s)WITH ranked_results AS \(.*ROW_NUMBER\(\) OVER \(PARTITION BY result.run_id.*WHERE result_rank <= \$2`).
		WithArgs(int64(91), clientEntryMonitorRunResultListLimit).
		WillReturnRows(resultRows)

	runs, err := service.ListClientEntryMonitorRuns(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListClientEntryMonitorRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run := runs[0]
	if len(run.Results) != int(clientEntryMonitorRunResultListLimit) || run.TotalResults != 250 || !run.ResultsTruncated {
		t.Fatalf("run result summary = visible %d total %d truncated %v", len(run.Results), run.TotalResults, run.ResultsTruncated)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestLoadClientEntryMonitorRunCapsTelegramResultSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	mock.ExpectQuery(`(?s)SELECT id, policy_ids, status, expected_results,.*FROM v2_client_entry_monitor_run WHERE id = \$1`).
		WithArgs(int64(91)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_ids", "status", "expected_results", "received_results",
			"started_at", "completed_at", "created_at",
		}).AddRow(int64(91), `[12]`, "completed", int64(230), int64(230), int64(100), int64(101), int64(100)))
	mock.ExpectQuery(`(?s)FROM v2_client_entry_monitor_run_result result.*WHERE result.run_id = \$1.*LIMIT \$2`).
		WithArgs(int64(91), clientEntryMonitorRunResultListLimit).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "target_id", "target_name", "host", "port", "probe_id", "probe_name",
			"success", "latency_ms", "error", "resolved_ip", "reported_at",
		}).AddRow(int64(1), int64(5), "入口", "entry.example.com", int64(443), int64(7), "探针",
			int64(0), nil, "timeout", "203.0.113.1", int64(101)))

	run, err := service.loadClientEntryMonitorRun(context.Background(), 91)
	if err != nil {
		t.Fatalf("loadClientEntryMonitorRun: %v", err)
	}
	if len(run.Results) != 1 || run.TotalResults != 230 || !run.ResultsTruncated {
		t.Fatalf("run result summary = visible %d total %d truncated %v", len(run.Results), run.TotalResults, run.ResultsTruncated)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
