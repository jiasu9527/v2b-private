package admin

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/dnspod"
	"forest/go-api/internal/queue"

	"github.com/DATA-DOG/go-sqlmock"
)

type dnsFailoverWorkerQueue struct {
	mu         sync.Mutex
	queueNames []string
	jobNames   []string
	jobs       []queue.JobFunc
	err        error
}

func (q *dnsFailoverWorkerQueue) Enqueue(queueName, jobName string, job queue.JobFunc) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queueNames = append(q.queueNames, queueName)
	q.jobNames = append(q.jobNames, jobName)
	if q.err != nil {
		return q.err
	}
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *dnsFailoverWorkerQueue) Snapshot() queue.Snapshot { return queue.Snapshot{} }

type dnsFailoverWorkerNotifier struct {
	mu           sync.Mutex
	messages     []string
	includeStaff []bool
	errors       []error
}

func (n *dnsFailoverWorkerNotifier) NotifyAdmins(_ context.Context, message string, includeStaff bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, message)
	n.includeStaff = append(n.includeStaff, includeStaff)
	if len(n.errors) == 0 {
		return nil
	}
	err := n.errors[0]
	n.errors = n.errors[1:]
	return err
}

type dnsFailoverWorkerDNSPod struct {
	fakeDNSPodAPI
	mu        sync.Mutex
	mutations []dnspod.RecordMutationRequest
	results   []dnspod.RecordMutationResult
	errors    []error
}

func (f *dnsFailoverWorkerDNSPod) ModifyRecord(_ context.Context, request dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutations = append(f.mutations, request)
	var result dnspod.RecordMutationResult
	if len(f.results) > 0 {
		result = f.results[0]
		f.results = f.results[1:]
	}
	if len(f.errors) == 0 {
		return result, nil
	}
	err := f.errors[0]
	f.errors = f.errors[1:]
	return result, err
}

func TestDNSFailoverWorkerBuildMutationPreservesRecordMetadataAcrossTypes(t *testing.T) {
	weight := int64(17)
	rule := DNSFailoverRuleRecord{
		Domain: "example.com", DomainID: 101, RecordID: 202, Subdomain: "edge",
		RecordLineID: "10=0", RecordLineName: "默认", TTL: 601, MX: 9, Weight: &weight,
	}

	for _, test := range []struct {
		name   string
		target DNSFailoverTargetRecord
	}{
		{name: "A to CNAME", target: DNSFailoverTargetRecord{DNSType: "CNAME", DNSValue: "backup.example.net."}},
		{name: "CNAME to A", target: DNSFailoverTargetRecord{DNSType: "A", DNSValue: "192.0.2.10"}},
		{name: "A to AAAA", target: DNSFailoverTargetRecord{DNSType: "AAAA", DNSValue: "2001:db8::10"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := buildDNSFailoverMutation(rule, test.target)
			if got.Domain != rule.Domain || got.DomainID != rule.DomainID || got.RecordID != rule.RecordID || got.SubDomain != rule.Subdomain {
				t.Fatalf("record identity was not preserved: %#v", got)
			}
			if got.RecordLineID != rule.RecordLineID || got.RecordLine != rule.RecordLineName || got.TTL != rule.TTL || got.MX != rule.MX || got.Weight != rule.Weight {
				t.Fatalf("record metadata was not preserved: %#v", got)
			}
			if got.RecordType != test.target.DNSType || got.Value != test.target.DNSValue {
				t.Fatalf("target mutation mismatch: %#v", got)
			}
		})
	}
}

