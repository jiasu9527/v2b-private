package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDNSProbeAuthenticateUsesUniqueTokenHashIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	secret := "probe-secret"
	digest := sha256.Sum256([]byte(secret))
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`SELECT id, token_hash FROM v2_dns_probe WHERE token_hash = \$1 AND enabled = 1 LIMIT 1`).
		WithArgs(hex.EncodeToString(digest[:])).
		WillReturnRows(sqlmock.NewRows([]string{"id", "token_hash"}).AddRow(int64(7), hex.EncodeToString(digest[:])))

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

func TestDNSProbeAuthenticateRejectsMissingDisabledAndDamagedIndexedRowsUniformly(t *testing.T) {
	for _, test := range []struct {
		name       string
		secret     string
		returnHash any
	}{
		{name: "missing", secret: "wrong"},
		{name: "disabled is absent from indexed enabled query", secret: "disabled-secret"},
		{name: "damaged returned hash", secret: "damaged-secret", returnHash: "damaged-hash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			digest := sha256.Sum256([]byte(test.secret))
			rows := sqlmock.NewRows([]string{"id", "token_hash"})
			if test.returnHash != nil {
				rows.AddRow(int64(7), test.returnHash)
			}
			mock.ExpectQuery(`SELECT id, token_hash FROM v2_dns_probe WHERE token_hash = \$1 AND enabled = 1 LIMIT 1`).
				WithArgs(hex.EncodeToString(digest[:])).
				WillReturnRows(rows)

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

func TestDNSProbeAuthenticateReturnsIndexedQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	secret := "probe-secret"
	digest := sha256.Sum256([]byte(secret))
	queryErr := errors.New("indexed lookup failed")
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`SELECT id, token_hash FROM v2_dns_probe WHERE token_hash = \$1 AND enabled = 1 LIMIT 1`).
		WithArgs(hex.EncodeToString(digest[:])).
		WillReturnError(queryErr)

	if _, err := service.AuthenticateDNSProbe(context.Background(), secret); err == nil || !errors.Is(err, queryErr) {
		t.Fatalf("error = %v, want indexed query error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeHeartbeatFirstResetsSafelyAndContinuousPreservesPrewarm(t *testing.T) {
	for _, test := range []struct {
		name          string
		lastHeartbeat any
		prewarm       int64
		wantReset     bool
	}{
		{name: "first heartbeat", lastHeartbeat: nil, prewarm: 0, wantReset: true},
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
			if test.wantReset {
				mock.ExpectExec(`UPDATE v2_dns_probe SET version = \$2, arch = \$3, public_ip = \$4, last_heartbeat_at = \$5, prewarm_count = 0, updated_at = \$5 WHERE id = \$1`).
					WithArgs(int64(7), "v1.2.3", "amd64", "203.0.113.7", sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0, updated_at = \$2 WHERE probe_id = \$1`).
					WithArgs(int64(7), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec(`UPDATE v2_dns_probe SET version = \$2, arch = \$3, public_ip = \$4, last_heartbeat_at = \$5, updated_at = \$5 WHERE id = \$1`).
					WithArgs(int64(7), "v1.2.3", "amd64", "203.0.113.7", sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
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

func TestDNSProbeHeartbeatResetsCorruptTimestampWithoutCallingItReconnect(t *testing.T) {
	for _, test := range []struct {
		name          string
		lastHeartbeat int64
	}{
		{name: "negative", lastHeartbeat: -1},
		{name: "future", lastHeartbeat: time.Now().Add(time.Hour).Unix()},
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
				WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), test.lastHeartbeat, int64(3)))
			mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
			mock.ExpectExec(`UPDATE v2_dns_probe SET .*prewarm_count = 0.*WHERE id = \$1`).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0`).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			result, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{
				Version: "v1", Arch: "amd64", PublicIP: "203.0.113.7",
			})
			if err != nil {
				t.Fatalf("HeartbeatDNSProbe: %v", err)
			}
			if result.Reconnected || result.PrewarmCount != 0 {
				t.Fatalf("result = %#v, want corrupt timestamp reset without reconnect", result)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
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

func TestDNSProbeOfflineThresholdUsesSafeDefaultAndConfiguredMinimum(t *testing.T) {
	if got := dnsProbeOfflineThreshold(sql.NullInt64{}); got != defaultProbeOfflineSec {
		t.Fatalf("NULL threshold = %d, want default %d", got, defaultProbeOfflineSec)
	}
	if got := dnsProbeOfflineThreshold(sql.NullInt64{Int64: 30, Valid: true}); got != 30 {
		t.Fatalf("configured MIN threshold = %d, want 30", got)
	}
}

func TestDNSProbeHeartbeatFreshUsesOverflowSafeCutoffAndRejectsAbnormalTimes(t *testing.T) {
	const maxInt64 = int64(1<<63 - 1)
	tests := []struct {
		name      string
		last      sql.NullInt64
		now       int64
		offline   int64
		wantFresh bool
	}{
		{name: "fresh", last: sql.NullInt64{Int64: 95, Valid: true}, now: 100, offline: 10, wantFresh: true},
		{name: "cutoff is inclusive", last: sql.NullInt64{Int64: 90, Valid: true}, now: 100, offline: 10, wantFresh: true},
		{name: "stale before cutoff", last: sql.NullInt64{Int64: 89, Valid: true}, now: 100, offline: 10},
		{name: "null", last: sql.NullInt64{}, now: 100, offline: 10},
		{name: "negative heartbeat", last: sql.NullInt64{Int64: -1, Valid: true}, now: 100, offline: 10},
		{name: "future heartbeat", last: sql.NullInt64{Int64: 101, Valid: true}, now: 100, offline: 10},
		{name: "invalid threshold", last: sql.NullInt64{Int64: 100, Valid: true}, now: 100, offline: 0},
		{name: "maximum timestamp does not overflow", last: sql.NullInt64{Int64: maxInt64 - 90, Valid: true}, now: maxInt64, offline: 90, wantFresh: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dnsProbeHeartbeatFresh(test.last, test.now, test.offline); got != test.wantFresh {
				t.Fatalf("dnsProbeHeartbeatFresh(%#v, %d, %d) = %v, want %v", test.last, test.now, test.offline, got, test.wantFresh)
			}
		})
	}
}

func TestDNSProbeHeartbeatOfflineThresholdNullDefaultAndMinimumBinding(t *testing.T) {
	for _, test := range []struct {
		name          string
		lastHeartbeat int64
		minimum       any
		wantReconnect bool
	}{
		{name: "no bindings uses safe default", lastHeartbeat: time.Now().Add(-80 * time.Second).Unix(), minimum: nil, wantReconnect: false},
		{name: "multiple bindings use SQL minimum", lastHeartbeat: time.Now().Add(-40 * time.Second).Unix(), minimum: int64(30), wantReconnect: true},
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
				WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), test.lastHeartbeat, int64(2)))
			mock.ExpectQuery(`(?s)SELECT MIN\(g.probe_offline_sec\).*FROM v2_dns_failover_group_probe gp.*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(test.minimum))
			if test.wantReconnect {
				mock.ExpectExec(`UPDATE v2_dns_probe SET .*prewarm_count = 0.*WHERE id = \$1`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0`).WillReturnResult(sqlmock.NewResult(0, 2))
			} else {
				mock.ExpectExec(`UPDATE v2_dns_probe SET version = \$2, arch = \$3, public_ip = \$4, last_heartbeat_at = \$5, updated_at = \$5 WHERE id = \$1`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()

			result, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{Version: "v1", Arch: "amd64", PublicIP: "203.0.113.7"})
			if err != nil {
				t.Fatalf("HeartbeatDNSProbe: %v", err)
			}
			if result.Reconnected != test.wantReconnect {
				t.Fatalf("result = %#v, want reconnect %v", result, test.wantReconnect)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
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
		name      string
		request   DNSProbeResultsRequest
		wantError string
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
		{name: "invalid resolved ip", request: DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "result-1", TargetID: 11, Success: &falseValue, Error: "timeout", ResolvedIP: "example.com"}}}, wantError: "resolved_ip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &DBService{dnsFailoverSchemaOK: true}
			if _, err := service.ReportDNSProbeResults(context.Background(), 7, test.request); !errors.Is(err, ErrDNSProbeInvalidRequest) {
				t.Fatalf("error = %v, want ErrDNSProbeInvalidRequest", err)
			} else if test.wantError != "" && !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want validation path containing %q", err, test.wantError)
			}
		})
	}

	service := &DBService{dnsFailoverSchemaOK: true}
	if _, err := service.ReportDNSProbeResults(context.Background(), 0, DNSProbeResultsRequest{Results: []DNSProbeResult{valid}}); !errors.Is(err, ErrDNSProbeUnauthorized) {
		t.Fatalf("invalid probe error = %v, want ErrDNSProbeUnauthorized", err)
	}
}

