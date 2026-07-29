package postgres

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureClientEntryMonitorSchemaCreatesTablesConstraintsAndIndexesIdempotently(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for range 2 {
		expectClientEntryMonitorSchema(mock)
	}
	for range 2 {
		if err := EnsureClientEntryMonitorSchema(context.Background(), db); err != nil {
			t.Fatalf("EnsureClientEntryMonitorSchema: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnsureClientEntryMonitorSchemaRejectsNilDB(t *testing.T) {
	t.Parallel()
	if err := EnsureClientEntryMonitorSchema(context.Background(), nil); err == nil || err.Error() != "db is nil" {
		t.Fatalf("nil db error = %v, want db is nil", err)
	}
}

func TestClientEntryMonitorSchemaKeepsMonitoringAndManualRunsIndependent(t *testing.T) {
	t.Parallel()

	joinedTables := strings.Join(clientEntryMonitorTableStatements, "\n")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_config (",
		"revision BIGINT NOT NULL DEFAULT 1",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor (",
		"policy_id INTEGER NOT NULL",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_target (",
		"source_key varchar(255) NOT NULL",
		"name varchar(255) NOT NULL DEFAULT ''",
		"generation BIGINT NOT NULL DEFAULT 1",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_probe (",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_state (",
		"last_success SMALLINT DEFAULT NULL",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_event (",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_event_delivery (",
		"event_id BIGINT NOT NULL",
		"chat_id BIGINT NOT NULL",
		"delivered_at BIGINT DEFAULT NULL",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_run (",
		"request_key varchar(255) DEFAULT NULL",
		"policy_ids jsonb NOT NULL DEFAULT '[]'::jsonb",
		"expected_pairs jsonb NOT NULL DEFAULT '[]'::jsonb",
		"notify_next_attempt_at BIGINT NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_run_result (",
		"target_name varchar(255) NOT NULL DEFAULT ''",
		"host varchar(255) NOT NULL DEFAULT ''",
		"port INTEGER NOT NULL DEFAULT 443",
		"probe_name varchar(255) NOT NULL DEFAULT ''",
		"CREATE TABLE IF NOT EXISTS v2_client_entry_monitor_result_inbox (",
		"result_id varchar(128) NOT NULL",
	} {
		if !strings.Contains(joinedTables, required) {
			t.Errorf("monitor schema missing %q", required)
		}
	}
	joinedMigrations := strings.Join(clientEntryMonitorMigrationStatements, "\n")
	for _, required := range []string{
		"ALTER TABLE v2_client_entry_monitor_event ADD COLUMN IF NOT EXISTS notify_next_attempt_at",
		"ALTER TABLE v2_client_entry_monitor_run ADD COLUMN IF NOT EXISTS request_key",
		"ALTER TABLE v2_client_entry_monitor_run ADD COLUMN IF NOT EXISTS expected_pairs",
		"ALTER TABLE v2_client_entry_monitor_target ADD COLUMN IF NOT EXISTS generation",
		"ALTER TABLE v2_client_entry_monitor_run_result ADD COLUMN IF NOT EXISTS target_name",
		"ALTER TABLE v2_client_entry_monitor_run_result ADD COLUMN IF NOT EXISTS host",
		"ALTER TABLE v2_client_entry_monitor_run_result ADD COLUMN IF NOT EXISTS port",
		"ALTER TABLE v2_client_entry_monitor_run_result ADD COLUMN IF NOT EXISTS probe_name",
	} {
		if !strings.Contains(joinedMigrations, required) {
			t.Errorf("monitor migration missing %q", required)
		}
	}
	if !strings.Contains(clientEntryMonitorConfigSeedStatement, "ON CONFLICT (id) DO NOTHING") {
		t.Fatal("config singleton seed must be repeatable")
	}
	if strings.Contains(joinedTables, "v2_dns_failover_group") || strings.Contains(joinedTables, "dns_value") {
		t.Fatal("client entry monitoring tables must not depend on DNS failover mutation state")
	}
}

func TestClientEntryMonitorConstraintsDefineOwnershipAndDeduplication(t *testing.T) {
	t.Parallel()

	constraints := make(map[string]string, len(clientEntryMonitorConstraints))
	for _, constraint := range clientEntryMonitorConstraints {
		if _, exists := constraints[constraint.name]; exists {
			t.Fatalf("duplicate constraint name %q", constraint.name)
		}
		constraints[constraint.name] = constraint.definition
	}
	for name, want := range map[string]string{
		"chk_v2_client_entry_monitor_config_singleton":    "CHECK (id = 1)",
		"fk_v2_client_entry_monitor_policy":               "FOREIGN KEY (policy_id) REFERENCES v2_client_entry_user_policy(id) ON DELETE CASCADE",
		"uniq_v2_client_entry_monitor_policy":             "UNIQUE (policy_id)",
		"uniq_v2_client_entry_monitor_target_source":      "UNIQUE (monitor_id, source_key)",
		"uniq_v2_client_entry_monitor_probe":              "UNIQUE (monitor_id, probe_id)",
		"uniq_v2_client_entry_monitor_state":              "UNIQUE (target_id, probe_id)",
		"fk_v2_client_entry_monitor_event_target":         "FOREIGN KEY (target_id) REFERENCES v2_client_entry_monitor_target(id) ON DELETE SET NULL",
		"fk_v2_client_entry_monitor_event_delivery_event": "FOREIGN KEY (event_id) REFERENCES v2_client_entry_monitor_event(id) ON DELETE CASCADE",
		"uniq_v2_client_entry_monitor_event_delivery":     "UNIQUE (event_id, chat_id)",
		"fk_v2_client_entry_monitor_run_user":             "FOREIGN KEY (requested_by_user_id) REFERENCES v2_user(id) ON DELETE SET NULL",
		"uniq_v2_client_entry_monitor_run_request_key":    "UNIQUE (request_key)",
		"uniq_v2_client_entry_monitor_run_result":         "UNIQUE (run_id, target_id, probe_id)",
		"uniq_v2_client_entry_monitor_inbox_result":       "UNIQUE (probe_id, result_id)",
		"fk_v2_client_entry_monitor_inbox_run":            "FOREIGN KEY (run_id) REFERENCES v2_client_entry_monitor_run(id) ON DELETE SET NULL",
		"fk_v2_client_entry_monitor_run_result_run":       "FOREIGN KEY (run_id) REFERENCES v2_client_entry_monitor_run(id) ON DELETE CASCADE",
		"fk_v2_client_entry_monitor_inbox_probe":          "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE CASCADE",
	} {
		got, exists := constraints[name]
		if !exists || got != want {
			t.Errorf("constraint %s = %q, want %q", name, got, want)
		}
	}
	for _, removed := range []string{
		"fk_v2_client_entry_monitor_event_monitor",
		"fk_v2_client_entry_monitor_run_result_target",
		"fk_v2_client_entry_monitor_run_result_probe",
	} {
		if _, exists := constraints[removed]; exists {
			t.Errorf("historical monitor data must not keep cascading constraint %s", removed)
		}
	}
	for _, constraint := range clientEntryMonitorConstraints {
		if strings.HasPrefix(constraint.name, "fk_") && !strings.Contains(constraint.definition, "ON DELETE ") {
			t.Errorf("foreign key %s has no delete behavior: %s", constraint.name, constraint.definition)
		}
	}
}

func expectClientEntryMonitorSchema(mock sqlmock.Sqlmock) {
	for _, stmt := range clientEntryMonitorTableStatements {
		mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, stmt := range clientEntryMonitorMigrationStatements {
		mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, constraint := range clientEntryMonitorConstraints {
		alter := "ALTER TABLE " + constraint.table + " ADD CONSTRAINT " + constraint.name + " " + constraint.definition
		mock.ExpectExec(`(?s)DO .*` + regexp.QuoteMeta(alter) + `.*duplicate_object OR duplicate_table`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, stmt := range clientEntryMonitorIndexStatements {
		mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(regexp.QuoteMeta(clientEntryMonitorConfigSeedStatement)).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