func TestDNSFailoverWorkerProbeOnlineRequiresFreshWarmedHeartbeat(t *testing.T) {
	now := int64(1_700_000_000)
	for _, test := range []struct {
		name      string
		heartbeat sql.NullInt64
		prewarm   int64
		offline   int64
		want      bool
	}{
		{name: "fresh and warmed", heartbeat: sql.NullInt64{Int64: now - 90, Valid: true}, prewarm: 3, offline: 90, want: true},
		{name: "not prewarmed", heartbeat: sql.NullInt64{Int64: now, Valid: true}, prewarm: 2, offline: 90},
		{name: "stale", heartbeat: sql.NullInt64{Int64: now - 91, Valid: true}, prewarm: 3, offline: 90},
		{name: "future", heartbeat: sql.NullInt64{Int64: now + 1, Valid: true}, prewarm: 3, offline: 90},
		{name: "overflow safe remains fresh", heartbeat: sql.NullInt64{Int64: 0, Valid: true}, prewarm: 3, offline: int64(^uint64(0) >> 1), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := dnsFailoverProbeOnline(test.heartbeat, test.prewarm, now, test.offline); got != test.want {
				t.Fatalf("dnsFailoverProbeOnline() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDNSFailoverWorkerRetryBackoffIsExponentialAndBounded(t *testing.T) {
	if got := dnsFailoverRetryDelay(0); got != 5*time.Second {
		t.Fatalf("first retry delay = %v", got)
	}
	if got := dnsFailoverRetryDelay(3); got != 40*time.Second {
		t.Fatalf("fourth retry delay = %v", got)
	}
	if got := dnsFailoverRetryDelay(1_000); got != 5*time.Minute {
		t.Fatalf("bounded retry delay = %v", got)
	}
}

func TestDNSFailoverWorkerRetriesDurableReconciliationIntentPersistence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}

	mock.ExpectBegin().WillReturnError(errors.New("database restarting"))
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*VALUES \(\$1, 'reconcile', \$2, \$3.*ON CONFLICT \(group_id\) DO UPDATE SET.*operation = 'reconcile'`).
		WithArgs(int64(8), int64(10), int64(20), int64(1000), int64(1005), "rollback failed").
		WillReturnResult(sqlmock.NewResult(1, 1))
	expectDNSFailoverGroupAdvisoryLock(mock, 8)
	mock.ExpectCommit()

	if err := service.persistDNSFailoverReconciliationIntent(context.Background(), 8, 10, 20, 1000, 1005, "rollback failed"); err != nil {
		t.Fatalf("persist reconciliation intent after transient DB failure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerRequestEvaluationOnlyQueuesWakeHint(t *testing.T) {
	jobs := &dnsFailoverWorkerQueue{}
	service := (&DBService{}).WithQueueRuntime(jobs)
	service.WithDNSFailoverEvaluationRequester(service)

	if err := service.RequestDNSFailoverEvaluation(context.Background(), []int64{3, 7}); err != nil {
		t.Fatalf("RequestDNSFailoverEvaluation: %v", err)
	}
	if len(jobs.queueNames) != 1 || jobs.queueNames[0] != "dns_failover" || len(jobs.jobs) != 1 {
		t.Fatalf("unexpected queue wake: names=%#v jobs=%d", jobs.queueNames, len(jobs.jobs))
	}
	if service.db != nil {
		t.Fatal("requester must not require or write the database")
	}
}

func TestDNSFailoverWorkerQueueUnavailableLeavesDurableWorkForTicker(t *testing.T) {
	wantErr := errors.New("queue unavailable")
	jobs := &dnsFailoverWorkerQueue{err: wantErr}
	service := (&DBService{}).WithQueueRuntime(jobs)

	err := service.RequestDNSFailoverEvaluation(context.Background(), []int64{9})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if len(jobs.jobs) != 0 {
		t.Fatalf("failed queue wake retained an in-memory job: %d", len(jobs.jobs))
	}
}

func TestDNSFailoverWorkerLifecycleRunsImmediatelyAndStopsWithContext(t *testing.T) {
	ctx := context.Background()
	called := make(chan struct{}, 4)
	service := &DBService{
		dnsFailoverTickInterval: time.Hour,
		dnsFailoverCycle: func(context.Context) error {
			called <- struct{}{}
			return nil
		},
	}

	service.StartDNSFailoverAutomation(ctx)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("automation did not run its startup scan")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.StopDNSFailoverAutomation(stopCtx); err != nil {
		t.Fatalf("StopDNSFailoverAutomation: %v", err)
	}
	if err := service.RequestDNSFailoverEvaluation(context.Background(), []int64{8}); !errors.Is(err, ErrDNSFailoverAutomationStopped) {
		t.Fatalf("wake after stop error = %v, want ErrDNSFailoverAutomationStopped", err)
	}
}

func TestDNSFailoverWorkerStopCancelsQueuedDrainBeforeQueueShutdown(t *testing.T) {
	jobs := &dnsFailoverWorkerQueue{}
	startupDone := make(chan struct{})
	queuedStarted := make(chan struct{})
	queuedExited := make(chan struct{})
	var cycleMu sync.Mutex
	cycleCalls := 0
	service := (&DBService{
		dnsFailoverTickInterval: time.Hour,
		dnsFailoverCycle: func(ctx context.Context) error {
			cycleMu.Lock()
			cycleCalls++
			call := cycleCalls
			cycleMu.Unlock()
			if call == 1 {
				close(startupDone)
				return nil
			}
			close(queuedStarted)
			<-ctx.Done()
			close(queuedExited)
			return ctx.Err()
		},
	}).WithQueueRuntime(jobs)

	service.StartDNSFailoverAutomation(context.Background())
	select {
	case <-startupDone:
	case <-time.After(time.Second):
		t.Fatal("startup cycle did not finish")
	}
	if err := service.RequestDNSFailoverEvaluation(context.Background(), []int64{8}); err != nil {
		t.Fatalf("queue drain: %v", err)
	}
	jobs.mu.Lock()
	job := jobs.jobs[0]
	jobs.mu.Unlock()
	queueCtx, cancelQueue := context.WithCancel(context.Background())
	defer cancelQueue()
	jobDone := make(chan struct{})
	go func() {
		_ = job(queueCtx)
		close(jobDone)
	}()
	select {
	case <-queuedStarted:
	case <-time.After(time.Second):
		t.Fatal("queued drain did not start")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 250*time.Millisecond)
	err := service.StopDNSFailoverAutomation(stopCtx)
	cancelStop()
	if err != nil {
		cancelQueue()
		<-jobDone
		t.Fatalf("StopDNSFailoverAutomation waited for queue shutdown instead of cancelling its drain: %v", err)
	}
	select {
	case <-queuedExited:
	case <-time.After(time.Second):
		t.Fatal("queued drain did not observe worker cancellation")
	}
	select {
	case <-jobDone:
	case <-time.After(time.Second):
		t.Fatal("queued drain goroutine leaked")
	}
}

func TestDNSFailoverWorkerTickerLogsCycleErrors(t *testing.T) {
	logged := make(chan string, 1)
	service := &DBService{
		dnsFailoverTickInterval: time.Hour,
		dnsFailoverCycle: func(context.Context) error {
			return errors.New("cycle failed")
		},
		dnsFailoverLogf: func(format string, args ...any) {
			logged <- fmt.Sprintf(format, args...)
		},
	}
	service.StartDNSFailoverAutomation(context.Background())
	select {
	case message := <-logged:
		if !strings.Contains(message, "cycle failed") {
			t.Fatalf("log message = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("worker cycle error was not logged")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.StopDNSFailoverAutomation(stopCtx); err != nil {
		t.Fatalf("stop automation: %v", err)
	}
}

func TestDNSFailoverWorkerNotificationFailureRemainsPendingThenMarksNotified(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	notifier := &dnsFailoverWorkerNotifier{errors: []error{errors.New("telegram unavailable"), nil}}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverNotifier(notifier)

	columns := []string{"id", "message"}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, message.*FROM v2_dns_failover_event.*notified_at IS NULL.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(11), "DNS 故障转移告警"))
	mock.ExpectRollback()
	if err := service.drainPendingDNSFailoverNotifications(context.Background(), 1); err == nil {
		t.Fatal("first notification should fail")
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, message.*FROM v2_dns_failover_event.*notified_at IS NULL.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(11), "DNS 故障转移告警"))
	mock.ExpectExec(`UPDATE v2_dns_failover_event SET notified_at = \$2 WHERE id = \$1 AND notified_at IS NULL`).
		WithArgs(int64(11), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := service.drainPendingDNSFailoverNotifications(context.Background(), 1); err != nil {
		t.Fatalf("retry notification: %v", err)
	}

	if len(notifier.messages) != 2 || !notifier.includeStaff[0] || !notifier.includeStaff[1] {
		t.Fatalf("unexpected notifier calls: messages=%#v staff=%#v", notifier.messages, notifier.includeStaff)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerNilNotifierLeavesEventPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, message.*FROM v2_dns_failover_event.*notified_at IS NULL.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "message"}).AddRow(int64(12), "DNS 告警"))
	mock.ExpectRollback()
	if err := service.drainPendingDNSFailoverNotifications(context.Background(), 1); !errors.Is(err, ErrDNSFailoverNotifierUnavailable) {
		t.Fatalf("error = %v, want ErrDNSFailoverNotifierUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerSuccessfulSendButDBFailureRemainsPendingForAtLeastOnceRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	notifier := &dnsFailoverWorkerNotifier{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverNotifier(notifier)
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "message"}).AddRow(int64(13), "DNS 告警")
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, message.*FOR UPDATE SKIP LOCKED`).WillReturnRows(rows())
	mock.ExpectExec(`UPDATE v2_dns_failover_event SET notified_at`).WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	if err := service.drainPendingDNSFailoverNotifications(context.Background(), 1); err == nil {
		t.Fatal("expected DB update failure")
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, message.*FOR UPDATE SKIP LOCKED`).WillReturnRows(rows())
	mock.ExpectExec(`UPDATE v2_dns_failover_event SET notified_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := service.drainPendingDNSFailoverNotifications(context.Background(), 1); err != nil {
		t.Fatalf("retry pending event: %v", err)
	}
	if len(notifier.messages) != 2 {
		t.Fatalf("at-least-once sends = %d, want 2", len(notifier.messages))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerNotificationTextIncludesOperationalContext(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)
	oldTarget := DNSFailoverTargetRecord{ID: 1, Name: "主站", DNSType: "A", DNSValue: "192.0.2.1", CheckPort: 443}
	newTarget := DNSFailoverTargetRecord{ID: 2, Name: "备用", DNSType: "CNAME", DNSValue: "backup.example.net.", CheckPort: 8443}
	rule := DNSFailoverRuleRecord{ID: 8, Name: "官网", Domain: "example.com", Subdomain: "www"}

	message := formatDNSFailoverSwitchNotification(rule, oldTarget, newTarget, "current_target_failed", []int64{4, 5}, false, now)
	for _, want := range []string{"官网", "www.example.com", "主站", "备用", "A", "192.0.2.1", "CNAME", "backup.example.net.", "8443", "4, 5", "current_target_failed", "2026-07-14"} {
		if !strings.Contains(message, want) {
			t.Fatalf("notification %q missing %q", message, want)
		}
	}

	single := formatDNSFailoverSwitchNotification(rule, oldTarget, newTarget, "current_target_failed", []int64{4}, true, now)
	if !strings.Contains(single, "单探针降级") {
		t.Fatalf("single-probe notification missing degradation marker: %q", single)
	}
}

func TestDNSFailoverWorkerIncidentNotificationIncludesCurrentTargetAndPort(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC).Unix()
	currentID := int64(10)
	rule := DNSFailoverRuleRecord{
		ID: 8, Name: "官网", Domain: "example.com", Subdomain: "www", CurrentTargetID: &currentID,
		Targets: []DNSFailoverTargetRecord{{ID: 10, Name: "主站", DNSType: "A", DNSValue: "192.0.2.1", CheckPort: 443, Enabled: true}},
	}
	message := formatDNSFailoverIncidentNotification(rule, "all_probes_offline", "全部探针离线", []int64{4, 5}, now)
	for _, want := range []string{"官网", "www.example.com", "主站", "A", "192.0.2.1", "443", "4, 5", "全部探针离线", "2026-07-14"} {
		if !strings.Contains(message, want) {
			t.Fatalf("incident notification %q missing %q", message, want)
		}
	}
}

func TestDNSFailoverWorkerSeedsOnlyDueEnabledGroupsWithoutResettingPendingRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := int64(1_700_000_100)

	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*operation, target_id, source_target_id.*SELECT g.id, 'evaluate', NULL, NULL, \$1, 0, \$1, '', \$1, \$1.*FROM v2_dns_failover_group g.*g.enabled = 1.*last_evaluated_at IS NULL.*last_evaluated_at <= \$1 - g.check_interval_sec.*ON CONFLICT \(group_id\) DO NOTHING`).
		WithArgs(now).WillReturnResult(sqlmock.NewResult(0, 2))
	if err := service.seedDueDNSFailoverEvaluations(context.Background(), now); err != nil {
		t.Fatalf("seed due evaluations: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerRestartClaimsPersistedOutboxWithSkipLockedGroupLockAndVersionedAck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 41, 8, 700, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 1, 0, "192.0.2.1"))
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET last_evaluated_at = \$2, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(8), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(41), int64(700)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerBusyGroupDefersClaimWithoutAttemptAndContinues(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, group_id, operation, target_id, source_target_id, requested_at, attempts, last_error.*FROM v2_dns_failover_eval_outbox.*next_attempt_at <= \$1.*ORDER BY next_attempt_at ASC, requested_at ASC.*LIMIT 1.*FOR UPDATE SKIP LOCKED`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "operation", "target_id", "source_target_id", "requested_at", "attempts", "last_error"}).
			AddRow(int64(48), int64(8), "evaluate", nil, nil, int64(707), 2, ""))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_eval_outbox SET next_attempt_at = \$2, updated_at = \$3 WHERE id = \$1 AND requested_at = \$4`).
		WithArgs(int64(48), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(707)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("busy group defer: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerInvalidCurrentTargetRecordsConfigErrorWithoutDNSCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 47, 8, 706, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 99, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 1, 0, "192.0.2.1"))
	expectDNSFailoverActiveIncidentSet(mock, 8, "config_error")
	expectDNSFailoverEventInsertNullableTarget(mock, 8, nil, "config_error")
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET last_evaluated_at = \$2, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(8), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(47), int64(706)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("config error evaluation: %v", err)
	}
	if len(dnsAPI.mutations) != 0 {
		t.Fatalf("invalid current target called DNSPod: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerFailoverAtoCNAMECallsDNSPodBeforeStateCommit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "req-failover"}}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()
	weight := int64(17)

	expectDNSFailoverOutboxClaim(mock, 42, 8, 701, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, &weight))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock,
		dnsFailoverWorkerProbeRow(4, now, 3),
		dnsFailoverWorkerProbeRow(5, now, 3),
	)
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 0, 3, "192.0.2.1"),
		dnsFailoverWorkerStateRow(5, 10, 0, 3, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 6, 0, "198.51.100.20"),
		dnsFailoverWorkerStateRow(5, 20, 6, 0, "198.51.100.20"),
	)
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2, last_switch_at = \$3, last_switch_reason = \$4, last_evaluated_at = \$3, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "current_target_failed").WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsert(mock, 8, 20, "failover")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(42), int64(701)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("drain failover: %v", err)
	}
	if len(dnsAPI.mutations) != 1 {
		t.Fatalf("DNSPod calls = %d, want 1", len(dnsAPI.mutations))
	}
	mutation := dnsAPI.mutations[0]
	if mutation.Domain != "example.com" || mutation.DomainID != 101 || mutation.RecordID != 202 || mutation.SubDomain != "www" ||
		mutation.RecordLineID != "10=0" || mutation.RecordLine != "默认" || mutation.RecordType != "CNAME" || mutation.Value != "backup.example.net." ||
		mutation.TTL != 601 || mutation.MX != 9 || mutation.Weight == nil || *mutation.Weight != weight {
		t.Fatalf("incomplete DNSPod mutation: %#v", mutation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerFailbackCNAMEtoA(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "req-failback"}}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 43, 8, 702, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 20, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock,
		dnsFailoverWorkerProbeRow(4, now, 3),
		dnsFailoverWorkerProbeRow(5, now-91, 3),
	)
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 1, 0, "198.51.100.20"),
	)
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2, last_switch_at = \$3, last_switch_reason = \$4, last_evaluated_at = \$3, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(8), int64(10), sqlmock.AnyArg(), "higher_priority_target_recovered").WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsertMessageContains(mock, 8, 10, "failback", "探针：4（单探针降级）")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(43), int64(702)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("drain failback: %v", err)
	}
	if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "A" || dnsAPI.mutations[0].Value != "192.0.2.1" {
		t.Fatalf("unexpected failback mutation: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerDNSPodErrorKeepsCurrentAndSchedulesRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{errors: []error{errors.New("DNSPod timeout")}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 44, 8, 703, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 0, 5, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 8, 0, "198.51.100.20"),
	)
	expectDNSFailoverActiveIncidentSet(mock, 8, "dnspod_error")
	expectDNSFailoverEventInsert(mock, 8, 20, "dnspod_error")
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_eval_outbox SET attempts = \$2, next_attempt_at = \$3, last_error = \$4, updated_at = \$5 WHERE id = \$1 AND requested_at = \$6`).
		WithArgs(int64(44), 3, sqlmock.AnyArg(), "DNSPod timeout", sqlmock.AnyArg(), int64(703)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("durably handled DNSPod error: %v", err)
	}
	if len(dnsAPI.mutations) != 1 {
		t.Fatalf("DNSPod calls = %d, want 1", len(dnsAPI.mutations))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerManualOperationRetriesRequestedTargetIgnoringHealth(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "manual-retry"}}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 60, 8, "manual", int64(20), int64(10), 900, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "手动目标", "CNAME", "manual.example.net.", 8443, true),
		dnsFailoverWorkerTargetRow(30, 8, 3, "健康候选", "AAAA", "2001:db8::30", 9443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 0, 5, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 0, 5, "198.51.100.20"),
		dnsFailoverWorkerStateRow(4, 30, 8, 0, "2001:db8::30"),
	)
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2, last_switch_at = \$3, last_switch_reason = 'manual', last_evaluated_at = \$3, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsert(mock, 8, 20, "manual_switch")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(60), int64(900)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("manual retry drain: %v", err)
	}
	if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "CNAME" || dnsAPI.mutations[0].Value != "manual.example.net." {
		t.Fatalf("manual operation followed health candidate instead of requested target: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerManualRetryStillRunsWhenAutomationIsDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "disabled-manual-retry"}}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 65, 8, "manual", int64(20), int64(10), 909, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, false, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "手动目标", "CNAME", "manual.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 20, 0, 5, "198.51.100.20"))
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2, last_switch_at = \$3, last_switch_reason = 'manual', last_evaluated_at = \$3, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsert(mock, 8, 20, "manual_switch")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(65), int64(909)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("disabled manual retry drain: %v", err)
	}
	if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "CNAME" || dnsAPI.mutations[0].Value != "manual.example.net." {
		t.Fatalf("disabled group discarded requested manual retry: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerDisabledManualTargetStaysPendingWithoutChoosingHealthyAlternative(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 67, 8, "manual", int64(20), int64(10), 912, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "已禁用手动目标", "CNAME", "manual.example.net.", 8443, false),
		dnsFailoverWorkerTargetRow(30, 8, 3, "健康但未请求", "AAAA", "2001:db8::30", 9443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 20, 0, 5, "198.51.100.20"),
		dnsFailoverWorkerStateRow(4, 30, 8, 0, "2001:db8::30"),
	)
	expectDNSFailoverActiveIncidentSet(mock, 8, "config_error")
	expectDNSFailoverEventInsert(mock, 8, 20, "config_error")
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_eval_outbox SET attempts = \$2, next_attempt_at = \$3, last_error = \$4, updated_at = \$5 WHERE id = \$1 AND requested_at = \$6`).
		WithArgs(int64(67), 3, sqlmock.AnyArg(), "manual DNS failover target 20 is missing or disabled", sqlmock.AnyArg(), int64(912)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("disabled manual target retry: %v", err)
	}
	if len(dnsAPI.mutations) != 0 {
		t.Fatalf("disabled requested target must not select another candidate: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("manual operation was not retained: %v", err)
	}
}

func TestDNSFailoverWorkerReconcileOperationForcesDNSWithoutHealthEvaluation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "reconciled"}}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 61, 8, "reconcile", int64(10), int64(20), 901, 3)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, nil, "dns_state_diverged", now-60))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "数据库目标", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "外部实际目标", "CNAME", "external.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"))
	expectDNSFailoverActiveIncidentClearDivergence(mock, 8)
	expectDNSFailoverEventInsert(mock, 8, 10, "dns_state_reconciled")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(61), int64(901)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("reconcile drain: %v", err)
	}
	if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "A" || dnsAPI.mutations[0].Value != "192.0.2.1" {
		t.Fatalf("reconcile did not force database target: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerReconcileFailureRetainsForcedOperationWithBackoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{errors: []error{errors.New("reconcile failed")}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 63, 8, "reconcile", int64(10), int64(20), 907, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, nil, "dns_state_diverged", now-60))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "数据库目标", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "外部实际目标", "CNAME", "external.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"))
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_eval_outbox SET attempts = \$2, next_attempt_at = \$3, last_error = \$4, updated_at = \$5 WHERE id = \$1 AND requested_at = \$6`).
		WithArgs(int64(63), 3, sqlmock.AnyArg(), "reconcile failed", sqlmock.AnyArg(), int64(907)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("reconcile retry: %v", err)
	}
	if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "A" {
		t.Fatalf("reconcile retry mutation = %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("reconcile failure must remain pending: %v", err)
	}
}

