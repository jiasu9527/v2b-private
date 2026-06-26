package user

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"forest/go-api/internal/config"
)

func TestInfoDefaultsNullablePreferenceFlags(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"email", "transfer_enable", "device_limit", "last_login_at", "created_at", "banned",
		"auto_renewal", "remind_expire", "remind_traffic", "expired_at", "balance",
		"commission_balance", "plan_id", "discount", "commission_rate", "telegram_id", "uuid",
	}).AddRow(
		"user@example.com", int64(1024), nil, nil, int64(1770000000), int64(0),
		nil, nil, nil, nil, int64(0),
		int64(0), nil, nil, nil, nil, "uuid-1",
	)
	mock.ExpectQuery(`SELECT\s+email, transfer_enable, device_limit, last_login_at, created_at, banned,\s*auto_renewal, remind_expire, remind_traffic, expired_at, balance,\s*commission_balance, plan_id, discount, commission_rate, telegram_id, uuid\s+FROM v2_user\s+WHERE id = \$1\s+LIMIT 1`).
		WithArgs(int64(10)).
		WillReturnRows(rows)

	svc := NewDBService(config.Config{}, db)
	info, err := svc.Info(context.Background(), 10)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	if info.AutoRenewal != 0 {
		t.Fatalf("expected auto_renewal default 0, got %d", info.AutoRenewal)
	}
	if info.RemindExpire != 1 {
		t.Fatalf("expected remind_expire default 1, got %d", info.RemindExpire)
	}
	if info.RemindTraffic != 1 {
		t.Fatalf("expected remind_traffic default 1, got %d", info.RemindTraffic)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestInfoTreatsZeroExpiredAtAsUnlimited(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"email", "transfer_enable", "device_limit", "last_login_at", "created_at", "banned",
		"auto_renewal", "remind_expire", "remind_traffic", "expired_at", "balance",
		"commission_balance", "plan_id", "discount", "commission_rate", "telegram_id", "uuid",
	}).AddRow(
		"user@example.com", int64(1024), nil, nil, int64(1770000000), int64(0),
		int64(0), int64(1), int64(1), int64(0), int64(0),
		int64(0), nil, nil, nil, nil, "uuid-1",
	)
	mock.ExpectQuery(`SELECT\s+email, transfer_enable, device_limit, last_login_at, created_at, banned,\s*auto_renewal, remind_expire, remind_traffic, expired_at, balance,\s*commission_balance, plan_id, discount, commission_rate, telegram_id, uuid\s+FROM v2_user\s+WHERE id = \$1\s+LIMIT 1`).
		WithArgs(int64(11)).
		WillReturnRows(rows)

	svc := NewDBService(config.Config{}, db)
	info, err := svc.Info(context.Background(), 11)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ExpiredAt != nil {
		t.Fatalf("expected zero expired_at to become nil, got %#v", info.ExpiredAt)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestInfoIncludesPlanForLifetimeUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"email", "transfer_enable", "device_limit", "last_login_at", "created_at", "banned",
		"auto_renewal", "remind_expire", "remind_traffic", "expired_at", "balance",
		"commission_balance", "plan_id", "discount", "commission_rate", "telegram_id", "uuid",
	}).AddRow(
		"lifetime@example.com", int64(214748364800), nil, nil, int64(1770000000), int64(0),
		int64(0), int64(1), int64(1), int64(0), int64(0),
		int64(0), int64(5), nil, nil, nil, "uuid-5",
	)
	mock.ExpectQuery(`SELECT\s+email, transfer_enable, device_limit, last_login_at, created_at, banned,\s*auto_renewal, remind_expire, remind_traffic, expired_at, balance,\s*commission_balance, plan_id, discount, commission_rate, telegram_id, uuid\s+FROM v2_user\s+WHERE id = \$1\s+LIMIT 1`).
		WithArgs(int64(12)).
		WillReturnRows(rows)
	mock.ExpectQuery(`SELECT \* FROM v2_plan WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "transfer_enable", "show", "renew"}).
			AddRow(int64(5), "不限时 200G", int64(214748364800), int64(0), int64(1)))

	svc := NewDBService(config.Config{}, db)
	info, err := svc.Info(context.Background(), 12)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.PlanID == nil || *info.PlanID != 5 {
		t.Fatalf("expected plan_id 5, got %#v", info.PlanID)
	}
	if info.ExpiredAt != nil {
		t.Fatalf("expected lifetime expired_at nil, got %#v", info.ExpiredAt)
	}
	if info.Plan == nil || info.Plan["name"] != "不限时 200G" {
		t.Fatalf("expected embedded plan, got %#v", info.Plan)
	}
	if got := info.Plan["transfer_enable"]; got != int64(200) {
		t.Fatalf("expected frontend plan traffic 200 GB, got %#v", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
