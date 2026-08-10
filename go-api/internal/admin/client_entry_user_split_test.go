package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPreviewClientEntryUserPolicySplitUsesDefaultHour(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectQuery(`SELECT COUNT\(\*\)::BIGINT\s+FROM v2_user_subscribe_activity activity\s+JOIN v2_user users ON users.id = activity.user_id\s+WHERE activity.last_subscribe_at BETWEEN \$1 AND \$2`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(7)))

	result, err := service.PreviewClientEntryUserPolicySplit(context.Background(), ClientEntryUserPolicySplitPreviewRequest{})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if result.Minutes != 60 || result.To-result.From != 3600 || result.UserCount != 7 {
		t.Fatalf("unexpected preview: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCreateClientEntryUserPolicySplitSnapshotsAndBalancesUsers(t *testing.T) {
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
	mock.ExpectQuery(`SELECT sort FROM v2_client_entry_user_policy\s+ORDER BY sort DESC NULLS LAST, id DESC\s+LIMIT 1\s+FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"sort"}))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy\s+\(name, sort, mode, action, conditions, entry_host, resolve_entry_host, extra_nodes, extra_nodes_position, snapshot_from, snapshot_to, enabled, remarks, created_at, updated_at\)`).
		WithArgs("最近活跃用户", int64(10), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), "排查", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "vmess", int64(11), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "A", "A", "a.example.com", int64(10), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "B", "B", "b.example.com", int64(20), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
	mock.ExpectQuery(`(?s)WITH eligible AS \(.*ROW_NUMBER\(\) OVER \(ORDER BY activity.user_id ASC\).*INSERT INTO v2_client_entry_user_policy_split_assignment.*RETURNING group_id`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(9), int64(101), int64(102)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(101)).AddRow(int64(101)).AddRow(int64(101)).AddRow(int64(102)).AddRow(int64(102)))
	mock.ExpectCommit()

	record, err := service.CreateClientEntryUserPolicySplit(context.Background(), ClientEntryUserPolicySplitCreateRequest{
		Name:             " 最近活跃用户 ",
		Minutes:          60,
		Members:          []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}},
		EntryHostA:       "A.EXAMPLE.COM",
		EntryHostB:       "B.EXAMPLE.COM",
		ResolveEntryHost: ptrInt64ForClientEntryPolicyTest(1),
		Enabled:          ptrInt64ForClientEntryPolicyTest(1),
		Remarks:          "排查",
	})
	if err != nil || record.ID != 9 || record.Mode != ClientEntryUserPolicyModeSplit || record.SnapshotUserCount != 5 {
		t.Fatalf("create split: record=%#v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCreateClientEntryUserPolicySplitRejectsSnapshotWithOneUser(t *testing.T) {
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
	mock.ExpectQuery(`SELECT sort FROM v2_client_entry_user_policy`).
		WillReturnRows(sqlmock.NewRows([]string{"sort"}))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
	mock.ExpectQuery(`(?s)WITH eligible AS .*RETURNING group_id`).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(101)))
	mock.ExpectRollback()

	record, err := service.CreateClientEntryUserPolicySplit(context.Background(), ClientEntryUserPolicySplitCreateRequest{
		Name: "活跃用户", Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}}, EntryHostA: "a.example.com", EntryHostB: "b.example.com",
	})
	if err == nil || record.ID != 0 {
		t.Fatalf("create split: record=%#v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCreateClientEntryUserPolicySplitReportsDatabaseStage(t *testing.T) {
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
	mock.ExpectQuery(`SELECT sort FROM v2_client_entry_user_policy`).
		WillReturnRows(sqlmock.NewRows([]string{"sort"}))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy`).
		WillReturnError(errors.New(`null value in column "server_type" violates not-null constraint`))
	mock.ExpectRollback()

	_, err = service.CreateClientEntryUserPolicySplit(context.Background(), ClientEntryUserPolicySplitCreateRequest{
		Name: "活跃用户", Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}}, EntryHostA: "a.example.com", EntryHostB: "b.example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "写入规则") || !strings.Contains(err.Error(), "server_type") {
		t.Fatalf("expected staged database error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConvertClientEntryUserPolicyToSplitKeepsOnePolicyAndBalancesCurrentUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes\s+FROM v2_client_entry_user_policy\s+WHERE id = \$1 AND mode = 'standard'\s+FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, `[]`))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "A", "A", "a.example.com", int64(10), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "B", "B", "b.example.com", int64(20), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
	mock.ExpectQuery(`(?s)WITH eligible AS \(.*FROM v2_user users.*WHERE users.id BETWEEN \$1 AND \$2.*INSERT INTO v2_client_entry_user_policy_split_assignment.*RETURNING group_id`).
		WithArgs(int64(100), int64(200), int64(9), int64(101), int64(102), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).
			AddRow(int64(101)).AddRow(int64(101)).AddRow(int64(101)).AddRow(int64(102)).AddRow(int64(102)))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy\s+SET mode = 'split', entry_host = '', extra_nodes = '\[\]', snapshot_from = NULL, snapshot_to = NULL, updated_at = \$2\s+WHERE id = \$1 AND mode = 'standard'`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	record, err := service.ConvertClientEntryUserPolicyToSplit(context.Background(), ClientEntryUserPolicySplitConvertRequest{
		PolicyID: 9, EntryHostA: "A.EXAMPLE.COM", EntryHostB: "B.EXAMPLE.COM",
	})
	if err != nil || record.ID != 9 || record.Mode != ClientEntryUserPolicyModeSplit || record.SnapshotUserCount != 5 || len(record.Conditions) != 1 || len(record.SplitGroups) != 2 || record.SplitGroups[0].UserCount != 3 || record.SplitGroups[1].UserCount != 2 {
		t.Fatalf("convert split: record=%#v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConvertClientEntryUserPolicyToSplitRejectsAdditionalCondition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200},{"field":"ua","operator":"contains_any","values":["Clash"]}]`, `[]`))
	mock.ExpectRollback()

	if _, err := service.ConvertClientEntryUserPolicyToSplit(context.Background(), ClientEntryUserPolicySplitConvertRequest{
		PolicyID: 9, EntryHostA: "a.example.com", EntryHostB: "b.example.com",
	}); err == nil {
		t.Fatal("expected rule with an additional condition to be rejected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConvertClientEntryUserPolicyToSplitRejectsExtraNodes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, `["trojan://secret@example.com:443"]`))
	mock.ExpectRollback()

	if _, err := service.ConvertClientEntryUserPolicyToSplit(context.Background(), ClientEntryUserPolicySplitConvertRequest{
		PolicyID: 9, EntryHostA: "a.example.com", EntryHostB: "b.example.com",
	}); err == nil {
		t.Fatal("expected rule with extra nodes to be rejected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConvertClientEntryUserPolicyToSplitRollsBackWhenRangeHasOneUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, `[]`))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
	mock.ExpectQuery(`(?s)WITH eligible AS .*RETURNING group_id`).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(101)))
	mock.ExpectRollback()

	if _, err := service.ConvertClientEntryUserPolicyToSplit(context.Background(), ClientEntryUserPolicySplitConvertRequest{
		PolicyID: 9, EntryHostA: "a.example.com", EntryHostB: "b.example.com",
	}); err == nil {
		t.Fatal("expected a one-user range to be rejected")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSplitClientEntryUserPolicyGroupKeepsParentAndMovesAssignments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT split_group.name, split_group.path.*policy.mode = 'split'.*NOT EXISTS.*FOR UPDATE OF split_group`).
		WithArgs(int64(101), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "path"}).AddRow("A", "A"))
	mock.ExpectQuery(`SELECT user_id\s+FROM v2_client_entry_user_policy_split_assignment.*FOR UPDATE`).
		WithArgs(int64(9), int64(101)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)).AddRow(int64(4)).AddRow(int64(5)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), int64(101), "A.1", "A.1", "a1.example.com", int64(10), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(201)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), int64(101), "A.2", "A.2", "a2.example.com", int64(20), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(202)))
	mock.ExpectExec(`(?s)WITH ranked AS .*UPDATE v2_client_entry_user_policy_split_assignment assignment.*SET group_id = CASE`).
		WithArgs(int64(9), int64(101), int64(3), int64(201), int64(202), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy_split_group\s+SET entry_host = '', updated_at = \$2`).
		WithArgs(int64(101), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	record, err := service.SplitClientEntryUserPolicyGroup(context.Background(), ClientEntryUserPolicyGroupSplitRequest{
		PolicyID: 9, GroupID: 101, EntryHostA: "a1.example.com", EntryHostB: "a2.example.com",
	})
	if err != nil || record.ID != 9 || record.Mode != ClientEntryUserPolicyModeSplit {
		t.Fatalf("split group: record=%#v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListClientEntryUserPoliciesIncludesSplitTreeAndSnapshotCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	policyRows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(9), "活跃用户", int64(10), "split", int64(100), int64(3700), "override", `[]`, "", int64(1), `[]`, "after", int64(1), "", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to`).WillReturnRows(policyRows)
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort\s+FROM v2_client_entry_user_policy_member`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}).AddRow(int64(9), "vmess", int64(11), int64(10)))
	groupRows := sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "user_count", "is_leaf", "created_at", "updated_at"}).
		AddRow(int64(101), int64(9), nil, "A", "A", "a.example.com", int64(10), int64(3), true, int64(100), int64(200)).
		AddRow(int64(102), int64(9), nil, "B", "B", "b.example.com", int64(20), int64(2), true, int64(100), int64(200))
	mock.ExpectQuery(`(?s)SELECT split_group.id, split_group.policy_id.*FROM v2_client_entry_user_policy_split_group split_group.*WHERE split_group.policy_id IN \(\$1\)`).
		WithArgs(int64(9)).WillReturnRows(groupRows)

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(policies) != 1 || policies[0].Mode != "split" || policies[0].SnapshotFrom == nil || *policies[0].SnapshotFrom != 100 || policies[0].SnapshotUserCount != 5 || len(policies[0].SplitGroups) != 2 {
		t.Fatalf("unexpected split policy: %#v", policies)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestResolveClientEntryMonitorPoliciesIncludesEverySplitLeaf(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	policyRows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(9), "活跃用户", int64(10), "split", int64(100), int64(3700), "override", `[]`, "", int64(0), `[]`, "after", int64(1), "", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to`).WillReturnRows(policyRows)
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort\s+FROM v2_client_entry_user_policy_member`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}).AddRow(int64(9), "vmess", int64(11), int64(10)))
	groupRows := sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "user_count", "is_leaf", "created_at", "updated_at"}).
		AddRow(int64(101), int64(9), nil, "A", "A", "", int64(10), int64(0), false, int64(100), int64(200)).
		AddRow(int64(201), int64(9), int64(101), "A.1", "A.1", "a1.example.com", int64(10), int64(3), true, int64(100), int64(200)).
		AddRow(int64(202), int64(9), int64(101), "A.2", "A.2", "a2.example.com", int64(20), int64(2), true, int64(100), int64(200))
	mock.ExpectQuery(`(?s)SELECT split_group.id, split_group.policy_id.*FROM v2_client_entry_user_policy_split_group split_group.*WHERE split_group.policy_id IN \(\$1\)`).
		WithArgs(int64(9)).WillReturnRows(groupRows)

	policies, err := service.resolveClientEntryMonitorPolicies(context.Background())
	if err != nil {
		t.Fatalf("resolve monitor policies: %v", err)
	}
	if len(policies) != 1 || len(policies[0].Targets) != 2 {
		t.Fatalf("unexpected monitor policies: %#v", policies)
	}
	if policies[0].Targets[0].SourceKey != "policy:9:split-group:201" || policies[0].Targets[0].Host != "a1.example.com" || policies[0].Targets[1].SourceKey != "policy:9:split-group:202" || policies[0].Targets[1].Host != "a2.example.com" {
		t.Fatalf("unexpected split targets: %#v", policies[0].Targets)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUpdateClientEntryUserPolicySplitGroupHostOnlyUpdatesLeaf(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group split_group.*NOT EXISTS.*policy.mode = 'split'`).
		WithArgs(int64(101), int64(9), "new.example.com", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET updated_at = \$2 WHERE id = \$1 AND mode = 'split'`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	record, err := service.UpdateClientEntryUserPolicySplitGroupHost(context.Background(), ClientEntryUserPolicyGroupHostUpdateRequest{
		PolicyID: 9, GroupID: 101, EntryHost: "NEW.EXAMPLE.COM",
	})
	if err != nil || record.ID != 9 || record.Mode != ClientEntryUserPolicyModeSplit {
		t.Fatalf("update host: record=%#v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSetClientEntryUserPolicyEnabledValidatesAndPersistsValue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET enabled = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(9), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ok, err := service.SetClientEntryUserPolicyEnabled(context.Background(), 9, 0)
	if err != nil || !ok {
		t.Fatalf("set enabled: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}

	if _, err := service.SetClientEntryUserPolicyEnabled(context.Background(), 9, 2); err == nil {
		t.Fatal("expected invalid enabled value to be rejected")
	}
}

func TestNormalizeClientEntryUserPolicySplitRejectsSameHosts(t *testing.T) {
	_, err := normalizeClientEntryUserPolicySplitCreateRequest(ClientEntryUserPolicySplitCreateRequest{
		Name: "活跃用户", Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}}, EntryHostA: "ENTRY.example.com", EntryHostB: "entry.example.com",
	})
	if err == nil {
		t.Fatal("expected identical group hosts to be rejected")
	}
}
