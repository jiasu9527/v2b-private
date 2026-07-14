package admin

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/dnspod"

	"github.com/DATA-DOG/go-sqlmock"
)

type dnsFailoverTraceCollector struct {
	entries []dnsFailoverLogEntry
	err     error
}

func (collector *dnsFailoverTraceCollector) write(_ context.Context, entries []dnsFailoverLogEntry) error {
	collector.entries = append(collector.entries, entries...)
	return collector.err
}

func (collector *dnsFailoverTraceCollector) find(stage, outcome string) (dnsFailoverLogEntry, bool) {
	for _, entry := range collector.entries {
		if entry.Stage == stage && entry.Outcome == outcome {
			return entry, true
		}
	}
	return dnsFailoverLogEntry{}, false
}

func normalizedTraceDetails(t *testing.T, entry dnsFailoverLogEntry) map[string]any {
	t.Helper()
	raw, err := json.Marshal(entry.Details)
	if err != nil {
		t.Fatalf("marshal trace details: %v", err)
	}
	var details map[string]any
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatalf("decode trace details: %v", err)
	}
	return details
}

type dnsFailoverTraceEntryExpectation struct {
	Stage           string
	Outcome         string
	TargetID        *int64
	DetailEquals    map[string]any
	DetailMustHave  []string
	DetailMustAvoid []string
}

type dnsFailoverTraceBatchArgument []dnsFailoverTraceEntryExpectation

