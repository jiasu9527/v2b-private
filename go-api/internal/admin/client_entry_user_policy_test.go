package admin

import (
	"context"
	"encoding/json"
	"testing"

	"forest/go-api/internal/cliententry"

	"github.com/DATA-DOG/go-sqlmock"
)

func readyClientEntrySchemaForPolicyTest(service *DBService) {
	service.clientEntryEnsureOnce.Do(func() {})
}

func expectClientEntryVisibleOrderLock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(clientEntryVisibleOrderLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectNextClientEntryVisibleSort(mock sqlmock.Sqlmock, last int64) {
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`(?s)SELECT COALESCE\(MAX\(visible_sort\), 0\)::BIGINT.*v2_client_entry_user_policy_split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(last))
}

func TestDBServiceListClientEntryUserPoliciesReturnsRulesInStoredOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	rows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(3), "Clash", int64(10), "standard", nil, nil, "override", `[{"field":"ua","operator":"contains_any","values":["Clash"]}]`, "vip-entry.example.com", int64(1), `["trojan://secret@extra.example.com:443#Extra"]`, "before", int64(1), "VIP", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to, p.action, p.conditions, p.entry_host, p.resolve_entry_host, p.extra_nodes, p.extra_nodes_position, p.enabled, p.remarks, p.created_at, p.updated_at\s+FROM v2_client_entry_user_policy p\s+ORDER BY p.sort ASC NULLS LAST, p.id ASC`).
		WillReturnRows(rows)
	memberRows := sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}).
		AddRow(int64(3), "vmess", int64(11), int64(10)).
		AddRow(int64(3), "trojan", int64(12), int64(20))
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort\s+FROM v2_client_entry_user_policy_member\s+WHERE policy_id IN \(\$1\)`).
		WithArgs(int64(3)).
		WillReturnRows(memberRows)

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) != 1 || policies[0].Name != "Clash" || policies[0].Sort != 10 || policies[0].Action != "override" {
		t.Fatalf("unexpected policies: %#v", policies)
	}
	if len(policies[0].Conditions) != 1 || policies[0].Conditions[0].Operator != "contains_any" {
		t.Fatalf("unexpected conditions: %#v", policies[0].Conditions)
	}
	if len(policies[0].Members) != 2 || policies[0].Members[0].ServerType != "vmess" || policies[0].Members[1].ServerID != 12 {
		t.Fatalf("unexpected selected nodes: %#v", policies[0].Members)
	}
	if len(policies[0].ExtraNodes) != 1 || policies[0].ExtraNodes[0] != "trojan://secret@extra.example.com:443#Extra" {
		t.Fatalf("unexpected extra nodes: %#v", policies[0].ExtraNodes)
	}
	if policies[0].ExtraNodesPosition != "before" {
		t.Fatalf("unexpected extra node position: %q", policies[0].ExtraNodesPosition)
	}
	if policies[0].ResolveEntryHost != 1 {
		t.Fatalf("unexpected resolve entry host setting: %d", policies[0].ResolveEntryHost)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceListClientEntryUserPoliciesCountsActualUsersInIDRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	rows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(4), "ID range", int64(10), "standard", nil, nil, "original", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, "", int64(0), `[]`, "after", int64(1), "", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to, p.action, p.conditions, p.entry_host, p.resolve_entry_host, p.extra_nodes, p.extra_nodes_position, p.enabled, p.remarks, p.created_at, p.updated_at\s+FROM v2_client_entry_user_policy p\s+ORDER BY p.sort ASC NULLS LAST, p.id ASC`).
		WillReturnRows(rows)
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort\s+FROM v2_client_entry_user_policy_member\s+WHERE policy_id IN \(\$1\)`).
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}))
	mock.ExpectQuery(`WITH ranges\(policy_id, min_id, max_id\) AS \(VALUES \(\$1::BIGINT, \$2::BIGINT, \$3::BIGINT\)\)\s+SELECT ranges.policy_id, COUNT\(users.id\)::BIGINT\s+FROM ranges\s+LEFT JOIN v2_user users ON users.id BETWEEN ranges.min_id AND ranges.max_id\s+GROUP BY ranges.policy_id`).
		WithArgs(int64(4), int64(100), int64(200)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "count"}).AddRow(int64(4), int64(73)))

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) != 1 || policies[0].IDRangeUserCount == nil || *policies[0].IDRangeUserCount != 73 {
		t.Fatalf("unexpected ID range count: %#v", policies)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCombinedClientEntryUserIDRangeIntersectsMultipleBounds(t *testing.T) {
	conditions, err := cliententry.NormalizeConditions([]cliententry.Condition{
		{Field: "user_id", Operator: "between", Min: json.RawMessage("10"), Max: json.RawMessage("100")},
		{Field: "user_id", Operator: "between", Min: json.RawMessage("40"), Max: json.RawMessage("80")},
		{Field: "plan_id", Operator: "between", Min: json.RawMessage("1"), Max: json.RawMessage("9")},
	})
	if err != nil {
		t.Fatalf("normalize conditions: %v", err)
	}
	minimum, maximum, ok := combinedClientEntryUserIDRange(conditions)
	if !ok || minimum != 40 || maximum != 80 {
		t.Fatalf("combined range = %d..%d ok=%v", minimum, maximum, ok)
	}
}

func TestDBServiceSaveClientEntryUserPolicyCreatesStructuredRule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM v2_user WHERE id = \$1\)`).
		WithArgs(int64(1001)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM "v2_server_vmess" WHERE id = \$1\)`).
		WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	expectNextClientEntryVisibleSort(mock, 0)
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy`).
		WithArgs("VIP Clash", int64(10), "override", `[{"field":"user_id","operator":"in","values":[1001]}]`, "vip-entry.example.com", int64(1), `["trojan://secret@extra.example.com:443#Extra"]`, "before", int64(1), "VIP", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "vmess", int64(11), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ok, err := service.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{
		Name: "VIP Clash", Action: "override", EntryHost: "VIP-ENTRY.example.com", ResolveEntryHost: ptrInt64ForClientEntryPolicyTest(1), Enabled: ptrInt64ForClientEntryPolicyTest(1), Remarks: "VIP",
		Conditions:         []cliententry.Condition{{Field: "user_id", Operator: "in", Values: []json.RawMessage{json.RawMessage("1001")}}},
		Members:            []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}},
		ExtraNodes:         []string{"trojan://secret@extra.example.com:443#Extra"},
		ExtraNodesPosition: "before",
	})
	if err != nil || !ok {
		t.Fatalf("save policy: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSaveClientEntryUserPolicyUpdatesResolveEntryHost(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM "v2_server_vmess" WHERE id = \$1\)`).
		WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy\s+SET name = \$2, action = \$3, conditions = \$4, entry_host = \$5, resolve_entry_host = \$6, extra_nodes = \$7, extra_nodes_position = \$8, enabled = \$9, remarks = \$10, updated_at = \$11\s+WHERE id = \$1 AND mode = 'standard'`).
		WithArgs(int64(9), "DNS entry", "override", `[]`, "entry.example.com", int64(1), `[]`, "after", int64(1), "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "vmess", int64(11), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ok, err := service.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{
		ID: ptrInt64ForClientEntryPolicyTest(9), Name: "DNS entry", Action: "override", EntryHost: "entry.example.com", ResolveEntryHost: ptrInt64ForClientEntryPolicyTest(1),
		Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}},
	})
	if err != nil || !ok {
		t.Fatalf("update policy: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSaveClientEntryUserPolicyRejectsInvalidActionBeforeDatabase(t *testing.T) {
	_, err := normalizeClientEntryUserPolicySaveRequest(ClientEntryUserPolicySaveRequest{
		Name: "bad", Action: "priority", EntryHost: "entry.example.com", Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 1}},
	})
	if err == nil {
		t.Fatal("expected invalid action to be rejected")
	}
}

func TestNormalizeClientEntryUserPolicyAllowsOriginalAddressWithoutEntryHost(t *testing.T) {
	prepared, err := normalizeClientEntryUserPolicySaveRequest(ClientEntryUserPolicySaveRequest{
		Name:   "指定用户原入口",
		Action: cliententry.ActionOriginal,
		Members: []ClientEntryGroupMemberSaveRequest{
			{ServerType: "vmess", ServerID: 11},
			{ServerType: "trojan", ServerID: 12},
		},
	})
	if err != nil {
		t.Fatalf("normalize original-address rule: %v", err)
	}
	if prepared.Action != cliententry.ActionOriginal || prepared.EntryHost != "" || prepared.ResolveEntryHost != 0 || len(prepared.Members) != 2 {
		t.Fatalf("unexpected original-address rule: %#v", prepared)
	}
}

func TestNormalizeClientEntryUserPolicyForcesResolveEntryHostOffOutsideOverride(t *testing.T) {
	prepared, err := normalizeClientEntryUserPolicySaveRequest(ClientEntryUserPolicySaveRequest{
		Name: "指定用户原入口", Action: cliententry.ActionOriginal, ResolveEntryHost: ptrInt64ForClientEntryPolicyTest(1),
		Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}},
	})
	if err != nil {
		t.Fatalf("normalize original-address rule: %v", err)
	}
	if prepared.ResolveEntryHost != 0 {
		t.Fatalf("resolve_entry_host = %d, want 0", prepared.ResolveEntryHost)
	}
}

func TestNormalizeClientEntryUserPolicyRejectsInvalidResolveEntryHost(t *testing.T) {
	_, err := normalizeClientEntryUserPolicySaveRequest(ClientEntryUserPolicySaveRequest{
		Name: "解析入口", Action: cliententry.ActionOverride, EntryHost: "entry.example.com", ResolveEntryHost: ptrInt64ForClientEntryPolicyTest(2),
		Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}},
	})
	if err == nil {
		t.Fatal("expected invalid resolve_entry_host to be rejected")
	}
}

func TestNormalizeClientEntryUserPolicyRejectsEntryHostForOriginalAddress(t *testing.T) {
	_, err := normalizeClientEntryUserPolicySaveRequest(ClientEntryUserPolicySaveRequest{
		Name: "指定用户原入口", Action: cliententry.ActionOriginal, EntryHost: "unexpected.example.com",
		Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}},
	})
	if err == nil {
		t.Fatal("expected original-address rule with entry_host to be rejected")
	}
}

func TestDBServiceSortClientEntryUserPoliciesRequiresExactRuleSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_user_policy FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(8)).AddRow(int64(3)))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET sort = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(3), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET sort = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(8), int64(20), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ok, err := service.SortClientEntryUserPolicies(context.Background(), []int64{3, 8})
	if err != nil || !ok {
		t.Fatalf("sort rules: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSortClientEntryUserPolicyRowsMixesStandardPoliciesAndSplitLeaves(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`SELECT id, mode FROM v2_client_entry_user_policy ORDER BY id ASC FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "mode"}).
			AddRow(int64(3), ClientEntryUserPolicyModeStandard).
			AddRow(int64(7), ClientEntryUserPolicyModeStandard).
			AddRow(int64(9), ClientEntryUserPolicyModeSplit))
	mock.ExpectQuery(`(?s)SELECT split_group\.id\s+FROM v2_client_entry_user_policy_split_group split_group.*WHERE policy\.mode = 'split'.*NOT EXISTS \(.*child\.parent_id = split_group\.id.*\).*FOR UPDATE OF split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(201)).AddRow(int64(202)))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy\s+SET sort = \$2, updated_at = \$3\s+WHERE id = \$1 AND mode <> 'split'`).
		WithArgs(int64(7), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group split_group\s+SET global_sort = \$2, updated_at = \$3.*WHERE split_group\.id = \$1.*policy\.mode = 'split'.*NOT EXISTS`).
		WithArgs(int64(202), int64(20), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy\s+SET sort = \$2, updated_at = \$3\s+WHERE id = \$1 AND mode <> 'split'`).
		WithArgs(int64(3), int64(30), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group split_group\s+SET global_sort = \$2, updated_at = \$3.*WHERE split_group\.id = \$1.*policy\.mode = 'split'.*NOT EXISTS`).
		WithArgs(int64(201), int64(40), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The split container is not a visible row of its own. Its compatibility
	// sort must track the earliest leaf after this mixed global reorder (20).
	expectSyncClientEntrySplitPolicySorts(mock)
	mock.ExpectCommit()

	ok, err := service.SortClientEntryUserPolicyRows(context.Background(), []ClientEntryUserPolicySortItem{
		{Kind: "policy", ID: 7},
		{Kind: "split_group", ID: 202},
		{Kind: "policy", ID: 3},
		{Kind: "split_group", ID: 201},
	})
	if err != nil || !ok {
		t.Fatalf("sort mixed visible rows: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestClientEntryRuleConditionsAreEncodedAsStructuredJSON(t *testing.T) {
	encoded, err := cliententry.EncodeConditions([]cliententry.Condition{{
		Field: "user_id", Operator: "in", Values: []json.RawMessage{json.RawMessage("1001")},
	}})
	if err != nil || encoded != `[{"field":"user_id","operator":"in","values":[1001]}]` {
		t.Fatalf("encoded conditions = %q, %v", encoded, err)
	}
}

func ptrInt64ForClientEntryPolicyTest(value int64) *int64 { return &value }