func TestDNSProbeResultsEnforceSuccessFailureFieldMatrix(t *testing.T) {
	trueValue := true
	falseValue := false
	latency := int64(12)
	tests := []struct {
		name   string
		result DNSProbeResult
	}{
		{name: "success cannot include error", result: DNSProbeResult{ResultID: "r1", TargetID: 11, Success: &trueValue, LatencyMS: &latency, Error: "unexpected"}},
		{name: "failure cannot include latency", result: DNSProbeResult{ResultID: "r1", TargetID: 11, Success: &falseValue, LatencyMS: &latency, Error: "timeout"}},
		{name: "failure requires error", result: DNSProbeResult{ResultID: "r1", TargetID: 11, Success: &falseValue}},
		{name: "failure rejects whitespace error", result: DNSProbeResult{ResultID: "r1", TargetID: 11, Success: &falseValue, Error: "  "}},
		{name: "failure rejects control characters", result: DNSProbeResult{ResultID: "r1", TargetID: 11, Success: &falseValue, Error: "timeout\nretry"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := DNSProbeResultsRequest{Results: []DNSProbeResult{test.result}}
			if err := normalizeDNSProbeResultsRequest(&request); !errors.Is(err, ErrDNSProbeInvalidRequest) {
				t.Fatalf("error = %v, want ErrDNSProbeInvalidRequest", err)
			}
		})
	}
}

