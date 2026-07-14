package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDNSProbeAuthenticateUsesEnabledHashSetAndScansEveryCandidate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	secret := "probe-secret"
	digest := sha256.Sum256([]byte(secret))
	otherDigest := sha256.Sum256([]byte("other-secret"))
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`SELECT id, token_hash FROM v2_dns_probe WHERE enabled = 1 ORDER BY id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token_hash"}).
			AddRow(int64(7), hex.EncodeToString(digest[:])).
			AddRow(int64(8), "damaged-hash").
			AddRow(int64(9), hex.EncodeToString(otherDigest[:])))

	identity, err := service.AuthenticateDNSProbe(context.Background(), secret)
	if err != nil {
		t.Fatalf("AuthenticateDNSProbe: %v", err)
	}
	if identity.ID != 7 {
		t.Fatalf("identity = %#v, want probe 7", identity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeAuthenticateRejectsWrongDisabledAndOversizedSecretsUniformly(t *testing.T) {
	for _, test := range []struct {
		name   string
		secret string
		rows   *sqlmock.Rows
	}{
		{name: "wrong", secret: "wrong", rows: sqlmock.NewRows([]string{"id", "token_hash"}).AddRow(int64(7), strings.Repeat("0", 64))},
		{name: "disabled is absent from enabled set", secret: "disabled-secret", rows: sqlmock.NewRows([]string{"id", "token_hash"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			mock.ExpectQuery(`SELECT id, token_hash FROM v2_dns_probe WHERE enabled = 1 ORDER BY id ASC`).WillReturnRows(test.rows)

			if _, err := service.AuthenticateDNSProbe(context.Background(), test.secret); !errors.Is(err, ErrDNSProbeUnauthorized) {
				t.Fatalf("error = %v, want ErrDNSProbeUnauthorized", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}

	service := &DBService{dnsFailoverSchemaOK: true}
	if _, err := service.AuthenticateDNSProbe(context.Background(), strings.Repeat("x", 513)); !errors.Is(err, ErrDNSProbeUnauthorized) {
		t.Fatalf("oversized secret error = %v, want ErrDNSProbeUnauthorized", err)
	}
}

func TestDNSProbeAuthenticateDoesNotReturnEarlyAfterMatchingHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	secret := "probe-secret"
	digest := sha256.Sum256([]byte(secret))
	rowErr := errors.New("candidate cursor failed")
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`SELECT id, token_hash FROM v2_dns_probe WHERE enabled = 1 ORDER BY id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token_hash"}).
			AddRow(int64(7), hex.EncodeToString(digest[:])).
			AddRow(int64(8), strings.Repeat("1", 64)).
			RowError(1, rowErr))

	if _, err := service.AuthenticateDNSProbe(context.Background(), secret); err == nil || !errors.Is(err, rowErr) {
		t.Fatalf("error = %v, want later cursor error proving all candidates were consumed", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeHeartbeatFirstAndContinuousUpdatesDoNotResetPrewarm(t *testing.T) {
	for _, test := range []struct {
		name          string
		lastHeartbeat any
		prewarm       int64
	}{
		{name: "first heartbeat", lastHeartbeat: nil, prewarm: 0},
		{name: "continuous heartbeat", lastHeartbeat: time.Now().Unix(), prewarm: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), test.lastHeartbeat, test.prewarm))
			mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*FROM v2_dns_failover_group_probe gp.*JOIN v2_dns_failover_group g ON g.id = gp.group_id.*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
			mock.ExpectExec(`UPDATE v2_dns_probe SET version = \$2, arch = \$3, public_ip = \$4, last_heartbeat_at = \$5, updated_at = \$5 WHERE id = \$1`).
				WithArgs(int64(7), "v1.2.3", "amd64", "203.0.113.7", sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			result, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{
				Version:  "v1.2.3",
				Arch:     "amd64",
				PublicIP: "203.0.113.7",
			})
			if err != nil {
				t.Fatalf("HeartbeatDNSProbe: %v", err)
			}
			if result.Reconnected || result.PrewarmCount != test.prewarm {
				t.Fatalf("result = %#v, want continuous prewarm %d", result, test.prewarm)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestDNSProbeHeartbeatOfflineReconnectResetsProbeAndState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), time.Now().Add(-2*time.Minute).Unix(), int64(3)))
	mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
	mock.ExpectExec(`UPDATE v2_dns_probe SET version = \$2, arch = \$3, public_ip = \$4, last_heartbeat_at = \$5, prewarm_count = 0, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(7), "v1.2.4", "arm64", "2001:db8::7", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0, updated_at = \$2 WHERE probe_id = \$1`).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectCommit()

	result, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{
		Version:  "v1.2.4",
		Arch:     "arm64",
		PublicIP: "2001:0db8:0:0::7",
	})
	if err != nil {
		t.Fatalf("HeartbeatDNSProbe: %v", err)
	}
	if !result.Reconnected || result.PrewarmCount != 0 {
		t.Fatalf("result = %#v, want reconnect reset", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeHeartbeatRejectsDisabledProbeAndInvalidMetadata(t *testing.T) {
	for _, request := range []DNSProbeHeartbeatRequest{
		{Version: strings.Repeat("v", 65), Arch: "amd64", PublicIP: "203.0.113.7"},
		{Version: "v1\n", Arch: "amd64", PublicIP: "203.0.113.7"},
		{Version: "v1", Arch: strings.Repeat("a", 33), PublicIP: "203.0.113.7"},
		{Version: "v1", Arch: "amd64", PublicIP: "not-an-ip"},
	} {
		service := &DBService{dnsFailoverSchemaOK: true}
		if _, err := service.HeartbeatDNSProbe(context.Background(), 7, request); !errors.Is(err, ErrDNSProbeInvalidRequest) {
			t.Fatalf("request %#v error = %v, want ErrDNSProbeInvalidRequest", request, err)
		}
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(0), sql.NullInt64{}, int64(0)))
	mock.ExpectRollback()
	if _, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{Version: "v1", Arch: "amd64", PublicIP: "203.0.113.7"}); !errors.Is(err, ErrDNSProbeUnauthorized) {
		t.Fatalf("disabled probe error = %v, want ErrDNSProbeUnauthorized", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeTasksReturnOnlyBoundEnabledGroupsAndTargetsInStableOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectQuery(`(?s)SELECT t.id, g.id, t.check_host, t.check_port, g.tcp_timeout_ms, g.check_interval_sec.*FROM v2_dns_failover_group_probe gp.*JOIN v2_dns_failover_group g ON g.id = gp.group_id.*JOIN v2_dns_failover_target t ON t.group_id = g.id.*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1.*ORDER BY g.id ASC, t.sort ASC, t.id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "group_id", "check_host", "check_port", "tcp_timeout_ms", "check_interval_sec"}).
			AddRow(int64(11), int64(3), "a.example.com", int64(443), int64(3000), int64(30)).
			AddRow(int64(10), int64(3), "203.0.113.10", int64(8443), int64(2000), int64(15)).
			AddRow(int64(20), int64(9), "2001:db8::20", int64(22), int64(5000), int64(60)))

	tasks, err := service.ListDNSProbeTasks(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListDNSProbeTasks: %v", err)
	}
	want := []DNSProbeTask{
		{TargetID: 11, GroupID: 3, CheckHost: "a.example.com", CheckPort: 443, TCPTimeoutMS: 3000, CheckIntervalSec: 30},
		{TargetID: 10, GroupID: 3, CheckHost: "203.0.113.10", CheckPort: 8443, TCPTimeoutMS: 2000, CheckIntervalSec: 15},
		{TargetID: 20, GroupID: 9, CheckHost: "2001:db8::20", CheckPort: 22, TCPTimeoutMS: 5000, CheckIntervalSec: 60},
	}
	if len(tasks) != len(want) {
		t.Fatalf("tasks = %#v, want %#v", tasks, want)
	}
	for i := range want {
		if tasks[i] != want[i] {
			t.Fatalf("task[%d] = %#v, want %#v", i, tasks[i], want[i])
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeTasksReturnEmptyArrayInsteadOfNil(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`(?s)SELECT t.id, g.id, t.check_host, t.check_port, g.tcp_timeout_ms, g.check_interval_sec.*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "group_id", "check_host", "check_port", "tcp_timeout_ms", "check_interval_sec"}))

	tasks, err := service.ListDNSProbeTasks(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListDNSProbeTasks: %v", err)
	}
	if tasks == nil || len(tasks) != 0 {
		t.Fatalf("tasks = %#v, want non-nil empty slice", tasks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsRejectMalformedBatchBeforeDatabaseWrites(t *testing.T) {
	trueValue := true
	falseValue := false
	zero := int64(0)
	negative := int64(-1)
	tooLarge := int64(3_600_001)
	valid := DNSProbeResult{ResultID: "result-1", TargetID: 11, Success: &trueValue, LatencyMS: &zero}

	tests := []struct {
		name    string
		request DNSProbeResultsRequest
	}{
		{name: "missing results", request: DNSProbeResultsRequest{}},
		{name: "batch limit", request: DNSProbeResultsRequest{Results: make([]DNSProbeResult, 501)}},
		{name: "missing result id", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{TargetID: 11, Success: &falseValue}}}},
		{name: "oversized result id", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: strings.Repeat("r", 129), TargetID: 11, Success: &falseValue}}}},
		{name: "result id whitespace", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: " result-1", TargetID: 11, Success: &falseValue}}}},
		{name: "invalid target", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", Success: &falseValue}}}},
		{name: "missing success", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11}}}},
		{name: "successful result needs latency", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11, Success: &trueValue}}}},
		{name: "negative latency", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11, Success: &falseValue, LatencyMS: &negative}}}},
		{name: "excessive latency", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11, Success: &trueValue, LatencyMS: &tooLarge}}}},
		{name: "oversized error", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11, Success: &falseValue, Error: strings.Repeat("e", 1025)}}}},
		{name: "invalid resolved ip", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11, Success: &falseValue, ResolvedIP: "example.com"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &DBService{dnsFailoverSchemaOK: true}
			if _, err := service.ReportDNSProbeResults(context.Background(), 7, test.request); !errors.Is(err, ErrDNSProbeInvalidRequest) {
				t.Fatalf("error = %v, want ErrDNSProbeInvalidRequest", err)
			}
		})
	}

	service := &DBService{dnsFailoverSchemaOK: true}
	if _, err := service.ReportDNSProbeResults(context.Background(), 0, DNSProbeResultsRequest{Results: []DNSProbeResult{valid}}); !errors.Is(err, ErrDNSProbeUnauthorized) {
		t.Fatalf("invalid probe error = %v, want ErrDNSProbeUnauthorized", err)
	}
}

