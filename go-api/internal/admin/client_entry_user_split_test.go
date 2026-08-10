package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectShiftClientEntryVisibleSortsAfter(mock sqlmock.Sqlmock, position, delta int64) {
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy\s+SET sort = sort \+ \$2\s+WHERE mode <> 'split' AND sort > \$1`).
		WithArgs(position, delta).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy_split_group\s+SET global_sort = global_sort \+ \$2\s+WHERE global_sort IS NOT NULL AND global_sort > \$1`).
		WithArgs(position, delta).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectSyncClientEntrySplitPolicySorts(mock)
}

func expectSyncClientEntrySplitPolicySorts(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy policy\s+SET sort = positions\.global_sort::INTEGER\s+FROM \(.*MIN\(split_group\.global_sort\).*\) positions\s+WHERE policy\.id = positions\.policy_id AND policy\.mode = 'split'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

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
	expectNextClientEntryVisibleSort(mock, 0)
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy\s+\(name, sort, mode, action, conditions, entry_host, resolve_entry_host, extra_nodes, extra_nodes_position, snapshot_from, snapshot_to, enabled, remarks, created_at, updated_at\)`).
		WithArgs("最近活跃用户", int64(10), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), "排查", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "vmess", int64(11), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "A", "A", "a.example.com", int64(10), int64(10), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "B", "B", "b.example.com", int64(20), int64(20), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
	mock.ExpectQuery(`(?s)WITH eligible AS \(.*ROW_NUMBER\(\) OVER \(ORDER BY activity.user_id ASC\).*INSERT INTO v2_client_entry_user_policy_split_assignment.*CASE WHEN position <= \(total \+ 1\) / 2 THEN \$4::BIGINT ELSE \$5::BIGINT END.*RETURNING group_id`).
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
	expectNextClientEntryVisibleSort(mock, 0)
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
	expectNextClientEntryVisibleSort(mock, 0)
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
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes, sort\s+FROM v2_client_entry_user_policy\s+WHERE id = \$1 AND mode = 'standard'\s+FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes", "sort"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, `[]`, int64(30)))
	expectShiftClientEntryVisibleSortsAfter(mock, 30, 10)
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "A", "A", "a.example.com", int64(10), int64(30), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "B", "B", "b.example.com", int64(20), int64(40), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
	mock.ExpectQuery(`(?s)WITH eligible AS \(.*FROM v2_user users.*WHERE users.id BETWEEN \$1 AND \$2.*INSERT INTO v2_client_entry_user_policy_split_assignment.*CASE WHEN position <= \(total \+ 1\) / 2 THEN \$4::BIGINT ELSE \$5::BIGINT END.*RETURNING group_id`).
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
	if record.SplitGroups[0].GlobalSort == nil || *record.SplitGroups[0].GlobalSort != 30 || record.SplitGroups[1].GlobalSort == nil || *record.SplitGroups[1].GlobalSort != 40 {
		t.Fatalf("converted children did not inherit the original row position: %#v", record.SplitGroups)
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
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes, sort`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes", "sort"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200},{"field":"ua","operator":"contains_any","values":["Clash"]}]`, `[]`, int64(30)))
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
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes, sort`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes", "sort"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, `["trojan://secret@example.com:443"]`, int64(30)))
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
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`SELECT action, conditions, extra_nodes, sort`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes", "sort"}).
			AddRow("override", `[{"field":"user_id","operator":"between","min":100,"max":200}]`, `[]`, int64(30)))
	expectShiftClientEntryVisibleSortsAfter(mock, 30, 10)
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "A", "A", "a.example.com", int64(10), int64(30), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), nil, "B", "B", "b.example.com", int64(20), int64(40), sqlmock.AnyArg()).
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
	expectClientEntryVisibleOrderLock(mock)
	mock.ExpectQuery(`(?s)SELECT split_group.name, split_group.path, split_group.global_sort.*policy.mode = 'split'.*NOT EXISTS.*FOR UPDATE OF split_group`).
		WithArgs(int64(101), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "path", "global_sort"}).AddRow("A", "A", int64(30)))
	mock.ExpectQuery(`SELECT user_id\s+FROM v2_client_entry_user_policy_split_assignment.*FOR UPDATE`).
		WithArgs(int64(9), int64(101)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)).AddRow(int64(4)).AddRow(int64(5)))
	expectShiftClientEntryVisibleSortsAfter(mock, 30, 10)
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), int64(101), "A.1", "A.1", "a1.example.com", int64(10), int64(30), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(201)))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
		WithArgs(int64(9), int64(101), "A.2", "A.2", "a2.example.com", int64(20), int64(40), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(202)))
	mock.ExpectExec(`(?s)WITH ranked AS .*UPDATE v2_client_entry_user_policy_split_assignment assignment.*SET group_id = CASE WHEN ranked.position <= \$3 THEN \$4::BIGINT ELSE \$5::BIGINT END`).
		WithArgs(int64(9), int64(101), int64(3), int64(201), int64(202), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy_split_group\s+SET entry_host = '', global_sort = NULL, updated_at = \$2`).
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

func TestSortClientEntryUserPolicySplitGroupsRequiresExactLeafSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_user_policy\s+WHERE id = \$1 AND mode = 'split'\s+FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectQuery(`(?s)SELECT split_group.id.*FROM v2_client_entry_user_policy_split_group split_group.*split_group.policy_id = \$1.*NOT EXISTS.*ORDER BY split_group.id ASC.*FOR UPDATE OF split_group`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(201)).AddRow(int64(202)))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group split_group.*SET sort = \$3, updated_at = \$4.*split_group.id = \$1 AND split_group.policy_id = \$2.*NOT EXISTS`).
		WithArgs(int64(202), int64(9), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE v2_client_entry_user_policy_split_group split_group.*SET sort = \$3, updated_at = \$4.*split_group.id = \$1 AND split_group.policy_id = \$2.*NOT EXISTS`).
		WithArgs(int64(201), int64(9), int64(20), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET updated_at = \$2 WHERE id = \$1 AND mode = 'split'`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ok, err := service.SortClientEntryUserPolicySplitGroups(context.Background(), ClientEntryUserPolicyGroupSortRequest{
		PolicyID: 9,
		IDs:      []int64{202, 201},
	})
	if err != nil || !ok {
		t.Fatalf("sort split groups: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSortClientEntryUserPolicyRowsRejectsHiddenSplitContainer(t *testing.T) {
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
			AddRow(int64(3), "standard").
			AddRow(int64(9), "split"))
	mock.ExpectQuery(`(?s)SELECT split_group.id\s+FROM v2_client_entry_user_policy_split_group split_group.*policy.mode = 'split'.*NOT EXISTS.*ORDER BY split_group.id ASC\s+FOR UPDATE OF split_group`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(201)).AddRow(int64(202)))
	mock.ExpectRollback()

	ok, err := service.SortClientEntryUserPolicyRows(context.Background(), []ClientEntryUserPolicySortItem{
		{Kind: "policy", ID: 3},
		{Kind: "policy", ID: 9},
		{Kind: "split_group", ID: 201},
	})
	if err == nil || ok || !strings.Contains(err.Error(), "规则列表已变化") {
		t.Fatalf("hidden split container must not be sortable: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMoveClientEntryUserPolicySplitGroupToRootRenamesAndCleansEmptyParent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_user_policy\s+WHERE id = \$1 AND mode = 'split'\s+FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectQuery(`SELECT id, parent_id, path, sort\s+FROM v2_client_entry_user_policy_split_group\s+WHERE policy_id = \$1\s+ORDER BY id ASC\s+FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "parent_id", "path", "sort"}).
			AddRow(int64(101), nil, "A", int64(10)).
			AddRow(int64(102), nil, "B", int64(20)).
			AddRow(int64(202), int64(101), "A.1", int64(10)))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy_split_group\s+SET parent_id = NULL, name = \$3, path = \$3, sort = \$4, updated_at = \$5\s+WHERE id = \$1 AND policy_id = \$2`).
		WithArgs(int64(202), int64(9), "C", int64(30), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM v2_client_entry_user_policy_split_assignment WHERE policy_id = \$1 AND group_id = \$2`).
		WithArgs(int64(9), int64(101)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_split_group WHERE id = \$1 AND policy_id = \$2`).
		WithArgs(int64(101), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET updated_at = \$2 WHERE id = \$1 AND mode = 'split'`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	ok, err := service.MoveClientEntryUserPolicySplitGroupToRoot(context.Background(), ClientEntryUserPolicyGroupMoveRequest{PolicyID: 9, GroupID: 202})
	if err != nil || !ok {
		t.Fatalf("move split group to root: ok=%v err=%v", ok, err)
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
	groupRows := sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "global_sort", "user_count", "is_leaf", "created_at", "updated_at"}).
		AddRow(int64(101), int64(9), nil, "A", "A", "a.example.com", int64(10), int64(10), int64(3), true, int64(100), int64(200)).
		AddRow(int64(102), int64(9), nil, "B", "B", "b.example.com", int64(20), int64(20), int64(2), true, int64(100), int64(200))
	mock.ExpectQuery(`(?s)SELECT split_group.id, split_group.policy_id.*FROM v2_client_entry_user_policy_split_group split_group.*WHERE split_group.policy_id IN \(\$1\)`).
		WithArgs(int64(9)).WillReturnRows(groupRows)

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(policies) != 1 || policies[0].Mode != "split" || policies[0].SnapshotFrom == nil || *policies[0].SnapshotFrom != 100 || policies[0].SnapshotUserCount != 5 || len(policies[0].SplitGroups) != 2 {
		t.Fatalf("unexpected split policy: %#v", policies)
	}
	if policies[0].SplitGroups[0].GlobalSort == nil || *policies[0].SplitGroups[0].GlobalSort != 10 || policies[0].SplitGroups[1].GlobalSort == nil || *policies[0].SplitGroups[1].GlobalSort != 20 {
		t.Fatalf("unexpected split global order: %#v", policies[0].SplitGroups)
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
	groupRows := sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "global_sort", "user_count", "is_leaf", "created_at", "updated_at"}).
		AddRow(int64(101), int64(9), nil, "A", "A", "", int64(10), nil, int64(0), false, int64(100), int64(200)).
		AddRow(int64(201), int64(9), int64(101), "A.1", "A.1", "a1.example.com", int64(10), int64(10), int64(3), true, int64(100), int64(200)).
		AddRow(int64(202), int64(9), int64(101), "A.2", "A.2", "a2.example.com", int64(20), int64(20), int64(2), true, int64(100), int64(200))
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
