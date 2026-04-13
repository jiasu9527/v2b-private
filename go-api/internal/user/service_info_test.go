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