type recordingDNSFailoverEvaluationRequester struct {
	calls  [][]int64
	err    error
	onCall func()
}

func (r *recordingDNSFailoverEvaluationRequester) RequestDNSFailoverEvaluation(_ context.Context, groupIDs []int64) error {
	if r.onCall != nil {
		r.onCall()
	}
	r.calls = append(r.calls, append([]int64(nil), groupIDs...))
	return r.err
}

func TestDNSProbeResultsUpdateStreaksWarmAfterThirdRoundAndEvaluateDeduplicatedGroups(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "prewarm_count"}).AddRow(int64(1), int64(2)))
	mock.ExpectQuery(`(?s)SELECT t.id, t.group_id.*FROM v2_dns_failover_group_probe gp.*JOIN v2_dns_failover_group g ON g.id = gp.group_id.*JOIN v2_dns_failover_target t ON t.group_id = g.id.*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1.*ORDER BY t.id ASC.*FOR SHARE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "group_id"}).
			AddRow(int64(11), int64(3)).
			AddRow(int64(12), int64(3)).
			AddRow(int64(20), int64(9)))

	expectAcceptedDNSProbeResult(mock, 7, 11, "success-1", 1, int64(40), "", "2001:db8::11", 1, 0, 0)
	expectAcceptedDNSProbeResult(mock, 7, 12, "failure-1", 0, nil, "timeout", "", 0, 1, 0)
	expectAcceptedDNSProbeResult(mock, 7, 20, "success-2", 1, int64(8), "", "203.0.113.20", 1, 0, 0)
	mock.ExpectExec(`UPDATE v2_dns_probe SET prewarm_count = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(7), int64(3), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 1, updated_at = \$2 WHERE probe_id = \$1 AND warmed_up = 0`).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()
	evaluator.onCall = func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("evaluator ran before commit: %v", err)
		}
	}

	trueValue := true
	falseValue := false
	latency40 := int64(40)
	latency8 := int64(8)
	result, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "success-1", TargetID: 11, Success: &trueValue, LatencyMS: &latency40, ResolvedIP: "2001:0db8::11"},
		{ResultID: "failure-1", TargetID: 12, Success: &falseValue, Error: "timeout"},
		{ResultID: "success-2", TargetID: 20, Success: &trueValue, LatencyMS: &latency8, ResolvedIP: "203.0.113.20"},
		{ResultID: "success-1", TargetID: 11, Success: &trueValue, LatencyMS: &latency40},
	}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 3 || result.Duplicates != 1 || result.PrewarmCount != 3 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.GroupIDs) != 2 || result.GroupIDs[0] != 3 || result.GroupIDs[1] != 9 {
		t.Fatalf("group IDs = %#v, want [3 9]", result.GroupIDs)
	}
	if len(evaluator.calls) != 1 || len(evaluator.calls[0]) != 2 || evaluator.calls[0][0] != 3 || evaluator.calls[0][1] != 9 {
		t.Fatalf("evaluator calls = %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func expectAcceptedDNSProbeResult(mock sqlmock.Sqlmock, probeID, targetID int64, resultID string, success int64, latency any, resultError, resolvedIP string, successStreak, failureStreak, warmedUp int64) {
	mock.ExpectQuery(`INSERT INTO v2_dns_probe_result_inbox \(probe_id, target_id, result_id, created_at\) VALUES \(\$1, \$2, \$3, \$4\) ON CONFLICT \(probe_id, result_id\) DO NOTHING RETURNING id`).
		WithArgs(probeID, targetID, resultID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(100)))
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_probe_target_state.*last_resolved_ip.*VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, \$7, \$8, \$9, \$10, \$9, \$9\).*ON CONFLICT \(probe_id, target_id\) DO UPDATE SET.*consecutive_success = CASE WHEN EXCLUDED.last_success = 1 THEN v2_dns_probe_target_state.consecutive_success \+ 1 ELSE 0 END.*consecutive_failure = CASE WHEN EXCLUDED.last_success = 0 THEN v2_dns_probe_target_state.consecutive_failure \+ 1 ELSE 0 END`).
		WithArgs(probeID, targetID, success, latency, resultError, resolvedIP, successStreak, failureStreak, sqlmock.AnyArg(), warmedUp).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestDNSProbeResultsCrossRequestReplayIsIdempotentAndEvaluatorFailureDoesNotRejectAcceptedResults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{err: errors.New("worker unavailable")}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	expectDNSProbeReportStart(mock, 7, 3, sqlmock.NewRows([]string{"target_id", "group_id"}).AddRow(int64(11), int64(4)))
	expectAcceptedDNSProbeResult(mock, 7, 11, "same-result", 1, int64(25), "", "203.0.113.11", 1, 0, 1)
	mock.ExpectCommit()

	expectDNSProbeReportStart(mock, 7, 3, sqlmock.NewRows([]string{"target_id", "group_id"}).AddRow(int64(11), int64(4)))
	mock.ExpectQuery(`INSERT INTO v2_dns_probe_result_inbox .* ON CONFLICT \(probe_id, result_id\) DO NOTHING RETURNING id`).
		WithArgs(int64(7), int64(11), "same-result", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	trueValue := true
	latency := int64(25)
	request := DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "same-result", TargetID: 11, Success: &trueValue, LatencyMS: &latency, ResolvedIP: "203.0.113.11",
	}}}
	first, err := service.ReportDNSProbeResults(context.Background(), 7, request)
	if err != nil || first.Accepted != 1 || first.Duplicates != 0 || first.PrewarmCount != 3 {
		t.Fatalf("first report = %#v, %v", first, err)
	}
	second, err := service.ReportDNSProbeResults(context.Background(), 7, request)
	if err != nil || second.Accepted != 0 || second.Duplicates != 1 || second.PrewarmCount != 3 || len(second.GroupIDs) != 0 {
		t.Fatalf("replayed report = %#v, %v", second, err)
	}
	if len(evaluator.calls) != 1 || len(evaluator.calls[0]) != 1 || evaluator.calls[0][0] != 4 {
		t.Fatalf("evaluator calls = %#v, want one accepted group despite evaluator error", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsRejectUnboundTargetWithoutInboxWrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	expectDNSProbeReportStart(mock, 7, 1, sqlmock.NewRows([]string{"target_id", "group_id"}).AddRow(int64(11), int64(4)))
	mock.ExpectRollback()

	falseValue := false
	_, err = service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "foreign-target", TargetID: 99, Success: &falseValue, Error: "timeout",
	}}})
	if !errors.Is(err, ErrDNSProbeInvalidRequest) || errors.Is(err, ErrDNSProbeUnauthorized) {
		t.Fatalf("error = %v, want ordinary invalid request", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsRollBackInboxAndSkipEvaluatorWhenStateWriteFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)
	expectDNSProbeReportStart(mock, 7, 3, sqlmock.NewRows([]string{"target_id", "group_id"}).AddRow(int64(11), int64(4)))
	mock.ExpectQuery(`INSERT INTO v2_dns_probe_result_inbox .*RETURNING id`).
		WithArgs(int64(7), int64(11), "rollback-result", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	stateErr := errors.New("state write failed")
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_probe_target_state.*ON CONFLICT`).WillReturnError(stateErr)
	mock.ExpectRollback()

	falseValue := false
	if _, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "rollback-result", TargetID: 11, Success: &falseValue, Error: "timeout",
	}}}); err == nil || !errors.Is(err, stateErr) {
		t.Fatalf("error = %v, want state write failure", err)
	}
	if len(evaluator.calls) != 0 {
		t.Fatalf("evaluator called before successful commit: %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func expectDNSProbeReportStart(mock sqlmock.Sqlmock, probeID, prewarm int64, allowedRows *sqlmock.Rows) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "prewarm_count"}).AddRow(int64(1), prewarm))
	mock.ExpectQuery(`(?s)SELECT t.id, t.group_id.*FROM v2_dns_failover_group_probe gp.*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1.*FOR SHARE`).
		WithArgs(probeID).
		WillReturnRows(allowedRows)
}
