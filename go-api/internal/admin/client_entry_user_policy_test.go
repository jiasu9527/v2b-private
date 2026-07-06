package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceListClientEntryUserPolicies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	rows := sqlmock.NewRows([]string{"id", "email", "entry_group_id", "entry_group_name", "server_type", "server_id", "server_name", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(3), "user@example.com", int64(7), "香港入口", "vmess", int64(11), "香港01", int64(1), "VIP", int64(100), int64(200))
	mock.ExpectQuery(`SELECT p.id, p.email, p.entry_group_id, g.display_name AS entry_group_name, p.server_type, p.server_id, COALESCE\(s.name, ''\) AS server_name, p.enabled, p.remarks, p.created_at, p.updated_at`).
		WillReturnRows(rows)

	policies, err := service.ListClientEntryUserPolicies(context.Background())
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) != 1 || policies[0].Email != "user@example.com" || policies[0].EntryGroupName != "香港入口" || policies[0].ServerName != "香港01" {
		t.Fatalf("unexpected policies: %#v", policies)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSaveClientEntryUserPolicyCreatesRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy`).
		WithArgs("user@example.com", int64(7), "vmess", int64(11), int64(1), "VIP", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	ok, err := service.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{
		Email: " User@Example.com ", EntryGroupID: 7, ServerType: "vmess", ServerID: 11, Enabled: ptrInt64ForClientEntryPolicyTest(1), Remarks: "VIP",
	})
	if err != nil || !ok {
		t.Fatalf("save policy: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func ptrInt64ForClientEntryPolicyTest(value int64) *int64 { return &value }