func TestDNSFailoverWorkerReconcileStillRunsWhenAutomationIsDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "disabled-reconciled"}}}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 64, 8, "reconcile", int64(10), int64(20), 908, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, false, true, nil, nil, "dns_state_diverged", now-60))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "数据库目标", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "外部实际目标", "CNAME", "external.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"))
	expectDNSFailoverActiveIncidentClearDivergence(mock, 8)
	expectDNSFailoverEventInsert(mock, 8, 10, "dns_state_reconciled")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(64), int64(908)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("disabled reconciliation drain: %v", err)
	}
	if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "A" || dnsAPI.mutations[0].Value != "192.0.2.1" {
		t.Fatalf("disabled group discarded forced reconciliation: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerInvalidReconcileTargetStaysPendingInsteadOfBeingAcked(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaimOperation(mock, 68, 8, "reconcile", int64(10), int64(20), 913, 2)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, nil, "dns_state_diverged", now-60))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "已禁用数据库目标", "A", "192.0.2.1", 443, false),
		dnsFailoverWorkerTargetRow(20, 8, 2, "外部实际目标", "CNAME", "external.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"))
	expectDNSFailoverActiveIncidentSet(mock, 8, "config_error")
	expectDNSFailoverEventInsertNullableTarget(mock, 8, nil, "config_error")
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_eval_outbox SET attempts = \$2, next_attempt_at = \$3, last_error = \$4, updated_at = \$5 WHERE id = \$1 AND requested_at = \$6`).
		WithArgs(int64(68), 3, sqlmock.AnyArg(), "current target is missing or disabled", sqlmock.AnyArg(), int64(913)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("invalid reconcile target retry: %v", err)
	}
	if len(dnsAPI.mutations) != 0 {
		t.Fatalf("invalid database target must not call DNSPod: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("reconcile operation was acked instead of retained: %v", err)
	}
}

func TestDNSFailoverWorkerCommitFailureCompensatesDNSPodAndPersistsRecovery(t *testing.T) {
	for _, test := range []struct {
		name             string
		compensationErr  error
		wantErrorSnippet string
	}{
		{name: "compensation succeeds", wantErrorSnippet: "commit failed"},
		{name: "compensation fails", compensationErr: errors.New("rollback DNS failed"), wantErrorSnippet: "rollback DNS failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			dnsAPI := &dnsFailoverWorkerDNSPod{
				results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "req-switch"}, {RecordID: 202, RequestID: "req-rollback"}, {RecordID: 202, RequestID: "req-reconcile"}},
				errors:  []error{nil, test.compensationErr, nil},
			}
			service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
			now := time.Now().Unix()

			expectDNSFailoverOutboxClaim(mock, 45, 8, 704, 0)
			expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
			expectDNSFailoverTargets(mock,
				dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
				dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
			)
			expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
			expectDNSFailoverStates(mock,
				dnsFailoverWorkerStateRow(4, 10, 0, 5, "192.0.2.1"),
				dnsFailoverWorkerStateRow(4, 20, 8, 0, "198.51.100.20"),
			)
			mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2`).
				WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "current_target_failed").WillReturnResult(sqlmock.NewResult(0, 1))
			expectDNSFailoverEventInsert(mock, 8, 20, "failover")
			mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
				WithArgs(int64(45), int64(704)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

			if test.compensationErr != nil {
				mock.ExpectBegin()
				mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*operation, target_id, source_target_id.*VALUES \(\$1, 'reconcile', \$2, \$3.*ON CONFLICT \(group_id\) DO UPDATE SET.*operation = 'reconcile'.*target_id = EXCLUDED.target_id.*source_target_id = EXCLUDED.source_target_id`).
					WithArgs(int64(8), int64(10), int64(20), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
				expectDNSFailoverGroupAdvisoryLock(mock, 8)
				mock.ExpectCommit()
				mock.ExpectBegin()
				expectDNSFailoverGroupAdvisoryLock(mock, 8)
				expectDNSFailoverActiveIncidentSet(mock, 8, "dns_state_diverged")
				expectDNSFailoverEventInsertDetailsContain(mock, 8, 20, "dns_state_diverged", `"desired_target"`, `"actual_target"`, `"rollback_error"`)
				mock.ExpectCommit()
			} else {
				mock.ExpectBegin()
				mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*operation, target_id, source_target_id.*VALUES \(\$1, 'evaluate', NULL, NULL.*ON CONFLICT \(group_id\) DO UPDATE SET.*operation = 'evaluate'.*target_id = NULL.*source_target_id = NULL`).
					WithArgs(int64(8), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
				expectDNSFailoverGroupAdvisoryLock(mock, 8)
				expectDNSFailoverActiveIncidentSet(mock, 8, "dnspod_error")
				expectDNSFailoverEventInsert(mock, 8, 20, "dnspod_error")
				mock.ExpectCommit()
			}

			err = service.drainDNSFailoverEvaluationOutbox(context.Background(), 1)
			if err == nil || !strings.Contains(err.Error(), test.wantErrorSnippet) {
				t.Fatalf("error = %v, want snippet %q", err, test.wantErrorSnippet)
			}
			if len(dnsAPI.mutations) != 2 {
				t.Fatalf("DNSPod calls = %d, want switch + compensation", len(dnsAPI.mutations))
			}
			rollback := dnsAPI.mutations[1]
			if rollback.RecordType != "A" || rollback.Value != "192.0.2.1" || rollback.RecordID != 202 {
				t.Fatalf("invalid compensation mutation: %#v", rollback)
			}
			if test.compensationErr != nil {
				expectDNSFailoverOutboxClaimOperation(mock, 45, 8, "reconcile", int64(10), int64(20), 905, 1)
				expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, nil, "dns_state_diverged", int64(904)))
				expectDNSFailoverTargets(mock,
					dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
					dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
				)
				expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
				expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"))
				expectDNSFailoverActiveIncidentClearDivergence(mock, 8)
				expectDNSFailoverEventInsert(mock, 8, 10, "dns_state_reconciled")
				mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
					WithArgs(int64(45), int64(905)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
					t.Fatalf("forced reconciliation after compensation failure: %v", err)
				}
				if len(dnsAPI.mutations) != 3 || dnsAPI.mutations[2].RecordType != "A" {
					t.Fatalf("forced reconciliation mutations = %#v", dnsAPI.mutations)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestDNSFailoverWorkerDivergenceEventFailureKeepsDurableForcedReconciliation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{
		results: []dnspod.RecordMutationResult{
			{RecordID: 202, RequestID: "req-switch"},
			{RecordID: 202, RequestID: "req-rollback"},
			{RecordID: 202, RequestID: "req-reconcile"},
		},
		errors: []error{nil, errors.New("rollback DNS failed"), nil},
	}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 66, 8, 910, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 0, 5, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 8, 0, "198.51.100.20"),
	)
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "current_target_failed").WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsert(mock, 8, 20, "failover")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(66), int64(910)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	// The safety operation must commit before the independently best-effort
	// divergence event, so an event failure cannot roll it back.
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*VALUES \(\$1, 'reconcile', \$2, \$3.*ON CONFLICT \(group_id\) DO UPDATE SET.*operation = 'reconcile'`).
		WithArgs(int64(8), int64(10), int64(20), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	expectDNSFailoverGroupAdvisoryLock(mock, 8)
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectDNSFailoverGroupAdvisoryLock(mock, 8)
	expectDNSFailoverActiveIncidentSet(mock, 8, "dns_state_diverged")
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_event.*VALUES \(\$1, NULL, \$2, \$3, \$4, \$5, \$6, NULL, \$7\)`).
		WithArgs(int64(8), int64(20), "dns_state_diverged", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("event write failed"))
	mock.ExpectRollback()

	err = service.drainDNSFailoverEvaluationOutbox(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "event write failed") {
		t.Fatalf("first drain error = %v", err)
	}

	// The next claim must remain reconcile even though the event transaction
	// rolled back. It reconstructs the missing divergence event from outbox
	// target/source/last_error before forcing DNS back to the DB target.
	expectDNSFailoverOutboxClaimOperationWithError(mock, 66, 8, "reconcile", int64(10), int64(20), 911, 1, "commit failed; rollback DNS failed")
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 8, 0, "192.0.2.1"))
	expectDNSFailoverActiveIncidentSet(mock, 8, "dns_state_diverged")
	expectDNSFailoverEventInsertDetailsContain(mock, 8, 20, "dns_state_diverged", `"desired_target"`, `"actual_target"`, `"rollback_error"`)
	expectDNSFailoverActiveIncidentClearDivergence(mock, 8)
	expectDNSFailoverEventInsert(mock, 8, 10, "dns_state_reconciled")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(66), int64(911)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("forced reconciliation after event failure: %v", err)
	}
	if len(dnsAPI.mutations) != 3 || dnsAPI.mutations[2].RecordType != "A" || dnsAPI.mutations[2].Value != "192.0.2.1" {
		t.Fatalf("forced reconciliation mutations = %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerPostDNSPersistenceFailureAlsoCompensates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{
		results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "req-switch"}, {RecordID: 202, RequestID: "req-rollback"}},
	}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 46, 8, 705, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 0, 5, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 8, 0, "198.51.100.20"),
	)
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "current_target_failed").WillReturnError(errors.New("state update failed"))
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*operation, target_id, source_target_id.*VALUES \(\$1, 'evaluate', NULL, NULL.*ON CONFLICT \(group_id\) DO UPDATE SET`).
		WithArgs(int64(8), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	expectDNSFailoverGroupAdvisoryLock(mock, 8)
	expectDNSFailoverActiveIncidentSet(mock, 8, "dnspod_error")
	expectDNSFailoverEventInsert(mock, 8, 20, "dnspod_error")
	mock.ExpectCommit()

	err = service.drainDNSFailoverEvaluationOutbox(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "state update failed") {
		t.Fatalf("error = %v", err)
	}
	if len(dnsAPI.mutations) != 2 || dnsAPI.mutations[1].RecordType != "A" {
		t.Fatalf("persistence failure did not compensate DNS: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerManualSwitchSuccessFailureAndSameTarget(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "manual-request"}}}
		service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}

		mock.ExpectBegin()
		expectManualDNSFailoverOperationUpsert(mock, 8, 20)
		expectDNSFailoverGroupAdvisoryLock(mock, 8)
		expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, nil, "all_probes_offline", int64(700)))
		expectDNSFailoverTargets(mock,
			dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
			dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
		)
		mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2, last_switch_at = \$3, last_switch_reason = 'manual', last_evaluated_at = \$3, updated_at = \$3 WHERE id = \$1`).
			WithArgs(int64(8), int64(20), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		expectDNSFailoverEventInsert(mock, 8, 20, "manual_switch")
		mock.ExpectExec(`(?s)DELETE FROM v2_dns_failover_eval_outbox WHERE group_id = \$1 AND operation = 'manual' AND target_id = \$2`).
			WithArgs(int64(8), int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		if err := service.ManualSwitchDNSFailoverTarget(context.Background(), 8, 20); err != nil {
			t.Fatalf("manual switch: %v", err)
		}
		if len(dnsAPI.mutations) != 1 || dnsAPI.mutations[0].RecordType != "CNAME" {
			t.Fatalf("unexpected manual DNS mutation: %#v", dnsAPI.mutations)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("sql expectations: %v", err)
		}
	})

	t.Run("DNSPod failure preserves current and queues retry", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		dnsAPI := &dnsFailoverWorkerDNSPod{
			results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "manual-error-request"}},
			errors:  []error{errors.New("manual DNS failed")},
		}
		service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}

		mock.ExpectBegin()
		expectManualDNSFailoverOperationUpsert(mock, 8, 20)
		expectDNSFailoverGroupAdvisoryLock(mock, 8)
		expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
		expectDNSFailoverTargets(mock,
			dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
			dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
		)
		expectDNSFailoverActiveIncidentSet(mock, 8, "dnspod_error")
		expectDNSFailoverEventInsertDetailsContain(mock, 8, 20, "dnspod_error", `"operation":"manual_switch"`, `"retry_semantics":"retry_requested_target"`, `"request_id":"manual-error-request"`)
		mock.ExpectExec(`(?s)UPDATE v2_dns_failover_eval_outbox SET attempts = attempts \+ 1, next_attempt_at = \$3, last_error = \$4, updated_at = \$5 WHERE group_id = \$1 AND operation = 'manual' AND target_id = \$2`).
			WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "manual DNS failed", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		err = service.ManualSwitchDNSFailoverTarget(context.Background(), 8, 20)
		if err == nil || !strings.Contains(err.Error(), "manual DNS failed") {
			t.Fatalf("error = %v", err)
		}
		if len(dnsAPI.mutations) != 1 {
			t.Fatalf("DNSPod calls = %d, want 1", len(dnsAPI.mutations))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("sql expectations: %v", err)
		}
	})

	t.Run("same target is idempotent", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		dnsAPI := &dnsFailoverWorkerDNSPod{}
		service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}

		mock.ExpectBegin()
		expectManualDNSFailoverOperationUpsert(mock, 8, 10)
		expectDNSFailoverGroupAdvisoryLock(mock, 8)
		expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
		expectDNSFailoverTargets(mock,
			dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
			dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
		)
		mock.ExpectExec(`(?s)DELETE FROM v2_dns_failover_eval_outbox WHERE group_id = \$1 AND operation = 'manual' AND target_id = \$2`).
			WithArgs(int64(8), int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		if err := service.ManualSwitchDNSFailoverTarget(context.Background(), 8, 10); err != nil {
			t.Fatalf("same-target manual switch: %v", err)
		}
		if len(dnsAPI.mutations) != 0 {
			t.Fatalf("same-target switch called DNSPod: %#v", dnsAPI.mutations)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("sql expectations: %v", err)
		}
	})
}

