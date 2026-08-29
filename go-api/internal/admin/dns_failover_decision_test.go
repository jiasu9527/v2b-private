package admin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureDNSFailoverSchemaRetriesAfterFailureAndStopsAfterSuccess(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_dns_probe`).
		WillReturnError(errors.New("temporary database error"))
	expectAdminDNSFailoverSchema(mock)

	service := &DBService{db: db}
	if err := service.ensureDNSFailoverSchema(context.Background()); err == nil {
		t.Fatal("first ensureDNSFailoverSchema call should fail")
	}
	if err := service.ensureDNSFailoverSchema(context.Background()); err != nil {
		t.Fatalf("second ensureDNSFailoverSchema call should retry: %v", err)
	}
	if err := service.ensureDNSFailoverSchema(context.Background()); err != nil {
		t.Fatalf("third ensureDNSFailoverSchema call should reuse success: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnsureDNSFailoverSchemaSerializesConcurrentInitialization(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectAdminDNSFailoverSchema(mock)

	service := &DBService{db: db}
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- service.ensureDNSFailoverSchema(context.Background())
		}()
	}
	close(start)
	wait.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensureDNSFailoverSchema: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestInitializeDNSFailoverSchemaEagerlyMarksServiceReady(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectAdminDNSFailoverSchema(mock)

	service := &DBService{db: db}
	if err := service.InitializeDNSFailoverSchema(context.Background()); err != nil {
		t.Fatalf("InitializeDNSFailoverSchema: %v", err)
	}
	// Public-request lazy fallback must observe the eager ready flag and issue
	// no second DDL sequence.
	if err := service.ensureDNSFailoverSchema(context.Background()); err != nil {
		t.Fatalf("ready fallback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func expectAdminDNSFailoverSchema(mock sqlmock.Sqlmock) {
	for _, table := range []string{
		"v2_dns_probe",
		"v2_dns_failover_group",
		"v2_dns_failover_target",
		"v2_dns_failover_group_probe",
		"v2_dns_probe_target_state",
		"v2_dns_probe_result_inbox",
		"v2_dns_failover_log",
		"v2_dns_failover_eval_outbox",
		"v2_dns_failover_saga",
		"v2_dns_failover_event",
	} {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + table).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`ALTER TABLE v2_dns_probe_target_state ADD COLUMN IF NOT EXISTS last_resolved_ip`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_dns_probe ADD COLUMN IF NOT EXISTS token_plaintext`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_dns_failover_group ADD COLUMN IF NOT EXISTS last_evaluated_at`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for range 7 {
		mock.ExpectExec(`ALTER TABLE v2_dns_failover_(group|eval_outbox) ADD COLUMN IF NOT EXISTS`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for range 4 {
		mock.ExpectExec(`ALTER TABLE v2_dns_failover_event ADD COLUMN IF NOT EXISTS`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for range 3 {
		mock.ExpectExec(`(?s)(UPDATE v2_dns_failover_group|ALTER TABLE v2_dns_failover_group DROP CONSTRAINT)`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`(?s)DO \$dns_probe_inbox_fk\$.*schema_name := current_schema\(\).*format\('%I.%I', schema_name, 'v2_dns_probe_result_inbox'\).*format\('%I.%I', schema_name, 'v2_dns_failover_target'\).*to_regclass\(inbox_table\).*to_regclass\(target_table\).*a.attrelid = inbox_relation.*a.attrelid = target_relation.*EXECUTE format\('ALTER TABLE %s ALTER COLUMN %I DROP NOT NULL'.*SELECT c.contype, c.confdeltype, c.confrelid, c.conkey, c.confkey.*current_referenced_relation IS DISTINCT FROM target_relation.*current_source_columns IS DISTINCT FROM ARRAY\[inbox_target_attnum\]::smallint\[\].*current_referenced_columns IS DISTINCT FROM ARRAY\[target_id_attnum\]::smallint\[\].*EXECUTE format\('ALTER TABLE %s DROP CONSTRAINT %I'.*EXECUTE format\('ALTER TABLE %s ADD CONSTRAINT %I FOREIGN KEY \(%I\) REFERENCES %s\(%I\) ON DELETE SET NULL'.*\$dns_probe_inbox_fk\$;`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)DO \$dns_failover_target_group_id_unique\$.*c\.conkey IS NOT DISTINCT FROM ARRAY\[group_id_attnum, target_id_attnum\]::smallint\[\].*i\.indisunique.*i\.indisvalid.*i\.indpred IS NULL.*UNIQUE USING INDEX %I.*UNIQUE \(%I, %I\).*\$dns_failover_target_group_id_unique\$;`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for _, constraint := range []string{
		"uniq_v2_dns_probe_token_hash",
		"chk_v2_dns_probe_enabled",
		"chk_v2_dns_probe_prewarm",
		"chk_v2_dns_failover_group_flags",
		"chk_v2_dns_failover_group_timing",
		"chk_v2_dns_failover_group_thresholds",
		"chk_v2_dns_failover_group_dns_values",
		"chk_v2_dns_failover_group_dns_incident",
		"uniq_v2_dns_failover_group_record",
		"fk_v2_dns_failover_target_group",
		"chk_v2_dns_failover_target_type",
		"chk_v2_dns_failover_target_enabled",
		"chk_v2_dns_failover_target_sort",
		"chk_v2_dns_failover_target_port",
		"uniq_v2_dns_failover_target_sort",
		"fk_v2_dns_failover_group_current_target",
		"fk_v2_dns_failover_group_probe_group",
		"fk_v2_dns_failover_group_probe_probe",
		"uniq_v2_dns_failover_group_probe",
		"fk_v2_dns_probe_target_state_probe",
		"fk_v2_dns_probe_target_state_target",
		"uniq_v2_dns_probe_target_state",
		"chk_v2_dns_probe_target_state_flags",
		"chk_v2_dns_probe_target_state_streaks",
		"chk_v2_dns_probe_target_state_latency",
		"fk_v2_dns_probe_result_inbox_probe",
		"uniq_v2_dns_probe_result_inbox_result",
		"chk_v2_dns_probe_result_inbox_result_id",
		"fk_v2_dns_failover_log_group",
		"fk_v2_dns_failover_log_probe",
		"fk_v2_dns_failover_log_target",
		"chk_v2_dns_failover_log_stage",
		"chk_v2_dns_failover_log_level",
		"chk_v2_dns_failover_log_outcome",
		"fk_v2_dns_failover_eval_outbox_group",
		"uniq_v2_dns_failover_eval_outbox_group",
		"chk_v2_dns_failover_eval_outbox_attempts",
		"chk_v2_dns_failover_eval_outbox_operation",
		"chk_v2_dns_failover_eval_outbox_target",
		"chk_v2_dns_failover_saga_phase",
		"chk_v2_dns_failover_saga_operation",
		"chk_v2_dns_failover_saga_targets",
		"chk_v2_dns_failover_saga_attempts",
		"fk_v2_dns_failover_event_group",
		"fk_v2_dns_failover_event_probe",
		"fk_v2_dns_failover_event_target",
		"chk_v2_dns_failover_event_type",
		"chk_v2_dns_failover_event_notify_attempts",
	} {
		if len(constraint) >= 5 && constraint[:5] == "uniq_" {
			mock.ExpectExec(`(?s)DO \$dns_failover_unique\$.*candidate_constraint_name text := '` + constraint + `'`).
				WillReturnResult(sqlmock.NewResult(0, 0))
			continue
		}
		mock.ExpectExec(`(?s)DO .*ADD CONSTRAINT ` + constraint).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, index := range []string{
		"idx_v2_dns_probe_enabled_heartbeat",
		"idx_v2_dns_failover_group_enabled",
		"idx_v2_dns_failover_group_due",
		"idx_v2_dns_failover_target_group_sort",
		"idx_v2_dns_failover_group_probe_probe",
		"idx_v2_dns_probe_target_state_target",
		"idx_v2_dns_probe_result_inbox_target",
		"idx_v2_dns_probe_result_inbox_created",
		"idx_v2_dns_failover_log_created_id",
		"idx_v2_dns_failover_log_group_created_id",
		"idx_v2_dns_failover_log_probe_created_id",
		"idx_v2_dns_failover_log_target_created_id",
		"idx_v2_dns_failover_log_stage_created_id",
		"idx_v2_dns_failover_log_level_created_id",
		"idx_v2_dns_failover_log_outcome_created_id",
		"idx_v2_dns_failover_log_group_stage_outcome_created_id",
		"idx_v2_dns_failover_eval_outbox_due",
		"idx_v2_dns_failover_eval_outbox_operation_due",
		"idx_v2_dns_failover_saga_due",
		"idx_v2_dns_failover_event_notify_due",
		"idx_v2_dns_failover_event_created_id",
		"idx_v2_dns_failover_event_group_created_id",
		"idx_v2_dns_failover_event_type_created_id",
		"idx_v2_dns_failover_event_group_type_created_id",
	} {
		mock.ExpectExec(`CREATE INDEX IF NOT EXISTS ` + index).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, table := range []string{
		"v2_client_entry_monitor_config",
		"v2_client_entry_monitor",
		"v2_client_entry_monitor_target",
		"v2_client_entry_monitor_probe",
		"v2_client_entry_monitor_state",
		"v2_client_entry_monitor_event",
		"v2_client_entry_monitor_event_delivery",
		"v2_client_entry_monitor_run",
		"v2_client_entry_monitor_run_result",
		"v2_client_entry_monitor_result_inbox",
		"v2_client_entry_backup_ip",
		"v2_client_entry_backup_ip_state",
		"v2_client_entry_backup_ip_result_inbox",
		"v2_client_entry_auto_split_operation",
	} {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + table + ` \(`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`(?s)DO \$client_entry_monitor_timeout\$.*UPDATE v2_client_entry_monitor SET tcp_timeout_ms = 5000.*ALTER TABLE v2_client_entry_monitor ALTER COLUMN tcp_timeout_ms SET DEFAULT 5000`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor ADD COLUMN IF NOT EXISTS failure_threshold`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor ADD COLUMN IF NOT EXISTS success_threshold`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_event ADD COLUMN IF NOT EXISTS notify_next_attempt_at`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_event ADD COLUMN IF NOT EXISTS address_key`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_event event.*SET address_key = CASE.*FROM v2_client_entry_monitor_target target.*WHERE event.target_id = target.id.*event.event_type IN \('down', 'recovered'\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)DELETE FROM v2_client_entry_monitor_event duplicate.*USING v2_client_entry_monitor_event keeper.*duplicate.notified_at IS NULL.*keeper.notified_at IS NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_run ADD COLUMN IF NOT EXISTS request_key`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_run ADD COLUMN IF NOT EXISTS expected_pairs`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for _, column := range []string{"progress_message_id", "progress_reported_results", "progress_reported_status", "progress_attempts", "progress_next_attempt_at", "progress_last_error"} {
		mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_run ADD COLUMN IF NOT EXISTS ` + column).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_target ADD COLUMN IF NOT EXISTS generation`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_target ADD COLUMN IF NOT EXISTS auto_split_enabled`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for _, column := range []string{"target_name", "host", "port", "probe_name", "policy_id", "policy_name"} {
		mock.ExpectExec(`ALTER TABLE v2_client_entry_monitor_run_result ADD COLUMN IF NOT EXISTS ` + column).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_monitor_run_result result.*SET policy_id = monitor.policy_id.*policy_name = policy.name.*FROM v2_client_entry_monitor_target target`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for range 76 {
		mock.ExpectExec(`(?s)DO \$client_entry_monitor\$.*ADD CONSTRAINT`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for range 29 {
		mock.ExpectExec(`CREATE (?:UNIQUE )?INDEX IF NOT EXISTS (?:idx|uniq)_v2_client_entry_(?:monitor|backup|auto)_`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`INSERT INTO v2_client_entry_monitor_config`).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestDecideDNSFailover(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	dualProbes := []dnsFailoverProbeSnapshot{{ID: 101, Online: true}, {ID: 102, Online: true}}
	oneProbe := []dnsFailoverProbeSnapshot{{ID: 101, Online: true}, {ID: 102, Online: false}}
	base := dnsFailoverDecisionInput{
		Now:                         now,
		CurrentTargetID:             1,
		AutoFailback:                true,
		Cooldown:                    5 * time.Minute,
		FailureThreshold:            3,
		SuccessThreshold:            6,
		SingleProbeFailureThreshold: 5,
		SingleProbeSuccessThreshold: 8,
		Targets: []dnsFailoverTargetSnapshot{
			{ID: 1, Sort: 10, Enabled: true},
			{ID: 2, Sort: 20, Enabled: true},
		},
	}

	tests := []struct {
		name   string
		mutate func(*dnsFailoverDecisionInput)
		want   dnsFailoverDecision
	}{
		{
			name: "dual probes agree current failed",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.States = states(
					state(101, 1, 0, 3), state(102, 1, 0, 3),
					state(101, 2, 6, 0), state(102, 2, 6, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionFailover, TargetID: 2, Reason: dnsFailoverReasonCurrentFailed},
		},
		{
			name: "dual probes disagree on current failure",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.States = states(
					state(101, 1, 0, 3), state(102, 1, 0, 2),
					state(101, 2, 6, 0), state(102, 2, 6, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonProbeDisagreement},
		},
		{
			name: "dual probes disagree on candidate health",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.States = states(
					state(101, 1, 0, 3), state(102, 1, 0, 3),
					state(101, 2, 6, 0), state(102, 2, 5, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonNoHealthyTarget},
		},
		{
			name: "single probe requires degraded failure threshold",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = oneProbe
				input.States = states(state(101, 1, 0, 4), state(101, 2, 8, 0))
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonFailureThresholdPending},
		},
		{
			name: "single probe switches at degraded thresholds",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = oneProbe
				input.States = states(state(101, 1, 0, 5), state(101, 2, 8, 0))
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionFailover, TargetID: 2, Reason: dnsFailoverReasonCurrentFailed},
		},
		{
			name: "one fresh probe and one stale probe switches only at single probe thresholds",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.States = states(state(101, 1, 0, 5), state(101, 2, 8, 0))
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionFailover, TargetID: 2, Reason: dnsFailoverReasonCurrentFailed},
		},
		{
			name: "one fresh probe and one stale probe does not use multi probe thresholds",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.States = states(state(101, 1, 0, 3), state(101, 2, 6, 0))
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonFailureThresholdPending},
		},
		{
			name: "single probe requires degraded recovery threshold",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = oneProbe
				input.States = states(state(101, 1, 0, 5), state(101, 2, 7, 0))
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonNoHealthyTarget},
		},
		{
			name: "all probes offline",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = []dnsFailoverProbeSnapshot{{ID: 101}, {ID: 102}}
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonAllProbesOffline},
		},
		{
			name: "selects first healthy target by sort",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.Targets = []dnsFailoverTargetSnapshot{
					{ID: 2, Sort: 30, Enabled: true},
					{ID: 1, Sort: 10, Enabled: true},
					{ID: 3, Sort: 20, Enabled: true},
				}
				input.States = states(
					state(101, 1, 0, 3), state(102, 1, 0, 3),
					state(101, 2, 6, 0), state(102, 2, 6, 0),
					state(101, 3, 6, 0), state(102, 3, 6, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionFailover, TargetID: 3, Reason: dnsFailoverReasonCurrentFailed},
		},
		{
			name: "cooldown suppresses switch",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.Probes = dualProbes
				input.LastSwitchAt = now.Add(-4 * time.Minute)
				input.States = states(
					state(101, 1, 0, 3), state(102, 1, 0, 3),
					state(101, 2, 6, 0), state(102, 2, 6, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCooldown},
		},
		{
			name: "automatically fails back to recovered higher priority target",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.CurrentTargetID = 2
				input.Probes = dualProbes
				input.States = states(
					state(101, 1, 6, 0), state(102, 1, 6, 0),
					state(101, 2, 6, 0), state(102, 2, 6, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionFailback, TargetID: 1, Reason: dnsFailoverReasonHigherPriorityRecovered},
		},
		{
			name: "automatic failback disabled",
			mutate: func(input *dnsFailoverDecisionInput) {
				input.CurrentTargetID = 2
				input.AutoFailback = false
				input.Probes = dualProbes
				input.States = states(
					state(101, 1, 6, 0), state(102, 1, 6, 0),
					state(101, 2, 6, 0), state(102, 2, 6, 0),
				)
			},
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCurrentHealthy},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Targets = append([]dnsFailoverTargetSnapshot(nil), base.Targets...)
			test.mutate(&input)

			if got := decideDNSFailover(input); got != test.want {
				t.Fatalf("decideDNSFailover() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func state(probeID, targetID int64, successes, failures int) dnsFailoverProbeTargetSnapshot {
	return dnsFailoverProbeTargetSnapshot{
		ProbeID:       probeID,
		TargetID:      targetID,
		SuccessStreak: successes,
		FailureStreak: failures,
	}
}

func states(items ...dnsFailoverProbeTargetSnapshot) []dnsFailoverProbeTargetSnapshot {
	return items
}
