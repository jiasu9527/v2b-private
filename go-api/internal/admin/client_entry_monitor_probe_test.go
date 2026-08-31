package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestConfirmClientEntryMonitorAvailability(t *testing.T) {
	tests := []struct {
		name             string
		previous         sql.NullInt64
		success          bool
		successStreak    int64
		failureStreak    int64
		failureThreshold int64
		successThreshold int64
		want             sql.NullInt64
		wantEvent        string
	}{
		{name: "unknown first failure", failureStreak: 1, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{}},
		{name: "unknown second failure stays pending", failureStreak: 2, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{}},
		{name: "unknown third failure", failureStreak: 3, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 0, Valid: true}, wantEvent: "down"},
		{name: "healthy second failure stays healthy", previous: sql.NullInt64{Int64: 1, Valid: true}, failureStreak: 2, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 1, Valid: true}},
		{name: "healthy third failure", previous: sql.NullInt64{Int64: 1, Valid: true}, failureStreak: 3, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 0, Valid: true}, wantEvent: "down"},
		{name: "confirmed failure stays down", previous: sql.NullInt64{Int64: 0, Valid: true}, failureStreak: 4, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 0, Valid: true}},
		{name: "first recovery success stays down", previous: sql.NullInt64{Int64: 0, Valid: true}, success: true, successStreak: 1, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 0, Valid: true}},
		{name: "second recovery success", previous: sql.NullInt64{Int64: 0, Valid: true}, success: true, successStreak: 2, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 1, Valid: true}, wantEvent: "recovered"},
		{name: "custom fourth failure stays healthy", previous: sql.NullInt64{Int64: 1, Valid: true}, failureStreak: 4, failureThreshold: 5, successThreshold: 3, want: sql.NullInt64{Int64: 1, Valid: true}},
		{name: "custom third recovery stays down", previous: sql.NullInt64{Int64: 0, Valid: true}, success: true, successStreak: 2, failureThreshold: 5, successThreshold: 3, want: sql.NullInt64{Int64: 0, Valid: true}},
		{name: "first healthy observation establishes baseline", success: true, successStreak: 1, failureThreshold: 3, successThreshold: 2, want: sql.NullInt64{Int64: 1, Valid: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, event := confirmClientEntryMonitorAvailability(test.previous, test.success, test.successStreak,
				test.failureStreak, test.failureThreshold, test.successThreshold)
			if got != test.want || event != test.wantEvent {
				t.Fatalf("availability = (%+v, %q), want (%+v, %q)", got, event, test.want, test.wantEvent)
			}
		})
	}
}

