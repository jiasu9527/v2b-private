package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestApplyClientEntryUserPoliciesUsesFirstMatchingRuleAndCanHide(t *testing.T) {
	servers := []map[string]any{
		{"id": int64(11), "type": "vmess", "host": "default.example.com"},
		{"id": int64(12), "type": "trojan", "host": "other.example.com"},
		{"id": int64(13), "type": "vless", "host": "keep.example.com"},
	}
	policies := []clientEntryUserPolicy{
		{
			ID: 1, Action: cliententry.ActionOverride, EntryHost: "first.example.com",
			Conditions: []cliententry.Condition{{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{json.RawMessage(`"Clash"`)}}},
			Members:    []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 11}},
		},
		{
			ID: 2, Action: cliententry.ActionOverride, EntryHost: "later.example.com",
			Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 11}},
		},
		{
			ID: 3, Action: cliententry.ActionHide,
			Conditions: []cliententry.Condition{{Field: "plan_id", Operator: "between", Min: json.RawMessage("2"), Max: json.RawMessage("3")}},
			Members:    []ClientEntryGroupMember{{ServerType: "trojan", ServerID: 12}},
		},
		{
			ID: 4, Action: cliententry.ActionOverride, EntryHost: "new.example.com",
			Conditions: []cliententry.Condition{
				{Field: "registration_days", Operator: "lte", Value: json.RawMessage("30")},
				{Field: "ua", Operator: "excludes_any", Values: []json.RawMessage{json.RawMessage(`"Shadowrocket"`)}},
			},
			Members: []ClientEntryGroupMember{{ServerType: "vless", ServerID: 13}},
		},
	}

	result := applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 100, PlanID: 2, RegistrationDays: 8, UA: "ClashMeta/1.0"}, policies)
	if len(result) != 2 {
		t.Fatalf("expected hide rule to remove one node, got %#v", result)
	}
	if result[0]["host"] != "first.example.com" {
		t.Fatalf("first matching rule should win, got %#v", result[0]["host"])
	}
	if result[1]["host"] != "new.example.com" {
		t.Fatalf("expected registration/UA rule host, got %#v", result[1]["host"])
	}
}

func TestServerFetchRegistrationDaysIncludesNewlyRegisteredUser(t *testing.T) {
	if got := serverFetchRegistrationDays(100, 100); got != 0 {
		t.Fatalf("registration days at creation = %d, want 0", got)
	}
	if got := serverFetchRegistrationDays(100, 101); got != 0 {
		t.Fatalf("registration days one second later = %d, want 0", got)
	}
	if got := serverFetchRegistrationDays(0, 100); got != -1 {
		t.Fatalf("invalid created_at registration days = %d, want -1", got)
	}
}

