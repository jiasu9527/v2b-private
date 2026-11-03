package admin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEntryMonitorInventoryPreservesSourceGapsDespiteOtherAddressCoverage(t *testing.T) {
	p1, m1 := supportMonitorFixture(1, "Entry.Example.COM.", 443, true, supportMonitorTestNow)
	p2, _ := supportMonitorFixture(2, "entry.example.com", 443, true, supportMonitorTestNow)
	p3, _ := supportMonitorFixture(3, "entry.example.com", 8443, true, supportMonitorTestNow)
	p4, _ := supportMonitorFixture(4, "disabled.example.com", 443, true, supportMonitorTestNow)
	p4.Enabled = false
	result := buildEntryMonitorInventory([]ClientEntryMonitorPolicy{p1, p2, p3, p4}, nil, []ClientEntryMonitorRecord{m1}, supportOnlineTestProbes(), nil, supportMonitorTestNow)
	want := EntryMonitorInventorySummary{TotalSources: 4, ActiveSources: 3, ConfiguredSources: 1, SourceGaps: 2, TotalAddresses: 3, ActiveAddresses: 2, CoveredAddresses: 1, AddressGaps: 1, UnknownSources: 2}
	if result.Summary != want {
		t.Fatalf("summary = %#v, want %#v", result.Summary, want)
	}
	if !result.Sources[1].AddressCovered || !result.Sources[1].AddressObserved || result.Sources[1].CoverageStatus != "unconfigured" {
		t.Fatalf("address coverage concealed source gap: %#v", result.Sources[1])
	}
	if result.Sources[2].AddressCovered || result.Sources[2].AddressObserved {
		t.Fatalf("same host's other port was incorrectly covered: %#v", result.Sources[2])
	}
	if result.Sources[3].CoverageStatus != "policy_disabled" {
		t.Fatalf("disabled source must be visible in inventory: %#v", result.Sources[3])
	}
	// Existing staff UI behavior is deliberately different and unchanged.
	home := buildSupportEntryOverview([]ClientEntryMonitorPolicy{p1, p2, p3, p4}, []ClientEntryMonitorRecord{m1}, supportOnlineTestProbes(), supportMonitorTestNow)
	if home.Summary.Total != 1 || len(home.Items[0].Sources) != 1 || home.Summary.Unmonitored != 0 {
		t.Fatalf("coverage inventory must not change homepage behavior: %#v", home)
	}
}

func TestEntryMonitorInventoryCanonicalAddressesAndInvalidValues(t *testing.T) {
	var policies []ClientEntryMonitorPolicy
	for i, host := range []string{"[2001:DB8::10]", "2001:db8::10", "::ffff:192.0.2.10", "192.0.2.10", "Entry.Example.COM.", "entry.example.com", "bad host", "bad host"} {
		policy, _ := supportMonitorFixture(int64(i+1), host, 443, true, supportMonitorTestNow)
		policies = append(policies, policy)
	}
	result := buildEntryMonitorInventory(policies, nil, nil, nil, nil, supportMonitorTestNow)
	if result.Summary.TotalSources != 8 || result.Summary.TotalAddresses != 3 || result.Summary.AddressGaps != 3 || result.Summary.SourceGaps != 8 {
		t.Fatalf("normalization or invalid-source count failed: %#v", result)
	}
	for _, item := range result.Sources[6:] {
		if item.AddressKey != "" || item.CoverageStatus != "invalid_address" || item.AddressCovered {
			t.Fatalf("invalid source put into coverage bucket: %#v", item)
		}
	}
	for _, tc := range []struct{ input, want string }{
		{" Entry.EXAMPLE.com. ", "entry.example.com"}, {"[2001:DB8:0:0::10]", "2001:db8::10"}, {"::ffff:192.0.2.10", "192.0.2.10"},
	} {
		got, err := NormalizeEntryMonitorHost(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeEntryMonitorHost(%q) = %q,%v", tc.input, got, err)
		}
	}
	for _, bad := range []string{"", "http://entry.example.com", "entry.example.com:443", "bad host", "[invalid]", "fe80::1%en0"} {
		if got, err := NormalizeEntryMonitorHost(bad); err == nil {
			t.Errorf("accepted malformed host %q as %q", bad, got)
		}
	}
}