func expectManualDNSFailoverOperationUpsert(mock sqlmock.Sqlmock, groupID, targetID int64) {
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*group_id, operation, target_id, source_target_id.*VALUES \(\$1, 'manual', \$2, NULL.*ON CONFLICT \(group_id\) DO UPDATE SET.*operation = 'manual'.*target_id = EXCLUDED.target_id.*attempts = 0.*WHERE v2_dns_failover_eval_outbox.operation <> 'reconcile'`).
		WithArgs(groupID, targetID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
}

func TestDNSFailoverWorkerOfflineIncidentDedupRecoveryAndFutureIncident(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	dnsAPI := &dnsFailoverWorkerDNSPod{}
	service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverAPI: dnsAPI}
	now := time.Now().Unix()

	// First offline period emits one alert.
	expectDNSFailoverNoActionEvaluation(mock, 51, 801, now-91, "", "all_probes_offline")
	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("first offline evaluation: %v", err)
	}
	// Repeated periodic evaluation observes the same persisted state and dedupes.
	expectDNSFailoverNoActionEvaluation(mock, 52, 802, now-91, "all_probes_offline", "")
	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("repeated offline evaluation: %v", err)
	}
	// A warmed probe heartbeat and healthy state emit recovered.
	expectDNSFailoverNoActionEvaluation(mock, 53, 803, now, "all_probes_offline", "recovered")
	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("recovery evaluation: %v", err)
	}
	// A later independent outage can alert again.
	expectDNSFailoverNoActionEvaluation(mock, 54, 804, now-91, "", "all_probes_offline")
	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("new offline incident: %v", err)
	}

	if len(dnsAPI.mutations) != 0 {
		t.Fatalf("all-offline handling must not call DNSPod: %#v", dnsAPI.mutations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverWorkerCooldownDoesNotClearActiveIncident(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()

	expectDNSFailoverOutboxClaim(mock, 62, 8, 906, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, now, "no_healthy_target", now-30))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
	expectDNSFailoverStates(mock,
		dnsFailoverWorkerStateRow(4, 10, 0, 5, "192.0.2.1"),
		dnsFailoverWorkerStateRow(4, 20, 8, 0, "198.51.100.20"),
	)
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET last_evaluated_at = \$2, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(8), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(int64(62), int64(906)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
		t.Fatalf("cooldown evaluation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cooldown must preserve active incident: %v", err)
	}
}

func expectDNSFailoverNoActionEvaluation(mock sqlmock.Sqlmock, outboxID, requestedAt, heartbeat int64, activeIncident, insertedEvent string) {
	expectDNSFailoverOutboxClaim(mock, outboxID, 8, requestedAt, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRowWithState(8, 10, true, true, nil, nil, activeIncident, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, heartbeat, 3))
	successStreak := int64(1)
	if activeIncident == "all_probes_offline" && insertedEvent == "recovered" {
		successStreak = 8
	}
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, successStreak, 0, "192.0.2.1"))
	if insertedEvent != "" {
		if insertedEvent == "recovered" {
			mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET active_incident_type = '', active_incident_since = NULL WHERE id = \$1 AND active_incident_type = \$2`).
				WithArgs(int64(8), activeIncident).WillReturnResult(sqlmock.NewResult(0, 1))
			expectDNSFailoverEventInsertMessageContains(mock, 8, 10, insertedEvent, "恢复")
		} else {
			mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET active_incident_type = \$2, active_incident_since = \$3 WHERE id = \$1`).
				WithArgs(int64(8), insertedEvent, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			expectDNSFailoverEventInsert(mock, 8, 10, insertedEvent)
		}
	}
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET last_evaluated_at = \$2, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(8), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
		WithArgs(outboxID, requestedAt).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func dnsFailoverWorkerRuleRow(groupID, currentTargetID int64, enabled, autoFailback bool, weight *int64) []driver.Value {
	return dnsFailoverWorkerRuleRowWithState(groupID, currentTargetID, enabled, autoFailback, weight, nil, "", nil)
}

func dnsFailoverWorkerRuleRowWithState(groupID, currentTargetID int64, enabled, autoFailback bool, weight *int64, lastSwitch any, activeIncident string, incidentSince any) []driver.Value {
	return []driver.Value{
		groupID, "官网", int64(101), "example.com", int64(202), "www", "10=0", "默认", int64(601), int64(9), weight,
		currentTargetID, boolToInt64(enabled), boolToInt64(autoFailback), int64(30), int64(3000), int64(3), int64(6), int64(5), int64(8), int64(90), int64(300), lastSwitch, "", activeIncident, incidentSince, int64(600), int64(600),
	}
}

func dnsFailoverWorkerTargetRow(id, groupID, sort int64, name, dnsType, dnsValue string, checkPort int64, enabled bool) []driver.Value {
	return []driver.Value{id, groupID, sort, name, dnsType, dnsValue, "check.example.com", checkPort, boolToInt64(enabled), int64(600), int64(600)}
}

func dnsFailoverWorkerProbeRow(id, heartbeat, prewarm int64) []driver.Value {
	return []driver.Value{id, heartbeat, prewarm}
}

func dnsFailoverWorkerStateRow(probeID, targetID, success, failure int64, resolvedIP string) []driver.Value {
	return []driver.Value{probeID, targetID, success, failure, resolvedIP}
}

func expectDNSFailoverOutboxClaim(mock sqlmock.Sqlmock, id, groupID, requestedAt int64, attempts int) {
	expectDNSFailoverOutboxClaimOperation(mock, id, groupID, "evaluate", nil, nil, requestedAt, attempts)
}

func expectDNSFailoverOutboxClaimOperation(mock sqlmock.Sqlmock, id, groupID int64, operation string, targetID, sourceTargetID any, requestedAt int64, attempts int) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, group_id, operation, target_id, source_target_id, requested_at, attempts, last_error.*FROM v2_dns_failover_eval_outbox.*next_attempt_at <= \$1.*ORDER BY next_attempt_at ASC, requested_at ASC.*LIMIT 1.*FOR UPDATE SKIP LOCKED`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "operation", "target_id", "source_target_id", "requested_at", "attempts", "last_error"}).
			AddRow(id, groupID, operation, targetID, sourceTargetID, requestedAt, attempts, ""))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(groupID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
}