func TestLoadServerFetchUserAllowsNullGroupID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{}, db)
	rows := sqlmock.NewRows([]string{"id", "email", "group_id", "plan_id", "transfer_enable", "banned", "created_at", "expired_at"}).
		AddRow(int64(1), "user@example.com", nil, nil, int64(100), int64(0), int64(1700000000), sql.NullInt64{})
	mock.ExpectQuery(`SELECT id, email, group_id, plan_id, transfer_enable, banned, created_at, expired_at`).
		WithArgs(int64(1)).
		WillReturnRows(rows)

	userRow, err := service.loadServerFetchUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("expected null group_id to be allowed, got %v", err)
	}
	if userRow.GroupID != 0 {
		t.Fatalf("expected default group_id 0, got %d", userRow.GroupID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestLoadServerFetchUserTreatsZeroExpiredAtAsUnlimited(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{}, db)
	rows := sqlmock.NewRows([]string{"id", "email", "group_id", "plan_id", "transfer_enable", "banned", "created_at", "expired_at"}).
		AddRow(int64(2), "user@example.com", int64(1), int64(1), int64(100), int64(0), int64(1700000000), int64(0))
	mock.ExpectQuery(`SELECT id, email, group_id, plan_id, transfer_enable, banned, created_at, expired_at`).
		WithArgs(int64(2)).
		WillReturnRows(rows)

	userRow, err := service.loadServerFetchUser(context.Background(), 2)
	if err != nil {
		t.Fatalf("loadServerFetchUser: %v", err)
	}
	if userRow.ExpiredAt.Valid {
		t.Fatalf("expected zero expired_at to become invalid, got %#v", userRow.ExpiredAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestParseIDStringSupportsJSONStringArray(t *testing.T) {
	ids := parseIDString(`["1","2"]`)
	if len(ids) != 2 {
		t.Fatalf("expected two ids, got %#v", ids)
	}
	if ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("expected [1 2], got %#v", ids)
	}
}

func TestParseServerStringListTreatsNilMarkerAsEmpty(t *testing.T) {
	tags := parseServerStringList("<nil>")
	if len(tags) != 0 {
		t.Fatalf("expected empty tags for nil marker, got %#v", tags)
	}
}

func TestNormalizeServerFetchRowOmitsEmptyTags(t *testing.T) {
	service := &DBService{}

	item, err := service.normalizeServerFetchRow(context.Background(), serverFetchTable{serverType: "vmess"}, map[string]any{
		"id":   int64(1),
		"tags": nil,
	}, map[int64]map[string]any{})
	if err != nil {
		t.Fatalf("normalize server row: %v", err)
	}
	if _, ok := item["tags"]; ok {
		t.Fatalf("expected empty tags to be omitted, got %#v", item["tags"])
	}
}

func TestApplyClientEntryUserPolicyOverridesSelectedNodeHosts(t *testing.T) {
	servers := []map[string]any{
		{"id": int64(11), "type": "vmess", "host": "manual.example.com(UShadowrocket),default.example.com"},
		{"id": int64(12), "type": "trojan", "host": "other.example.com"},
		{"id": int64(13), "type": "vless", "host": "keep.example.com"},
	}
	policies := []clientEntryUserPolicy{
		{
			ID:        int64(3),
			EntryHost: "vip-entry.example.com",
			Members: []ClientEntryGroupMember{
				{ServerType: "vmess", ServerID: int64(11)},
				{ServerType: "trojan", ServerID: int64(12)},
			},
		},
	}

	servers = applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 1}, policies)

	if len(servers) != 3 {
		t.Fatalf("expected all nodes to remain, got %#v", servers)
	}
	if got := servers[0]["host"]; got != "vip-entry.example.com" {
		t.Fatalf("expected selected vmess host override, got %#v", got)
	}
	if got := servers[1]["host"]; got != "vip-entry.example.com" {
		t.Fatalf("expected selected trojan host override, got %#v", got)
	}
	if got := servers[2]["host"]; got != "keep.example.com" {
		t.Fatalf("expected non-selected node to keep original host, got %#v", got)
	}
}

func TestApplyClientEntryUserPolicyKeepsOriginalServersWhenNoPolicy(t *testing.T) {
	servers := []map[string]any{{"id": int64(11), "type": "vmess", "host": "default.example.com"}}

	result := applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 1}, nil)

	if len(result) != 1 || result[0]["host"] != "default.example.com" {
		t.Fatalf("expected no policy to keep original servers, got %#v", result)
	}
}

func TestApplyClientEntryUserPolicyRequiresMatchingOverrideForEntryOnlyNode(t *testing.T) {
	servers := []map[string]any{
		{"id": int64(11), "type": "vmess", "host": "private.example.com", "client_entry_only": int64(1)},
		{"id": int64(12), "type": "trojan", "host": "public.example.com", "client_entry_only": int64(0)},
	}
	policies := []clientEntryUserPolicy{{
		ID: 7, Action: cliententry.ActionOverride, EntryHost: "assigned.example.com",
		Conditions: []cliententry.Condition{{Field: "user_id", Operator: "in", Values: []json.RawMessage{json.RawMessage("100")}}},
		Members:    []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 11}},
	}}

	unmatched := applyClientEntryUserPolicies(cloneServerMapsForTest(servers), cliententry.Subject{UserID: 99}, policies)
	if len(unmatched) != 1 || unmatched[0]["id"] != int64(12) {
		t.Fatalf("unmatched user received entry-only node: %#v", unmatched)
	}
	matched := applyClientEntryUserPolicies(cloneServerMapsForTest(servers), cliententry.Subject{UserID: 100}, policies)
	if len(matched) != 2 || matched[0]["host"] != "assigned.example.com" {
		t.Fatalf("matched user did not receive entry-only node: %#v", matched)
	}
	for _, server := range matched {
		if _, exists := server["client_entry_only"]; exists {
			t.Fatalf("internal entry-only marker leaked to subscription: %#v", server)
		}
	}
}

