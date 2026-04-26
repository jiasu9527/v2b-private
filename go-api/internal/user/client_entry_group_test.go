package user

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceClientEntryGroupsReturnsShownGroupsWithBindingsAndIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	groupRows := sqlmock.NewRows([]string{"id", "code", "name", "display_name", "strategy", "hide_member_nodes", "show", "created_at", "updated_at"}).
		AddRow(int64(7), "asia", "Asia", "Asia Entry", "sticky-low-latency", int64(1), int64(1), int64(100), int64(200))
	mock.ExpectQuery(`SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", created_at, updated_at\s+FROM v2_client_entry_group\s+WHERE "show" = 1\s+ORDER BY id ASC`).
		WillReturnRows(groupRows)

	memberRows := sqlmock.NewRows([]string{"entry_group_id", "server_type", "server_id", "sort"}).
		AddRow(int64(7), "vmess", int64(11), int64(1))
	mock.ExpectQuery(`SELECT entry_group_id, server_type, server_id, sort\s+FROM v2_client_entry_group_member\s+WHERE entry_group_id IN \(\$1\)\s+ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(memberRows)

	ipRows := sqlmock.NewRows([]string{"entry_group_id", "ip", "sort"}).
		AddRow(int64(7), "1.1.1.1", int64(1)).
		AddRow(int64(7), "8.8.8.8", int64(2))
	mock.ExpectQuery(`SELECT entry_group_id, ip, sort\s+FROM v2_client_entry_group_ip\s+WHERE entry_group_id IN \(\$1\)\s+ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(ipRows)

	groups, err := service.ClientEntryGroups(context.Background(), int64(10))
	if err != nil {
		t.Fatalf("client entry groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 client entry group, got %#v", groups)
	}
	if groups[0].Code != "asia" || groups[0].DisplayName != "Asia Entry" || !groups[0].HideMemberNodes {
		t.Fatalf("unexpected client entry group: %#v", groups[0])
	}
	if len(groups[0].Members) != 1 || groups[0].Members[0].ServerType != "vmess" || groups[0].Members[0].ServerID != 11 {
		t.Fatalf("unexpected client entry group members: %#v", groups[0].Members)
	}
	if len(groups[0].IPs) != 2 || groups[0].IPs[0].IP != "1.1.1.1" || groups[0].IPs[1].IP != "8.8.8.8" {
		t.Fatalf("unexpected client entry group ips: %#v", groups[0].IPs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
