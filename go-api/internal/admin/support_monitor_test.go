package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const supportMonitorTestNow int64 = 10_000

func supportMonitorFixture(id int64, host string, port int64, success bool, reported int64) (ClientEntryMonitorPolicy, ClientEntryMonitorRecord) {
	key := "policy:" + strconv.FormatInt(id, 10)
	policy := ClientEntryMonitorPolicy{
		ID: id, Name: "测试规则", Enabled: true,
		Targets: []ClientEntryMonitorCandidateTarget{{SourceKey: key, Name: "入口组", Host: host, SuggestedPort: port}},
	}
	monitor := ClientEntryMonitorRecord{
		ID: id, PolicyID: id, Enabled: true, CheckIntervalSec: 30, TCPTimeoutMS: 5000,
		FailureThreshold: 3, SuccessThreshold: 2,
		Targets: []ClientEntryMonitorTarget{{ID: id, SourceKey: key, Host: host, Port: port,
			States: []ClientEntryMonitorState{{ProbeID: 7, LastSuccess: &success, LastReportedAt: &reported}}}},
	}
	return policy, monitor
}

func supportOnlineTestProbes() []supportMonitorProbe {
	return []supportMonitorProbe{{ID: 7, Name: "在线探针", LastHeartbeatAt: sql.NullInt64{Int64: supportMonitorTestNow, Valid: true}}}
}

func TestSupportEntryOverviewDeduplicatesCanonicalEndpointsAndKeepsPorts(t *testing.T) {
	cases := []struct {
		host string
		port int64
	}{
		{"Entry.Example.COM.", 443}, {"entry.example.com", 443}, {"entry.example.com", 8443},
		{"[2001:DB8::10]", 443}, {"2001:db8::10", 443},
		{"::ffff:192.0.2.10", 443}, {"192.0.2.10", 443},
		// Domain and IP identities remain distinct even if a DNS response happens
		// to resolve the domain to that same address.
		{"different.example.com", 443},
	}
	policies, monitors := []ClientEntryMonitorPolicy{}, []ClientEntryMonitorRecord{}
	for index, item := range cases {
		policy, monitor := supportMonitorFixture(int64(index+1), item.host, item.port, true, supportMonitorTestNow)
		monitor.Targets[0].States[0].LastResolvedIP = "192.0.2.10"
		policies, monitors = append(policies, policy), append(monitors, monitor)
	}
	result := buildSupportEntryOverview(policies, monitors, supportOnlineTestProbes(), supportMonitorTestNow)
	if result.Summary.Total != 5 || result.Summary.Healthy != 5 {
		t.Fatalf("summary = %#v", result.Summary)
	}
	expected := map[string]int{
		"tcp:entry.example.com:443": 2, "tcp:entry.example.com:8443": 1,
		"tcp:[2001:db8::10]:443": 2, "tcp:192.0.2.10:443": 2, "tcp:different.example.com:443": 1,
	}
	for _, item := range result.Items {
		if len(item.Sources) != expected[item.Key] || item.Status != "healthy" || item.Protocol != "tcp" {
			t.Fatalf("unexpected endpoint %#v", item)
		}
		if len(item.Probes) != 1 || item.Probes[0].SourceCount != int64(expected[item.Key]) {
			t.Fatalf("probe aggregation = %#v", item.Probes)
		}
	}
}

func TestSupportEntryOverviewDoesNotHideConflictingSourceResults(t *testing.T) {
	p1, m1 := supportMonitorFixture(1, "entry.example.com", 443, true, supportMonitorTestNow)
	p2, m2 := supportMonitorFixture(2, "ENTRY.example.com.", 443, false, supportMonitorTestNow-1)
	m2.FailureThreshold, m2.SuccessThreshold = 6, 4
	result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1, p2}, []ClientEntryMonitorRecord{m1, m2}, supportOnlineTestProbes(), supportMonitorTestNow)
	item := result.Items[0]
	if item.Status != "partial" || item.Probes[0].Status != "partial" || item.Probes[0].LastSuccess != nil {
		t.Fatalf("conflict was hidden: %#v", item)
	}
	if item.Sources[0].Status != "healthy" || item.Sources[1].Status != "unhealthy" || item.Sources[1].FailureThreshold != 6 || item.Sources[1].SuccessThreshold != 4 {
		t.Fatalf("per-source details lost: %#v", item.Sources)
	}
	if item.LastReportedAt == nil || *item.LastReportedAt != supportMonitorTestNow {
		t.Fatalf("latest report = %v", item.LastReportedAt)
	}
}

