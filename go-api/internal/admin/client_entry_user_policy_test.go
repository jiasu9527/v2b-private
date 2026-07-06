package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceListClientEntryUserPoliciesReturnsOneRuleWithEmails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	rows := sqlmock.NewRows([]string{"id", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(3), int64(1), "VIP", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.enabled, p.remarks, p.created_at, p.updated_at`).
		WillReturnRows(rows)
	emailRows := sqlmock.NewRows([]string{"policy_id", "email"}).
		AddRow(int64(3), "a@example.com").
		AddRow(int64(3), "b@example.com")
	mock.ExpectQuery(`SELECT policy_id, email\s+FROM v2_client_entry_user_policy_user\s+WHERE policy_id IN \(\$1\)`).
		WithArgs(int64(3)).
		WillReturnRows(emailRows)
	memberRows := sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "server_name"}).
		AddRow(int64(3), "vmess", int64(11), "香港01").
		AddRow(int64(3), "trojan", int64(12), "香港02")
	mock.ExpectQuery(`SELECT m.policy_id, m.server_type, m.server_id, COALESCE\(s.name, ''\) AS server_name`).
		WithArgs(int64(3)).
		WillReturnRows(memberRows)

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) != 1 {
		t.Fatalf("unexpected policies: %#v", policies)
	}
	if len(policies[0].Emails) != 2 || policies[0].Emails[0] != "a@example.com" || policies[0].Emails[1] != "b@example.com" {
		t.Fatalf("expected one rule with emails, got %#v", policies[0].Emails)
	}
	if len(policies[0].Members) != 2 || policies[0].Members[0].ServerType != "vmess" || policies[0].Members[0].ServerID != 11 || policies[0].Members[1].ServerType != "trojan" || policies[0].Members[1].ServerID != 12 {
		t.Fatalf("expected one rule with selected nodes, got %#v", policies[0].Members)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSaveClientEntryUserPolicyCreatesOneRuleWithMultipleEmails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy`).
		WithArgs(int64(1), "VIP", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_user WHERE policy_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_user`).
		WithArgs(int64(9), "a@example.com", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_user`).
		WithArgs(int64(9), "b@example.com", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "vmess", int64(11), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).
		WithArgs(int64(9), "trojan", int64(12), int64(2), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ok, err := service.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{
		Emails: []string{" A@Example.com ", "b@example.com", "a@example.com"}, Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}, {ServerType: "trojan", ServerID: 12}}, Enabled: ptrInt64ForClientEntryPolicyTest(1), Remarks: "VIP",
	})
	if err != nil || !ok {
		t.Fatalf("save policy: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func ptrInt64ForClientEntryPolicyTest(value int64) *int64 { return &value }
