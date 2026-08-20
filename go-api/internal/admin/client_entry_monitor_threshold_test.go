package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNormalizeClientEntryMonitorThresholds(t *testing.T) {
	tests := []struct {
		name        string
		failure     int64
		success     int64
		wantFailure int64
		wantSuccess int64
		wantError   string
	}{
		{name: "defaults", wantFailure: 3, wantSuccess: 2},
		{name: "minimum", failure: 2, success: 1, wantFailure: 2, wantSuccess: 1},
		{name: "maximum", failure: 10, success: 10, wantFailure: 10, wantSuccess: 10},
		{name: "failure too small", failure: 1, success: 2, wantError: "故障确认次数"},
		{name: "failure too large", failure: 11, success: 2, wantError: "故障确认次数"},
		{name: "success too large", failure: 3, success: 11, wantError: "恢复确认次数"},
		{name: "negative success", failure: 3, success: -1, wantError: "恢复确认次数"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := ClientEntryMonitorSaveItem{FailureThreshold: test.failure, SuccessThreshold: test.success}
			err := normalizeClientEntryMonitorThresholds(&item)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize thresholds: %v", err)
			}
			if item.FailureThreshold != test.wantFailure || item.SuccessThreshold != test.wantSuccess {
				t.Fatalf("thresholds = (%d, %d), want (%d, %d)", item.FailureThreshold,
					item.SuccessThreshold, test.wantFailure, test.wantSuccess)
			}
		})
	}
	if err := normalizeClientEntryMonitorThresholds(nil); err == nil {
		t.Fatal("nil monitor item was accepted")
	}
}

func TestLoadClientEntryMonitorRecordsIncludesConfirmationThresholds(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}

	mock.ExpectQuery(`(?s)SELECT m\.id, m\.policy_id,.*m\.failure_threshold, m\.success_threshold.*FROM v2_client_entry_monitor m`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_id", "name", "action", "enabled", "check_interval_sec", "tcp_timeout_ms",
			"failure_threshold", "success_threshold", "created_at", "updated_at",
		}).AddRow(int64(7), int64(42), "动态入口", "override", int64(1), int64(30), int64(5000),
			int64(5), int64(3), int64(100), int64(200)))
	mock.ExpectQuery(`(?s)SELECT t\.id, t\.monitor_id,.*FROM v2_client_entry_monitor_target t`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "monitor_id", "source_key", "name", "host", "port", "sort", "auto_split_enabled",
		}))
	mock.ExpectQuery(`(?s)SELECT s\.target_id, s\.probe_id,.*FROM v2_client_entry_monitor_state s`).
		WillReturnRows(sqlmock.NewRows([]string{
			"target_id", "probe_id", "probe_name", "last_success", "last_latency_ms", "last_error",
			"last_resolved_ip", "consecutive_success", "consecutive_failure", "last_reported_at",
		}))

	items, err := service.loadClientEntryMonitorRecords(context.Background())
	if err != nil {
		t.Fatalf("loadClientEntryMonitorRecords: %v", err)
	}
	if len(items) != 1 || items[0].FailureThreshold != 5 || items[0].SuccessThreshold != 3 {
		t.Fatalf("records = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