func TestSupportEntryOverviewFreshnessIsPerSourceAndRequiresOnlineProbe(t *testing.T) {
	p1, m1 := supportMonitorFixture(1, "entry.example.com", 443, true, supportMonitorTestNow-40)
	p2, m2 := supportMonitorFixture(2, "entry.example.com", 443, true, supportMonitorTestNow-40)
	m2.CheckIntervalSec, m2.TCPTimeoutMS = 5, 1000
	result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1, p2}, []ClientEntryMonitorRecord{m1, m2}, supportOnlineTestProbes(), supportMonitorTestNow)
	if result.Items[0].Status != "partial" || result.Items[0].Sources[0].Status != "healthy" || result.Items[0].Sources[1].Status != "stale" {
		t.Fatalf("freshness ignored differing intervals: %#v", result.Items[0])
	}
	probes := supportOnlineTestProbes()
	probes[0].LastHeartbeatAt.Int64 = supportMonitorTestNow - defaultProbeOfflineSec - 1
	result = buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1}, []ClientEntryMonitorRecord{m1}, probes, supportMonitorTestNow)
	if result.Items[0].Status != "stale" || result.Items[0].Probes[0].Online {
		t.Fatalf("offline probe accepted as healthy: %#v", result.Items[0])
	}
}

func TestSupportEntryOverviewKeepsEveryProbeAndUnmonitoredSourceVisible(t *testing.T) {
	p1, m1 := supportMonitorFixture(1, "entry.example.com", 443, true, supportMonitorTestNow)
	p2, _ := supportMonitorFixture(2, "entry.example.com", 443, true, supportMonitorTestNow)
	result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1, p2}, []ClientEntryMonitorRecord{m1}, supportOnlineTestProbes(), supportMonitorTestNow)
	if result.Items[0].Status != "partial" || len(result.Items[0].Sources) != 2 || result.Items[0].Sources[1].Status != "unmonitored" {
		t.Fatalf("unmonitored source hidden: %#v", result.Items[0])
	}
	probes := append(supportOnlineTestProbes(), supportMonitorProbe{ID: 8, Name: "无结果探针", LastHeartbeatAt: sql.NullInt64{Int64: supportMonitorTestNow, Valid: true}})
	result = buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1}, []ClientEntryMonitorRecord{m1}, probes, supportMonitorTestNow)
	if result.Items[0].Status != "partial" || len(result.Items[0].Probes) != 2 || result.Items[0].Probes[1].Status != "stale" {
		t.Fatalf("missing probe hidden: %#v", result.Items[0])
	}
	p2.Enabled = false
	result = buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1, p2}, []ClientEntryMonitorRecord{m1}, supportOnlineTestProbes(), supportMonitorTestNow)
	if result.Items[0].Status != "healthy" || result.Items[0].Sources[1].Status != "disabled" {
		t.Fatalf("disabled source should stay visible without affecting active endpoint: %#v", result.Items[0])
	}
}

func TestSupportEntryOverviewMissingDisabledAndPendingStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ClientEntryMonitorPolicy, *ClientEntryMonitorRecord, *[]supportMonitorProbe)
		status string
	}{
		{"policy disabled", func(p *ClientEntryMonitorPolicy, _ *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			p.Enabled = false
		}, "disabled"},
		{"monitor disabled", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Enabled = false
		}, "disabled"},
		{"no probes", func(_ *ClientEntryMonitorPolicy, _ *ClientEntryMonitorRecord, p *[]supportMonitorProbe) { *p = nil }, "stale"},
		{"no target", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets = nil
		}, "stale"},
		{"no report", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets[0].States = nil
		}, "stale"},
		{"future report", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			*m.Targets[0].States[0].LastReportedAt = supportMonitorTestNow + 1
		}, "stale"},
		{"unconfirmed", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets[0].States[0].LastSuccess = nil
		}, "stale"},
		{"failure confirmation", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets[0].States[0].ConsecutiveFailure = 1
		}, "partial"},
		{"recovery confirmation", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			*m.Targets[0].States[0].LastSuccess = false
			m.Targets[0].States[0].ConsecutiveSuccess = 1
		}, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, monitor := supportMonitorFixture(1, "entry.example.com", 443, true, supportMonitorTestNow)
			probes := supportOnlineTestProbes()
			tc.change(&policy, &monitor, &probes)
			result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{policy}, []ClientEntryMonitorRecord{monitor}, probes, supportMonitorTestNow)
			if result.Items[0].Status != tc.status {
				t.Fatalf("status = %q, want %q: %#v", result.Items[0].Status, tc.status, result.Items[0])
			}
		})
	}
}

func TestSupportEntryOverviewListsUnmonitoredSourcesAndDoesNotReuseOldHostHealth(t *testing.T) {
	policy, monitor := supportMonitorFixture(1, "new.example.com", 443, true, supportMonitorTestNow)
	result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{policy}, nil, supportOnlineTestProbes(), supportMonitorTestNow)
	if result.Items[0].Status != "unmonitored" || result.Items[0].Sources[0].Monitored || result.Summary.Unmonitored != 1 {
		t.Fatalf("unmonitored entry absent or healthy: %#v", result)
	}
	monitor.Targets[0].Host = "old.example.com"
	result = buildSupportEntryOverview([]ClientEntryMonitorPolicy{policy}, []ClientEntryMonitorRecord{monitor}, supportOnlineTestProbes(), supportMonitorTestNow)
	item := result.Items[0]
	if item.Host != "new.example.com" || item.Status != "stale" || item.LastReportedAt != nil || item.Probes[0].LastSuccess != nil {
		t.Fatalf("old target state reused: %#v", item)
	}
}

