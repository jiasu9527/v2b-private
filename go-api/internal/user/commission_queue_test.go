package user

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestHandlePendingOrdersAutoConfirmsCommission(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		CommissionAutoCheckEnable:  true,
		CommissionAutoCheckMinutes: 60,
	}, db)

	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}))
	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status = 3 AND commission_balance > 0 AND invite_user_id IS NOT NULL AND commission_status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}).AddRow("T901"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT\s+id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,\s*total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,\s*surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,\s*invite_campaign_discount_amount, paid_at, created_at, updated_at\s+FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T901").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
			"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
			"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
			"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
		}).AddRow(
			int64(9), int64(12), int64(3), nil, nil, int64(1), "month_price", "T901", "cb901",
			int64(1000), nil, nil, nil, nil, nil,
			nil, int64(3), int64(0), int64(120), nil,
			int64(88), nil, int64(0), time.Now().Unix()-(2*3600), time.Now().Unix()-(2*3600), time.Now().Unix()-(2*3600),
		))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(88)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(88), nil, int64(300), int64(500), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(88), int64(300), int64(620), int64(0), int64(0), int64(0),
			nil, nil, nil, nil, nil, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_commission_log`).
		WithArgs(int64(88), int64(12), "T901", int64(1000), int64(120), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE v2_order SET commission_status = \$2, actual_commission_balance = \$3, updated_at = \$4 WHERE id = \$1`).
		WithArgs(int64(9), int64(2), int64(120), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectNoTrafficResetCandidates(mock)

	if err := service.HandlePendingOrders(context.Background()); err != nil {
		t.Fatalf("handle pending orders: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandlePendingOrdersOffsetsCommissionDebtBeforeBalanceCredit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		CommissionAutoCheckEnable:  true,
		CommissionAutoCheckMinutes: 60,
		WithdrawCloseEnable:        true,
	}, db)

	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}))
	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status = 3 AND commission_balance > 0 AND invite_user_id IS NOT NULL AND commission_status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}).AddRow("T902"))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT\s+id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,\s*total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,\s*surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,\s*invite_campaign_discount_amount, paid_at, created_at, updated_at\s+FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T902").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
			"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
			"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
			"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
		}).AddRow(
			int64(10), int64(12), int64(3), nil, nil, int64(1), "month_price", "T902", "cb902",
			int64(1000), nil, nil, nil, nil, nil,
			nil, int64(3), int64(0), int64(120), nil,
			int64(88), nil, int64(0), time.Now().Unix()-(2*3600), time.Now().Unix()-(2*3600), time.Now().Unix()-(2*3600),
		))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(88)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(88), nil, int64(300), int64(-50), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(88), int64(370), int64(0), int64(0), int64(0), int64(0),
			nil, nil, nil, nil, nil, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_commission_log`).
		WithArgs(int64(88), int64(12), "T902", int64(1000), int64(120), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE v2_order SET commission_status = \$2, actual_commission_balance = \$3, updated_at = \$4 WHERE id = \$1`).
		WithArgs(int64(10), int64(2), int64(120), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectNoTrafficResetCandidates(mock)

	if err := service.HandlePendingOrders(context.Background()); err != nil {
		t.Fatalf("handle pending orders: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