func expectDNSFailoverOutboxClaimOperationWithError(mock sqlmock.Sqlmock, id, groupID int64, operation string, targetID, sourceTargetID any, requestedAt int64, attempts int, lastError string) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, group_id, operation, target_id, source_target_id, requested_at, attempts, last_error.*FROM v2_dns_failover_eval_outbox.*next_attempt_at <= \$1.*ORDER BY next_attempt_at ASC, requested_at ASC.*LIMIT 1.*FOR UPDATE SKIP LOCKED`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "operation", "target_id", "source_target_id", "requested_at", "attempts", "last_error"}).
			AddRow(id, groupID, operation, targetID, sourceTargetID, requestedAt, attempts, lastError))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(groupID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
}

func expectDNSFailoverGroupAdvisoryLock(mock sqlmock.Sqlmock, groupID int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(groupID).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectDNSFailoverGroupLock(mock sqlmock.Sqlmock, values []driver.Value) {
	columns := []string{
		"id", "name", "domain_id", "domain", "record_id", "subdomain", "record_line_id", "record_line_name", "ttl", "mx", "weight",
		"current_target_id", "enabled", "auto_failback", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold",
		"single_probe_failure_threshold", "single_probe_success_threshold", "probe_offline_sec", "cooldown_sec", "last_switch_at", "last_switch_reason", "active_incident_type", "active_incident_since", "created_at", "updated_at",
	}
	mock.ExpectQuery(`(?s)SELECT id, name, domain_id.*FROM v2_dns_failover_group.*WHERE id = \$1.*FOR UPDATE`).
		WithArgs(values[0]).WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
}

func expectDNSFailoverTargets(mock sqlmock.Sqlmock, values ...[]driver.Value) {
	rows := sqlmock.NewRows([]string{"id", "group_id", "sort", "name", "dns_type", "dns_value", "check_host", "check_port", "enabled", "created_at", "updated_at"})
	for _, value := range values {
		rows.AddRow(value...)
	}
	mock.ExpectQuery(`(?s)SELECT id, group_id, sort, name, dns_type, dns_value, check_host, check_port, enabled, created_at, updated_at.*FROM v2_dns_failover_target.*WHERE group_id = \$1.*ORDER BY sort ASC, id ASC`).
		WithArgs(values[0][1]).WillReturnRows(rows)
}

func expectDNSFailoverProbes(mock sqlmock.Sqlmock, values ...[]driver.Value) {
	rows := sqlmock.NewRows([]string{"id", "last_heartbeat_at", "prewarm_count"})
	for _, value := range values {
		rows.AddRow(value...)
	}
	mock.ExpectQuery(`(?s)SELECT p.id, p.last_heartbeat_at, p.prewarm_count.*FROM v2_dns_failover_group_probe gp.*JOIN v2_dns_probe p.*p.enabled = 1.*WHERE gp.group_id = \$1.*ORDER BY p.id ASC`).
		WithArgs(int64(8)).WillReturnRows(rows)
}

func expectDNSFailoverStates(mock sqlmock.Sqlmock, values ...[]driver.Value) {
	rows := sqlmock.NewRows([]string{"probe_id", "target_id", "consecutive_success", "consecutive_failure", "last_resolved_ip"})
	for _, value := range values {
		rows.AddRow(value...)
	}
	mock.ExpectQuery(`(?s)SELECT s.probe_id, s.target_id, s.consecutive_success, s.consecutive_failure, s.last_resolved_ip.*FROM v2_dns_probe_target_state s.*s.warmed_up = 1.*t.group_id = \$1.*ORDER BY s.probe_id ASC, s.target_id ASC`).
		WithArgs(int64(8)).WillReturnRows(rows)
}

func expectDNSFailoverActiveIncidentSet(mock sqlmock.Sqlmock, groupID int64, eventType string) {
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET active_incident_type = \$2, active_incident_since = \$3 WHERE id = \$1`).
		WithArgs(groupID, eventType, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectDNSFailoverActiveIncidentClearDivergence(mock sqlmock.Sqlmock, groupID int64) {
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET active_incident_type = '', active_incident_since = NULL WHERE id = \$1 AND active_incident_type = 'dns_state_diverged'`).
		WithArgs(groupID).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectDNSFailoverEventInsert(mock sqlmock.Sqlmock, groupID, targetID int64, eventType string) {
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_event.*group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at.*VALUES \(\$1, NULL, \$2, \$3, \$4, \$5, \$6, NULL, \$7\)`).
		WithArgs(groupID, targetID, eventType, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func expectDNSFailoverEventInsertNullableTarget(mock sqlmock.Sqlmock, groupID int64, targetID any, eventType string) {
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_event.*group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at.*VALUES \(\$1, NULL, \$2, \$3, \$4, \$5, \$6, NULL, \$7\)`).
		WithArgs(groupID, targetID, eventType, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

type dnsFailoverSQLStringContains string

func (expected dnsFailoverSQLStringContains) Match(value driver.Value) bool {
	text, ok := value.(string)
	return ok && strings.Contains(text, string(expected))
}

type dnsFailoverSQLStringContainsAll []string

func (expected dnsFailoverSQLStringContainsAll) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, part := range expected {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func expectDNSFailoverEventInsertMessageContains(mock sqlmock.Sqlmock, groupID, targetID int64, eventType, messagePart string) {
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_event.*group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at.*VALUES \(\$1, NULL, \$2, \$3, \$4, \$5, \$6, NULL, \$7\)`).
		WithArgs(groupID, targetID, eventType, dnsFailoverSQLStringContains(messagePart), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func expectDNSFailoverEventInsertDetailsContain(mock sqlmock.Sqlmock, groupID, targetID int64, eventType string, detailParts ...string) {
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_event.*group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at.*VALUES \(\$1, NULL, \$2, \$3, \$4, \$5, \$6, NULL, \$7\)`).
		WithArgs(groupID, targetID, eventType, sqlmock.AnyArg(), dnsFailoverSQLStringContainsAll(detailParts), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
}
