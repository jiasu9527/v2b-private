package admin

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

type dnsFailoverLogBatchArgument []dnsFailoverLogEntry

func (expected dnsFailoverLogBatchArgument) Match(value driver.Value) bool {
	encoded, ok := value.(string)
	if !ok {
		return false
	}
	var actual []dnsFailoverLogEntry
	if err := json.Unmarshal([]byte(encoded), &actual); err != nil {
		return false
	}
	expectedJSON, err := json.Marshal([]dnsFailoverLogEntry(expected))
	if err != nil {
		return false
	}
	var normalizedExpected []dnsFailoverLogEntry
	if err := json.Unmarshal(expectedJSON, &normalizedExpected); err != nil {
		return false
	}
	return reflect.DeepEqual(actual, normalizedExpected)
}

func TestInsertDNSFailoverLogsBatchesEntriesAndPreservesStructuredDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	probeID := int64(7)
	targetID := int64(11)
	entries := []dnsFailoverLogEntry{
		{
			GroupID: 4, ProbeID: &probeID, TargetID: &targetID,
			Stage: "probe_result", Level: "warning", Outcome: "failure",
			Message:   "probe check failed",
			Details:   map[string]any{"result_id": "result-1", "success": false, "error": "timeout"},
			CreatedAt: 1234,
		},
		{
			GroupID: 4, Stage: "evaluation", Level: "info", Outcome: "no_switch",
			Message: "threshold not reached", Details: map[string]any{"failure_streak": float64(2)}, CreatedAt: 1235,
		},
	}
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_log.*group_id, probe_id, target_id, stage, level, outcome, message, details, created_at.*SELECT requested.group_id, requested.probe_id, requested.target_id.*COALESCE\(requested.details, '\{\}'::jsonb\)::text.*jsonb_to_recordset\(\$1::jsonb\)`).
		WithArgs(dnsFailoverLogBatchArgument(entries)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	if err := insertDNSFailoverLogs(context.Background(), db, entries); err != nil {
		t.Fatalf("insertDNSFailoverLogs: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestInsertDNSFailoverLogsSkipsEmptyBatchAndReturnsWriteError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	if err := insertDNSFailoverLogs(context.Background(), db, nil); err != nil {
		t.Fatalf("empty insertDNSFailoverLogs: %v", err)
	}

	writeErr := errors.New("write failed")
	mock.ExpectExec(`INSERT INTO v2_dns_failover_log`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(writeErr)
	if err := insertDNSFailoverLogs(context.Background(), db, []dnsFailoverLogEntry{{
		GroupID: 1, Stage: "evaluation", Level: "error", Outcome: "failed", Details: map[string]any{}, CreatedAt: 1234,
	}}); !errors.Is(err, writeErr) {
		t.Fatalf("error = %v, want write error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestListDNSFailoverLogsFiltersAndPaginates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	groupID := int64(4)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_log WHERE group_id = \$1 AND stage = \$2`).WithArgs(groupID, "probe_result").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT id, group_id, probe_id, target_id, stage, level, outcome, message, details, created_at`).WithArgs(groupID, "probe_result", int64(20), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "probe_id", "target_id", "stage", "level", "outcome", "message", "details", "created_at"}).AddRow(int64(9), groupID, int64(7), int64(11), "probe_result", "warning", "failure", "probe check failed", `{"error":"timeout"}`, int64(1234)))
	result, err := service.ListDNSFailoverLogs(context.Background(), DNSFailoverLogListRequest{GroupID: &groupID, Stage: "probe_result", Current: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListDNSFailoverLogs: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Data[0].Outcome != "failure" || result.Data[0].ProbeID == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