func TestDNSProbeResultsRejectDuplicateTargetInBatch(t *testing.T) {
	trueValue := true
	falseValue := false
	latency := int64(10)
	service := &DBService{dnsFailoverSchemaOK: true}
	_, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "success", TargetID: 11, Success: &trueValue, LatencyMS: &latency},
		{ResultID: "failure", TargetID: 11, Success: &falseValue, Error: "timeout"},
	}})
	if !errors.Is(err, ErrDNSProbeInvalidRequest) || !strings.Contains(err.Error(), "target_id") {
		t.Fatalf("error = %v, want duplicate target_id validation", err)
	}
}

func TestDNSProbeResultsNormalizeAllowedSuccessFailureFields(t *testing.T) {
	trueValue := true
	falseValue := false
	latency := int64(12)
	request := DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "success", TargetID: 11, Success: &trueValue, LatencyMS: &latency, Error: "  ", ResolvedIP: "2001:0db8::11"},
		{ResultID: "failure", TargetID: 12, Success: &falseValue, Error: "  timeout  ", ResolvedIP: "203.0.113.12"},
	}}
	if err := normalizeDNSProbeResultsRequest(&request); err != nil {
		t.Fatalf("normalizeDNSProbeResultsRequest: %v", err)
	}
	if request.Results[0].Error != "" || request.Results[0].ResolvedIP != "2001:db8::11" {
		t.Fatalf("normalized success = %#v", request.Results[0])
	}
	if request.Results[1].LatencyMS != nil || request.Results[1].Error != "timeout" || request.Results[1].ResolvedIP != "203.0.113.12" {
		t.Fatalf("normalized failure = %#v", request.Results[1])
	}
}

func TestDNSProbeStateWriteValuesDefensivelyClearContradictoryFields(t *testing.T) {
	trueValue := true
	falseValue := false
	latency := int64(12)
	success := dnsProbeStateWriteValues(DNSProbeResult{Success: &trueValue, LatencyMS: &latency, Error: "must-clear"})
	if success.Success != 1 || success.Latency != int64(12) || success.Error != "" || success.SuccessStreak != 1 || success.FailureStreak != 0 {
		t.Fatalf("success values = %#v", success)
	}
	failure := dnsProbeStateWriteValues(DNSProbeResult{Success: &falseValue, LatencyMS: &latency, Error: "timeout"})
	if failure.Success != 0 || failure.Latency != nil || failure.Error != "timeout" || failure.SuccessStreak != 0 || failure.FailureStreak != 1 {
		t.Fatalf("failure values = %#v", failure)
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

func expectDNSProbeReportLock(mock sqlmock.Sqlmock, probeID, prewarm int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), time.Now().Unix(), prewarm))
	mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
}