func TestSupportEntryOverviewDoesNotDeduplicateInvalidLegacyHosts(t *testing.T) {
	p1, m1 := supportMonitorFixture(1, "bad host", 443, true, supportMonitorTestNow)
	p2, m2 := supportMonitorFixture(2, "bad host", 443, true, supportMonitorTestNow)
	result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1, p2}, []ClientEntryMonitorRecord{m1, m2}, supportOnlineTestProbes(), supportMonitorTestNow)
	if len(result.Items) != 2 || result.Items[0].Key == result.Items[1].Key || result.Summary.Stale != 2 {
		t.Fatalf("invalid hosts were merged: %#v", result)
	}
}

func TestSupportEntryOverviewSortsProblemsBeforeHealthyAndKeepsCoverageExplicit(t *testing.T) {
	var policies []ClientEntryMonitorPolicy
	var monitors []ClientEntryMonitorRecord
	for index, host := range []string{"a-healthy.example.com", "z-failing.example.com", "b-stale.example.com", "c-unmonitored.example.com", "d-disabled.example.com"} {
		policy, monitor := supportMonitorFixture(int64(index+1), host, 443, index != 1, supportMonitorTestNow)
		if index == 2 {
			*monitor.Targets[0].States[0].LastReportedAt = 1
		}
		if index == 4 {
			policy.Enabled = false
		}
		policies = append(policies, policy)
		if index != 3 {
			monitors = append(monitors, monitor)
		}
	}
	result := buildSupportEntryOverview(policies, monitors, supportOnlineTestProbes(), supportMonitorTestNow)
	want := []string{"unhealthy", "stale", "unmonitored", "healthy", "disabled"}
	for i, status := range want {
		if result.Items[i].Status != status {
			t.Fatalf("sort[%d] = %q, want %q", i, result.Items[i].Status, status)
		}
	}
	if result.Coverage != "entry_assignment" || !strings.Contains(result.CoverageNote, "不包含独立客户端入口") {
		t.Fatalf("coverage is ambiguous: %#v", result)
	}
}

func TestSupportEntryOverviewJSONHasNoAdministratorSecretsOrConfiguration(t *testing.T) {
	policy, monitor := supportMonitorFixture(1, "entry.example.com", 443, true, supportMonitorTestNow)
	result := buildSupportEntryOverview([]ClientEntryMonitorPolicy{policy}, []ClientEntryMonitorRecord{monitor}, supportOnlineTestProbes(), supportMonitorTestNow)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var checkKeys func(any)
	checkKeys = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, item := range value {
				for _, forbidden := range []string{"secret", "token", "password", "conditions", "extra_nodes", "public_ip", "auto_split_enabled"} {
					if strings.Contains(key, forbidden) {
						t.Fatalf("sensitive/config field %q leaked: %s", key, raw)
					}
				}
				checkKeys(item)
			}
		case []any:
			for _, item := range value {
				checkKeys(item)
			}
		}
	}
	checkKeys(value)
}

func TestSupportMonitorProbeReadUsesOnlySafeColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	mock.ExpectQuery(`^SELECT id, name, last_heartbeat_at\s+FROM v2_dns_probe WHERE enabled = 1 ORDER BY id$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}).AddRow(7, "探针", supportMonitorTestNow))
	probes, err := service.loadSupportMonitorProbes(context.Background())
	if err != nil || len(probes) != 1 || probes[0].ID != 7 {
		t.Fatalf("probe read: %#v, %v", probes, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSupportEntryOverviewReadOnlyDoesNotRefreshOrScheduleMonitors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	// Startup owns schema initialization. All subsequent overview work must be
	// SELECT-only; unexpected writes, task scheduling or secret reads fail.
	service.clientEntryEnsureOnce.Do(func() {})
	mock.ExpectQuery(`(?s)^SELECT p.id, p.name, p.sort, p.mode,.*FROM v2_client_entry_user_policy p.*ORDER BY p.sort ASC NULLS LAST, p.id ASC$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}))
	mock.ExpectQuery(`(?s)^SELECT m.id, m.policy_id, p.name, p.action, m.enabled,.*FROM v2_client_entry_monitor m.*ORDER BY p.sort ASC NULLS LAST, p.id ASC$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id", "name", "action", "enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "created_at", "updated_at"}))
	mock.ExpectQuery(`^SELECT id, name, last_heartbeat_at\s+FROM v2_dns_probe WHERE enabled = 1 ORDER BY id$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}).AddRow(7, "探针", supportMonitorTestNow))
	result, err := service.SupportEntryOverview(context.Background())
	if err != nil || result.Items == nil || len(result.Items) != 0 || result.Summary.Total != 0 {
		t.Fatalf("overview = %#v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSupportEntryOverviewRejectsUnavailableService(t *testing.T) {
	for _, service := range []*DBService{nil, {}} {
		if _, err := service.SupportEntryOverview(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("error = %v", err)
		}
	}
}
