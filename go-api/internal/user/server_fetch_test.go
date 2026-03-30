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

func TestLoadServerFetchUserAllowsNullGroupID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{}, db)
	rows := sqlmock.NewRows([]string{"id", "group_id", "plan_id", "transfer_enable", "banned", "created_at", "expired_at"}).
		AddRow(int64(1), nil, nil, int64(100), int64(0), int64(1700000000), sql.NullInt64{})
	mock.ExpectQuery(`SELECT id, group_id, plan_id, transfer_enable, banned, created_at, expired_at`).
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

func TestParseIDStringSupportsJSONStringArray(t *testing.T) {
	ids := parseIDString(`["1","2"]`)
	if len(ids) != 2 {
		t.Fatalf("expected two ids, got %#v", ids)
	}
	if ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("expected [1 2], got %#v", ids)
	}
}
