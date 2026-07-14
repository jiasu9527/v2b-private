package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureDNSFailoverSchemaCreatesTablesAndIndexesIdempotently(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for range 2 {
		expectDNSFailoverSchema(mock)
	}

	for range 2 {
		if err := EnsureDNSFailoverSchema(context.Background(), db); err != nil {
			t.Fatalf("EnsureDNSFailoverSchema: %v", err)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func expectDNSFailoverSchema(mock sqlmock.Sqlmock) {
	for _, pattern := range []string{
		`CREATE TABLE IF NOT EXISTS v2_dns_probe`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_group`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_target`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_group_probe`,
		`CREATE TABLE IF NOT EXISTS v2_dns_probe_target_state`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_probe_result_inbox.*result_id varchar\(128\) NOT NULL.*CONSTRAINT uniq_v2_dns_probe_result_inbox_result UNIQUE \(probe_id, result_id\)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_event`,
	} {
		mock.ExpectExec(pattern).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	for _, constraint := range expectedDNSFailoverConstraints {
		alter := "ALTER TABLE " + constraint.table + " ADD CONSTRAINT " + constraint.name + " " + constraint.definition
		mock.ExpectExec(`(?s)DO .*` + regexp.QuoteMeta(alter) + `.*duplicate_object`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	for _, index := range []string{
		"idx_v2_dns_probe_enabled_heartbeat",
		"idx_v2_dns_failover_group_enabled",
		"idx_v2_dns_failover_target_group_sort",
		"idx_v2_dns_failover_group_probe_probe",
		"idx_v2_dns_probe_target_state_target",
		"idx_v2_dns_probe_result_inbox_target",
		"idx_v2_dns_failover_event_group_created",
	} {
		mock.ExpectExec(`CREATE INDEX IF NOT EXISTS ` + index).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

var expectedDNSFailoverConstraints = []struct {
	table      string
	name       string
	definition string
}{
	{"v2_dns_probe", "chk_v2_dns_probe_enabled", "CHECK (enabled IN (0, 1))"},
	{"v2_dns_probe", "chk_v2_dns_probe_prewarm", "CHECK (prewarm_count >= 0)"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_flags", "CHECK (enabled IN (0, 1) AND auto_failback IN (0, 1))"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_timing", "CHECK (check_interval_sec > 0 AND tcp_timeout_ms > 0 AND probe_offline_sec > 0 AND cooldown_sec >= 0)"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_thresholds", "CHECK (failure_threshold > 0 AND success_threshold > 0 AND single_probe_failure_threshold > failure_threshold AND single_probe_success_threshold > success_threshold)"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_dns_values", "CHECK (ttl >= 0 AND mx >= 0 AND (weight IS NULL OR weight >= 0))"},
	{"v2_dns_failover_target", "fk_v2_dns_failover_target_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_target", "uniq_v2_dns_failover_target_group_id", "UNIQUE (group_id, id)"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_type", "CHECK (dns_type IN ('A', 'AAAA', 'CNAME'))"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_enabled", "CHECK (enabled IN (0, 1))"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_sort", "CHECK (sort >= 0)"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_port", "CHECK (check_port BETWEEN 1 AND 65535)"},
	{"v2_dns_failover_group", "fk_v2_dns_failover_group_current_target", "FOREIGN KEY (id, current_target_id) REFERENCES v2_dns_failover_target(group_id, id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED"},
	{"v2_dns_failover_group_probe", "fk_v2_dns_failover_group_probe_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_group_probe", "fk_v2_dns_failover_group_probe_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE CASCADE"},
	{"v2_dns_probe_target_state", "fk_v2_dns_probe_target_state_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE CASCADE"},
	{"v2_dns_probe_target_state", "fk_v2_dns_probe_target_state_target", "FOREIGN KEY (target_id) REFERENCES v2_dns_failover_target(id) ON DELETE CASCADE"},
	{"v2_dns_probe_target_state", "chk_v2_dns_probe_target_state_flags", "CHECK ((last_success IS NULL OR last_success IN (0, 1)) AND warmed_up IN (0, 1))"},
	{"v2_dns_probe_target_state", "chk_v2_dns_probe_target_state_streaks", "CHECK (consecutive_success >= 0 AND consecutive_failure >= 0)"},
	{"v2_dns_probe_target_state", "chk_v2_dns_probe_target_state_latency", "CHECK (last_latency_ms IS NULL OR last_latency_ms >= 0)"},
	{"v2_dns_probe_result_inbox", "fk_v2_dns_probe_result_inbox_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE CASCADE"},
	{"v2_dns_probe_result_inbox", "fk_v2_dns_probe_result_inbox_target", "FOREIGN KEY (target_id) REFERENCES v2_dns_failover_target(id) ON DELETE CASCADE"},
	{"v2_dns_probe_result_inbox", "uniq_v2_dns_probe_result_inbox_result", "UNIQUE (probe_id, result_id)"},
	{"v2_dns_probe_result_inbox", "chk_v2_dns_probe_result_inbox_result_id", "CHECK (btrim(result_id) <> '')"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE SET NULL"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_target", "FOREIGN KEY (target_id) REFERENCES v2_dns_failover_target(id) ON DELETE SET NULL"},
	{"v2_dns_failover_event", "chk_v2_dns_failover_event_type", "CHECK (btrim(event_type) <> '')"},
}
