package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestClientEntryMonitorAddressKeyCanonicalizesEquivalentHosts(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int64
		want string
	}{
		{name: "lowercases hostname and removes trailing dot", host: "Entry.Example.COM.", port: 443, want: "entry.example.com:443"},
		{name: "canonicalizes ipv4 mapped address", host: "::ffff:192.0.2.10", port: 443, want: "192.0.2.10:443"},
		{name: "formats ipv6 with brackets", host: "2001:DB8::10", port: 8443, want: "[2001:db8::10]:8443"},
		{name: "accepts historical bracketed ipv6", host: "[2001:db8::10]", port: 8443, want: "[2001:db8::10]:8443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := clientEntryMonitorAddressKey(test.host, test.port); got != test.want {
				t.Fatalf("address key = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClientEntryMonitorAddressKeySeparatesPortsAndRejectsInvalidValues(t *testing.T) {
	if got, want := clientEntryMonitorAddressKey("entry.example.com", 443), "entry.example.com:443"; got != want {
		t.Fatalf("443 key = %q, want %q", got, want)
	}
	if got, want := clientEntryMonitorAddressKey("entry.example.com", 8443), "entry.example.com:8443"; got != want {
		t.Fatalf("8443 key = %q, want %q", got, want)
	}
	for _, test := range []struct {
		name string
		host string
		port int64
	}{
		{name: "empty host", host: "", port: 443},
		{name: "zero port", host: "entry.example.com", port: 0},
		{name: "port above range", host: "entry.example.com", port: 65536},
		{name: "whitespace host", host: "entry example.com", port: 443},
		{name: "scheme host", host: "https://entry.example.com", port: 443},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := clientEntryMonitorAddressKey(test.host, test.port); got != "" {
				t.Fatalf("invalid address key = %q, want empty", got)
			}
		})
	}
}

func TestFormatClientEntryMonitorTransitionIsAddressOnly(t *testing.T) {
	snapshot := clientEntryProbeTargetSnapshot{
		PolicyName: "不应出现在消息中的规则",
		Host:       "Entry.Example.COM.",
		Port:       443,
		ProbeName:  "不应出现在消息中的探针",
	}
	for _, test := range []struct {
		name       string
		transition string
		result     DNSProbeResult
		wantStatus string
		wantDetail string
	}{
		{name: "down", transition: "down", result: DNSProbeResult{Error: "timeout"}, wantStatus: "用户入口掉线", wantDetail: "详情：timeout"},
		{name: "recovered", transition: "recovered", result: DNSProbeResult{LatencyMS: int64PtrForDedupTest(31)}, wantStatus: "用户入口恢复", wantDetail: "详情：延迟 31 ms"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := formatClientEntryMonitorTransition(snapshot, test.transition, test.result, 0)
			if !strings.Contains(message, test.wantStatus) || !strings.Contains(message, "地址：entry.example.com:443") || !strings.Contains(message, test.wantDetail) {
				t.Fatalf("address-only alert = %q", message)
			}
			if strings.Contains(message, "规则：") || strings.Contains(message, "探针：") || strings.Contains(message, snapshot.PolicyName) || strings.Contains(message, snapshot.ProbeName) {
				t.Fatalf("alert still contains group/probe details: %q", message)
			}
		})
	}
}

func int64PtrForDedupTest(value int64) *int64 { return &value }

// A probe can report two different policy targets that point at the same
// address in one batch.  State remains independent per target, but the event
// queue must only retain one pending transition for that address/type.  The
// second INSERT therefore legitimately affects zero rows through the partial
// unique index; treating that as an error would make an otherwise valid probe
// report fail and could trigger retries/duplicate alerts.
func TestClientEntryProbeCoalescesSameAddressPendingEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	probeID := int64(7)
	now := time.Now().Unix()
	failed := false

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(int64(1), now))

	for _, target := range []struct {
		id        int64
		monitorID int64
		resultID  string
		inboxID   int64
	}{
		{id: 5, monitorID: 3, resultID: "same-address-down-a", inboxID: 1},
		{id: 6, monitorID: 4, resultID: "same-address-down-b", inboxID: 2},
	} {
		mock.ExpectQuery(`(?s)SELECT target.id, target.generation, monitor.id, monitor.policy_id,.*FROM v2_client_entry_monitor_target target.*WHERE target.id = \$2`).
			WithArgs(probeID, target.id).
			WillReturnRows(sqlmock.NewRows([]string{"target_id", "generation", "monitor_id", "policy_id", "policy_name", "target_name", "source_key", "host", "port", "probe_name", "auto_split_enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold"}).
				AddRow(target.id, int64(1), target.monitorID, int64(42), "规则", "入口", "policy:42", "Entry.Example.COM.", int64(443), "探针", int64(0), int64(30), int64(5000), int64(3), int64(2)))
		mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor_result_inbox.*ON CONFLICT \(probe_id, result_id\) DO NOTHING.*RETURNING id`).
			WithArgs(probeID, target.id, nil, target.resultID, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(target.inboxID))
		mock.ExpectQuery(`(?s)SELECT last_success, consecutive_success, consecutive_failure.*FROM v2_client_entry_monitor_state.*FOR UPDATE`).
			WithArgs(target.id, probeID).
			WillReturnRows(sqlmock.NewRows([]string{"last_success", "consecutive_success", "consecutive_failure", "last_reported_at"}).
				AddRow(int64(1), int64(0), int64(2), now))
		mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_state.*ON CONFLICT \(target_id, probe_id\) DO UPDATE SET`).
			WithArgs(target.id, probeID, int64(0), nil, "timeout", "", int64(0), int64(3), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\(\$1, 0::bigint\)\)`).
			WithArgs("entry.example.com:443").
			WillReturnResult(sqlmock.NewResult(0, 1))

		insertExpectation := mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_event.*ON CONFLICT \(address_key, event_type\).*DO NOTHING`)
		insertExpectation.WithArgs(target.monitorID, target.id, probeID, "down", sqlmock.AnyArg(), sqlmock.AnyArg(), "entry.example.com:443", sqlmock.AnyArg())
		if target.id == 5 {
			insertExpectation.WillReturnResult(sqlmock.NewResult(1, 1))
		} else {
			insertExpectation.WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	mock.ExpectCommit()

	result, err := service.ReportDNSProbeResults(context.Background(), probeID, DNSProbeResultsRequest{Results: []DNSProbeResult{
		{ResultID: "same-address-down-a", TargetID: clientEntryProbeTargetOffset + 5, TargetVersion: 1, Success: &failed, Error: "timeout"},
		{ResultID: "same-address-down-b", TargetID: clientEntryProbeTargetOffset + 6, TargetVersion: 1, Success: &failed, Error: "timeout"},
	}})
	if err != nil {
		t.Fatalf("ReportDNSProbeResults: %v", err)
	}
	if result.Accepted != 2 || result.Skipped != 0 || result.Duplicates != 0 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryMonitorAddressKeyDoesNotTreatEmptyKeyAsDeduplicable(t *testing.T) {
	// Invalid/legacy targets intentionally leave address_key NULL.  PostgreSQL
	// partial unique indexes ignore NULL values, so each such historical event
	// remains independently auditable rather than being collapsed into one
	// arbitrary bucket.
	if got := clientEntryMonitorAddressKey("bad host", 443); got != "" {
		t.Fatalf("invalid host key = %q, want empty", got)
	}
}