func expectDNSProbeExistingResultIDs(mock sqlmock.Sqlmock, probeID int64, resultIDs ...string) {
	rows := sqlmock.NewRows([]string{"result_id"})
	for _, resultID := range resultIDs {
		rows.AddRow(resultID)
	}
	mock.ExpectQuery(`(?s)SELECT i.result_id.*FROM v2_dns_probe_result_inbox i.*jsonb_to_recordset\(\$2::jsonb\).*WHERE i.probe_id = \$1`).
		WithArgs(probeID, sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func expectDNSProbeAllowedTargets(mock sqlmock.Sqlmock, probeID int64, targetGroups ...[2]int64) {
	rows := sqlmock.NewRows([]string{"target_id", "group_id"})
	for _, targetGroup := range targetGroups {
		rows.AddRow(targetGroup[0], targetGroup[1])
	}
	mock.ExpectQuery(`(?s)SELECT requested.target_id, t.group_id.*jsonb_to_recordset\(\$2::jsonb\).*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1.*FOR SHARE`).
		WithArgs(probeID, sqlmock.AnyArg()).
		WillReturnRows(rows)
}

type dnsProbeAcceptedResult struct {
	resultID string
	targetID int64
}

type dnsProbeResultsBatchArgument []DNSProbeResult

func (expected dnsProbeResultsBatchArgument) Match(value driver.Value) bool {
	encoded, ok := value.(string)
	if !ok {
		return false
	}
	var actual []DNSProbeResult
	if err := json.Unmarshal([]byte(encoded), &actual); err != nil {
		return false
	}
	return reflect.DeepEqual(actual, []DNSProbeResult(expected))
}

type dnsProbeStateBatchArgument []dnsProbeStateBatchRow

func (expected dnsProbeStateBatchArgument) Match(value driver.Value) bool {
	encoded, ok := value.(string)
	if !ok {
		return false
	}
	var actual []dnsProbeStateBatchRow
	if err := json.Unmarshal([]byte(encoded), &actual); err != nil {
		return false
	}
	return reflect.DeepEqual(actual, []dnsProbeStateBatchRow(expected))
}

func expectDNSProbeBatchInbox(mock sqlmock.Sqlmock, probeID int64, accepted ...dnsProbeAcceptedResult) {
	rows := sqlmock.NewRows([]string{"result_id", "target_id"})
	for _, result := range accepted {
		rows.AddRow(result.resultID, result.targetID)
	}
	mock.ExpectQuery(`(?s)INSERT INTO v2_dns_probe_result_inbox.*jsonb_to_recordset\(\$2::jsonb\).*ON CONFLICT \(probe_id, result_id\) DO NOTHING.*RETURNING result_id, target_id`).
		WithArgs(probeID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func expectDNSProbeBatchState(mock sqlmock.Sqlmock, probeID, warmedUp, affected int64, batchArguments ...sqlmock.Argument) {
	var batchArgument sqlmock.Argument = sqlmock.AnyArg()
	if len(batchArguments) > 0 {
		batchArgument = batchArguments[0]
	}
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_probe_target_state.*jsonb_to_recordset\(\$2::jsonb\).*ON CONFLICT \(probe_id, target_id\) DO UPDATE SET.*CASE WHEN v2_dns_probe_target_state.consecutive_success >= 2147483647 THEN 2147483647 ELSE v2_dns_probe_target_state.consecutive_success \+ 1 END.*CASE WHEN v2_dns_probe_target_state.consecutive_failure >= 2147483647 THEN 2147483647 ELSE v2_dns_probe_target_state.consecutive_failure \+ 1 END`).
		WithArgs(probeID, batchArgument, sqlmock.AnyArg(), warmedUp).
		WillReturnResult(sqlmock.NewResult(0, affected))
}

func expectDNSFailoverEvaluationOutbox(mock sqlmock.Sqlmock, affected int64) {
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*jsonb_to_recordset\(\$1::jsonb\).*ON CONFLICT \(group_id\) DO UPDATE SET`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, affected))
}

func TestDNSProbeResultsUpdateStreaksWarmAfterThirdRoundAndEvaluateDeduplicatedGroups(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)
	trueValue := true
	falseValue := false
	latency40 := int64(40)
	latency8 := int64(8)

	expectDNSProbeReportLock(mock, 7, 2)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 3}, [2]int64{12, 3}, [2]int64{20, 9})
	expectDNSProbeBatchInbox(mock, 7,
		dnsProbeAcceptedResult{"success-1", 11},
		dnsProbeAcceptedResult{"failure-1", 12},
		dnsProbeAcceptedResult{"success-2", 20},
	)
	expectDNSProbeBatchState(mock, 7, 0, 3, dnsProbeStateBatchArgument{
		{TargetID: 11, LastSuccess: 1, LatencyMS: &latency40, LastError: "", ResolvedIP: "2001:db8::11", InitialSuccess: 1, InitialFailure: 0},
		{TargetID: 12, LastSuccess: 0, LatencyMS: nil, LastError: "timeout", ResolvedIP: "", InitialSuccess: 0, InitialFailure: 1},
		{TargetID: 20, LastSuccess: 1, LatencyMS: &latency8, LastError: "", ResolvedIP: "203.0.113.20", InitialSuccess: 1, InitialFailure: 0},
	})
	mock.ExpectExec(`UPDATE v2_dns_probe SET prewarm_count = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(7), int64(3), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 1, updated_at = \$2 WHERE probe_id = \$1 AND warmed_up = 0`).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))
	expectDNSFailoverEvaluationOutbox(mock, 2)
	mock.ExpectCommit()
	evaluator.onCall = func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("evaluator ran before commit: %v", err)
		}
	}

	result, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "success-1", TargetID: 11, Success: &trueValue, LatencyMS: &latency40, ResolvedIP: "2001:0db8::11"},
		{ResultID: "failure-1", TargetID: 12, Success: &falseValue, Error: "timeout"},
		{ResultID: "success-2", TargetID: 20, Success: &trueValue, LatencyMS: &latency8, ResolvedIP: "203.0.113.20"},
	}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 3 || result.Duplicates != 0 || result.PrewarmCount != 3 {
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

func TestDNSProbeResultsUsesConstantBatchSQLAndPersistsEvaluationOutboxForMaxBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	latency := int64(10)
	trueValue := true
	results := make([]DNSProbeResult, maxDNSProbeResultBatch)
	allowedRows := sqlmock.NewRows([]string{"target_id", "group_id"})
	acceptedRows := sqlmock.NewRows([]string{"result_id", "target_id"})
	for index := range results {
		targetID := int64(index + 1)
		resultID := fmt.Sprintf("batch-%03d", index+1)
		results[index] = DNSProbeResult{ResultID: resultID, TargetID: targetID, Success: &trueValue, LatencyMS: &latency}
		allowedRows.AddRow(targetID, int64(9))
		acceptedRows.AddRow(resultID, targetID)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), time.Now().Unix(), int64(0)))
	mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
	mock.ExpectQuery(`(?s)SELECT i.result_id.*FROM v2_dns_probe_result_inbox i.*jsonb_to_recordset\(\$2::jsonb\).*WHERE i.probe_id = \$1`).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"result_id"}))
	mock.ExpectQuery(`(?s)SELECT requested.target_id, t.group_id.*jsonb_to_recordset\(\$2::jsonb\).*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1`).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnRows(allowedRows)
	mock.ExpectQuery(`(?s)INSERT INTO v2_dns_probe_result_inbox.*jsonb_to_recordset\(\$2::jsonb\).*ON CONFLICT \(probe_id, result_id\) DO NOTHING.*RETURNING result_id, target_id`).
		WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(acceptedRows)
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_probe_target_state.*jsonb_to_recordset\(\$2::jsonb\).*ON CONFLICT \(probe_id, target_id\) DO UPDATE SET.*CASE WHEN v2_dns_probe_target_state.consecutive_success >= 2147483647 THEN 2147483647 ELSE v2_dns_probe_target_state.consecutive_success \+ 1 END.*CASE WHEN v2_dns_probe_target_state.consecutive_failure >= 2147483647 THEN 2147483647 ELSE v2_dns_probe_target_state.consecutive_failure \+ 1 END`).
		WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, maxDNSProbeResultBatch))
	mock.ExpectExec(`UPDATE v2_dns_probe SET prewarm_count = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(7), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*jsonb_to_recordset\(\$1::jsonb\).*ON CONFLICT \(group_id\) DO UPDATE SET`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	report, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: results})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if report.Accepted != maxDNSProbeResultBatch || report.Duplicates != 0 || report.PrewarmCount != 1 || len(report.GroupIDs) != 1 || report.GroupIDs[0] != 9 {
		t.Fatalf("report = %#v", report)
	}
	if len(evaluator.calls) != 1 || evaluator.calls[0][0] != 9 {
		t.Fatalf("evaluator calls = %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsCrossRequestReplayIsIdempotentAndEvaluatorFailureDoesNotRejectAcceptedResults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{err: errors.New("worker unavailable")}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"same-result", 11})
	expectDNSProbeBatchState(mock, 7, 1, 1)
	expectDNSFailoverEvaluationOutbox(mock, 1)
	mock.ExpectCommit()

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7, "same-result")
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

func TestDNSProbeResultsSameResultIDDifferentTargetRemainsDuplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"stable-result-id", 11})
	expectDNSProbeBatchState(mock, 7, 1, 1)
	expectDNSFailoverEvaluationOutbox(mock, 1)
	mock.ExpectCommit()

	// The original target may have been replaced between requests. The inbox
	// tombstone is keyed only by probe/result ID, so the new target must not
	// resurrect the already accepted result.
	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7, "stable-result-id")
	mock.ExpectCommit()

	trueValue := true
	falseValue := false
	latency := int64(25)
	first, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "stable-result-id", TargetID: 11, Success: &trueValue, LatencyMS: &latency, ResolvedIP: "203.0.113.11",
	}}})
	if err != nil || first.Accepted != 1 {
		t.Fatalf("first report = %#v, %v", first, err)
	}
	second, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "stable-result-id", TargetID: 22, Success: &falseValue, Error: "timeout",
	}}})
	if err != nil || second.Accepted != 0 || second.Duplicates != 1 || len(second.GroupIDs) != 0 {
		t.Fatalf("second report = %#v, %v", second, err)
	}
	if len(evaluator.calls) != 1 || evaluator.calls[0][0] != 4 {
		t.Fatalf("evaluator calls = %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsAuthorizesOnlyNewResultsAfterPersistedDuplicateClassification(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)
	falseValue := false
	trueValue := true
	latency := int64(9)
	newResult := DNSProbeResult{ResultID: "new-result", TargetID: 11, Success: &trueValue, LatencyMS: &latency}

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7, "deleted-target-result")
	// The duplicate's old target 99 is no longer bound. Only target 11 is
	// authorized, proving tombstones are classified before current target auth.
	mock.ExpectQuery(`(?s)SELECT requested.target_id, t.group_id.*jsonb_to_recordset\(\$2::jsonb\).*WHERE gp.probe_id = \$1 AND g.enabled = 1 AND t.enabled = 1.*FOR SHARE`).
		WithArgs(int64(7), dnsProbeResultsBatchArgument{newResult}).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "group_id"}).AddRow(int64(11), int64(4)))
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"new-result", 11})
	expectDNSProbeBatchState(mock, 7, 1, 1)
	expectDNSFailoverEvaluationOutbox(mock, 1)
	mock.ExpectCommit()

	report, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "deleted-target-result", TargetID: 99, Success: &falseValue, Error: "old timeout"},
		newResult,
	}})
	if err != nil || report.Accepted != 1 || report.Duplicates != 1 || len(report.GroupIDs) != 1 || report.GroupIDs[0] != 4 {
		t.Fatalf("report = %#v, %v", report, err)
	}
	if len(evaluator.calls) != 1 || evaluator.calls[0][0] != 4 {
		t.Fatalf("evaluator calls = %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsDuplicateDoesNotHideNewUnauthorizedTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7, "old-duplicate")
	expectDNSProbeAllowedTargets(mock, 7)
	mock.ExpectRollback()

	falseValue := false
	_, err = service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "old-duplicate", TargetID: 98, Success: &falseValue, Error: "old timeout"},
		{ResultID: "new-unauthorized", TargetID: 99, Success: &falseValue, Error: "timeout"},
	}})
	if !errors.Is(err, ErrDNSProbeInvalidRequest) {
		t.Fatalf("error = %v, want invalid request for the new target", err)
	}
	if len(evaluator.calls) != 0 {
		t.Fatalf("evaluator called for rolled-back batch: %#v", evaluator.calls)
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
	expectDNSProbeReportLock(mock, 7, 1)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	mock.ExpectRollback()

	falseValue := false
	_, err = service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "valid-first", TargetID: 11, Success: &falseValue, Error: "timeout",
	}, {
		ResultID: "foreign-target-later", TargetID: 99, Success: &falseValue, Error: "timeout",
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
	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4}, [2]int64{12, 4})
	expectDNSProbeBatchInbox(mock, 7,
		dnsProbeAcceptedResult{"first-result-rolls-back", 11},
		dnsProbeAcceptedResult{"second-result-fails", 12},
	)
	stateErr := errors.New("state write failed")
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_probe_target_state.*jsonb_to_recordset\(\$2::jsonb\).*ON CONFLICT`).
		WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1)).
		WillReturnError(stateErr)
	mock.ExpectRollback()

	trueValue := true
	falseValue := false
	latency := int64(10)
	if _, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "first-result-rolls-back", TargetID: 11, Success: &trueValue, LatencyMS: &latency,
	}, {
		ResultID: "second-result-fails", TargetID: 12, Success: &falseValue, Error: "timeout",
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

func TestDNSProbeResultsRollBackStateWhenEvaluationOutboxWriteFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"outbox-fails", 11})
	expectDNSProbeBatchState(mock, 7, 1, 1)
	outboxErr := errors.New("outbox write failed")
	mock.ExpectExec(`(?s)INSERT INTO v2_dns_failover_eval_outbox.*ON CONFLICT \(group_id\) DO UPDATE SET`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(outboxErr)
	mock.ExpectRollback()

	falseValue := false
	_, err = service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "outbox-fails", TargetID: 11, Success: &falseValue, Error: "timeout",
	}}})
	if err == nil || !errors.Is(err, outboxErr) {
		t.Fatalf("error = %v, want outbox write failure", err)
	}
	if len(evaluator.calls) != 0 {
		t.Fatalf("evaluator called for rolled-back outbox: %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeResultsSkipWakeHintWhenCommitFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	expectDNSProbeReportLock(mock, 7, 3)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"commit-fails", 11})
	expectDNSProbeBatchState(mock, 7, 1, 1)
	expectDNSFailoverEvaluationOutbox(mock, 1)
	commitErr := errors.New("commit failed")
	mock.ExpectCommit().WillReturnError(commitErr)

	falseValue := false
	_, err = service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "commit-fails", TargetID: 11, Success: &falseValue, Error: "timeout",
	}}})
	if err == nil || !errors.Is(err, commitErr) {
		t.Fatalf("error = %v, want commit failure", err)
	}
	if len(evaluator.calls) != 0 {
		t.Fatalf("evaluator called before successful commit: %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

type dnsFailoverEvaluationRequesterFunc func(context.Context, []int64) error

func (requester dnsFailoverEvaluationRequesterFunc) RequestDNSFailoverEvaluation(ctx context.Context, groupIDs []int64) error {
	return requester(ctx, groupIDs)
}

func TestDNSProbeEvaluationWakeIgnoresParentCancellationAndRecoversPanic(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), struct{}{}, "preserved"))
	cancel()
	called := false
	requester := dnsFailoverEvaluationRequesterFunc(func(ctx context.Context, groupIDs []int64) error {
		called = true
		if err := ctx.Err(); err != nil {
			t.Fatalf("wake context inherited parent cancellation: %v", err)
		}
		if got := ctx.Value(struct{}{}); got != "preserved" {
			t.Fatalf("wake context value = %#v", got)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
			t.Fatalf("wake deadline = %v, ok=%v", deadline, ok)
		}
		if len(groupIDs) != 2 || groupIDs[0] != 3 || groupIDs[1] != 9 {
			t.Fatalf("group IDs = %#v", groupIDs)
		}
		panic("wake panic")
	})

	requestDNSFailoverEvaluationWake(parent, requester, []int64{3, 9})
	if !called {
		t.Fatal("wake requester was not called")
	}
}