func TestApplyClientEntryUserPolicyHidesEntryOnlyNodeWithoutPolicies(t *testing.T) {
	servers := []map[string]any{{"id": int64(11), "type": "vmess", "host": "private.example.com", "client_entry_only": int64(1)}}
	if result := applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 100}, nil); len(result) != 0 {
		t.Fatalf("entry-only node must be denied when no enabled rules exist: %#v", result)
	}
}

func TestApplyClientEntryUserPolicyHideRuleDoesNotGrantEntryOnlyNode(t *testing.T) {
	servers := []map[string]any{{"id": int64(11), "type": "vmess", "host": "private.example.com", "client_entry_only": int64(1)}}
	policies := []clientEntryUserPolicy{{
		ID: 8, Action: cliententry.ActionHide,
		Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 11}},
	}}
	if result := applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 100}, policies); len(result) != 0 {
		t.Fatalf("hide rule must not grant entry-only node: %#v", result)
	}
}

func TestServersDoesNotExposeShowZeroNodeEvenWhenOverrideMatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{}, db)
	expectServerFetchUserForVisibilityTest(mock, 100, 1)
	expectEnsureClientEntrySchema(mock)
	expectMatchingOverridePolicyForVisibilityTest(mock, "vmess", 11, 100)

	// Node #11 is show=0 in the scenario, so PostgreSQL must remove it at the
	// WHERE show=1 boundary before its otherwise matching override is evaluated.
	expectServerFetchTableQueriesForVisibilityTest(mock, "", 0, "")

	servers, err := service.Servers(context.Background(), 100, "ClashMeta/1.0")
	if err != nil {
		t.Fatalf("fetch servers: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("show=0 node must not be granted by an override rule: %#v", servers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestServersDoesNotGrantGroupMismatchEvenWhenOverrideMatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{}, db)
	expectServerFetchUserForVisibilityTest(mock, 100, 1)
	expectEnsureClientEntrySchema(mock)
	expectMatchingOverridePolicyForVisibilityTest(mock, "vmess", 11, 100)
	expectServerFetchTableQueriesForVisibilityTest(mock, "vmess", 11, `[2]`)

	servers, err := service.Servers(context.Background(), 100, "ClashMeta/1.0")
	if err != nil {
		t.Fatalf("fetch servers: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("entry override must not grant a node from another permission group: %#v", servers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectServerFetchUserForVisibilityTest(mock sqlmock.Sqlmock, userID, groupID int64) {
	mock.ExpectQuery(`SELECT id, email, group_id, plan_id, transfer_enable, banned, created_at, expired_at`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "group_id", "plan_id", "transfer_enable", "banned", "created_at", "expired_at"}).
			AddRow(userID, "allowed@example.com", groupID, int64(2), int64(1024), int64(0), int64(1700000000), nil))
}

func expectMatchingOverridePolicyForVisibilityTest(mock sqlmock.Sqlmock, serverType string, serverID, userID int64) {
	conditions := `[{"field":"user_id","operator":"in","values":[` + fmt.Sprint(userID) + `]}]`
	mock.ExpectQuery(`SELECT p.id, p.action, p.conditions, p.entry_host, m.server_type, m.server_id, m.sort AS member_sort\s+FROM v2_client_entry_user_policy p\s+JOIN v2_client_entry_user_policy_member m ON m.policy_id = p.id\s+WHERE p.enabled = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "action", "conditions", "entry_host", "server_type", "server_id", "member_sort"}).
			AddRow(int64(7), cliententry.ActionOverride, conditions, "assigned.example.com", serverType, serverID, int64(1)))
}

func expectServerFetchTableQueriesForVisibilityTest(mock sqlmock.Sqlmock, visibleType string, visibleID int64, visibleGroupIDs string) {
	for _, table := range serverFetchTables {
		rows := sqlmock.NewRows([]string{"id", "group_id", "show", "client_entry_only"})
		if table.serverType == visibleType {
			rows.AddRow(visibleID, visibleGroupIDs, int64(1), int64(1))
		}
		mock.ExpectQuery(`SELECT \*\s+FROM ` + table.table + `\s+WHERE "show" = 1\s+ORDER BY sort ASC NULLS LAST, id ASC`).
			WillReturnRows(rows)
	}
}

func cloneServerMapsForTest(values []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item := make(map[string]any, len(value))
		for key, field := range value {
			item[key] = field
		}
		result = append(result, item)
	}
	return result
}
