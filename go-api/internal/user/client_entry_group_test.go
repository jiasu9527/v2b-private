package user

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectEnsureClientEntrySchema(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_group_ip`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_member`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, item := range []struct {
		table  string
		column string
	}{
		{"v2_client_entry_group", "remote_enabled"},
		{"v2_client_entry_group", "remote_host"},
		{"v2_client_entry_group", "remote_ssh_port"},
		{"v2_client_entry_group", "remote_ssh_user"},
		{"v2_client_entry_group", "remote_ssh_password"},
		{"v2_client_entry_group", "remote_group_ref"},
		{"v2_client_entry_group", "remote_exclude_names"},
		{"v2_client_entry_group", "remote_refresh_sec"},
		{"v2_client_entry_user_policy", "name"},
		{"v2_client_entry_user_policy", "sort"},
		{"v2_client_entry_user_policy", "action"},
		{"v2_client_entry_user_policy", "conditions"},
		{"v2_client_entry_user_policy", "entry_host"},
		{"v2_client_entry_user_policy", "resolve_entry_host"},
		{"v2_client_entry_user_policy", "extra_nodes"},
		{"v2_client_entry_user_policy", "extra_nodes_position"},
		{"v2_server_shadowsocks", "client_entry_only"},
		{"v2_server_vmess", "client_entry_only"},
		{"v2_server_vless", "client_entry_only"},
		{"v2_server_trojan", "client_entry_only"},
		{"v2_server_tuic", "client_entry_only"},
		{"v2_server_hysteria", "client_entry_only"},
		{"v2_server_anytls", "client_entry_only"},
		{"v2_server_v2node", "client_entry_only"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_client_entry_user_policy", "email").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_client_entry_user_policy", "server_type").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_member_group ON v2_client_entry_group_member\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_ip_group ON v2_client_entry_group_ip\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_sort ON v2_client_entry_user_policy\(sort, id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_policy ON v2_client_entry_user_policy_member\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_member_server ON v2_client_entry_user_policy_member\(server_type, server_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestDBServiceClientEntryGroupsReturnsShownGroupsWithBindingsAndIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	groupRows := sqlmock.NewRows([]string{"id", "code", "name", "display_name", "strategy", "hide_member_nodes", "show", "remote_enabled", "remote_host", "remote_ssh_port", "remote_ssh_user", "remote_ssh_password", "remote_group_ref", "remote_exclude_names", "remote_refresh_sec", "created_at", "updated_at"}).
		AddRow(int64(7), "asia", "Asia", "Asia Entry", "sticky-low-latency", int64(1), int64(1), int64(1), "192.0.2.10", int64(2222), "root", "secret", "专线直出 (#15)", `["alice","bob"]`, int64(300), int64(100), int64(200))
	mock.ExpectQuery(`SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", remote_enabled, remote_host, remote_ssh_port, remote_ssh_user, remote_ssh_password, remote_group_ref, remote_exclude_names, remote_refresh_sec, created_at, updated_at\s+FROM v2_client_entry_group\s+WHERE "show" = 1\s+ORDER BY id ASC`).
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
	if !groups[0].RemoteEnabled || groups[0].RemoteHost != "192.0.2.10" || groups[0].RemoteSSHPort != 2222 || groups[0].RemoteGroupRef != "专线直出 (#15)" || groups[0].RemoteRefreshSec != 300 {
		t.Fatalf("unexpected client entry remote config: %#v", groups[0])
	}
	if len(groups[0].RemoteExcludeNames) != 2 || groups[0].RemoteExcludeNames[0] != "alice" || groups[0].RemoteExcludeNames[1] != "bob" {
		t.Fatalf("unexpected client entry remote excludes: %#v", groups[0].RemoteExcludeNames)
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