func (expected dnsFailoverTraceBatchArgument) Match(value driver.Value) bool {
	encoded, ok := value.(string)
	if !ok {
		return false
	}
	var entries []struct {
		TargetID *int64         `json:"target_id"`
		Stage    string         `json:"stage"`
		Outcome  string         `json:"outcome"`
		Details  map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(encoded), &entries); err != nil || len(entries) != len(expected) {
		return false
	}
	for index, want := range expected {
		got := entries[index]
		if got.Stage != want.Stage || got.Outcome != want.Outcome || !equalOptionalInt64(got.TargetID, want.TargetID) {
			return false
		}
		for key, value := range want.DetailEquals {
			if !jsonScalarEqual(got.Details[key], value) {
				return false
			}
		}
		rawDetails, err := json.Marshal(got.Details)
		if err != nil {
			return false
		}
		for _, fragment := range want.DetailMustHave {
			if !strings.Contains(string(rawDetails), fragment) {
				return false
			}
		}
		for _, fragment := range want.DetailMustAvoid {
			if strings.Contains(string(rawDetails), fragment) {
				return false
			}
		}
	}
	return true
}

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func jsonScalarEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func TestDNSFailoverTraceSuccessfulSwitchRecordsProviderResultAndFinalAck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()

	now := int64(1_721_306_600)
	targetID := int64(20)
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "provider-request-42"}}}
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	service.dnsFailoverAPI = dnsAPI
	rule := DNSFailoverRuleRecord{ID: 8, Name: "官网", Domain: "example.com", DomainID: 101, RecordID: 202, Subdomain: "www", RecordLineID: "10=0", RecordLineName: "默认", TTL: 600}
	oldTarget := DNSFailoverTargetRecord{ID: 10, Name: "主站", DNSType: "A", DNSValue: "192.0.2.1"}
	newTarget := DNSFailoverTargetRecord{ID: targetID, Name: "备用", DNSType: "CNAME", DNSValue: "backup.example.net."}
	rule.Targets = []DNSFailoverTargetRecord{oldTarget, newTarget}
	snapshot := dnsFailoverWorkerSnapshot{Rule: rule, Targets: rule.Targets, ProbeIDs: []int64{4}}
	saga := dnsFailoverSagaRecord{
		GroupID: 8, Phase: "prepared", OriginalOperation: "evaluate", OriginalRequestedAt: 701,
		Reason: "current_target_failed", DesiredTargetID: targetID, RollbackTargetID: 10,
		DesiredMutation:  freezeDNSFailoverMutation(buildDNSFailoverMutation(rule, newTarget)),
		RollbackMutation: freezeDNSFailoverMutation(buildDNSFailoverMutation(rule, oldTarget)),
	}
	outbox := dnsFailoverOutboxRow{ID: 42, GroupID: 8, Operation: "evaluate", RequestedAt: 701}

	mock.ExpectExec(`INSERT INTO v2_dns_failover_log`).
		WithArgs(dnsFailoverTraceBatchArgument{{
			Stage: "dns_provider", Outcome: "request_started", TargetID: &targetID,
			DetailEquals:   map[string]any{"operation": "desired", "record_id": int64(202), "record_type": "CNAME"},
			DetailMustHave: []string{`"domain":"example.com"`, `"value":"backup.example.net."`},
		}}).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_dns_failover_log`).
		WithArgs(dnsFailoverTraceBatchArgument{{
			Stage: "dns_provider", Outcome: "success", TargetID: &targetID,
			DetailEquals: map[string]any{"operation": "desired", "request_id": "provider-request-42"},
		}}).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectBegin()
	expectDNSFailoverNoActiveDNSIncident(mock, 8)
	mock.ExpectExec(`UPDATE v2_dns_failover_group`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsert(mock, 8, targetID, "failover")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox`).WithArgs(int64(42), int64(701)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_saga`).WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`INSERT INTO v2_dns_failover_log`).
		WithArgs(dnsFailoverTraceBatchArgument{
			{Stage: "switch", Outcome: "succeeded", TargetID: &targetID, DetailEquals: map[string]any{"from_target_id": int64(10), "to_target_id": targetID, "reason": "current_target_failed", "request_id": "provider-request-42"}},
			{Stage: "saga", Outcome: "finalized", TargetID: &targetID, DetailEquals: map[string]any{"rollback_target_id": int64(10), "desired_target_id": targetID}},
			{Stage: "outbox", Outcome: "acked", TargetID: &targetID, DetailEquals: map[string]any{"outbox_id": int64(42), "requested_at": int64(701), "operation": "evaluate"}},
		}).WillReturnResult(sqlmock.NewResult(3, 3))

	if err := service.executePreparedDNSFailoverSaga(context.Background(), conn, saga, outbox, snapshot, oldTarget, newTarget, "failover", now); err != nil {
		t.Fatalf("executePreparedDNSFailoverSaga: %v", err)
	}
	if len(dnsAPI.mutations) != 1 {
		t.Fatalf("provider mutations = %d, want 1", len(dnsAPI.mutations))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("trace expectations: %v", err)
	}
}

func TestDNSFailoverTraceWriteFailureDoesNotBreakSuccessfulSwitch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()

	now := int64(1_721_306_600)
	dnsAPI := &dnsFailoverWorkerDNSPod{results: []dnspod.RecordMutationResult{{RecordID: 202, RequestID: "provider-request-42"}}}
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	service.dnsFailoverAPI = dnsAPI
	service.dnsFailoverLogf = func(string, ...any) {}
	rule := DNSFailoverRuleRecord{ID: 8, Domain: "example.com", DomainID: 101, RecordID: 202, Subdomain: "www", RecordLineID: "10=0", RecordLineName: "默认", TTL: 600}
	oldTarget := DNSFailoverTargetRecord{ID: 10, DNSType: "A", DNSValue: "192.0.2.1"}
	newTarget := DNSFailoverTargetRecord{ID: 20, DNSType: "CNAME", DNSValue: "backup.example.net."}
	saga := dnsFailoverSagaRecord{GroupID: 8, Phase: "prepared", OriginalOperation: "evaluate", OriginalRequestedAt: 701, Reason: "current_target_failed", DesiredTargetID: 20, RollbackTargetID: 10, DesiredMutation: freezeDNSFailoverMutation(buildDNSFailoverMutation(rule, newTarget)), RollbackMutation: freezeDNSFailoverMutation(buildDNSFailoverMutation(rule, oldTarget))}
	outbox := dnsFailoverOutboxRow{ID: 42, GroupID: 8, Operation: "evaluate", RequestedAt: 701}

	for range 3 {
		mock.ExpectExec(`INSERT INTO v2_dns_failover_log`).WithArgs(sqlmock.AnyArg()).WillReturnError(errors.New("diagnostic log storage unavailable"))
	}
	mock.ExpectBegin()
	expectDNSFailoverNoActiveDNSIncident(mock, 8)
	mock.ExpectExec(`UPDATE v2_dns_failover_group`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEventInsert(mock, 8, 20, "failover")
	mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox`).WithArgs(int64(42), int64(701)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_saga`).WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = service.executePreparedDNSFailoverSaga(context.Background(), conn, saga, outbox, dnsFailoverWorkerSnapshot{Rule: rule}, oldTarget, newTarget, "failover", now)
	if err != nil {
		t.Fatalf("log failure broke a successful switch: %v", err)
	}
	if len(dnsAPI.mutations) != 1 {
		t.Fatalf("provider mutations = %d, want 1", len(dnsAPI.mutations))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverTraceEvaluationRecordsSnapshotDecisionThresholdAndNoData(t *testing.T) {
	for _, test := range []struct {
		name              string
		states            [][]driver.Value
		decisionOutcome   string
		decisionReason    string
		freshStateCount   float64
		missingStateCount float64
		availableCount    int
		effectiveFailure  any
		effectiveSuccess  any
	}{
		{
			name: "failure threshold pending",
			states: [][]driver.Value{
				dnsFailoverWorkerStateRowAt(4, 10, 0, 2, "192.0.2.1", time.Now().Unix()),
				dnsFailoverWorkerStateRowAt(4, 20, 8, 0, "198.51.100.20", time.Now().Unix()),
			},
			decisionOutcome: "threshold_pending", decisionReason: dnsFailoverReasonFailureThresholdPending,
			freshStateCount: 2, missingStateCount: 0,
			availableCount: 1, effectiveFailure: float64(5), effectiveSuccess: float64(8),
		},
		{
			name: "no probe data", states: nil,
			decisionOutcome: "no_data", decisionReason: dnsFailoverReasonNoProbeData,
			freshStateCount: 0, missingStateCount: 2,
			availableCount: 0, effectiveFailure: nil, effectiveSuccess: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			collector := &dnsFailoverTraceCollector{}
			service := &DBService{db: db, dnsFailoverSchemaOK: true, dnsFailoverLogWriter: collector.write}
			now := time.Now().Unix()

			expectDNSFailoverOutboxClaim(mock, 41, 8, 700, 0)
			expectDNSFailoverGroupLock(mock, dnsFailoverWorkerRuleRow(8, 10, true, true, nil))
			expectDNSFailoverTargets(mock,
				dnsFailoverWorkerTargetRow(10, 8, 1, "主站", "A", "192.0.2.1", 443, true),
				dnsFailoverWorkerTargetRow(20, 8, 2, "备用", "A", "198.51.100.20", 443, true),
			)
			expectDNSFailoverProbes(mock, dnsFailoverWorkerProbeRow(4, now, 3))
			expectDNSFailoverStates(mock, test.states...)
			mock.ExpectExec(`UPDATE v2_dns_failover_group SET last_evaluated_at = \$2, updated_at = \$2 WHERE id = \$1`).
				WithArgs(int64(8), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`DELETE FROM v2_dns_failover_eval_outbox WHERE id = \$1 AND requested_at = \$2`).
				WithArgs(int64(41), int64(700)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			expectDNSFailoverGroupSessionUnlock(mock, 8)

			if err := service.drainDNSFailoverEvaluationOutbox(context.Background(), 1); err != nil {
				t.Fatalf("drain evaluation: %v", err)
			}
			claimed, ok := collector.find("outbox", "claimed")
			if !ok {
				t.Fatalf("missing outbox claim log: %#v", collector.entries)
			}
			claimedDetails := normalizedTraceDetails(t, claimed)
			if claimedDetails["outbox_id"] != float64(41) || claimedDetails["attempt"] != float64(0) {
				t.Fatalf("claim details = %#v", claimedDetails)
			}
			snapshot, ok := collector.find("evaluation", "snapshot")
			if !ok {
				t.Fatalf("missing evaluation snapshot log: %#v", collector.entries)
			}
			snapshotDetails := normalizedTraceDetails(t, snapshot)
			freshness, _ := snapshotDetails["freshness"].(map[string]any)
			thresholds, _ := snapshotDetails["thresholds"].(map[string]any)
			if snapshotDetails["current_target_id"] != float64(10) || freshness["fresh_state_count"] != test.freshStateCount || freshness["missing_state_count"] != test.missingStateCount {
				t.Fatalf("snapshot freshness = %#v", snapshotDetails)
			}
			if thresholds["failure"] != float64(3) || thresholds["success"] != float64(6) || thresholds["single_probe_failure"] != float64(5) || thresholds["single_probe_success"] != float64(8) {
				t.Fatalf("threshold details = %#v", thresholds)
			}
			decision, ok := collector.find("evaluation", test.decisionOutcome)
			if !ok {
				t.Fatalf("missing decision outcome %q: %#v", test.decisionOutcome, collector.entries)
			}
			decisionDetails := normalizedTraceDetails(t, decision)
			if decisionDetails["action"] != "none" || decisionDetails["reason"] != test.decisionReason || decisionDetails["current_target_id"] != float64(10) {
				t.Fatalf("decision details = %#v", decisionDetails)
			}
			available, ok := decisionDetails["decision_available_probe_ids"].([]any)
			if !ok || len(available) != test.availableCount || (len(available) == 1 && available[0] != float64(4)) {
				t.Fatalf("decision-available probes = %#v", decisionDetails["decision_available_probe_ids"])
			}
			if decisionDetails["effective_failure_threshold"] != test.effectiveFailure || decisionDetails["effective_success_threshold"] != test.effectiveSuccess {
				t.Fatalf("effective thresholds = %#v", decisionDetails)
			}
			if candidates, ok := decisionDetails["candidate_target_ids"].([]any); !ok || len(candidates) != 1 || candidates[0] != float64(20) {
				t.Fatalf("decision candidate targets = %#v", decisionDetails["candidate_target_ids"])
			}
			if _, ok := collector.find("outbox", "acked"); !ok {
				t.Fatalf("missing no-switch outbox ack: %#v", collector.entries)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestDNSFailoverOutboxTraceRedactsStoredProviderCredentials(t *testing.T) {
	entry := dnsFailoverOutboxTrace(dnsFailoverOutboxRow{
		ID: 9, GroupID: 8, Operation: "evaluate", RequestedAt: 700,
		LastError: "provider rejected Authorization: Bearer secret-value SecretKey=hidden-value",
	}, "claimed", "info", "claimed", 701, nil)
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	if strings.Contains(string(raw), "secret-value") || strings.Contains(string(raw), "hidden-value") || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("stored provider credentials were not redacted: %s", raw)
	}
}
