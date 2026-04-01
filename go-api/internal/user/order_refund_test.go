package user

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestRefundManagedOrderClearsUserSubscriptionAndRollsBackCommission(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	service := NewDBService(config.Config{}, db)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT\s+id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,\s*total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,\s*surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,\s*invite_campaign_discount_amount, paid_at, created_at, updated_at\s+FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T902").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
			"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
			"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
			"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
		}).AddRow(
			int64(19), int64(12), int64(9), nil, nil, int64(1), "month_price", "T902", "cb902",
			int64(2000), nil, nil, nil, nil, nil,
			nil, int64(3), int64(2), int64(180), int64(180),
			int64(66), nil, int64(0), now-1800, now-1800, now-1800,
		))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_order WHERE user_id = \$1 AND status = 3 AND id <> \$2 AND created_at > \$3\)`).
		WithArgs(int64(12), int64(19), now-1800).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(12), nil, int64(0), int64(0), nil, int64(0), nil,
			int64(11), int64(22), int64(1073741824), int64(3), int64(0), int64(2), int64(9), int64(50), now+86400,
		))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(66)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(66), nil, int64(30), int64(250), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(66), int64(30), int64(70), int64(0), int64(0), int64(0),
			nil, nil, nil, nil, nil, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_commission_log WHERE trade_no = \$1`).
		WithArgs("T902").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(12), int64(0), int64(0), int64(0), int64(0), int64(0),
			nil, nil, nil, nil, nil, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, commission_status = \$3, actual_commission_balance = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(19), int64(2), int64(3), nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.RefundManagedOrder(context.Background(), "T902"); err != nil {
		t.Fatalf("refund managed order: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
