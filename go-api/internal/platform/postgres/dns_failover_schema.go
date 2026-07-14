package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const dnsProbeInboxTargetFKMigration = `DO $dns_probe_inbox_fk$
DECLARE
schema_name text;
inbox_table text;
target_table text;
inbox_relation oid;
target_relation oid;
inbox_target_attnum smallint;
target_id_attnum smallint;
target_is_not_null boolean;
current_constraint_type "char";
current_delete_action "char";
current_referenced_relation oid;
current_source_columns smallint[];
current_referenced_columns smallint[];
BEGIN
schema_name := current_schema();
IF schema_name IS NULL THEN
RAISE EXCEPTION 'DNS failover schema is not present in search_path';
END IF;

inbox_table := format('%I.%I', schema_name, 'v2_dns_probe_result_inbox');
target_table := format('%I.%I', schema_name, 'v2_dns_failover_target');
inbox_relation := to_regclass(inbox_table);
target_relation := to_regclass(target_table);
IF inbox_relation IS NULL OR target_relation IS NULL THEN
RAISE EXCEPTION 'DNS failover tables are missing from schema %', schema_name;
END IF;

SELECT a.attnum, a.attnotnull
INTO inbox_target_attnum, target_is_not_null
FROM pg_attribute a
WHERE a.attrelid = inbox_relation
AND a.attname = 'target_id'
AND NOT a.attisdropped;

SELECT a.attnum
INTO target_id_attnum
FROM pg_attribute a
WHERE a.attrelid = target_relation
AND a.attname = 'id'
AND NOT a.attisdropped;

IF inbox_target_attnum IS NULL OR target_id_attnum IS NULL THEN
RAISE EXCEPTION 'DNS failover target key columns are missing from schema %', schema_name;
END IF;

IF target_is_not_null THEN
EXECUTE format('ALTER TABLE %s ALTER COLUMN %I DROP NOT NULL', inbox_table, 'target_id');
END IF;

SELECT c.contype, c.confdeltype, c.confrelid, c.conkey, c.confkey
INTO current_constraint_type, current_delete_action, current_referenced_relation,
current_source_columns, current_referenced_columns
FROM pg_constraint c
WHERE c.conrelid = inbox_relation
AND c.conname = 'fk_v2_dns_probe_result_inbox_target';

IF current_constraint_type IS NULL THEN
EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I FOREIGN KEY (%I) REFERENCES %s(%I) ON DELETE SET NULL',
inbox_table, 'fk_v2_dns_probe_result_inbox_target', 'target_id', target_table, 'id');
ELSIF current_constraint_type IS DISTINCT FROM 'f'
OR current_delete_action IS DISTINCT FROM 'n'
OR current_referenced_relation IS DISTINCT FROM target_relation
OR current_source_columns IS DISTINCT FROM ARRAY[inbox_target_attnum]::smallint[]
OR current_referenced_columns IS DISTINCT FROM ARRAY[target_id_attnum]::smallint[] THEN
EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', inbox_table, 'fk_v2_dns_probe_result_inbox_target');
EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I FOREIGN KEY (%I) REFERENCES %s(%I) ON DELETE SET NULL',
inbox_table, 'fk_v2_dns_probe_result_inbox_target', 'target_id', target_table, 'id');
END IF;
END;
$dns_probe_inbox_fk$;`