func TestClientEntryProbeFailureAfterStaleGapStartsNewConfirmation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	probeID := int64(7)
	targetID := int64(5)
	now := time.Now().Unix()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(int64(1), now))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryMonitorEventLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT target.id, target.generation, monitor.id, monitor.policy_id,.*FROM v2_client_entry_monitor_target target.*WHERE target.id = \$2`).
		WithArgs(probeID, targetID).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "generation", "monitor_id", "policy_id", "policy_name", "target_name", "source_key", "host", "port", "probe_name", "auto_split_enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold"}).
			AddRow(targetID, int64(2), int64(3), int64(42), "高级入口", "独立入口", "policy:42", "entry.example.com", int64(443), "东京探针", int64(0), int64(30), int64(3000), int64(3), int64(2)))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor_result_inbox.*ON CONFLICT \(probe_id, result_id\) DO NOTHING.*RETURNING id`).
		WithArgs(probeID, targetID, nil, "entry-timeout-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery(`(?s)SELECT last_success, consecutive_success, consecutive_failure.*FROM v2_client_entry_monitor_state.*FOR UPDATE`).
		WithArgs(targetID, probeID).
		WillReturnRows(sqlmock.NewRows([]string{"last_success", "consecutive_success", "consecutive_failure", "last_reported_at"}).
			AddRow(int64(0), int64(0), int64(5), now-1000))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_state.*ON CONFLICT \(target_id, probe_id\) DO UPDATE SET`).
		WithArgs(targetID, probeID, nil, nil, "timeout", "203.0.113.9", int64(0), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	failed := false
	result, err := service.ReportDNSProbeResults(context.Background(), probeID, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "entry-timeout-1", TargetID: clientEntryProbeTargetOffset + targetID,
		TargetVersion: 2, Success: &failed, Error: "timeout", ResolvedIP: "203.0.113.9",
	}}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 1 || result.Skipped != 0 || result.Duplicates != 0 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryProbeThirdFailureCreatesStateAndAlertEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	probeID := int64(7)
	targetID := int64(5)
	now := time.Now().Unix()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(int64(1), now))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryMonitorEventLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT target.id, target.generation, monitor.id, monitor.policy_id,.*FROM v2_client_entry_monitor_target target.*WHERE target.id = \$2`).
		WithArgs(probeID, targetID).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "generation", "monitor_id", "policy_id", "policy_name", "target_name", "source_key", "host", "port", "probe_name", "auto_split_enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold"}).
			AddRow(targetID, int64(2), int64(3), int64(42), "高级入口", "独立入口", "policy:42", "entry.example.com", int64(443), "东京探针", int64(0), int64(30), int64(3000), int64(3), int64(2)))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor_result_inbox.*ON CONFLICT \(probe_id, result_id\) DO NOTHING.*RETURNING id`).
		WithArgs(probeID, targetID, nil, "entry-timeout-2", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery(`(?s)SELECT last_success, consecutive_success, consecutive_failure.*FROM v2_client_entry_monitor_state.*FOR UPDATE`).
		WithArgs(targetID, probeID).
		WillReturnRows(sqlmock.NewRows([]string{"last_success", "consecutive_success", "consecutive_failure", "last_reported_at"}).
			AddRow(int64(1), int64(0), int64(2), now))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_state.*ON CONFLICT \(target_id, probe_id\) DO UPDATE SET`).
		WithArgs(targetID, probeID, int64(0), nil, "timeout", "203.0.113.9", int64(0), int64(3), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)SELECT EXISTS \(.*FROM v2_client_entry_monitor_state.*`).
		WithArgs("entry.example.com:443", targetID, probeID, sqlmock.AnyArg(), defaultProbeOfflineSec).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event.*VALUES`).
		WithArgs(int64(3), targetID, probeID, "down", sqlmock.AnyArg(), sqlmock.AnyArg(), "entry.example.com:443", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	failed := false
	result, err := service.ReportDNSProbeResults(context.Background(), probeID, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "entry-timeout-2", TargetID: clientEntryProbeTargetOffset + targetID,
		TargetVersion: 2, Success: &failed, Error: "timeout", ResolvedIP: "203.0.113.9",
	}}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 1 || result.Skipped != 0 || result.Duplicates != 0 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryProbeSecondRecoveryCreatesAlertEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	probeID := int64(7)
	targetID := int64(5)
	now := time.Now().Unix()
	latency := int64(32)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(int64(1), now))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryMonitorEventLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT target.id, target.generation, monitor.id, monitor.policy_id,.*FROM v2_client_entry_monitor_target target.*WHERE target.id = \$2`).
		WithArgs(probeID, targetID).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "generation", "monitor_id", "policy_id", "policy_name", "target_name", "source_key", "host", "port", "probe_name", "auto_split_enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold"}).
			AddRow(targetID, int64(2), int64(3), int64(42), "高级入口", "独立入口", "policy:42:split-group:9", "entry.example.com", int64(443), "东京探针", int64(0), int64(30), int64(3000), int64(3), int64(2)))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor_result_inbox.*ON CONFLICT \(probe_id, result_id\) DO NOTHING.*RETURNING id`).
		WithArgs(probeID, targetID, nil, "entry-recovered-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(2)))
	mock.ExpectQuery(`(?s)SELECT last_success, consecutive_success, consecutive_failure.*FROM v2_client_entry_monitor_state.*FOR UPDATE`).
		WithArgs(targetID, probeID).
		WillReturnRows(sqlmock.NewRows([]string{"last_success", "consecutive_success", "consecutive_failure", "last_reported_at"}).AddRow(int64(0), int64(1), int64(0), now))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_state.*ON CONFLICT \(target_id, probe_id\) DO UPDATE SET`).
		WithArgs(targetID, probeID, int64(1), latency, "", "203.0.113.9", int64(2), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_auto_split_operation.*last_error = '入口已有探针恢复，取消待处理二分'.*target_generation = \$2`).
		WithArgs(targetID, int64(2), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT EXISTS \(.*FROM v2_client_entry_monitor_state.*`).
		WithArgs("entry.example.com:443", targetID, probeID, sqlmock.AnyArg(), defaultProbeOfflineSec).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event.*VALUES`).
		WithArgs(int64(3), targetID, probeID, "recovered", sqlmock.AnyArg(), sqlmock.AnyArg(), "entry.example.com:443", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	succeeded := true
	result, err := service.ReportDNSProbeResults(context.Background(), probeID, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "entry-recovered-1", TargetID: clientEntryProbeTargetOffset + targetID,
		TargetVersion: 2, Success: &succeeded, LatencyMS: &latency, ResolvedIP: "203.0.113.9",
	}}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 1 || result.Skipped != 0 || result.Duplicates != 0 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryProbeFirstRecoverySampleKeepsIncidentPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	probeID := int64(7)
	targetID := int64(5)
	now := time.Now().Unix()
	latency := int64(2900)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(int64(1), now))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryMonitorEventLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT target.id, target.generation, monitor.id, monitor.policy_id,.*FROM v2_client_entry_monitor_target target.*WHERE target.id = \$2`).
		WithArgs(probeID, targetID).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "generation", "monitor_id", "policy_id", "policy_name", "target_name", "source_key", "host", "port", "probe_name", "auto_split_enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold"}).
			AddRow(targetID, int64(2), int64(3), int64(42), "高级入口", "独立入口", "policy:42:split-group:9", "entry.example.com", int64(443), "东京探针", int64(1), int64(30), int64(3000), int64(3), int64(2)))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor_result_inbox.*RETURNING id`).
		WithArgs(probeID, targetID, nil, "entry-recovery-pending", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
	mock.ExpectQuery(`(?s)SELECT last_success, consecutive_success, consecutive_failure.*FROM v2_client_entry_monitor_state.*FOR UPDATE`).
		WithArgs(targetID, probeID).
		WillReturnRows(sqlmock.NewRows([]string{"last_success", "consecutive_success", "consecutive_failure", "last_reported_at"}).
			AddRow(int64(0), int64(0), int64(3), now))
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_state.*ON CONFLICT \(target_id, probe_id\) DO UPDATE SET`).
		WithArgs(targetID, probeID, int64(0), latency, "", "203.0.113.9", int64(1), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	// No recovery event and no pending auto-split cancellation are expected until
	// the second consecutive successful sample reaches success_threshold=2.
	mock.ExpectCommit()

	succeeded := true
	result, err := service.ReportDNSProbeResults(context.Background(), probeID, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "entry-recovery-pending", TargetID: clientEntryProbeTargetOffset + targetID,
		TargetVersion: 2, Success: &succeeded, LatencyMS: &latency, ResolvedIP: "203.0.113.9",
	}}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 1 || result.Skipped != 0 || result.Duplicates != 0 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryProbeTargetEncodingDoesNotOverlapDNSIDs(t *testing.T) {
	encoded := encodeClientEntryProbeTargetID(123)
	if encoded <= clientEntryProbeTargetOffset {
		t.Fatalf("encoded = %d", encoded)
	}
	decoded, ok := decodeClientEntryProbeTargetID(encoded)
	if !ok || decoded != 123 {
		t.Fatalf("decoded = %d, ok = %v", decoded, ok)
	}
	if isClientEntryProbeTargetID(123) {
		t.Fatal("ordinary DNS target was classified as a client-entry target")
	}
}

func TestClientEntryManualRunRejectsLegacyAndUnexpectedPairs(t *testing.T) {
	if runID, err := validateClientEntryMonitorRunResult(context.Background(), nil, 0, 5, 7, 2); err != nil || runID != 0 {
		t.Fatalf("legacy run result = (%d, %v), want ignored", runID, err)
	}

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
	mock.ExpectQuery(`(?s)SELECT id, status, expected_pairs.*FROM v2_client_entry_monitor_run WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(91)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expected_pairs"}).
			AddRow(int64(91), "running", `[{"target_id":5,"probe_id":8,"target_version":2}]`))
	runID, err := validateClientEntryMonitorRunResult(context.Background(), tx, 91, 5, 7, 2)
	if err != nil || runID != 0 {
		t.Fatalf("unexpected tuple result = (%d, %v), want ignored", runID, err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	mock.ExpectBegin()
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx generation: %v", err)
	}
	mock.ExpectQuery(`(?s)SELECT id, status, expected_pairs.*FROM v2_client_entry_monitor_run WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(91)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expected_pairs"}).
			AddRow(int64(91), "running", `[{"target_id":5,"probe_id":7,"target_version":1}]`))
	runID, err = validateClientEntryMonitorRunResult(context.Background(), tx, 91, 5, 7, 2)
	if err != nil || runID != 0 {
		t.Fatalf("changed endpoint generation result = (%d, %v), want ignored", runID, err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback generation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryProbeSkipsStaleTargetGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	probeID := int64(7)
	targetID := int64(5)
	now := time.Now().Unix()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(int64(1), now))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryMonitorEventLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT target.id, target.generation, monitor.id, monitor.policy_id,.*WHERE target.id = \$2`).
		WithArgs(probeID, targetID).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "generation", "monitor_id", "policy_id", "policy_name", "target_name", "source_key", "host", "port", "probe_name", "auto_split_enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold"}).
			AddRow(targetID, int64(2), int64(3), int64(42), "高级入口", "独立入口", "policy:42", "new.example.com", int64(443), "东京探针", int64(0), int64(30), int64(3000), int64(3), int64(2)))
	mock.ExpectCommit()
	failed := false
	result, err := service.ReportDNSProbeResults(context.Background(), probeID, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "old-endpoint-result", TargetID: clientEntryProbeTargetOffset + targetID,
		TargetVersion: 1, Success: &failed, Error: "old endpoint timeout",
	}}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Skipped != 1 || result.Accepted != 0 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
