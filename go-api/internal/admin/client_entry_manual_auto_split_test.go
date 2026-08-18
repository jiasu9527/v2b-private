package admin

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListClientEntryManualAutoSplitOptionsReturnsOnlyQueryEligibleLeaves(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true, clientEntryMonitorAt: time.Now()}
	mock.ExpectQuery(`(?s)WITH online_probe AS .*state\.consecutive_failure >= \$3.*NOT EXISTS \(.*v2_client_entry_auto_split_operation.*HAVING COUNT\(online_probe\.id\) > 0`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(clientEntryMonitorFailureThreshold)).
		WillReturnRows(sqlmock.NewRows([]string{
			"target_id", "policy_id", "group_id", "policy_name", "group_name", "host", "port",
			"user_count", "online_probe_count", "failed_probe_count", "auto_split_enabled",
		}).AddRow(int64(17), int64(42), int64(9), " 高级入口 ", " 故障叶子 ", " 198.51.100.9 ",
			int64(443), int64(12), int64(3), int64(3), int64(0)))

	options, err := service.ListClientEntryManualAutoSplitOptions(context.Background())
	if err != nil {
		t.Fatalf("ListClientEntryManualAutoSplitOptions: %v", err)
	}
	want := []ClientEntryManualAutoSplitOption{{
		TargetID: 17, PolicyID: 42, GroupID: 9, PolicyName: "高级入口", GroupName: "故障叶子",
		Host: "198.51.100.9", Port: 443, UserCount: 12, OnlineProbeCount: 3,
		FailedProbeCount: 3, AutoSplitEnabled: false,
	}}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("options = %#v, want %#v", options, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRequestClientEntryManualAutoSplitAllowsDisabledAutomaticFlag(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()
	expectManualClientEntrySplitValidation(mock, now, []any{
		[]any{int64(101), int64(0), int64(2), now},
		[]any{int64(102), int64(0), int64(3), now},
	})
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_auto_split_operation AS current_operation.*trigger_probe_id, trigger_result_inbox_id.*VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, NULL, NULL.*ON CONFLICT.*DO UPDATE SET\s*updated_at = current_operation\.updated_at\s*RETURNING current_operation\.id`).
		WithArgs(int64(3), int64(5), int64(2), int64(42), int64(9), "198.51.100.9", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(77)))
	mock.ExpectCommit()

	operationID, err := service.RequestClientEntryManualAutoSplit(context.Background(), 5, 88)
	if err != nil {
		t.Fatalf("RequestClientEntryManualAutoSplit: %v", err)
	}
	if operationID != 77 {
		t.Fatalf("operation id = %d, want 77", operationID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRequestClientEntryManualAutoSplitRejectsRecoveredProbe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()
	expectManualClientEntrySplitValidation(mock, now, []any{
		[]any{int64(101), int64(0), int64(2), now},
		[]any{int64(102), int64(1), int64(0), now},
	})
	mock.ExpectRollback()

	operationID, err := service.RequestClientEntryManualAutoSplit(context.Background(), 5, 88)
	if !errors.Is(err, ErrClientEntryManualAutoSplitUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	if operationID != 0 {
		t.Fatalf("operation id = %d, want 0", operationID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRequestClientEntryManualAutoSplitReturnsExistingPendingOperationWithoutReset(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_user.*is_admin = 1 OR is_staff = 1`).
		WithArgs(int64(88)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(88)))
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_client_entry_auto_split_operation.*target_id = \$1 AND status = 'pending'.*LIMIT 1`).
		WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(77)))
	mock.ExpectCommit()

	operationID, err := service.RequestClientEntryManualAutoSplit(context.Background(), 5, 88)
	if err != nil {
		t.Fatalf("RequestClientEntryManualAutoSplit: %v", err)
	}
	if operationID != 77 {
		t.Fatalf("operation id = %d, want existing 77", operationID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestLoadClientEntryAutoSplitSourceStatesAllowsManualRequestWhenAutomaticFlagDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	now := time.Now().Unix()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	mock.ExpectQuery(`(?s)SELECT probe.id.*FROM v2_dns_probe probe.*FOR SHARE OF probe`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_client_entry_monitor.*FOR KEY SHARE`).
		WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
	mock.ExpectQuery(`(?s)SELECT target.source_key, target.host, target.generation,.*target.auto_split_enabled.*FOR UPDATE OF target`).
		WithArgs(int64(5), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{
			"source_key", "host", "generation", "auto_split_enabled", "enabled", "check_interval_sec", "tcp_timeout_ms",
		}).AddRow("policy:42:split-group:9", "198.51.100.9", int64(2), int64(0), int64(1), int64(30), int64(3000)))
	mock.ExpectQuery(`(?s)SELECT probe_id, last_success,.*FROM v2_client_entry_monitor_state.*ORDER BY probe_id`).
		WithArgs(int64(5), int64(101)).
		WillReturnRows(sqlmock.NewRows([]string{"probe_id", "last_success", "consecutive_failure", "last_reported_at"}).
			AddRow(int64(101), int64(0), int64(2), now))

	operation := clientEntryAutoSplitOperation{
		MonitorID: 3, TargetID: 5, TargetGeneration: 2, PolicyID: 42,
		SourceGroupID: 9, SourceHost: "198.51.100.9", TriggerProbeID: sql.NullInt64{},
	}
	_, _, sourceAutoSplitEnabled, states, err := loadClientEntryAutoSplitSourceStates(context.Background(), tx, operation, now)
	if err != nil {
		t.Fatalf("loadClientEntryAutoSplitSourceStates: %v", err)
	}
	if sourceAutoSplitEnabled {
		t.Fatal("source automatic split flag = true, want false")
	}
	if len(states) != 1 || states[0].ProbeID != 101 || states[0].ConsecutiveFailure != 2 {
		t.Fatalf("states = %#v", states)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func expectManualClientEntrySplitValidation(mock sqlmock.Sqlmock, now int64, states []any) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_user.*is_admin = 1 OR is_staff = 1`).
		WithArgs(int64(88)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(88)))
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_client_entry_auto_split_operation.*target_id = \$1 AND status = 'pending'.*LIMIT 1`).
		WithArgs(int64(5)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT monitor_id.*v2_client_entry_monitor_target WHERE id = \$1`).
		WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"monitor_id"}).AddRow(int64(3)))
	probeRows := sqlmock.NewRows([]string{"id"})
	for _, raw := range states {
		probeRows.AddRow(raw.([]any)[0])
	}
	mock.ExpectQuery(`(?s)SELECT probe.id.*FROM v2_dns_probe probe.*FOR SHARE OF probe`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(probeRows)
	mock.ExpectQuery(`(?s)SELECT id.*FROM v2_client_entry_monitor.*FOR KEY SHARE`).
		WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
	// This query intentionally has no auto_split_enabled predicate or result:
	// explicit manual confirmation must work while the automatic switch is off.
	mock.ExpectQuery(`(?s)SELECT target.source_key, target.host, target.generation,.*monitor.enabled.*FOR UPDATE OF target`).
		WithArgs(int64(5), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{
			"source_key", "host", "generation", "policy_id", "enabled", "check_interval_sec", "tcp_timeout_ms",
		}).AddRow("policy:42:split-group:9", "198.51.100.9", int64(2), int64(42), int64(1), int64(30), int64(3000)))
	mock.ExpectQuery(`(?s)SELECT policy.name, split_group.name,.*FOR KEY SHARE OF split_group, policy`).
		WithArgs(int64(9), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_name", "group_name", "entry_host", "global_sort"}).
			AddRow("高级入口", "故障叶子", "198.51.100.9", int64(20)))
	mock.ExpectQuery(`(?s)SELECT user_id.*FROM v2_client_entry_user_policy_split_assignment.*FOR UPDATE`).
		WithArgs(int64(42), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)).AddRow(int64(4)))
	stateRows := sqlmock.NewRows([]string{"probe_id", "last_success", "consecutive_failure", "last_reported_at"})
	for _, raw := range states {
		values := raw.([]any)
		stateRows.AddRow(values[0], values[1], values[2], values[3])
	}
	mock.ExpectQuery(`(?s)SELECT probe_id, last_success,.*FROM v2_client_entry_monitor_state.*ORDER BY probe_id`).
		WillReturnRows(stateRows)
}