const dnsFailoverTargetGroupIDUniqueMigration = `DO $dns_failover_target_group_id_unique$
DECLARE
schema_name text;
target_table text;
target_relation oid;
group_id_attnum smallint;
target_id_attnum smallint;
current_constraint_columns smallint[];
existing_index_name name;
unique_constraint_name name := 'uq_v2_dns_failover_target_group_id';
unique_constraint_suffix integer := 0;
BEGIN
schema_name := current_schema();
IF schema_name IS NULL THEN
RAISE EXCEPTION 'DNS failover schema is not present in search_path';
END IF;

target_table := format('%I.%I', schema_name, 'v2_dns_failover_target');
target_relation := to_regclass(target_table);
IF target_relation IS NULL THEN
RAISE EXCEPTION 'DNS failover target table is missing from schema %', schema_name;
END IF;

SELECT a.attnum
INTO group_id_attnum
FROM pg_attribute a
WHERE a.attrelid = target_relation
AND a.attname = 'group_id'
AND NOT a.attisdropped;

SELECT a.attnum
INTO target_id_attnum
FROM pg_attribute a
WHERE a.attrelid = target_relation
AND a.attname = 'id'
AND NOT a.attisdropped;

IF group_id_attnum IS NULL OR target_id_attnum IS NULL THEN
RAISE EXCEPTION 'DNS failover target key columns are missing from schema %', schema_name;
END IF;

SELECT c.conkey
INTO current_constraint_columns
FROM pg_constraint c
WHERE c.conrelid = target_relation
AND c.contype = 'u'
AND c.conkey IS NOT DISTINCT FROM ARRAY[group_id_attnum, target_id_attnum]::smallint[];

IF current_constraint_columns IS NOT NULL THEN
RETURN;
END IF;

SELECT index_class.relname
INTO existing_index_name
FROM pg_index i
JOIN pg_class index_class ON index_class.oid = i.indexrelid
WHERE i.indrelid = target_relation
AND i.indisunique
AND i.indisvalid
AND i.indpred IS NULL
AND i.indkey::smallint[] IS NOT DISTINCT FROM ARRAY[group_id_attnum, target_id_attnum]::smallint[]
LIMIT 1;

WHILE EXISTS (
SELECT 1
FROM pg_constraint c
WHERE c.conrelid = target_relation
AND c.conname = unique_constraint_name
)
OR to_regclass(format('%I.%I', schema_name, unique_constraint_name)) IS NOT NULL LOOP
unique_constraint_suffix := unique_constraint_suffix + 1;
unique_constraint_name := format('uq_v2_dns_failover_target_group_id_%s', unique_constraint_suffix);
END LOOP;

IF existing_index_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I UNIQUE USING INDEX %I', target_table, unique_constraint_name, existing_index_name);
ELSE
EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I UNIQUE (%I, %I)', target_table, unique_constraint_name, 'group_id', 'id');
END IF;
END;
$dns_failover_target_group_id_unique$;`

// dnsFailoverUniqueConstraintMigration reconciles a legacy UNIQUE index with
// the constraint name used by the current schema. PostgreSQL places indexes
// and constraints in related namespaces, so reusing an index named like the
// desired constraint must first choose a free constraint/index name.
func dnsFailoverUniqueConstraintMigration(table, constraintName string, columns []string) string {
	columnNames := make([]string, len(columns))
	for i, column := range columns {
		columnNames[i] = "'" + strings.ReplaceAll(column, "'", "''") + "'"
	}
	return strings.NewReplacer(
		"{{TABLE}}", table,
		"{{CONSTRAINT}}", constraintName,
		"{{COLUMNS}}", strings.Join(columnNames, ", "),
		"{{COLUMNS_SQL}}", strings.Join(columns, ", "),
	).Replace(`DO $dns_failover_unique$
DECLARE
schema_name text;
table_name text;
table_relation oid;
expected_column_names name[] := ARRAY[{{COLUMNS}}]::name[];
expected_columns smallint[];
expected_columns_sql text := '{{COLUMNS_SQL}}';
existing_index_name name;
candidate_constraint_name text := '{{CONSTRAINT}}';
candidate_suffix integer := 0;
BEGIN
schema_name := current_schema();
IF schema_name IS NULL THEN
RAISE EXCEPTION 'DNS failover schema is not present in search_path';
END IF;

table_name := format('%I.%I', schema_name, '{{TABLE}}');
table_relation := to_regclass(table_name);
IF table_relation IS NULL THEN
RAISE EXCEPTION 'DNS failover table {{TABLE}} is missing from schema %', schema_name;
END IF;

SELECT array_agg(a.attnum::smallint ORDER BY requested.ordinality)
INTO expected_columns
FROM unnest(expected_column_names) WITH ORDINALITY AS requested(attname, ordinality)
JOIN pg_attribute a ON a.attrelid = table_relation
AND a.attname = requested.attname
AND a.attnum > 0
AND NOT a.attisdropped;
IF cardinality(expected_columns) IS DISTINCT FROM cardinality(expected_column_names) THEN
RAISE EXCEPTION 'DNS failover unique columns are missing from table {{TABLE}} in schema %', schema_name;
END IF;

IF EXISTS (
SELECT 1 FROM pg_constraint c
WHERE c.conrelid = table_relation
AND c.contype = 'u'
AND c.conkey IS NOT DISTINCT FROM expected_columns
) THEN
RETURN;
END IF;

SELECT index_class.relname
INTO existing_index_name
FROM pg_index i
JOIN pg_class index_class ON index_class.oid = i.indexrelid
WHERE i.indrelid = table_relation
AND i.indisunique
AND i.indisvalid
AND i.indpred IS NULL
AND i.indkey::smallint[] IS NOT DISTINCT FROM expected_columns
LIMIT 1;

WHILE to_regclass(format('%I.%I', schema_name, candidate_constraint_name)) IS NOT NULL
OR EXISTS (
SELECT 1 FROM pg_constraint c
WHERE c.conrelid = table_relation
AND c.conname = candidate_constraint_name
) LOOP
candidate_suffix := candidate_suffix + 1;
candidate_constraint_name := format('{{CONSTRAINT}}_%s', candidate_suffix);
END LOOP;

IF existing_index_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I UNIQUE USING INDEX %I', table_name, candidate_constraint_name, existing_index_name);
ELSE
EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I UNIQUE (%s)', table_name, candidate_constraint_name, expected_columns_sql);
END IF;
END;
$dns_failover_unique$;`)
}