func TestDNSProbeResultsRequireFreshHeartbeatBeforeAnyWrites(t *testing.T) {
	for _, test := range []struct {
		name          string
		lastHeartbeat any
	}{
		{name: "never heartbeated", lastHeartbeat: nil},
		{name: "stale heartbeat", lastHeartbeat: time.Now().Add(-2 * time.Minute).Unix()},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			evaluator := &recordingDNSFailoverEvaluationRequester{}
			service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), test.lastHeartbeat, int64(3)))
			mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
			mock.ExpectRollback()

			falseValue := false
			_, err = service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
				ResultID: "heartbeat-required", TargetID: 11, Success: &falseValue, Error: "timeout",
			}}})
			if !errors.Is(err, ErrDNSProbeHeartbeatRequired) {
				t.Fatalf("error = %v, want ErrDNSProbeHeartbeatRequired", err)
			}
			if len(evaluator.calls) != 0 {
				t.Fatalf("evaluator called without fresh heartbeat: %#v", evaluator.calls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestDNSProbeStaleResultsThenHeartbeatResetStartsFirstPrewarmRound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)
	staleAt := time.Now().Add(-2 * time.Minute).Unix()

	// A stale report is rejected before target lookup or inbox writes.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), staleAt, int64(3)))
	mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
	mock.ExpectRollback()

	// Heartbeat owns reconnect detection and resets old decision state.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), staleAt, int64(3)))
	mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
	mock.ExpectExec(`UPDATE v2_dns_probe SET .*prewarm_count = 0.*WHERE id = \$1`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0`).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	// The first new result after that reset is prewarm round one.
	expectDNSProbeReportLock(mock, 7, 0)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"after-heartbeat", 11})
	expectDNSProbeBatchState(mock, 7, 0, 1)
	mock.ExpectExec(`UPDATE v2_dns_probe SET prewarm_count = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(7), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEvaluationOutbox(mock, 1)
	mock.ExpectCommit()

	trueValue := true
	latency := int64(25)
	request := DNSProbeResultsRequest{Results: []DNSProbeResult{{ResultID: "after-heartbeat", TargetID: 11, Success: &trueValue, LatencyMS: &latency, ResolvedIP: "203.0.113.11"}}}
	if _, err := service.ReportDNSProbeResults(context.Background(), 7, request); !errors.Is(err, ErrDNSProbeHeartbeatRequired) {
		t.Fatalf("stale report error = %v", err)
	}
	heartbeat, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{Version: "v1", Arch: "amd64", PublicIP: "203.0.113.7"})
	if err != nil || !heartbeat.Reconnected || heartbeat.PrewarmCount != 0 {
		t.Fatalf("heartbeat = %#v, %v", heartbeat, err)
	}
	report, err := service.ReportDNSProbeResults(context.Background(), 7, request)
	if err != nil || report.Accepted != 1 || report.PrewarmCount != 1 {
		t.Fatalf("report = %#v, %v", report, err)
	}
	if len(evaluator.calls) != 1 || evaluator.calls[0][0] != 4 {
		t.Fatalf("evaluator calls = %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSProbeNullHeartbeatLegacyStateResetsBeforeFirstPrewarmRound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	evaluator := &recordingDNSFailoverEvaluationRequester{}
	service := (&DBService{db: db, dnsFailoverSchemaOK: true}).WithDNSFailoverEvaluationRequester(evaluator)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at", "prewarm_count"}).AddRow(int64(1), nil, int64(3)))
	mock.ExpectQuery(`SELECT MIN\(g.probe_offline_sec\).*WHERE gp.probe_id = \$1 AND g.enabled = 1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(90)))
	mock.ExpectExec(`UPDATE v2_dns_probe SET .*prewarm_count = 0.*WHERE id = \$1`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0`).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	expectDNSProbeReportLock(mock, 7, 0)
	expectDNSProbeExistingResultIDs(mock, 7)
	expectDNSProbeAllowedTargets(mock, 7, [2]int64{11, 4})
	expectDNSProbeBatchInbox(mock, 7, dnsProbeAcceptedResult{"after-first-heartbeat", 11})
	expectDNSProbeBatchState(mock, 7, 0, 1)
	mock.ExpectExec(`UPDATE v2_dns_probe SET prewarm_count = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(7), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectDNSFailoverEvaluationOutbox(mock, 1)
	mock.ExpectCommit()

	heartbeat, err := service.HeartbeatDNSProbe(context.Background(), 7, DNSProbeHeartbeatRequest{Version: "v1", Arch: "amd64", PublicIP: "203.0.113.7"})
	if err != nil {
		t.Fatalf("HeartbeatDNSProbe: %v", err)
	}
	if heartbeat.Reconnected || heartbeat.PrewarmCount != 0 {
		t.Fatalf("heartbeat = %#v, want first heartbeat reset without reconnect", heartbeat)
	}
	trueValue := true
	latency := int64(25)
	report, err := service.ReportDNSProbeResults(context.Background(), 7, DNSProbeResultsRequest{Results: []DNSProbeResult{{
		ResultID: "after-first-heartbeat", TargetID: 11, Success: &trueValue, LatencyMS: &latency, ResolvedIP: "203.0.113.11",
	}}})
	if err != nil || report.Accepted != 1 || report.PrewarmCount != 1 {
		t.Fatalf("report = %#v, %v", report, err)
	}
	if len(evaluator.calls) != 1 || evaluator.calls[0][0] != 4 {
		t.Fatalf("evaluator calls = %#v", evaluator.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
