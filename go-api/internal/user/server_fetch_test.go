package user

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestParseServerHostByConditionMatchesUserAgentAndRange(t *testing.T) {
	createdAt := time.Now().Add(-10 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"default.example.com,clash.example.com(UClash),range.example.com(1-20)",
		serverFetchUser{
			ID:        10,
			PlanID:    1,
			CreatedAt: createdAt,
		},
		"ClashMeta/1.0",
		time.Now().Unix(),
	)
	if !ok {
		t.Fatalf("expected host match")
	}
	if host != "clash.example.com" {
		t.Fatalf("expected clash host, got %q", host)
	}
}

func TestParseServerHostByConditionMatchesPlanAndDays(t *testing.T) {
	createdAt := time.Now().Add(-40 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"plan.example.com(P2-5),days.example.com(D>30),default.example.com",
		serverFetchUser{
			ID:        30,
			PlanID:    3,
			CreatedAt: createdAt,
		},
		"Mozilla/5.0",
		time.Now().Unix(),
	)
	if !ok {
		t.Fatalf("expected host match")
	}
	if host != "plan.example.com" {
		t.Fatalf("expected plan host, got %q", host)
	}
}

func TestParseServerHostByConditionDropsServerWhenNoConditionMatches(t *testing.T) {
	createdAt := time.Now().Add(-5 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"locked.example.com(P2-3)",
		serverFetchUser{
			ID:        1,
			PlanID:    1,
			CreatedAt: createdAt,
		},
		"Mozilla/5.0",
		time.Now().Unix(),
	)
	if ok {
		t.Fatalf("expected no host match, got %q", host)
	}
}

func TestParseServerHostByConditionMatchesMissingUserAgent(t *testing.T) {
	createdAt := time.Now().Add(-5 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"noua.example.com(UNoUA),default.example.com",
		serverFetchUser{
			ID:        1,
			PlanID:    1,
			CreatedAt: createdAt,
		},
		"",
		time.Now().Unix(),
	)
	if !ok {
		t.Fatalf("expected no-ua host match")
	}
	if host != "noua.example.com" {
		t.Fatalf("expected no-ua host, got %q", host)
	}
}

func TestParseServerHostByConditionDoesNotMatchNoUAWhenUserAgentPresent(t *testing.T) {
	createdAt := time.Now().Add(-5 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"noua.example.com(UNoUA),default.example.com",
		serverFetchUser{
			ID:        1,
			PlanID:    1,
			CreatedAt: createdAt,
		},
		"ClashMeta/1.0",
		time.Now().Unix(),
	)
	if !ok {
		t.Fatalf("expected default host match")
	}
	if host != "default.example.com" {
		t.Fatalf("expected default host, got %q", host)
	}
}

func TestParseServerHostByConditionMatchesWhenUserAgentExcluded(t *testing.T) {
	createdAt := time.Now().Add(-5 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"nonclash.example.com(U!Clash),default.example.com",
		serverFetchUser{
			ID:        1,
			PlanID:    1,
			CreatedAt: createdAt,
		},
		"Shadowrocket/1.0",
		time.Now().Unix(),
	)
	if !ok {
		t.Fatalf("expected excluded-ua host match")
	}
	if host != "nonclash.example.com" {
		t.Fatalf("expected excluded-ua host, got %q", host)
	}
}

func TestParseServerHostByConditionSkipsWhenExcludedUserAgentMatches(t *testing.T) {
	createdAt := time.Now().Add(-5 * 24 * time.Hour).Unix()
	host, ok := parseServerHostByCondition(
		"nonclash.example.com(U!Clash),default.example.com",
		serverFetchUser{
			ID:        1,
			PlanID:    1,
			CreatedAt: createdAt,
		},
		"ClashMeta/1.0",
		time.Now().Unix(),
	)
	if !ok {
		t.Fatalf("expected default host match")
	}
	if host != "default.example.com" {
		t.Fatalf("expected default host, got %q", host)
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

func TestApplyClientEntryUserPolicyOverridesConditionalHost(t *testing.T) {
	servers := []map[string]any{
		{"id": int64(11), "type": "vmess", "host": "manual.example.com(UShadowrocket),default.example.com"},
		{"id": int64(12), "type": "trojan", "host": "other.example.com"},
	}
	policies := []clientEntryUserPolicy{
		{
			EntryGroupID: int64(7),
			ServerType:   "vmess",
			ServerID:     int64(11),
			Entries:      []string{"entry-a.example.com", "entry-b.example.com"},
		},
	}

	applyClientEntryUserPolicies(servers, "user@example.com", policies)

	if got := servers[0]["host"]; got != "entry-a.example.com" {
		t.Fatalf("expected policy entry to override conditional host, got %#v", got)
	}
	if got := servers[1]["host"]; got != "other.example.com" {
		t.Fatalf("expected unmatched server to keep host, got %#v", got)
	}
}

func TestApplyClientEntryUserPolicyKeepsOriginalHostWhenEntryGroupEmpty(t *testing.T) {
	servers := []map[string]any{{"id": int64(11), "type": "vmess", "host": "default.example.com"}}
	policies := []clientEntryUserPolicy{{EntryGroupID: int64(7), ServerType: "vmess", ServerID: int64(11)}}

	applyClientEntryUserPolicies(servers, "user@example.com", policies)

	if got := servers[0]["host"]; got != "default.example.com" {
		t.Fatalf("expected empty policy to fall back to original host, got %#v", got)
	}
}