func TestEntryMonitorInventoryCoverageStatusAndObservationIssues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*ClientEntryMonitorPolicy, *ClientEntryMonitorRecord, *[]supportMonitorProbe)
		coverage string
		issue    string
		covered  bool
		observed bool
	}{
		{"ready", func(_ *ClientEntryMonitorPolicy, _ *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {}, "ready", "", true, true},
		{"TCP failed remains observed", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			*m.Targets[0].States[0].LastSuccess = false
		}, "ready", "", true, true},
		{"monitor disabled", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Enabled = false
		}, "monitor_disabled", "monitor_disabled", false, false},
		{"no enabled probe", func(_ *ClientEntryMonitorPolicy, _ *ClientEntryMonitorRecord, p *[]supportMonitorProbe) { *p = nil }, "no_enabled_probes", "no_enabled_probes", false, false},
		{"target missing", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets = nil
		}, "target_missing", "target_missing", false, false},
		{"changed address", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets[0].Host = "old.example.com"
		}, "target_address_mismatch", "target_address_mismatch", false, false},
		{"probe offline", func(_ *ClientEntryMonitorPolicy, _ *ClientEntryMonitorRecord, p *[]supportMonitorProbe) {
			(*p)[0].LastHeartbeatAt.Int64 = 0
		}, "ready", "probe_offline", true, false},
		{"result stale", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			*m.Targets[0].States[0].LastReportedAt = 1
		}, "ready", "observation_stale", true, false},
		{"result future", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			*m.Targets[0].States[0].LastReportedAt = supportMonitorTestNow + 1
		}, "ready", "observation_stale", true, false},
		{"result absent", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets[0].States = nil
		}, "ready", "no_observation", true, false},
		{"result undecided", func(_ *ClientEntryMonitorPolicy, m *ClientEntryMonitorRecord, _ *[]supportMonitorProbe) {
			m.Targets[0].States[0].LastSuccess = nil
		}, "ready", "observation_unconfirmed", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, monitor := supportMonitorFixture(1, "entry.example.com", 443, true, supportMonitorTestNow)
			probes := supportOnlineTestProbes()
			tc.change(&policy, &monitor, &probes)
			result := buildEntryMonitorInventory([]ClientEntryMonitorPolicy{policy}, nil, []ClientEntryMonitorRecord{monitor}, probes, nil, supportMonitorTestNow)
			item := result.Sources[0]
			if item.CoverageStatus != tc.coverage || item.AddressCovered != tc.covered || item.AddressObserved != tc.observed {
				t.Fatalf("unexpected coverage: %#v", item)
			}
			if tc.issue != "" && !strings.Contains(strings.Join(item.Issues, ","), tc.issue) {
				t.Fatalf("missing machine-readable issue %q: %#v", tc.issue, item)
			}
			if tc.issue == "" && len(item.Issues) != 0 {
				t.Fatalf("unexpected issues %#v", item.Issues)
			}
		})
	}
}

