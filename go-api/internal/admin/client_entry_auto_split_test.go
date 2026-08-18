package admin

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestParseClientEntryMonitorSplitGroupSourceKey(t *testing.T) {
	policyID, groupID, ok := parseClientEntryMonitorSplitGroupSourceKey("policy:42:split-group:9")
	if !ok || policyID != 42 || groupID != 9 {
		t.Fatalf("parse = (%d, %d, %v), want (42, 9, true)", policyID, groupID, ok)
	}
	for _, invalid := range []string{
		"", "policy:42", "policy:42:split-group:0", "policy:0:split-group:9",
		"policy:42:node:9", "policy:42:split-group:9:extra", "policy:x:split-group:9",
	} {
		if _, _, ok := parseClientEntryMonitorSplitGroupSourceKey(invalid); ok {
			t.Errorf("invalid source key %q was accepted", invalid)
		}
	}
}

func TestEnqueueClientEntryAutoSplitReplacesStalePendingGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_auto_split_operation AS current_operation.*ON CONFLICT \(policy_id, source_group_id\) WHERE status = 'pending' DO UPDATE SET.*target_generation = EXCLUDED.target_generation`).
		WithArgs(int64(3), int64(5), int64(9), int64(42), int64(7), "198.51.100.7", int64(11), int64(13), int64(100)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	snapshot := clientEntryProbeTargetSnapshot{
		TargetID: 5, TargetVersion: 9, MonitorID: 3, PolicyID: 42,
		SourceKey: "policy:42:split-group:7", Host: "198.51.100.7", AutoSplitEnabled: true,
	}
	if err := enqueueClientEntryAutoSplit(context.Background(), tx, snapshot, 11, 13, 100); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryAutoSplitWaitsForEveryOnlineProbe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()
	expectClientEntryAutoSplitClaim(mock, now)
	expectClientEntryAutoSplitSource(mock, now, []any{
		[]any{int64(101), int64(0), int64(2), now},
		[]any{int64(102), int64(1), int64(0), now},
	})
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_auto_split_operation.*SET last_error = \$2, next_attempt_at = \$3`).
		WithArgs(int64(1), "尚未满足所有在线探针连续两次失败", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processed, changed, err := service.processNextClientEntryAutoSplit(context.Background())
	if err != nil {
		t.Fatalf("processNextClientEntryAutoSplit: %v", err)
	}
	if !processed || changed {
		t.Fatalf("result = (%v, %v), want (true, false)", processed, changed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryAutoSplitKeepsPendingWhenBackupPoolIsInsufficient(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()
	expectClientEntryAutoSplitClaim(mock, now, "尚未满足所有在线探针连续两次失败")
	expectClientEntryAutoSplitSource(mock, now, []any{
		[]any{int64(101), int64(0), int64(2), now},
		[]any{int64(102), int64(0), int64(3), now},
	}, "尚未满足所有在线探针连续两次失败")
	expectClientEntryAutoSplitLeafAndAssignments(mock, 4)
	mock.ExpectQuery(`(?s)SELECT backup.id, backup.name, backup.ip, backup.port,.*FOR UPDATE OF backup SKIP LOCKED`).
		WillReturnRows(clientEntryAutoSplitBackupRows().
			AddRow(int64(51), "备用一", "203.0.113.51", int64(443), int64(1), int64(30), int64(3000), int64(1), int64(10), int64(0), now, now))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event.*VALUES \(\$1, \$2, NULL, \$3, \$4, \$5, NULL, 0, \$6, '', \$6\)`).
		WithArgs(int64(3), int64(5), clientEntryAutoSplitBackupIPShortageEventType,
			"用户入口自动二分等待备用 IP\n规则：高级入口\n故障入口：198.51.100.9\n原因：可用备用 IP 不足 2 个\n系统将在备用 IP 可用后自动重试",
			`{"operation_id":1,"policy_id":42,"source_group_id":9,"source_host":"198.51.100.9"}`, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_auto_split_operation.*SET last_error = \$2, next_attempt_at = \$3`).
		WithArgs(int64(1), clientEntryAutoSplitBackupIPInsufficientReason, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processed, changed, err := service.processNextClientEntryAutoSplit(context.Background())
	if err != nil {
		t.Fatalf("processNextClientEntryAutoSplit: %v", err)
	}
	if !processed || changed {
		t.Fatalf("result = (%v, %v), want (true, false)", processed, changed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryAutoSplitDoesNotRepeatBackupPoolShortageEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()
	expectClientEntryAutoSplitClaim(mock, now, clientEntryAutoSplitBackupIPInsufficientReason)
	expectClientEntryAutoSplitSource(mock, now, []any{
		[]any{int64(101), int64(0), int64(2), now},
		[]any{int64(102), int64(0), int64(3), now},
	}, clientEntryAutoSplitBackupIPInsufficientReason)
	expectClientEntryAutoSplitLeafAndAssignments(mock, 4)
	mock.ExpectQuery(`(?s)SELECT backup.id, backup.name, backup.ip, backup.port,.*FOR UPDATE OF backup SKIP LOCKED`).
		WillReturnRows(clientEntryAutoSplitBackupRows().
			AddRow(int64(51), "备用一", "203.0.113.51", int64(443), int64(1), int64(30), int64(3000), int64(1), int64(10), int64(0), now, now))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_auto_split_operation.*SET last_error = \$2, next_attempt_at = \$3`).
		WithArgs(int64(1), clientEntryAutoSplitBackupIPInsufficientReason, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processed, changed, err := service.processNextClientEntryAutoSplit(context.Background())
	if err != nil {
		t.Fatalf("processNextClientEntryAutoSplit: %v", err)
	}
	if !processed || changed {
		t.Fatalf("result = (%v, %v), want (true, false)", processed, changed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryAutoSplitAtomicallyAssignsTwoHealthyIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()
	expectClientEntryAutoSplitClaim(mock, now)
	expectClientEntryAutoSplitSource(mock, now, []any{
		[]any{int64(101), int64(0), int64(2), now},
		[]any{int64(102), int64(0), int64(2), now},
	})
	expectClientEntryAutoSplitLeafAndAssignments(mock, 4)
	mock.ExpectQuery(`(?s)SELECT backup.id, backup.name, backup.ip, backup.port,.*FOR UPDATE OF backup SKIP LOCKED`).
		WillReturnRows(clientEntryAutoSplitBackupRows().
			AddRow(int64(51), "备用一", "203.0.113.51", int64(8443), int64(1), int64(30), int64(3000), int64(1), int64(10), int64(0), now, now).
			AddRow(int64(52), "备用二", "203.0.113.52", int64(9443), int64(1), int64(30), int64(3000), int64(1), int64(20), int64(0), now, now))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy.*SET sort = sort \+ \$2`).
		WithArgs(int64(20), clientEntryRuleSortStep).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group.*SET global_sort = global_sort \+ \$2`).
		WithArgs(int64(20), clientEntryRuleSortStep).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy policy.*SET sort = positions.global_sort::INTEGER`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_user_policy_split_group.*RETURNING id`).
		WithArgs(int64(42), sqlmock.AnyArg(), "旧入口 A", "A.1", "203.0.113.51", clientEntryRuleSortStep, int64(20), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(91)))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_user_policy_split_group.*RETURNING id`).
		WithArgs(int64(42), sqlmock.AnyArg(), "旧入口 B", "A.2", "203.0.113.52", 2*clientEntryRuleSortStep, int64(30), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(92)))
	mock.ExpectExec(`(?s)WITH ranked AS .*UPDATE v2_client_entry_user_policy_split_assignment`).
		WithArgs(int64(42), int64(9), int64(2), int64(91), int64(92), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group.*SET entry_host = '', global_sort = NULL`).
		WithArgs(int64(9), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy.*SET updated_at = \$2.*mode = 'split'`).
		WithArgs(int64(42), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	for _, target := range []struct {
		key, name, host string
		port, sort      int64
	}{
		{"policy:42:split-group:91", "旧入口 A", "203.0.113.51", 8443, 10},
		{"policy:42:split-group:92", "旧入口 B", "203.0.113.52", 9443, 20},
	} {
		mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_target.*auto_split_enabled.*ON CONFLICT`).
			WithArgs(int64(3), target.key, target.name, target.host, target.port, target.sort, int64(1), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_backup_ip.*SET quarantine_until = GREATEST`).
		WithArgs("198.51.100.9", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_auto_split_operation.*SET status = \$2, backup_ip_a_id = \$3`).
		WithArgs(int64(1), "succeeded", int64(51), int64(52), int64(91), int64(92), "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event.*'auto_split'`).
		WithArgs(int64(3), int64(5), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_client_entry_monitor_target`).
		WithArgs(int64(5), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processed, changed, err := service.processNextClientEntryAutoSplit(context.Background())
	if err != nil {
		t.Fatalf("processNextClientEntryAutoSplit: %v", err)
	}
	if !processed || !changed {
		t.Fatalf("result = (%v, %v), want (true, true)", processed, changed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func expectClientEntryAutoSplitClaim(mock sqlmock.Sqlmock, now int64, lastErrors ...string) {
	lastError := ""
	if len(lastErrors) > 0 {
		lastError = lastErrors[0]
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, monitor_id, target_id, target_generation,.*ORDER BY id ASC\s*LIMIT 1`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "monitor_id", "target_id", "target_generation", "policy_id", "source_group_id", "source_host", "attempts", "created_at", "last_error",
		}).AddRow(int64(1), int64(3), int64(5), int64(2), int64(42), int64(9), "198.51.100.9", int64(0), now, lastError))
}

func expectClientEntryAutoSplitSource(mock sqlmock.Sqlmock, now int64, states []any, lastErrors ...string) {
	lastError := ""
	if len(lastErrors) > 0 {
		lastError = lastErrors[0]
	}
	probeRows := sqlmock.NewRows([]string{"id"})
	for _, raw := range states {
		values := raw.([]any)
		probeRows.AddRow(values[0])
	}
	mock.ExpectQuery(`(?s)SELECT probe.id.*FROM v2_dns_probe probe.*FOR SHARE OF probe`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(probeRows)
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_client_entry_monitor.*WHERE id = \$1.*FOR KEY SHARE`).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
	mock.ExpectQuery(`(?s)SELECT target.source_key, target.host, target.generation,.*FOR UPDATE OF target`).
		WithArgs(int64(5), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{
			"source_key", "host", "generation", "auto_split_enabled", "enabled", "check_interval_sec", "tcp_timeout_ms",
		}).AddRow("policy:42:split-group:9", "198.51.100.9", int64(2), int64(1), int64(1), int64(30), int64(3000)))
	stateRows := sqlmock.NewRows([]string{"probe_id", "last_success", "consecutive_failure", "last_reported_at"})
	for _, raw := range states {
		values := raw.([]any)
		stateRows.AddRow(values[0], values[1], values[2], values[3])
	}
	mock.ExpectQuery(`(?s)SELECT probe_id, last_success,.*FROM v2_client_entry_monitor_state.*ORDER BY probe_id`).
		WillReturnRows(stateRows)
	mock.ExpectQuery(`(?s)SELECT id, monitor_id, target_id, target_generation,.*WHERE id = \$1 AND status = 'pending'.*FOR UPDATE SKIP LOCKED`).
		WithArgs(int64(1), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "monitor_id", "target_id", "target_generation", "policy_id", "source_group_id", "source_host", "attempts", "created_at", "last_error",
		}).AddRow(int64(1), int64(3), int64(5), int64(2), int64(42), int64(9), "198.51.100.9", int64(0), now, lastError))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_auto_split_operation.*SET attempts = attempts \+ 1`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectClientEntryAutoSplitLeafAndAssignments(mock sqlmock.Sqlmock, userCount int) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryVisibleOrderLockKey).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT policy.name, split_group.name, split_group.path,.*FOR UPDATE OF split_group, policy`).
		WithArgs(int64(9), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_name", "name", "path", "entry_host", "global_sort"}).
			AddRow("高级入口", "旧入口", "A", "198.51.100.9", int64(20)))
	assignmentRows := sqlmock.NewRows([]string{"user_id"})
	for index := 1; index <= userCount; index++ {
		assignmentRows.AddRow(int64(index))
	}
	mock.ExpectQuery(`(?s)SELECT user_id.*FROM v2_client_entry_user_policy_split_assignment.*FOR UPDATE`).
		WithArgs(int64(42), int64(9)).WillReturnRows(assignmentRows)
}

func clientEntryAutoSplitBackupRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "ip", "port", "enabled", "check_interval_sec", "tcp_timeout_ms",
		"generation", "sort", "quarantine_until", "created_at", "updated_at",
	})
}
