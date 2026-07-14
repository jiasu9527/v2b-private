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
		"v2_dns_failover_eval_outbox",
		"v2_dns_failover_event",
	} {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + table).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`ALTER TABLE v2_dns_probe_target_state ADD COLUMN IF NOT EXISTS last_resolved_ip`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)DO \$dns_probe_inbox_fk\$.*SELECT a.attnotnull.*IF target_is_not_null THEN.*ALTER TABLE public.v2_dns_probe_result_inbox ALTER COLUMN target_id DROP NOT NULL.*SELECT c.contype, c.confdeltype.*IF current_constraint_type IS NULL THEN.*REFERENCES public.v2_dns_failover_target\(id\) ON DELETE SET NULL.*ELSIF current_constraint_type <> 'f' OR current_delete_action <> 'n' THEN.*\$dns_probe_inbox_fk\$;`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for _, constraint := range []string{
		"chk_v2_dns_probe_enabled",
		"chk_v2_dns_probe_prewarm",
		"chk_v2_dns_failover_group_flags",
		"chk_v2_dns_failover_group_timing",
		"chk_v2_dns_failover_group_thresholds",
		"chk_v2_dns_failover_group_dns_values",
		"fk_v2_dns_failover_target_group",
		"uniq_v2_dns_failover_target_group_id",
		"chk_v2_dns_failover_target_type",
		"chk_v2_dns_failover_target_enabled",
		"chk_v2_dns_failover_target_sort",
		"chk_v2_dns_failover_target_port",
		"fk_v2_dns_failover_group_current_target",
		"fk_v2_dns_failover_group_probe_group",
		"fk_v2_dns_failover_group_probe_probe",
		"fk_v2_dns_probe_target_state_probe",
		"fk_v2_dns_probe_target_state_target",
		"chk_v2_dns_probe_target_state_flags",
		"chk_v2_dns_probe_target_state_streaks",
		"chk_v2_dns_probe_target_state_latency",
		"fk_v2_dns_probe_result_inbox_probe",
		"uniq_v2_dns_probe_result_inbox_result",
		"chk_v2_dns_probe_result_inbox_result_id",
		"fk_v2_dns_failover_eval_outbox_group",
		"uniq_v2_dns_failover_eval_outbox_group",
		"chk_v2_dns_failover_eval_outbox_attempts",
		"fk_v2_dns_failover_event_group",
		"fk_v2_dns_failover_event_probe",
		"fk_v2_dns_failover_event_target",
		"chk_v2_dns_failover_event_type",
	} {
		mock.ExpectExec(`(?s)DO .*ADD CONSTRAINT ` + constraint).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	for _, index := range []string{
		"idx_v2_dns_probe_enabled_heartbeat",
		"idx_v2_dns_failover_group_enabled",
		"idx_v2_dns_failover_target_group_sort",
		"idx_v2_dns_failover_group_probe_probe",
		"idx_v2_dns_probe_target_state_target",
		"idx_v2_dns_probe_result_inbox_target",
		"idx_v2_dns_probe_result_inbox_created",
		"idx_v2_dns_failover_eval_outbox_due",
		"idx_v2_dns_failover_event_created_id",
		"idx_v2_dns_failover_event_group_created_id",
		"idx_v2_dns_failover_event_type_created_id",
		"idx_v2_dns_failover_event_group_type_created_id",
	} {
		mock.ExpectExec(`CREATE INDEX IF NOT EXISTS ` + index).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
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
			want: dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCurrentHealthy},
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