func TestEntryMonitorInventoryTypesReferencesAndDoesNotClaimResolvedIPIdentity(t *testing.T) {
	details := []ClientEntryUserPolicyRecord{
		{ID: 1, Name: "fixed leaf", Mode: ClientEntryUserPolicyModeSplit, Action: cliententry.ActionOverride, Enabled: 1, Members: []ClientEntryGroupMemberRecord{{ServerType: "v2node", ServerID: 14}}, SplitGroups: []ClientEntryUserPolicySplitGroupRecord{{ID: 58, IsLeaf: true, EntryHost: "entry.example.com", Name: "VIP A", Path: "A"}}},
		{ID: 2, Name: "original", Action: cliententry.ActionOriginal, Enabled: 1, Members: []ClientEntryGroupMemberRecord{{ServerType: "v2node", ServerID: 14}, {ServerType: "v2node", ServerID: 15}}},
	}
	nodes := map[string]clientEntryMonitorNode{
		"v2node:14": {ServerType: "v2node", ServerID: 14, Host: "192.0.2.10", Port: 443},
		"v2node:15": {ServerType: "v2node", ServerID: 15, Host: "other.example.com", Port: 8443},
	}
	policies := buildClientEntryMonitorPolicies(details, nodes)
	collections := map[string][]EntryMonitorReference{"split_group:58": {{Type: "collection", ID: 4, Name: "VIP pool"}}}
	result := buildEntryMonitorInventory(policies, details, nil, nil, collections, supportMonitorTestNow)
	if len(result.Sources) != 3 || result.Summary.TotalAddresses != 3 {
		t.Fatalf("candidate inventory: %#v", result)
	}
	wantRefs := []EntryMonitorReference{{Type: "policy", ID: 1, Name: "fixed leaf"}, {Type: "split_group", ID: 58, Name: "VIP A · A 组入口"}, {Type: "node", ID: 14, Protocol: "v2node"}, {Type: "collection", ID: 4, Name: "VIP pool"}}
	if !reflect.DeepEqual(result.Sources[0].References, wantRefs) {
		t.Fatalf("typed leaf/collection/node references = %#v", result.Sources[0].References)
	}
	if result.Sources[1].References[1].ID != 14 || len(result.Sources[1].References) != 2 {
		t.Fatalf("original node should reference only the node producing this address, not all policy members: %#v", result.Sources[1])
	}
	policy, monitor := supportMonitorFixture(3, "entry.example.com", 443, true, supportMonitorTestNow)
	monitor.Targets[0].States[0].LastResolvedIP = "192.0.2.10"
	result = buildEntryMonitorInventory([]ClientEntryMonitorPolicy{policy}, nil, []ClientEntryMonitorRecord{monitor}, supportOnlineTestProbes(), nil, supportMonitorTestNow)
	if result.Sources[0].Host != "entry.example.com" || result.Sources[0].AddressKey != "entry.example.com:443" {
		t.Fatalf("probe-resolved IP substituted for configured domain identity: %#v", result)
	}
	if result.Sources[0].PortBasis != "configured_monitor_port" || !strings.Contains(result.CoverageNote, "不保证等于订阅连接端口") {
		t.Fatal("monitoring port was presented as delivered node port")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"password"`, `"secret"`, `"token"`, `"conditions"`, `"extra_nodes"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("sensitive value field %s leaked: %s", forbidden, raw)
		}
	}
}

func TestEntryMonitorInventoryReadsWithoutSchemaInitializationOrTargetRefresh(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	// Unlike the older homepage read, do not pre-mark ensureOnce: every call,
	// including the first, must be SELECT-only and no secret columns are loaded.
	mock.ExpectQuery(`(?s)^SELECT p.id, p.name, p.sort, p.mode,.*FROM v2_client_entry_user_policy p.*ORDER BY p.sort ASC NULLS LAST, p.id ASC$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}))
	mock.ExpectQuery(`(?s)^SELECT m.id, m.policy_id, p.name, p.action, m.enabled,.*FROM v2_client_entry_monitor m.*ORDER BY p.sort ASC NULLS LAST, p.id ASC$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id", "name", "action", "enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "created_at", "updated_at"}))
	mock.ExpectQuery(`^SELECT id, name, last_heartbeat_at\s+FROM v2_dns_probe WHERE enabled = 1 ORDER BY id$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}))
	mock.ExpectQuery(`(?s)^SELECT c.id, c.name, m.policy_id, m.split_group_id.*JOIN v2_client_entry_collection_member.*ORDER BY.*$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "policy_id", "split_group_id"}))
	result, err := service.EntryMonitorInventory(context.Background())
	if err != nil || result.Sources == nil || result.Summary.TotalSources != 0 {
		t.Fatalf("inventory = %#v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	for _, nilService := range []*DBService{nil, {}} {
		if _, err := nilService.EntryMonitorInventory(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("nil service: %v", err)
		}
	}
}

func TestEntryMonitorCollectionReferencesUseTypedPolicyOrLeafIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`(?s)^SELECT c.id, c.name, m.policy_id, m.split_group_id.*`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "policy_id", "split_group_id"}).AddRow(8, "unified", 5, nil).AddRow(8, "unified", nil, 5))
	refs, err := (&DBService{db: db}).loadEntryMonitorCollectionReferences(context.Background())
	if err != nil || len(refs) != 2 || len(refs["policy:5"]) != 1 || len(refs["split_group:5"]) != 1 {
		t.Fatalf("typed collection source keys collide: %#v, %v", refs, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
