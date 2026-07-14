package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetDNSFailoverStatusExplainsThresholdProgress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM v2_dns_failover_group WHERE id = \$1 ORDER BY id ASC`).WithArgs(int64(50)).WillReturnRows(dnsFailoverGroupRows())
	expectDNSFailoverRuleRelations(mock)
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT last_evaluated_at, active_incident_type, active_incident_since, active_dns_incident_type, active_dns_incident_since`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"last_evaluated_at", "active_incident_type", "active_incident_since", "active_dns_incident_type", "active_dns_incident_since"}).AddRow(now-3, "", nil, "", nil))
	mock.ExpectQuery(`SELECT p.id, p.name, p.enabled, p.last_heartbeat_at, p.prewarm_count`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(4), "广州探针", int64(1), now-5, int64(3)))
	mock.ExpectQuery(`SELECT s.probe_id, s.target_id, s.last_success, s.last_latency_ms, s.last_error`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"probe_id", "target_id", "last_success", "last_latency_ms", "last_error", "last_resolved_ip", "consecutive_success", "consecutive_failure", "last_reported_at", "warmed_up"}).
			AddRow(int64(4), int64(71), int64(0), nil, "connection refused", "2001:db8::1", int64(0), int64(2), now-2, int64(1)).
			AddRow(int64(4), int64(72), int64(1), int64(35), "", "203.0.113.9", int64(9), int64(0), now-2, int64(1)))
	mock.ExpectQuery(`SELECT operation, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"operation", "attempts", "next_attempt_at", "last_error"}))
	mock.ExpectQuery(`SELECT phase, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"phase", "attempts", "next_attempt_at", "last_error"}))

	status, err := service.GetDNSFailoverStatus(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetDNSFailoverStatus: %v", err)
	}
	if len(status.Probes) != 1 || !status.Probes[0].Online || len(status.States) != 2 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if !status.Probes[0].DecisionAvailable {
		t.Fatalf("fresh heartbeat and current-target state should be decision-available: %#v", status.Probes[0])
	}
	if status.Decision.Action != "none" || status.Decision.Reason != "failure_threshold_pending" {
		t.Fatalf("unexpected decision: %#v", status.Decision)
	}
	if status.States[0].FailureStreak != 2 || status.States[0].LastError != "connection refused" {
		t.Fatalf("missing failure evidence: %#v", status.States[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestGetDNSFailoverStatusSeparatesHeartbeatOnlineFromDecisionAvailability(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM v2_dns_failover_group WHERE id = \$1 ORDER BY id ASC`).WithArgs(int64(50)).WillReturnRows(dnsFailoverStatusGroupRows(true))
	expectDNSFailoverRuleRelations(mock)
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT last_evaluated_at, active_incident_type, active_incident_since, active_dns_incident_type, active_dns_incident_since`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"last_evaluated_at", "active_incident_type", "active_incident_since", "active_dns_incident_type", "active_dns_incident_since"}).AddRow(now-3, "", nil, "", nil))
	mock.ExpectQuery(`SELECT p.id, p.name, p.enabled, p.last_heartbeat_at, p.prewarm_count`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "enabled", "last_heartbeat_at", "prewarm_count"}).
			AddRow(int64(4), "广州探针", int64(1), now-5, int64(3)).
			AddRow(int64(9), "上海探针", int64(1), now-5, int64(3)))
	mock.ExpectQuery(`SELECT s.probe_id, s.target_id, s.last_success, s.last_latency_ms, s.last_error`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"probe_id", "target_id", "last_success", "last_latency_ms", "last_error", "last_resolved_ip", "consecutive_success", "consecutive_failure", "last_reported_at", "warmed_up"}).
			AddRow(int64(4), int64(71), int64(0), nil, "connection refused", "2001:db8::1", int64(0), int64(5), now-2, int64(1)).
			AddRow(int64(4), int64(72), int64(1), int64(35), "", "203.0.113.9", int64(8), int64(0), now-2, int64(1)).
			AddRow(int64(9), int64(71), int64(0), nil, "connection refused", "2001:db8::1", int64(0), int64(5), now-67, int64(1)).
			AddRow(int64(9), int64(72), int64(1), int64(35), "", "203.0.113.9", int64(8), int64(0), now-67, int64(1)))
	mock.ExpectQuery(`SELECT operation, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"operation", "attempts", "next_attempt_at", "last_error"}))
	mock.ExpectQuery(`SELECT phase, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"phase", "attempts", "next_attempt_at", "last_error"}))

	status, err := service.GetDNSFailoverStatus(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetDNSFailoverStatus: %v", err)
	}
	if status.Decision.Action != "failover" || status.Decision.TargetID != 72 {
		t.Fatalf("stale probe blocked fresh single-probe decision: %#v", status.Decision)
	}
	if len(status.Probes) != 2 || !status.Probes[0].Online || !status.Probes[1].Online || !status.Probes[0].DecisionAvailable || status.Probes[1].DecisionAvailable {
		t.Fatalf("heartbeat and decision availability were not separated: %#v", status.Probes)
	}
	staleCount := 0
	for _, state := range status.States {
		if state.Stale {
			staleCount++
		}
	}
	if staleCount != 2 {
		t.Fatalf("stale states = %d, want 2: %#v", staleCount, status.States)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestGetDNSFailoverStatusMarksStaleResultsAndExcludesThemFromDecision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM v2_dns_failover_group WHERE id = \$1 ORDER BY id ASC`).WithArgs(int64(50)).WillReturnRows(dnsFailoverStatusGroupRows(true))
	expectDNSFailoverRuleRelations(mock)
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT last_evaluated_at, active_incident_type, active_incident_since, active_dns_incident_type, active_dns_incident_since`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"last_evaluated_at", "active_incident_type", "active_incident_since", "active_dns_incident_type", "active_dns_incident_since"}).AddRow(now-3, "", nil, "", nil))
	mock.ExpectQuery(`SELECT p.id, p.name, p.enabled, p.last_heartbeat_at, p.prewarm_count`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(4), "广州探针", int64(1), now-5, int64(3)))
	mock.ExpectQuery(`SELECT s.probe_id, s.target_id, s.last_success, s.last_latency_ms, s.last_error`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"probe_id", "target_id", "last_success", "last_latency_ms", "last_error", "last_resolved_ip", "consecutive_success", "consecutive_failure", "last_reported_at", "warmed_up"}).
			AddRow(int64(4), int64(71), int64(0), nil, "connection refused", "2001:db8::1", int64(0), int64(5), now-67, int64(1)).
			AddRow(int64(4), int64(72), int64(1), int64(35), "", "203.0.113.9", int64(8), int64(0), now-67, int64(1)))
	mock.ExpectQuery(`SELECT operation, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"operation", "attempts", "next_attempt_at", "last_error"}))
	mock.ExpectQuery(`SELECT phase, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"phase", "attempts", "next_attempt_at", "last_error"}))

	status, err := service.GetDNSFailoverStatus(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetDNSFailoverStatus: %v", err)
	}
	if status.Decision.Action != "none" || status.Decision.Reason != "no_probe_data" {
		t.Fatalf("stale state affected decision: %#v", status.Decision)
	}
	encoded, err := json.Marshal(status.States)
	if err != nil {
		t.Fatalf("marshal states: %v", err)
	}
	if strings.Count(string(encoded), `"stale":true`) != 2 {
		t.Fatalf("stale state is not explicit in API payload: %s", encoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestGetDNSFailoverStatusDoesNotAdvertiseSwitchForDisabledRule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	now := time.Now().Unix()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM v2_dns_failover_group WHERE id = \$1 ORDER BY id ASC`).WithArgs(int64(50)).WillReturnRows(dnsFailoverStatusGroupRows(false))
	expectDNSFailoverRuleRelations(mock)
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT last_evaluated_at, active_incident_type, active_incident_since, active_dns_incident_type, active_dns_incident_since`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"last_evaluated_at", "active_incident_type", "active_incident_since", "active_dns_incident_type", "active_dns_incident_since"}).AddRow(now-3, "", nil, "", nil))
	mock.ExpectQuery(`SELECT p.id, p.name, p.enabled, p.last_heartbeat_at, p.prewarm_count`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(4), "广州探针", int64(1), now-5, int64(3)))
	mock.ExpectQuery(`SELECT s.probe_id, s.target_id, s.last_success, s.last_latency_ms, s.last_error`).WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"probe_id", "target_id", "last_success", "last_latency_ms", "last_error", "last_resolved_ip", "consecutive_success", "consecutive_failure", "last_reported_at", "warmed_up"}).
			AddRow(int64(4), int64(71), int64(0), nil, "connection refused", "2001:db8::1", int64(0), int64(5), now-2, int64(1)).
			AddRow(int64(4), int64(72), int64(1), int64(35), "", "203.0.113.9", int64(8), int64(0), now-2, int64(1)))
	mock.ExpectQuery(`SELECT operation, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"operation", "attempts", "next_attempt_at", "last_error"}))
	mock.ExpectQuery(`SELECT phase, attempts, next_attempt_at, last_error`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"phase", "attempts", "next_attempt_at", "last_error"}))

	status, err := service.GetDNSFailoverStatus(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetDNSFailoverStatus: %v", err)
	}
	if status.Decision.Action != "none" || status.Decision.Reason != "rule_disabled" || status.Decision.TargetID != 0 {
		t.Fatalf("disabled rule advertised a switch: %#v", status.Decision)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func dnsFailoverStatusGroupRows(enabled bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "domain_id", "domain", "record_id", "subdomain", "record_line_id", "record_line_name", "ttl", "mx", "weight", "current_target_id",
		"enabled", "auto_failback", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "single_probe_failure_threshold",
		"single_probe_success_threshold", "probe_offline_sec", "cooldown_sec", "last_switch_at", "last_switch_reason", "created_at", "updated_at",
	}).AddRow(
		int64(50), "主站故障转移", int64(101), "example.com", int64(202), "www", "0=0", "默认", int64(600), int64(0), int64(10), int64(71),
		boolToInt64(enabled), int64(1), int64(30), int64(3000), int64(3), int64(6), int64(5), int64(8), int64(90), int64(300), nil, "", int64(100), int64(200),
	)
}
