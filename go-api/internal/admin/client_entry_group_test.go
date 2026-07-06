package admin

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
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_user`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_user_policy_entry`).
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
		{"v2_client_entry_user_policy", "email"},
		{"v2_client_entry_user_policy", "entry_group_id"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}

	mock.ExpectExec(`ALTER TABLE v2_client_entry_user_policy ALTER COLUMN entry_group_id SET DEFAULT 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_member_group ON v2_client_entry_group_member\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_group_ip_group ON v2_client_entry_group_ip\(entry_group_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_server ON v2_client_entry_user_policy\(server_type, server_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_email ON v2_client_entry_user_policy_user\(lower\(email\)\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_user_policy ON v2_client_entry_user_policy_user\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_client_entry_user_policy_entry_policy ON v2_client_entry_user_policy_entry\(policy_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_user`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_entry`).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestDBServiceListClientEntryGroupsIncludesMembersAndIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	groupRows := sqlmock.NewRows([]string{"id", "code", "name", "display_name", "strategy", "hide_member_nodes", "show", "remote_enabled", "remote_host", "remote_ssh_port", "remote_ssh_user", "remote_ssh_password", "remote_group_ref", "remote_exclude_names", "remote_refresh_sec", "created_at", "updated_at"}).
		AddRow(int64(7), "asia", "Asia", "Asia Entry", "sticky-low-latency", int64(1), int64(1), int64(1), "192.0.2.10", int64(2222), "root", "secret", "专线直出 (#15)", `["alice","bob"]`, int64(300), int64(100), int64(200))
	mock.ExpectQuery(`SELECT id, code, name, display_name, strategy, hide_member_nodes, "show", remote_enabled, remote_host, remote_ssh_port, remote_ssh_user, remote_ssh_password, remote_group_ref, remote_exclude_names, remote_refresh_sec, created_at, updated_at\s+FROM v2_client_entry_group\s+ORDER BY id ASC`).
		WillReturnRows(groupRows)

	memberRows := sqlmock.NewRows([]string{"entry_group_id", "server_type", "server_id", "sort"}).
		AddRow(int64(7), "vmess", int64(11), int64(1)).
		AddRow(int64(7), "trojan", int64(12), int64(2))
	mock.ExpectQuery(`SELECT entry_group_id, server_type, server_id, sort\s+FROM v2_client_entry_group_member\s+WHERE entry_group_id IN \(\$1\)\s+ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(memberRows)

	ipRows := sqlmock.NewRows([]string{"entry_group_id", "ip", "sort"}).
		AddRow(int64(7), "1.1.1.1", int64(1)).
		AddRow(int64(7), "8.8.8.8", int64(2))
	mock.ExpectQuery(`SELECT entry_group_id, ip, sort\s+FROM v2_client_entry_group_ip\s+WHERE entry_group_id IN \(\$1\)\s+ORDER BY entry_group_id ASC, sort ASC NULLS LAST, id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(ipRows)

	groups, err := service.ListClientEntryGroups(context.Background(), nil)
	if err != nil {
		t.Fatalf("list client entry groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %#v", groups)
	}
	if groups[0].Code != "asia" || groups[0].DisplayName != "Asia Entry" || groups[0].Strategy != "ordered-fallback" {
		t.Fatalf("unexpected client entry group: %#v", groups[0])
	}
	if !groups[0].RemoteEnabled || groups[0].RemoteHost != "192.0.2.10" || groups[0].RemoteSSHPort != 2222 || groups[0].RemoteGroupRef != "专线直出 (#15)" || groups[0].RemoteRefreshSec != 300 {
		t.Fatalf("unexpected client entry remote config: %#v", groups[0])
	}
	if len(groups[0].RemoteExcludeNames) != 2 || groups[0].RemoteExcludeNames[0] != "alice" || groups[0].RemoteExcludeNames[1] != "bob" {
		t.Fatalf("unexpected client entry remote excludes: %#v", groups[0].RemoteExcludeNames)
	}
	if len(groups[0].Members) != 2 || groups[0].Members[0].ServerType != "vmess" || groups[0].Members[1].ServerID != 12 {
		t.Fatalf("unexpected client entry group members: %#v", groups[0].Members)
	}
	if len(groups[0].IPs) != 2 || groups[0].IPs[0].IP != "1.1.1.1" || groups[0].IPs[1].IP != "8.8.8.8" {
		t.Fatalf("unexpected client entry group ips: %#v", groups[0].IPs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceSaveClientEntryGroupCreatesMembersAndIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	sortA := int64(1)
	sortB := int64(2)

	expectEnsureClientEntrySchema(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO v2_client_entry_group \(code, name, display_name, strategy, hide_member_nodes, "show", remote_enabled, remote_host, remote_ssh_port, remote_ssh_user, remote_ssh_password, remote_group_ref, remote_exclude_names, remote_refresh_sec, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, \$7, \$8, \$9, \$10, \$11, \$12, \$13, \$14, \$15, \$16\)\s+RETURNING id`).
		WithArgs("asia", "Asia", "Asia Entry", "ordered-fallback", int64(1), int64(1), int64(1), "192.0.2.10", int64(2222), "root", "secret", "专线直出 (#15)", `["alice","bob"]`, int64(300), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_member \(entry_group_id, server_type, server_id, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6\)`).
		WithArgs(int64(7), "vmess", int64(11), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_member \(entry_group_id, server_type, server_id, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6\)`).
		WithArgs(int64(7), "trojan", int64(12), int64(2), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_ip \(entry_group_id, ip, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5\)`).
		WithArgs(int64(7), "1.1.1.1", int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_ip \(entry_group_id, ip, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5\)`).
		WithArgs(int64(7), "8.8.8.8", int64(2), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	saved, err := service.SaveClientEntryGroup(context.Background(), ClientEntryGroupSaveRequest{
		Code:              "asia",
		Name:              "Asia",
		DisplayName:       "Asia Entry",
		Strategy:          "sticky-low-latency",
		HideMemberNodes:   true,
		RemoteEnabled:     true,
		RemoteHost:        "192.0.2.10",
		RemoteSSHPort:     2222,
		RemoteSSHUser:     "root",
		RemoteSSHPassword: "secret",
		RemoteGroupRef:    "专线直出 (#15)",
		RemoteExcludeNames: []string{
			"alice",
			"bob",
		},
		RemoteRefreshSec: 300,
		Members: []ClientEntryGroupMemberSaveRequest{
			{ServerType: " vmess ", ServerID: 11, Sort: &sortA},
			{ServerType: "trojan", ServerID: 12, Sort: &sortB},
			{ServerType: "vmess", ServerID: 11, Sort: &sortB},
		},
		IPs: []ClientEntryGroupIPSaveRequest{
			{IP: "1.1.1.1", Sort: &sortA},
			{IP: "8.8.8.8", Sort: &sortB},
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

func TestDBServiceSaveClientEntryGroupReplacesMembersAndIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	id := int64(7)
	sortA := int64(1)

	expectEnsureClientEntrySchema(mock)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE v2_client_entry_group\s+SET code = \$2,\s+name = \$3,\s+display_name = \$4,\s+strategy = \$5,\s+hide_member_nodes = \$6,\s+"show" = \$7,\s+remote_enabled = \$8,\s+remote_host = \$9,\s+remote_ssh_port = \$10,\s+remote_ssh_user = \$11,\s+remote_ssh_password = \$12,\s+remote_group_ref = \$13,\s+remote_exclude_names = \$14,\s+remote_refresh_sec = \$15,\s+updated_at = \$16\s+WHERE id = \$1`).
		WithArgs(id, "asia", "Asia", "Asia Entry", "ordered-fallback", int64(0), int64(1), int64(0), "", int64(0), "", "", "", `[]`, int64(300), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_client_entry_group_ip WHERE entry_group_id = \$1`).
		WithArgs(id).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`DELETE FROM v2_client_entry_group_member WHERE entry_group_id = \$1`).
		WithArgs(id).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_member \(entry_group_id, server_type, server_id, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6\)`).
		WithArgs(id, "shadowsocks", int64(21), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_ip \(entry_group_id, ip, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5\)`).
		WithArgs(id, "entry-a.example.com", nil, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	saved, err := service.SaveClientEntryGroup(context.Background(), ClientEntryGroupSaveRequest{
		ID:          &id,
		Code:        "asia",
		Name:        "Asia",
		DisplayName: "Asia Entry",
		Strategy:    "ordered-fallback",
		Members: []ClientEntryGroupMemberSaveRequest{
			{ServerType: "shadowsocks", ServerID: 21, Sort: &sortA},
		},
		IPs: []ClientEntryGroupIPSaveRequest{
			{IP: "entry-a.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("save client entry group update: %v", err)
	}
	if !saved {
		t.Fatalf("expected save to succeed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestNormalizeClientEntryRemoteSaveRequestUsesWebsiteDefaults(t *testing.T) {
	t.Helper()

	req := ClientEntryGroupSaveRequest{
		RemoteEnabled:     true,
		RemoteHost:        "iso.sllbaidu.com",
		RemoteSSHUser:     "admin",
		RemoteSSHPassword: "secret",
		RemoteGroupRef:    "专线直出 (#15)",
	}
	if err := normalizeClientEntryRemoteSaveRequest(&req); err != nil {
		t.Fatalf("normalize remote save request: %v", err)
	}
	if req.RemoteSSHPort != 0 {
		t.Fatalf("expected remote website port to stay unset, got %d", req.RemoteSSHPort)
	}
	if req.RemoteRefreshSec != 300 {
		t.Fatalf("expected default refresh 300, got %d", req.RemoteRefreshSec)
	}

	req.RemoteEnabled = false
	req.RemoteHost = "https://example.com"
	req.RemoteSSHPort = 8443
	req.RemoteSSHUser = "admin"
	req.RemoteSSHPassword = "secret"
	req.RemoteGroupRef = "group"
	req.RemoteExcludeNames = []string{"a"}
	if err := normalizeClientEntryRemoteSaveRequest(&req); err != nil {
		t.Fatalf("normalize disabled remote request: %v", err)
	}
	if req.RemoteHost != "" || req.RemoteSSHPort != 0 || req.RemoteSSHUser != "" || req.RemoteSSHPassword != "" || req.RemoteGroupRef != "" || len(req.RemoteExcludeNames) != 0 {
		t.Fatalf("expected disabled remote config to be cleared, got %#v", req)
	}
}

func TestDBServiceSaveClientEntryGroupAcceptsDomainEntries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO v2_client_entry_group \(code, name, display_name, strategy, hide_member_nodes, "show", remote_enabled, remote_host, remote_ssh_port, remote_ssh_user, remote_ssh_password, remote_group_ref, remote_exclude_names, remote_refresh_sec, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, \$7, \$8, \$9, \$10, \$11, \$12, \$13, \$14, \$15, \$16\)\s+RETURNING id`).
		WithArgs("asia", "Asia", "Asia Entry", "ordered-fallback", int64(0), int64(1), int64(0), "", int64(0), "", "", "", `[]`, int64(300), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectExec(`INSERT INTO v2_client_entry_group_ip \(entry_group_id, ip, sort, created_at, updated_at\)\s+VALUES \(\$1, \$2, \$3, \$4, \$5\)`).
		WithArgs(int64(7), "entry-a.example.com", nil, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	saved, err := service.SaveClientEntryGroup(context.Background(), ClientEntryGroupSaveRequest{
		Code:        "asia",
		Name:        "Asia",
		DisplayName: "Asia Entry",
		Strategy:    "ordered-fallback",
		IPs: []ClientEntryGroupIPSaveRequest{
			{IP: "entry-a.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("save client entry group with domain: %v", err)
	}
	if !saved {
		t.Fatalf("expected save to succeed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
