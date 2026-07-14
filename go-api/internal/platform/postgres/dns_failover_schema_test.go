package postgres

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDNSFailoverInboxTargetFKMigrationUsesCurrentSchemaAndValidatesExactFK(t *testing.T) {
	migration := dnsProbeInboxTargetFKMigration
	if strings.Contains(migration, "public.v2_") || strings.Contains(migration, "'public.") {
		t.Fatalf("migration must not hard-code public schema:\n%s", migration)
	}
	for _, required := range []string{
		"schema_name := current_schema();",
		"format('%I.%I', schema_name, 'v2_dns_probe_result_inbox')",
		"format('%I.%I', schema_name, 'v2_dns_failover_target')",
		"inbox_relation := to_regclass(inbox_table);",
		"target_relation := to_regclass(target_table);",
		"a.attrelid = inbox_relation",
		"a.attrelid = target_relation",
		"SELECT c.contype, c.confdeltype, c.confrelid, c.conkey, c.confkey",
		"current_constraint_type IS DISTINCT FROM 'f'",
		"current_delete_action IS DISTINCT FROM 'n'",
		"current_referenced_relation IS DISTINCT FROM target_relation",
		"current_source_columns IS DISTINCT FROM ARRAY[inbox_target_attnum]::smallint[]",
		"current_referenced_columns IS DISTINCT FROM ARRAY[target_id_attnum]::smallint[]",
		"EXECUTE format('ALTER TABLE %s ALTER COLUMN %I DROP NOT NULL'",
		"EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I'",
		"EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I FOREIGN KEY (%I) REFERENCES %s(%I) ON DELETE SET NULL'",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("migration missing %q", required)
		}
	}
}

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
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_probe_target_state.*target_id BIGINT NOT NULL.*last_resolved_ip varchar\(128\) NOT NULL DEFAULT ''`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_probe_result_inbox.*target_id BIGINT DEFAULT NULL.*result_id varchar\(128\) NOT NULL.*CONSTRAINT uniq_v2_dns_probe_result_inbox_result UNIQUE \(probe_id, result_id\)`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_failover_eval_outbox.*group_id BIGINT NOT NULL.*requested_at BIGINT NOT NULL.*attempts INTEGER NOT NULL DEFAULT 0.*next_attempt_at BIGINT NOT NULL.*last_error text NOT NULL DEFAULT ''.*CONSTRAINT uniq_v2_dns_failover_eval_outbox_group UNIQUE \(group_id\)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_event`,
	} {
		mock.ExpectExec(pattern).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`ALTER TABLE v2_dns_probe_target_state ADD COLUMN IF NOT EXISTS last_resolved_ip varchar\(128\) NOT NULL DEFAULT ''`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(dnsProbeInboxTargetFKMigration)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, constraint := range expectedDNSFailoverConstraints {
		alter := "ALTER TABLE " + constraint.table + " ADD CONSTRAINT " + constraint.name + " " + constraint.definition
		mock.ExpectExec(`(?s)DO .*` + regexp.QuoteMeta(alter) + `.*duplicate_object`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	for _, index := range []string{
		`idx_v2_dns_probe_enabled_heartbeat ON v2_dns_probe\(enabled, last_heartbeat_at\)`,
		`idx_v2_dns_failover_group_enabled ON v2_dns_failover_group\(enabled\)`,
		`idx_v2_dns_failover_target_group_sort ON v2_dns_failover_target\(group_id, enabled, sort\)`,
		`idx_v2_dns_failover_group_probe_probe ON v2_dns_failover_group_probe\(probe_id\)`,
		`idx_v2_dns_probe_target_state_target ON v2_dns_probe_target_state\(target_id\)`,
		`idx_v2_dns_probe_result_inbox_target ON v2_dns_probe_result_inbox\(target_id\)`,
		`idx_v2_dns_probe_result_inbox_created ON v2_dns_probe_result_inbox\(created_at\)`,
		`idx_v2_dns_failover_eval_outbox_due ON v2_dns_failover_eval_outbox\(next_attempt_at, requested_at\)`,
		`idx_v2_dns_failover_event_created_id ON v2_dns_failover_event\(created_at DESC, id DESC\)`,
		`idx_v2_dns_failover_event_group_created_id ON v2_dns_failover_event\(group_id, created_at DESC, id DESC\)`,
		`idx_v2_dns_failover_event_type_created_id ON v2_dns_failover_event\(event_type, created_at DESC, id DESC\)`,
		`idx_v2_dns_failover_event_group_type_created_id ON v2_dns_failover_event\(group_id, event_type, created_at DESC, id DESC\)`,
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
	{"v2_dns_probe_result_inbox", "uniq_v2_dns_probe_result_inbox_result", "UNIQUE (probe_id, result_id)"},
	{"v2_dns_probe_result_inbox", "chk_v2_dns_probe_result_inbox_result_id", "CHECK (btrim(result_id) <> '')"},
	{"v2_dns_failover_eval_outbox", "fk_v2_dns_failover_eval_outbox_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_eval_outbox", "uniq_v2_dns_failover_eval_outbox_group", "UNIQUE (group_id)"},
	{"v2_dns_failover_eval_outbox", "chk_v2_dns_failover_eval_outbox_attempts", "CHECK (attempts >= 0)"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE SET NULL"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_target", "FOREIGN KEY (target_id) REFERENCES v2_dns_failover_target(id) ON DELETE SET NULL"},
	{"v2_dns_failover_event", "chk_v2_dns_failover_event_type", "CHECK (btrim(event_type) <> '')"},
}

func TestDNSFailoverGenericConstraintsExcludeAtomicInboxTargetFK(t *testing.T) {
	for _, constraint := range dnsFailoverConstraints {
		if constraint.table == "v2_dns_probe_result_inbox" && constraint.name == "fk_v2_dns_probe_result_inbox_target" {
			t.Fatal("inbox target FK must be managed only by the atomic conditional migration")
		}
	}
}
