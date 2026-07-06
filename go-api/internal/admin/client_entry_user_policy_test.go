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
	rows := sqlmock.NewRows([]string{"id", "server_type", "server_id", "server_name", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(3), "vmess", int64(11), "香港01", int64(1), "VIP", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.server_type, p.server_id, COALESCE\(s.name, ''\) AS server_name, p.enabled, p.remarks, p.created_at, p.updated_at`).
		WillReturnRows(rows)
	emailRows := sqlmock.NewRows([]string{"policy_id", "email"}).
		AddRow(int64(3), "a@example.com").
		AddRow(int64(3), "b@example.com")
	mock.ExpectQuery(`SELECT policy_id, email\s+FROM v2_client_entry_user_policy_user\s+WHERE policy_id IN \(\$1\)`).
		WithArgs(int64(3)).
		WillReturnRows(emailRows)
	entryRows := sqlmock.NewRows([]string{"policy_id", "entry"}).
		AddRow(int64(3), "entry-a.example.com").
		AddRow(int64(3), "entry-b.example.com")
	mock.ExpectQuery(`SELECT policy_id, entry\s+FROM v2_client_entry_user_policy_entry\s+WHERE policy_id IN \(\$1\)`).
		WithArgs(int64(3)).
		WillReturnRows(entryRows)

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) != 1 || policies[0].ServerName != "香港01" {
		t.Fatalf("unexpected policies: %#v", policies)
	}
	if len(policies[0].Emails) != 2 || policies[0].Emails[0] != "a@example.com" || policies[0].Emails[1] != "b@example.com" {
		t.Fatalf("expected one rule with emails, got %#v", policies[0].Emails)
	}
	if len(policies[0].Entries) != 2 || policies[0].Entries[0] != "entry-a.example.com" || policies[0].Entries[1] != "entry-b.example.com" {
		t.Fatalf("expected one rule with entries, got %#v", policies[0].Entries)
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
		WithArgs("vmess", int64(11), int64(1), "VIP", sqlmock.AnyArg(), sqlmock.AnyArg()).
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
	mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_entry WHERE policy_id = \$1`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_entry`).
		WithArgs(int64(9), "entry-a.example.com", int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_entry`).
		WithArgs(int64(9), "entry-b.example.com", int64(2), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ok, err := service.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{
		Emails: []string{" A@Example.com ", "b@example.com", "a@example.com"}, Entries: []string{"entry-a.example.com", "entry-b.example.com"}, ServerType: "vmess", ServerID: 11, Enabled: ptrInt64ForClientEntryPolicyTest(1), Remarks: "VIP",
	})
	if err != nil || !ok {
		t.Fatalf("save policy: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func ptrInt64ForClientEntryPolicyTest(value int64) *int64 { return &value }
