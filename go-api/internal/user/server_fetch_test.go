package user

import (
	"context"
	"database/sql"
	"encoding/json"
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
