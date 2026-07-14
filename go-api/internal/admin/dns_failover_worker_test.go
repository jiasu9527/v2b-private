package admin

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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

func TestDNSFailoverWorkerIncidentDedupRecoveryAndNewIncident(t *testing.T) {
	if got := dnsFailoverIncidentTransition("all_probes_offline", "all_probes_offline"); got != "" {
		t.Fatalf("consecutive incident should dedupe, got %q", got)
	}
	if got := dnsFailoverIncidentTransition("all_probes_offline", ""); got != "recovered" {
		t.Fatalf("healthy transition should recover, got %q", got)
	}
	if got := dnsFailoverIncidentTransition("recovered", "all_probes_offline"); got != "all_probes_offline" {
		t.Fatalf("new incident after recovery should be emitted, got %q", got)
	}
	if got := dnsFailoverIncidentTransition("probe_disagreement", "no_healthy_target"); got != "no_healthy_target" {
		t.Fatalf("changed incident should be emitted, got %q", got)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	cancel()
	done := make(chan struct{})
	go func() {
		service.dnsFailoverWorkerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("automation goroutine did not stop with context")
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

	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*SELECT g.id, \$1, 0, \$1, '', \$1, \$1.*FROM v2_dns_failover_group g.*g.enabled = 1.*last_evaluated_at IS NULL.*last_evaluated_at <= \$1 - g.check_interval_sec.*ON CONFLICT \(group_id\) DO NOTHING`).
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
	mock.ExpectQuery(`(?s)SELECT event_type.*FROM v2_dns_failover_event.*FOR UPDATE`).
		WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"event_type"}))
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
	expectNoDNSFailoverIncident(mock, 8)
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
	expectNoDNSFailoverIncident(mock, 8)
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
	expectNoDNSFailoverIncident(mock, 8)
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
	expectNoDNSFailoverIncident(mock, 8)
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
				results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "req-switch"}, {RecordID: 202, RequestID: "req-rollback"}},
				errors:  []error{nil, test.compensationErr},
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
			expectNoDNSFailoverIncident(mock, 8)
			mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2`).
				WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "current_target_failed").WillReturnResult(sqlmock.NewResult(0, 1))
			expectDNSFailoverEventInsert(mock, 8, 20, "failover")
			mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
				WithArgs(int64(45), int64(704)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

			mock.ExpectBegin()
			expectDNSFailoverGroupAdvisoryLock(mock, 8)
			expectNoDNSFailoverIncident(mock, 8)
			expectDNSFailoverEventInsert(mock, 8, 20, "dnspod_error")
			mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*ON CONFLICT \(group_id\) DO UPDATE SET.*attempts = v2_dns_failover_eval_outbox.attempts \+ 1.*next_attempt_at = EXCLUDED.next_attempt_at.*last_error = EXCLUDED.last_error`).
				WithArgs(int64(8), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()

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
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
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
	expectNoDNSFailoverIncident(mock, 8)
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg(), "current_target_failed").WillReturnError(errors.New("state update failed"))
	mock.ExpectRollback()

	mock.ExpectBegin()
	expectDNSFailoverGroupAdvisoryLock(mock, 8)
	expectNoDNSFailoverIncident(mock, 8)
	expectDNSFailoverEventInsert(mock, 8, 20, "dnspod_error")
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*ON CONFLICT \(group_id\) DO UPDATE SET`).
		WithArgs(int64(8), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
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
		expectDNSFailoverGroupAdvisoryLock(mock, 8)
		expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
		expectDNSFailoverTargets(mock,
			dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
			dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
		)
		expectNoDNSFailoverIncident(mock, 8)
		mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group SET current_target_id = \$2, last_switch_at = \$3, last_switch_reason = 'manual', last_evaluated_at = \$3, updated_at = \$3 WHERE id = \$1`).
			WithArgs(int64(8), int64(20), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		expectDNSFailoverEventInsert(mock, 8, 20, "manual_switch")
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
		expectDNSFailoverGroupAdvisoryLock(mock, 8)
		expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
		expectDNSFailoverTargets(mock,
			dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
			dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
		)
		expectNoDNSFailoverIncident(mock, 8)
		expectDNSFailoverEventInsertDetailsContain(mock, 8, 20, "dnspod_error", `"operation":"manual_switch"`, `"retry_semantics":"reevaluate_group_health"`, `"request_id":"manual-error-request"`)
		mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*VALUES \(\$1, \$2, 1, \$3, \$4, \$2, \$2\).*ON CONFLICT \(group_id\) DO UPDATE SET`).
			WithArgs(int64(8), sqlmock.AnyArg(), sqlmock.AnyArg(), "manual DNS failed").WillReturnResult(sqlmock.NewResult(1, 1))
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
		expectDNSFailoverGroupAdvisoryLock(mock, 8)
		expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
		expectDNSFailoverTargets(mock,
			dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
			dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
		)
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
	expectDNSFailoverNoActionEvaluation(mock, 54, 804, now-91, "recovered", "all_probes_offline")
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

func expectDNSFailoverNoActionEvaluation(mock sqlmock.Sqlmock, outboxID, requestedAt, heartbeat int64, lastEvent, insertedEvent string) {
	expectDNSFailoverOutboxClaim(mock, outboxID, 8, requestedAt, 0)
	expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
	expectDNSFailoverTargets(mock,
		dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
		dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "CNAME", "backup.example.net.", 8443, true),
	)
	expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, heartbeat, 3))
	expectDNSFailoverStates(mock, dnsFailoverWorkerStateRow(4, 10, 1, 0, "192.0.2.1"))
	rows := sqlmock.NewRows([]string{"event_type"})
	if lastEvent != "" {
		rows.AddRow(lastEvent)
	}
	mock.ExpectQuery(`(?s)SELECT event_type.*FROM v2_dns_failover_event.*FOR UPDATE`).WithArgs(int64(8)).WillReturnRows(rows)
	if insertedEvent != "" {
		if insertedEvent == "recovered" {
			expectDNSFailoverEventInsertMessageContains(mock, 8, 10, insertedEvent, "恢复")
		} else {
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
	return []driver.Value{
		groupID, "官网", int64(101), "example.com", int64(202), "www", "10=0", "默认", int64(601), int64(9), weight,
		currentTargetID, boolToInt64(enabled), boolToInt64(autoFailback), int64(30), int64(3000), int64(3), int64(6), int64(5), int64(8), int64(90), int64(300), nil, "", int64(600), int64(600),
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
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, group_id.*FROM v2_dns_failover_eval_outbox.*next_attempt_at <= \$1.*ORDER BY next_attempt_at ASC, requested_at ASC.*LIMIT 1`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id"}).AddRow(id, groupID))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).
		WithArgs(groupID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT id, group_id, requested_at, attempts.*FROM v2_dns_failover_eval_outbox.*id = \$1.*group_id = \$2.*next_attempt_at <= \$3.*FOR UPDATE SKIP LOCKED`).
		WithArgs(id, groupID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "requested_at", "attempts"}).AddRow(id, groupID, requestedAt, attempts))
}

func expectDNSFailoverGroupAdvisoryLock(mock sqlmock.Sqlmock, groupID int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(groupID).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectDNSFailoverGroupLock(mock sqlmock.Sqlmock, values []driver.Value) {
	columns := []string{
		"id", "name", "domain_id", "domain", "record_id", "subdomain", "record_line_id", "record_line_name", "ttl", "mx", "weight",
		"current_target_id", "enabled", "auto_failback", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold",
		"single_probe_failure_threshold", "single_probe_success_threshold", "probe_offline_sec", "cooldown_sec", "last_switch_at", "last_switch_reason", "created_at", "updated_at",
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

func expectNoDNSFailoverIncident(mock sqlmock.Sqlmock, groupID int64) {
	mock.ExpectQuery(`(?s)SELECT event_type.*FROM v2_dns_failover_event.*FOR UPDATE`).
		WithArgs(groupID).WillReturnRows(sqlmock.NewRows([]string{"event_type"}))
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