// EnsureDNSFailoverSchema creates the persistent state used by DNS failover.
// Every statement is safe to execute more than once for eager startup and the
// defensive lazy fallback without coordinating across processes.
func EnsureDNSFailoverSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS v2_dns_probe (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
name varchar(255) NOT NULL,
token_hash char(64) NOT NULL,
token_plaintext text NOT NULL DEFAULT '',
enabled SMALLINT NOT NULL DEFAULT 1,
version varchar(64) NOT NULL DEFAULT '',
arch varchar(32) NOT NULL DEFAULT '',
public_ip varchar(128) NOT NULL DEFAULT '',
last_heartbeat_at BIGINT DEFAULT NULL,
prewarm_count INTEGER NOT NULL DEFAULT 0,
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT uniq_v2_dns_probe_token_hash UNIQUE (token_hash)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_group (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
name varchar(255) NOT NULL DEFAULT '',
domain_id BIGINT NOT NULL,
domain varchar(255) NOT NULL,
record_id BIGINT NOT NULL,
subdomain varchar(255) NOT NULL,
record_line_id varchar(255) NOT NULL DEFAULT '',
record_line_name varchar(255) NOT NULL DEFAULT '',
ttl INTEGER NOT NULL DEFAULT 600,
mx INTEGER NOT NULL DEFAULT 0,
weight INTEGER DEFAULT NULL,
current_target_id BIGINT DEFAULT NULL,
enabled SMALLINT NOT NULL DEFAULT 1,
auto_failback SMALLINT NOT NULL DEFAULT 1,
check_interval_sec INTEGER NOT NULL DEFAULT 30,
tcp_timeout_ms INTEGER NOT NULL DEFAULT 3000,
failure_threshold INTEGER NOT NULL DEFAULT 3,
success_threshold INTEGER NOT NULL DEFAULT 6,
single_probe_failure_threshold INTEGER NOT NULL DEFAULT 5,
single_probe_success_threshold INTEGER NOT NULL DEFAULT 8,
probe_offline_sec INTEGER NOT NULL DEFAULT 90,
cooldown_sec INTEGER NOT NULL DEFAULT 300,
last_switch_at BIGINT DEFAULT NULL,
last_switch_reason text NOT NULL DEFAULT '',
last_evaluated_at BIGINT DEFAULT NULL,
active_incident_type varchar(32) NOT NULL DEFAULT '',
active_incident_since BIGINT DEFAULT NULL,
active_dns_incident_type varchar(32) NOT NULL DEFAULT '',
active_dns_incident_since BIGINT DEFAULT NULL,
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT uniq_v2_dns_failover_group_record UNIQUE (domain_id, record_id)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_target (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
group_id BIGINT NOT NULL,
sort INTEGER NOT NULL DEFAULT 0,
name varchar(255) NOT NULL DEFAULT '',
dns_type varchar(8) NOT NULL,
dns_value varchar(1024) NOT NULL,
check_host varchar(255) NOT NULL,
check_port INTEGER NOT NULL,
enabled SMALLINT NOT NULL DEFAULT 1,
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT chk_v2_dns_failover_target_type CHECK (dns_type IN ('A', 'AAAA', 'CNAME')),
CONSTRAINT uniq_v2_dns_failover_target_sort UNIQUE (group_id, sort)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_group_probe (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
group_id BIGINT NOT NULL,
probe_id BIGINT NOT NULL,
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT uniq_v2_dns_failover_group_probe UNIQUE (group_id, probe_id)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_probe_target_state (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
probe_id BIGINT NOT NULL,
target_id BIGINT NOT NULL,
last_success SMALLINT DEFAULT NULL,
last_latency_ms INTEGER DEFAULT NULL,
last_error text NOT NULL DEFAULT '',
last_resolved_ip varchar(128) NOT NULL DEFAULT '',
consecutive_success INTEGER NOT NULL DEFAULT 0,
consecutive_failure INTEGER NOT NULL DEFAULT 0,
last_reported_at BIGINT DEFAULT NULL,
warmed_up SMALLINT NOT NULL DEFAULT 0,
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT uniq_v2_dns_probe_target_state UNIQUE (probe_id, target_id)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_probe_result_inbox (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
probe_id BIGINT NOT NULL,
target_id BIGINT DEFAULT NULL,
result_id varchar(128) NOT NULL,
created_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT uniq_v2_dns_probe_result_inbox_result UNIQUE (probe_id, result_id)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_eval_outbox (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
group_id BIGINT NOT NULL,
operation varchar(16) NOT NULL DEFAULT 'evaluate',
target_id BIGINT DEFAULT NULL,
source_target_id BIGINT DEFAULT NULL,
requested_at BIGINT NOT NULL,
attempts INTEGER NOT NULL DEFAULT 0,
next_attempt_at BIGINT NOT NULL,
last_error text NOT NULL DEFAULT '',
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (id),
CONSTRAINT uniq_v2_dns_failover_eval_outbox_group UNIQUE (group_id)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_saga (
group_id BIGINT NOT NULL,
phase varchar(16) NOT NULL DEFAULT 'prepared',
original_operation varchar(16) NOT NULL,
original_target_id BIGINT DEFAULT NULL,
original_requested_at BIGINT NOT NULL,
reason text NOT NULL DEFAULT '',
desired_target_id BIGINT NOT NULL,
rollback_target_id BIGINT NOT NULL,
desired_mutation text NOT NULL,
rollback_mutation text NOT NULL,
attempts INTEGER NOT NULL DEFAULT 0,
next_attempt_at BIGINT NOT NULL,
last_error text NOT NULL DEFAULT '',
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (group_id)
)`,
		`CREATE TABLE IF NOT EXISTS v2_dns_failover_event (
id BIGINT GENERATED BY DEFAULT AS IDENTITY NOT NULL,
group_id BIGINT NOT NULL,
probe_id BIGINT DEFAULT NULL,
target_id BIGINT DEFAULT NULL,
event_type varchar(32) NOT NULL,
message text NOT NULL DEFAULT '',
details text NOT NULL DEFAULT '{}',
dedupe_key varchar(255) NOT NULL DEFAULT '',
notified_at BIGINT DEFAULT NULL,
notify_claim_token varchar(64) NOT NULL DEFAULT '',
notify_claimed_at BIGINT DEFAULT NULL,
notify_attempts INTEGER NOT NULL DEFAULT 0,
notify_next_attempt_at BIGINT NOT NULL DEFAULT 0,
created_at BIGINT NOT NULL,
PRIMARY KEY (id)
)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure DNS failover table: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE v2_dns_probe_target_state ADD COLUMN IF NOT EXISTS last_resolved_ip varchar(128) NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("ensure DNS failover state columns: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE v2_dns_probe ADD COLUMN IF NOT EXISTS token_plaintext text NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("ensure DNS probe secret column: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS last_evaluated_at BIGINT DEFAULT NULL`); err != nil {
		return fmt.Errorf("ensure DNS failover scheduling columns: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS active_incident_type varchar(32) NOT NULL DEFAULT ''`,
		`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS active_incident_since BIGINT DEFAULT NULL`,
		`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS active_dns_incident_type varchar(32) NOT NULL DEFAULT ''`,
		`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS active_dns_incident_since BIGINT DEFAULT NULL`,
		`ALTER TABLE v2_dns_failover_eval_outbox ADD COLUMN IF NOT EXISTS operation varchar(16) NOT NULL DEFAULT 'evaluate'`,
		`ALTER TABLE v2_dns_failover_eval_outbox ADD COLUMN IF NOT EXISTS target_id BIGINT DEFAULT NULL`,
		`ALTER TABLE v2_dns_failover_eval_outbox ADD COLUMN IF NOT EXISTS source_target_id BIGINT DEFAULT NULL`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure DNS failover worker state columns: %w", err)
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE v2_dns_failover_event ADD COLUMN IF NOT EXISTS notify_claim_token varchar(64) NOT NULL DEFAULT ''`,
		`ALTER TABLE v2_dns_failover_event ADD COLUMN IF NOT EXISTS notify_claimed_at BIGINT DEFAULT NULL`,
		`ALTER TABLE v2_dns_failover_event ADD COLUMN IF NOT EXISTS notify_attempts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE v2_dns_failover_event ADD COLUMN IF NOT EXISTS notify_next_attempt_at BIGINT NOT NULL DEFAULT 0`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure DNS failover notification claim columns: %w", err)
		}
	}
	for _, stmt := range []string{
		`UPDATE v2_dns_failover_group
SET active_dns_incident_type = active_incident_type,
    active_dns_incident_since = active_incident_since
WHERE active_incident_type IN ('dnspod_error', 'dns_state_diverged')
  AND active_dns_incident_type = ''`,
		`UPDATE v2_dns_failover_group
SET active_incident_type = '', active_incident_since = NULL
WHERE active_incident_type IN ('dnspod_error', 'dns_state_diverged')`,
		`ALTER TABLE v2_dns_failover_group
DROP CONSTRAINT IF EXISTS chk_v2_dns_failover_group_incident,
ADD CONSTRAINT chk_v2_dns_failover_group_incident
CHECK (active_incident_type IN ('', 'all_probes_offline', 'probe_disagreement', 'no_healthy_target', 'config_error'))`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate DNS failover incident state: %w", err)
		}
	}
	// Keep the nullable-column change and FK inspection/replacement in one
	// PostgreSQL statement so an error cannot leave the inbox without its FK.
	// It follows current_schema so it touches the same unqualified relations as
	// the ordinary DDL above. Cross-schema shadowing and all catalog branches
	// remain real-PostgreSQL checks for Task 9.
	if _, err := db.ExecContext(ctx, dnsProbeInboxTargetFKMigration); err != nil {
		return fmt.Errorf("atomically ensure DNS probe result tombstone target constraint: %w", err)
	}
	if _, err := db.ExecContext(ctx, dnsFailoverTargetGroupIDUniqueMigration); err != nil {
		return fmt.Errorf("ensure DNS failover target group/id unique constraint: %w", err)
	}

	for _, constraint := range dnsFailoverConstraints {
		if uniqueColumns, ok := dnsFailoverUniqueConstraintColumns[constraint.name]; ok {
			if _, err := db.ExecContext(ctx, dnsFailoverUniqueConstraintMigration(constraint.table, constraint.name, uniqueColumns)); err != nil {
				return fmt.Errorf("ensure DNS failover constraint %s: %w", constraint.name, err)
			}
			continue
		}
		stmt := fmt.Sprintf(`DO $dns_failover$
BEGIN
ALTER TABLE %s ADD CONSTRAINT %s %s;
EXCEPTION
WHEN duplicate_object THEN NULL;
END
$dns_failover$;`, constraint.table, constraint.name, constraint.definition)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure DNS failover constraint %s: %w", constraint.name, err)
		}
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_probe_enabled_heartbeat ON v2_dns_probe(enabled, last_heartbeat_at)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_group_enabled ON v2_dns_failover_group(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_group_due ON v2_dns_failover_group(enabled, last_evaluated_at, check_interval_sec)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_target_group_sort ON v2_dns_failover_target(group_id, enabled, sort)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_group_probe_probe ON v2_dns_failover_group_probe(probe_id)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_probe_target_state_target ON v2_dns_probe_target_state(target_id)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_probe_result_inbox_target ON v2_dns_probe_result_inbox(target_id)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_probe_result_inbox_created ON v2_dns_probe_result_inbox(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_eval_outbox_due ON v2_dns_failover_eval_outbox(next_attempt_at, requested_at)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_eval_outbox_operation_due ON v2_dns_failover_eval_outbox(operation, next_attempt_at, requested_at)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_saga_due ON v2_dns_failover_saga(next_attempt_at, created_at, group_id)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_event_notify_due ON v2_dns_failover_event(notified_at, notify_next_attempt_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_event_created_id ON v2_dns_failover_event(created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_event_group_created_id ON v2_dns_failover_event(group_id, created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_event_type_created_id ON v2_dns_failover_event(event_type, created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_dns_failover_event_group_type_created_id ON v2_dns_failover_event(group_id, event_type, created_at DESC, id DESC)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure DNS failover index: %w", err)
		}
	}

	return nil
}

var dnsFailoverConstraints = []struct {
	table      string
	name       string
	definition string
}{
	{"v2_dns_probe", "uniq_v2_dns_probe_token_hash", "UNIQUE (token_hash)"},
	{"v2_dns_probe", "chk_v2_dns_probe_enabled", "CHECK (enabled IN (0, 1))"},
	{"v2_dns_probe", "chk_v2_dns_probe_prewarm", "CHECK (prewarm_count >= 0)"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_flags", "CHECK (enabled IN (0, 1) AND auto_failback IN (0, 1))"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_timing", "CHECK (check_interval_sec > 0 AND tcp_timeout_ms > 0 AND probe_offline_sec > 0 AND cooldown_sec >= 0)"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_thresholds", "CHECK (failure_threshold > 0 AND success_threshold > 0 AND single_probe_failure_threshold > failure_threshold AND single_probe_success_threshold > success_threshold)"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_dns_values", "CHECK (ttl >= 0 AND mx >= 0 AND (weight IS NULL OR weight >= 0))"},
	{"v2_dns_failover_group", "chk_v2_dns_failover_group_dns_incident", "CHECK (active_dns_incident_type IN ('', 'dnspod_error', 'dns_state_diverged'))"},
	{"v2_dns_failover_group", "uniq_v2_dns_failover_group_record", "UNIQUE (domain_id, record_id)"},
	{"v2_dns_failover_target", "fk_v2_dns_failover_target_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_type", "CHECK (dns_type IN ('A', 'AAAA', 'CNAME'))"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_enabled", "CHECK (enabled IN (0, 1))"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_sort", "CHECK (sort >= 0)"},
	{"v2_dns_failover_target", "chk_v2_dns_failover_target_port", "CHECK (check_port BETWEEN 1 AND 65535)"},
	{"v2_dns_failover_target", "uniq_v2_dns_failover_target_sort", "UNIQUE (group_id, sort)"},
	{"v2_dns_failover_group", "fk_v2_dns_failover_group_current_target", "FOREIGN KEY (id, current_target_id) REFERENCES v2_dns_failover_target(group_id, id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED"},
	{"v2_dns_failover_group_probe", "fk_v2_dns_failover_group_probe_group", "FOREIGN KEY (group_id) REFERENCES v2_dns_failover_group(id) ON DELETE CASCADE"},
	{"v2_dns_failover_group_probe", "fk_v2_dns_failover_group_probe_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE CASCADE"},
	{"v2_dns_failover_group_probe", "uniq_v2_dns_failover_group_probe", "UNIQUE (group_id, probe_id)"},
	{"v2_dns_probe_target_state", "fk_v2_dns_probe_target_state_probe", "FOREIGN KEY (probe_id) REFERENCES v2_dns_probe(id) ON DELETE CASCADE"},
	{"v2_dns_probe_target_state", "fk_v2_dns_probe_target_state_target", "FOREIGN KEY (target_id) REFERENCES v2_dns_failover_target(id) ON DELETE CASCADE"},
	{"v2_dns_probe_target_state", "uniq_v2_dns_probe_target_state", "UNIQUE (probe_id, target_id)"},
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

var dnsFailoverUniqueConstraintColumns = map[string][]string{
	"uniq_v2_dns_probe_token_hash":           {"token_hash"},
	"uniq_v2_dns_failover_group_record":      {"domain_id", "record_id"},
	"uniq_v2_dns_failover_target_sort":       {"group_id", "sort"},
	"uniq_v2_dns_failover_group_probe":       {"group_id", "probe_id"},
	"uniq_v2_dns_probe_target_state":         {"probe_id", "target_id"},
	"uniq_v2_dns_probe_result_inbox_result":  {"probe_id", "result_id"},
	"uniq_v2_dns_failover_eval_outbox_group": {"group_id"},
}
