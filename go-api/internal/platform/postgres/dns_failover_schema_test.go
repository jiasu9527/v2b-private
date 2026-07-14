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

func TestDNSFailoverTargetGroupIDUniqueMigrationReusesLegacyNamedUniqueIndex(t *testing.T) {
	migration := dnsFailoverTargetGroupIDUniqueMigration
	for _, required := range []string{
		"schema_name := current_schema();",
		"target_table := format('%I.%I', schema_name, 'v2_dns_failover_target');",
		"target_relation := to_regclass(target_table);",
		"c.conkey IS NOT DISTINCT FROM ARRAY[group_id_attnum, target_id_attnum]::smallint[]",
		"i.indisunique",
		"i.indisvalid",
		"i.indpred IS NULL",
		"i.indkey::smallint[] IS NOT DISTINCT FROM ARRAY[group_id_attnum, target_id_attnum]::smallint[]",
		"ALTER TABLE %s ADD CONSTRAINT %I UNIQUE USING INDEX %I",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	if strings.Contains(migration, "uniq_v2_dns_failover_target_group_id', 'group_id'") {
		t.Fatal("migration must not attempt to create a constraint with the legacy index name")
	}
}

func TestDNSFailoverTargetGroupIDUniqueMigrationCreatesConstraintOnFreshSchema(t *testing.T) {
	migration := dnsFailoverTargetGroupIDUniqueMigration
	for _, required := range []string{
		"IF existing_index_name IS NOT NULL THEN",
		"UNIQUE USING INDEX %I",
		"ELSE",
		"UNIQUE (%I, %I)",
		"group_id", "id",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("fresh-schema migration missing %q", required)
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
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_failover_group.*last_evaluated_at BIGINT DEFAULT NULL.*active_incident_type varchar\(32\) NOT NULL DEFAULT ''.*active_incident_since BIGINT DEFAULT NULL.*active_dns_incident_type varchar\(32\) NOT NULL DEFAULT ''.*active_dns_incident_since BIGINT DEFAULT NULL`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_target`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_group_probe`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_probe_target_state.*target_id BIGINT NOT NULL.*last_resolved_ip varchar\(128\) NOT NULL DEFAULT ''`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_probe_result_inbox.*target_id BIGINT DEFAULT NULL.*result_id varchar\(128\) NOT NULL.*CONSTRAINT uniq_v2_dns_probe_result_inbox_result UNIQUE \(probe_id, result_id\)`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_failover_eval_outbox.*group_id BIGINT NOT NULL.*operation varchar\(16\) NOT NULL DEFAULT 'evaluate'.*target_id BIGINT DEFAULT NULL.*source_target_id BIGINT DEFAULT NULL.*requested_at BIGINT NOT NULL.*attempts INTEGER NOT NULL DEFAULT 0.*next_attempt_at BIGINT NOT NULL.*last_error text NOT NULL DEFAULT ''.*CONSTRAINT uniq_v2_dns_failover_eval_outbox_group UNIQUE \(group_id\)`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_failover_saga.*group_id BIGINT NOT NULL.*phase varchar\(16\) NOT NULL DEFAULT 'prepared'.*original_operation varchar\(16\) NOT NULL.*original_target_id BIGINT DEFAULT NULL.*original_requested_at BIGINT NOT NULL.*reason text NOT NULL.*desired_target_id BIGINT NOT NULL.*rollback_target_id BIGINT NOT NULL.*desired_mutation text NOT NULL.*rollback_mutation text NOT NULL.*attempts INTEGER NOT NULL DEFAULT 0.*next_attempt_at BIGINT NOT NULL.*last_error text NOT NULL DEFAULT ''.*PRIMARY KEY \(group_id\)`,
		`(?s)CREATE TABLE IF NOT EXISTS v2_dns_failover_event.*notify_claim_token varchar\(64\) NOT NULL DEFAULT ''.*notify_claimed_at BIGINT DEFAULT NULL.*notify_attempts INTEGER NOT NULL DEFAULT 0.*notify_next_attempt_at BIGINT NOT NULL DEFAULT 0`,
	} {
		mock.ExpectExec(pattern).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`ALTER TABLE v2_dns_probe_target_state ADD COLUMN IF NOT EXISTS last_resolved_ip varchar\(128\) NOT NULL DEFAULT ''`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS last_evaluated_at BIGINT DEFAULT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for _, column := range []string{
		`active_incident_type varchar\(32\) NOT NULL DEFAULT ''`,
		`active_incident_since BIGINT DEFAULT NULL`,
		`active_dns_incident_type varchar\(32\) NOT NULL DEFAULT ''`,
		`active_dns_incident_since BIGINT DEFAULT NULL`,
	} {
		mock.ExpectExec(`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS ` + column).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, column := range []string{
		`operation varchar\(16\) NOT NULL DEFAULT 'evaluate'`,
		`target_id BIGINT DEFAULT NULL`,
		`source_target_id BIGINT DEFAULT NULL`,
	} {
		mock.ExpectExec(`ALTER TABLE v2_dns_failover_eval_outbox ADD COLUMN IF NOT EXISTS ` + column).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, column := range []string{
		`notify_claim_token varchar\(64\) NOT NULL DEFAULT ''`,
		`notify_claimed_at BIGINT DEFAULT NULL`,
		`notify_attempts INTEGER NOT NULL DEFAULT 0`,
		`notify_next_attempt_at BIGINT NOT NULL DEFAULT 0`,
	} {
		mock.ExpectExec(`ALTER TABLE v2_dns_failover_event ADD COLUMN IF NOT EXISTS ` + column).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group.*active_dns_incident_type = active_incident_type.*active_incident_type IN \('dnspod_error', 'dns_state_diverged'\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)UPDATE v2_dns_failover_group.*active_incident_type = ''.*active_incident_type IN \('dnspod_error', 'dns_state_diverged'\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)ALTER TABLE v2_dns_failover_group DROP CONSTRAINT IF EXISTS chk_v2_dns_failover_group_incident.*ADD CONSTRAINT chk_v2_dns_failover_group_incident CHECK \(active_incident_type IN \('', 'all_probes_offline', 'probe_disagreement', 'no_healthy_target', 'config_error'\)\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(dnsProbeInboxTargetFKMigration)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(dnsFailoverTargetGroupIDUniqueMigration)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, constraint := range dnsFailoverConstraints {
		if columns, isUnique := dnsFailoverUniqueConstraintColumns[constraint.name]; isUnique {
			mock.ExpectExec(regexp.QuoteMeta(dnsFailoverUniqueConstraintMigration(constraint.table, constraint.name, columns))).
				WillReturnResult(sqlmock.NewResult(0, 0))
			continue
		}
		alter := "ALTER TABLE " + constraint.table + " ADD CONSTRAINT " + constraint.name + " " + constraint.definition
		mock.ExpectExec(`(?s)DO .*` + regexp.QuoteMeta(alter) + `.*duplicate_object`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	for _, index := range []string{
		`idx_v2_dns_probe_enabled_heartbeat ON v2_dns_probe\(enabled, last_heartbeat_at\)`,
		`idx_v2_dns_failover_group_enabled ON v2_dns_failover_group\(enabled\)`,
		`idx_v2_dns_failover_group_due ON v2_dns_failover_group\(enabled, last_evaluated_at, check_interval_sec\)`,
		`idx_v2_dns_failover_target_group_sort ON v2_dns_failover_target\(group_id, enabled, sort\)`,
		`idx_v2_dns_failover_group_probe_probe ON v2_dns_failover_group_probe\(probe_id\)`,
		`idx_v2_dns_probe_target_state_target ON v2_dns_probe_target_state\(target_id\)`,
		`idx_v2_dns_probe_result_inbox_target ON v2_dns_probe_result_inbox\(target_id\)`,
		`idx_v2_dns_probe_result_inbox_created ON v2_dns_probe_result_inbox\(created_at\)`,
		`idx_v2_dns_failover_eval_outbox_due ON v2_dns_failover_eval_outbox\(next_attempt_at, requested_at\)`,
		`idx_v2_dns_failover_eval_outbox_operation_due ON v2_dns_failover_eval_outbox\(operation, next_attempt_at, requested_at\)`,
		`idx_v2_dns_failover_saga_due ON v2_dns_failover_saga\(next_attempt_at, created_at, group_id\)`,
		`idx_v2_dns_failover_event_notify_due ON v2_dns_failover_event\(notified_at, notify_next_attempt_at, id\)`,
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
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_dns_incident", "CHECK (active_dns_incident_type IN ('', 'dnspod_error', 'dns_state_diverged'))"},
	{"v2_dns_failover_target", "fk_v2_dns_failover_target_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
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
	{"v2_dns_failover_eval_outbox", "chk_v2_dns_failover_eval_outbox_operation", "CHECK (operation IN ('evaluate', 'manual', 'reconcile'))"},
	{"v2_dns_failover_eval_outbox", "chk_v2_dns_failover_eval_outbox_target", "CHECK ((operation = 'evaluate' AND target_id IS NULL) OR (operation IN ('manual', 'reconcile') AND target_id IS NOT NULL AND target_id > 0))"},
	{"v2_dns_failover_saga", "chk_v2_dns_failover_saga_phase", "CHECK (phase = 'prepared')"},
	{"v2_dns_failover_saga", "chk_v2_dns_failover_saga_operation", "CHECK (original_operation IN ('evaluate', 'manual'))"},
	{"v2_dns_failover_saga", "chk_v2_dns_failover_saga_targets", "CHECK (desired_target_id > 0 AND rollback_target_id > 0 AND ((original_operation = 'evaluate' AND original_target_id IS NULL) OR (original_operation = 'manual' AND original_target_id IS NOT NULL AND original_target_id > 0)))"},
	{"v2_dns_failover_saga", "chk_v2_dns_failover_saga_attempts", "CHECK (attempts >= 0)"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE SET NULL"},
	{"v2_dns_failover_event", "fk_v2_dns_failover_event_target", "FOREIGN KEY (target_id) REFERENCES v2_dns_failover_target(id) ON DELETE SET NULL"},
	{"v2_dns_failover_event", "chk_v2_dns_failover_event_type", "CHECK (btrim(event_type) <> '')"},
	{"v2_dns_failover_event", "chk_v2_dns_failover_event_notify_attempts", "CHECK (notify_attempts >= 0)"},
}

func TestDNSFailoverGenericConstraintsExcludeAtomicInboxTargetFK(t *testing.T) {
	for _, constraint := range dnsFailoverConstraints {
		if constraint.table == "v2_dns_probe_result_inbox" && constraint.name == "fk_v2_dns_probe_result_inbox_target" {
			t.Fatal("inbox target FK must be managed only by the atomic conditional migration")
		}
		if constraint.table == "v2_dns_failover_target" && constraint.name == "uniq_v2_dns_failover_target_group_id" {
			t.Fatal("target group/id unique key must be managed only by the dedicated catalog-aware migration")
		}
	}
}

func TestDNSFailoverUniqueConstraintMigrationReusesLegacyIndexesWithoutNameCollisions(t *testing.T) {
	for _, tc := range []struct {
		table   string
		name    string
		columns []string
	}{
		{"v2_dns_probe_result_inbox", "uniq_v2_dns_probe_result_inbox_result", []string{"probe_id", "result_id"}},
		{"v2_dns_probe", "uniq_v2_dns_probe_token_hash", []string{"token_hash"}},
		{"v2_dns_failover_group_probe", "uniq_v2_dns_failover_group_probe", []string{"group_id", "probe_id"}},
	} {
		migration := dnsFailoverUniqueConstraintMigration(tc.table, tc.name, tc.columns)
		for _, required := range []string{
			"schema_name := current_schema();",
			"to_regclass(table_name)",
			"expected_column_names name[] := ARRAY[",
			"unnest(expected_column_names)",
			"c.contype = 'u'",
			"c.conkey IS NOT DISTINCT FROM expected_columns",
			"i.indisunique",
			"i.indisvalid",
			"i.indpred IS NULL",
			"i.indkey::smallint[] IS NOT DISTINCT FROM expected_columns",
			"to_regclass(format('%I.%I', schema_name, candidate_constraint_name)) IS NOT NULL",
			"UNIQUE USING INDEX %I",
			"UNIQUE (%s)",
		} {
			if !strings.Contains(migration, required) {
				t.Errorf("%s migration missing %q", tc.name, required)
			}
		}
		for _, column := range tc.columns {
			if !strings.Contains(migration, "'"+column+"'") {
				t.Errorf("%s migration missing column %q", tc.name, column)
			}
		}
	}
}

func TestDNSFailoverGenericConstraintsDelegateEveryUniqueToCatalogAwareMigration(t *testing.T) {
	for _, constraint := range dnsFailoverConstraints {
		if strings.HasPrefix(constraint.definition, "UNIQUE (") && len(dnsFailoverUniqueConstraintColumns[constraint.name]) == 0 {
			t.Errorf("unique constraint %s must declare its ordered columns", constraint.name)
		}
	}
}
