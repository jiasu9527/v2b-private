package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceListClientEntryGroupsIncludesMembers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	groupRows := sqlmock.NewRows([]string{"id", "code", "name", "display_name", "strategy", "hide_member_nodes", "show", "created_at", "updated_at"}).
		AddRow(int64(7), "asia", "Asia", "Asia Entry", "sticky-low-latency", int64(1), int64(1), int64(100), int64(200))
	mock.ExpectQuery(`SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", created_at, updated_at\s+FROM v2_client_entry_group\s+ORDER BY id ASC`).
		WillReturnRows(groupRows)

	memberRows := sqlmock.NewRows([]string{"entry_group_id", "server_type", "server_id", "sort"}).
		AddRow(int64(7), "vmess", int64(11), int64(1)).
		AddRow(int64(7), "trojan", int64(12), int64(2))
	mock.ExpectQuery(`SELECT entry_group_id, server_type, server_id, sort\s+FROM v2_client_entry_group_member\s+WHERE entry_group_id IN \(\$1\)\s+ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(memberRows)

	groups, err := service.ListClientEntryGroups(context.Background(), nil)
	if err != nil {
		t.Fatalf("list client entry groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %#v", groups)
	}
	if groups[0].Code != "asia" || groups[0].DisplayName != "Asia Entry" || groups[0].Strategy != "sticky-low-latency" {
		t.Fatalf("unexpected client entry group: %#v", groups[0])
	}
	if len(groups[0].Members) != 2 || groups[0].Members[0].ServerType != "vmess" || groups[0].Members[1].ServerID != 12 {
		t.Fatalf("unexpected client entry group members: %#v", groups[0].Members)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSaveClientEntryGroupCreatesMembers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	sortA := int64(1)
	sortB := int64(2)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO v2_client_entry_group \(code, name, display_name, strategy, hide_member_nodes, "show", created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, \$7, \$8\)\s+RETURNING id`).
		WithArgs("asia", "Asia", "Asia Entry", "sticky-low-latency", int64(1), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_member \(entry_group_id, server_type, server_id, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6\)`).
		WithArgs(int64(7), "vmess", int64(11), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_member \(entry_group_id, server_type, server_id, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6\)`).
		WithArgs(int64(7), "trojan", int64(12), int64(2), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	saved, err := service.SaveClientEntryGroup(context.Background(), ClientEntryGroupSaveRequest{
		Code:            "asia",
		Name:            "Asia",
		DisplayName:     "Asia Entry",
		Strategy:        "sticky-low-latency",
		HideMemberNodes: true,
		Members: []ClientEntryGroupMemberSaveRequest{
			{ServerType: "vmess", ServerID: int64(11), Sort: &sortA},
			{ServerType: "trojan", ServerID: int64(12), Sort: &sortB},
		},
	})
	if err != nil {
		t.Fatalf("save client entry group: %v", err)
	}
	if !saved {
		t.Fatalf("expected save to succeed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
