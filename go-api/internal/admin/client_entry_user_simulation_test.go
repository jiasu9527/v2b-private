package admin

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/cliententry"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSimulateClientEntryUserPolicyReadsPersistedAssignment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	mock.ExpectQuery(`SELECT id, email, plan_id, created_at\s+FROM v2_user\s+WHERE LOWER\(email\) = LOWER\(\$1\)`).
		WithArgs("demo@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "plan_id", "created_at"}).AddRow(int64(1001), "Demo@Example.com", int64(2), time.Now().Add(-48*time.Hour).Unix()))
	policyRows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(9), "父规则", int64(10), "split", int64(100), int64(3700), "override", `[]`, "", int64(1), `[]`, "after", int64(1), "", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to`).WillReturnRows(policyRows)
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort\s+FROM v2_client_entry_user_policy_member`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}).AddRow(int64(9), "vmess", int64(11), int64(10)))
	groupRows := sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "global_sort", "user_count", "is_leaf", "created_at", "updated_at"}).
		AddRow(int64(201), int64(9), nil, "独立规则 C", "C", "split-c.example.com", int64(10), int64(10), int64(8), true, int64(100), int64(200))
	mock.ExpectQuery(`(?s)SELECT split_group.id, split_group.policy_id.*FROM v2_client_entry_user_policy_split_group split_group.*WHERE split_group.policy_id IN \(\$1\)`).
		WithArgs(int64(9)).WillReturnRows(groupRows)
	mock.ExpectQuery(`SELECT policy_id, group_id\s+FROM v2_client_entry_user_policy_split_assignment\s+WHERE user_id = \$1`).
		WithArgs(int64(1001)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "group_id"}).AddRow(int64(9), int64(201)))

	result, err := service.SimulateClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySimulationRequest{
		Email: " DEMO@example.com ", UA: "ClashMeta/1.0", MemberType: "vmess", MemberID: 11,
	})
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if !result.Found || result.User == nil || result.User.ID != 1001 || result.User.Email != "demo@example.com" || result.User.PlanID != 2 || result.Matched == nil || result.Matched.Name != "独立规则 C" || result.Matched.EntryHost != "split-c.example.com" {
		t.Fatalf("simulation result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSelectClientEntryUserPolicySimulationUsesAssignedSplitLeafInGlobalOrder(t *testing.T) {
	leafSort := int64(10)
	policies := []ClientEntryUserPolicyRecord{
		{
			ID: 10, Name: "普通规则", Sort: 20, Mode: ClientEntryUserPolicyModeStandard,
			Action: cliententry.ActionOverride, EntryHost: "standard.example.com", Enabled: 1,
			Members: []ClientEntryGroupMemberRecord{{ServerType: "vmess", ServerID: 11}},
		},
		{
			ID: 90, Name: "隐藏父规则", Sort: 100, Mode: ClientEntryUserPolicyModeSplit,
			Action: cliententry.ActionOverride, Enabled: 1,
			Members: []ClientEntryGroupMemberRecord{{ServerType: "vmess", ServerID: 11}},
			SplitGroups: []ClientEntryUserPolicySplitGroupRecord{
				{ID: 201, PolicyID: 90, Name: "独立规则 C", Path: "C", EntryHost: "split-c.example.com", GlobalSort: &leafSort, UserCount: 8, IsLeaf: true},
			},
		},
	}

	matched := selectClientEntryUserPolicySimulation(policies, map[int64]int64{90: 201}, cliententry.Subject{UserID: 1001}, "vmess", 11)
	if matched == nil || matched.ID != 90 || matched.Name != "独立规则 C" || matched.EntryHost != "split-c.example.com" || matched.SnapshotUserCount != 8 || matched.SplitGroups != nil {
		t.Fatalf("assigned split match = %#v", matched)
	}
}

func TestSelectClientEntryUserPolicySimulationSkipsUnassignedAndUnrelatedNodes(t *testing.T) {
	leafSort := int64(10)
	policies := []ClientEntryUserPolicyRecord{
		{
			ID: 90, Sort: 100, Mode: ClientEntryUserPolicyModeSplit, Action: cliententry.ActionOverride, Enabled: 1,
			Members:     []ClientEntryGroupMemberRecord{{ServerType: "vmess", ServerID: 11}},
			SplitGroups: []ClientEntryUserPolicySplitGroupRecord{{ID: 201, PolicyID: 90, Name: "独立规则 C", EntryHost: "split-c.example.com", GlobalSort: &leafSort, IsLeaf: true}},
		},
		{
			ID: 10, Name: "Trojan 普通规则", Sort: 20, Mode: ClientEntryUserPolicyModeStandard,
			Action: cliententry.ActionOverride, EntryHost: "trojan.example.com", Enabled: 1,
			Members: []ClientEntryGroupMemberRecord{{ServerType: "trojan", ServerID: 12}},
		},
	}

	if matched := selectClientEntryUserPolicySimulation(policies, nil, cliententry.Subject{UserID: 1001}, "vmess", 11); matched != nil {
		t.Fatalf("unassigned split policy matched: %#v", matched)
	}
	matched := selectClientEntryUserPolicySimulation(policies, nil, cliententry.Subject{UserID: 1001}, "trojan", 12)
	if matched == nil || matched.ID != 10 {
		t.Fatalf("selected standard node match = %#v", matched)
	}
}
