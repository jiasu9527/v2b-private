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

func TestDBServiceListClientEntryUserPoliciesReturnsRulesInStoredOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(service)
	rows := sqlmock.NewRows([]string{"id", "name", "sort", "action", "conditions", "entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(3), "Clash", int64(10), "override", `[{"field":"ua","operator":"contains_any","values":["Clash"]}]`, "vip-entry.example.com", `["trojan://secret@extra.example.com:443#Extra"]`, "before", int64(1), "VIP", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.action, p.conditions, p.entry_host, p.extra_nodes, p.extra_nodes_position, p.enabled, p.remarks, p.created_at, p.updated_at\s+FROM v2_client_entry_user_policy p\s+ORDER BY p.sort ASC NULLS LAST, p.id ASC`).
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
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
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
	mock.ExpectQuery(`SELECT sort FROM v2_client_entry_user_policy\s+ORDER BY sort DESC NULLS LAST, id DESC\s+LIMIT 1\s+FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"sort"}))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy`).
		WithArgs("VIP Clash", int64(10), "override", `[{"field":"user_id","operator":"in","values":[1001]}]`, "vip-entry.example.com", `["trojan://secret@extra.example.com:443#Extra"]`, "before", int64(1), "VIP", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "vmess", int64(11), int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ok, err := service.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{
		Name: "VIP Clash", Action: "override", EntryHost: "VIP-ENTRY.example.com", Enabled: ptrInt64ForClientEntryPolicyTest(1), Remarks: "VIP",
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
	if prepared.Action != cliententry.ActionOriginal || prepared.EntryHost != "" || len(prepared.Members) != 2 {
		t.Fatalf("unexpected original-address rule: %#v", prepared)
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

func TestClientEntryRuleConditionsAreEncodedAsStructuredJSON(t *testing.T) {
	encoded, err := cliententry.EncodeConditions([]cliententry.Condition{{
		Field: "user_id", Operator: "in", Values: []json.RawMessage{json.RawMessage("1001")},
	}})
	if err != nil || encoded != `[{"field":"user_id","operator":"in","values":[1001]}]` {
		t.Fatalf("encoded conditions = %q, %v", encoded, err)
	}
}

func ptrInt64ForClientEntryPolicyTest(value int64) *int64 { return &value }
