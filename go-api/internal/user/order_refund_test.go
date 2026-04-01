package user

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestRefundManagedOrderRestoresPreviousSubscription(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	now := time.Now()
	targetPaidAt := now.AddDate(0, 0, -5).Unix()
	expectedExpiredAt := now.AddDate(0, 0, 25).Unix()
	currentExpiredAt := time.Unix(expectedExpiredAt, 0).AddDate(0, 1, 0).Unix()

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
			int64(19), int64(12), int64(9), nil, nil, int64(2), "month_price", "T902", "cb902",
			int64(2000), nil, nil, nil, nil, nil,
			nil, int64(3), int64(0), int64(0), nil,
			nil, nil, int64(0), targetPaidAt, targetPaidAt, targetPaidAt,
		))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_order WHERE user_id = \$1 AND status = 3 AND id <> \$2 AND created_at > \$3\)`).
		WithArgs(int64(12), int64(19), targetPaidAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(12), nil, int64(0), int64(0), nil, int64(0), nil,
			int64(11), int64(22), int64(trafficGB), int64(3), int64(0), int64(2), int64(9), int64(50), currentExpiredAt,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(12), int64(0), int64(0), int64(11), int64(22), int64(trafficGB),
			int64(3), int64(2), int64(9), int64(50), expectedExpiredAt, sqlmock.AnyArg(),
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

func TestRefundManagedOrderReopensClosedSurplusOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	now := time.Now()
	firstPaidAt := now.AddDate(0, 0, -40).Unix()
	secondPaidAt := now.AddDate(0, 0, -20).Unix()
	targetPaidAt := now.AddDate(0, 0, -1).Unix()
	expectedExpiredAt := time.Unix(firstPaidAt, 0).AddDate(0, 2, 0).Unix()

	service := NewDBService(config.Config{}, db)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT\s+id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,\s*total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,\s*surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,\s*invite_campaign_discount_amount, paid_at, created_at, updated_at\s+FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T903").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
			"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
			"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
			"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
		}).AddRow(
			int64(30), int64(12), int64(10), nil, nil, int64(3), "month_price", "T903", "cb903",
			int64(3000), nil, nil, int64(500), nil, nil,
			`[17,18]`, int64(3), int64(0), int64(0), nil,
			nil, nil, int64(0), targetPaidAt, targetPaidAt, targetPaidAt,
		))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_order WHERE user_id = \$1 AND status = 3 AND id <> \$2 AND created_at > \$3\)`).
		WithArgs(int64(12), int64(30), targetPaidAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(12), nil, int64(0), int64(0), nil, int64(0), nil,
			int64(7), int64(9), int64(2*trafficGB), int64(5), int64(0), int64(3), int64(10), int64(80), now.AddDate(0, 0, 29).Unix(),
		))
	mock.ExpectExec(`UPDATE v2_order SET status = 3, updated_at = \$3 WHERE status = 4 AND id IN \(\$1,\$2\)`).
		WithArgs(int64(17), int64(18), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(`SELECT\s+id, plan_id, type, period, surplus_order_ids, paid_at, created_at\s+FROM v2_order\s+WHERE id IN \(\$1,\$2\)\s+ORDER BY COALESCE\(paid_at, created_at\) ASC, id ASC`).
		WithArgs(int64(17), int64(18)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "plan_id", "type", "period", "surplus_order_ids", "paid_at", "created_at",
		}).AddRow(
			int64(17), int64(9), int64(1), "month_price", nil, firstPaidAt, firstPaidAt,
		).AddRow(
			int64(18), int64(9), int64(2), "month_price", nil, secondPaidAt, secondPaidAt,
		))
	mock.ExpectQuery(`SELECT\s+id, group_id, transfer_enable, device_limit, speed_limit, "show", renew,\s*month_price, quarter_price, half_year_price, year_price, two_year_price, three_year_price,\s*onetime_price, reset_price, reset_traffic_method, capacity_limit\s+FROM v2_plan\s+WHERE id = \$1\s+LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "group_id", "transfer_enable", "device_limit", "speed_limit", "show", "renew",
			"month_price", "quarter_price", "half_year_price", "year_price", "two_year_price", "three_year_price",
			"onetime_price", "reset_price", "reset_traffic_method", "capacity_limit",
		}).AddRow(
			int64(9), int64(2), int64(1), int64(3), int64(50), int64(1), int64(1),
			int64(2000), nil, nil, nil, nil, nil,
			nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(12), int64(0), int64(0), int64(7), int64(9), int64(trafficGB),
			int64(3), int64(2), int64(9), int64(50), expectedExpiredAt, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, commission_status = \$3, actual_commission_balance = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(30), int64(2), int64(3), nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.RefundManagedOrder(context.Background(), "T903"); err != nil {
		t.Fatalf("refund managed order: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRefundManagedOrderCarriesCommissionDebtWhenFundsInsufficient(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	now := time.Now()
	targetPaidAt := now.AddDate(0, 0, -3).Unix()
	expectedExpiredAt := now.AddDate(0, 0, 27).Unix()
	currentExpiredAt := time.Unix(expectedExpiredAt, 0).AddDate(0, 0, 30).Unix()

	service := NewDBService(config.Config{}, db)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT\s+id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,\s*total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,\s*surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,\s*invite_campaign_discount_amount, paid_at, created_at, updated_at\s+FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T904").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
			"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
			"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
			"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
		}).AddRow(
			int64(40), int64(12), int64(9), nil, nil, int64(2), "month_price", "T904", "cb904",
			int64(2000), nil, nil, nil, nil, nil,
			nil, int64(3), int64(2), int64(300), int64(300),
			int64(88), nil, int64(0), targetPaidAt, targetPaidAt, targetPaidAt,
		))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_order WHERE user_id = \$1 AND status = 3 AND id <> \$2 AND created_at > \$3\)`).
		WithArgs(int64(12), int64(40), targetPaidAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(12), nil, int64(0), int64(0), nil, int64(0), nil,
			int64(4), int64(8), int64(trafficGB), int64(2), int64(0), int64(2), int64(9), int64(40), currentExpiredAt,
		))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(88)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(88), nil, int64(50), int64(80), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(88), int64(0), int64(-170), int64(0), int64(0), int64(0),
			nil, nil, nil, nil, nil, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_commission_log WHERE trade_no = \$1`).
		WithArgs("T904").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(12), int64(0), int64(0), int64(4), int64(8), int64(trafficGB),
			int64(2), int64(2), int64(9), int64(40), expectedExpiredAt, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, commission_status = \$3, actual_commission_balance = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(40), int64(2), int64(3), nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.RefundManagedOrder(context.Background(), "T904"); err != nil {
		t.Fatalf("refund managed order: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRefundManagedOrderCarriesCommissionDebtWhenWithdrawClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	now := time.Now()
	targetPaidAt := now.AddDate(0, 0, -2).Unix()
	expectedExpiredAt := now.AddDate(0, 0, 28).Unix()
	currentExpiredAt := time.Unix(expectedExpiredAt, 0).AddDate(0, 0, 30).Unix()

	service := NewDBService(config.Config{WithdrawCloseEnable: true}, db)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT\s+id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,\s*total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,\s*surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,\s*invite_campaign_discount_amount, paid_at, created_at, updated_at\s+FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T905").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
			"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
			"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
			"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
		}).AddRow(
			int64(41), int64(12), int64(9), nil, nil, int64(2), "month_price", "T905", "cb905",
			int64(2000), nil, nil, nil, nil, nil,
			nil, int64(3), int64(2), int64(300), int64(300),
			int64(89), nil, int64(0), targetPaidAt, targetPaidAt, targetPaidAt,
		))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_order WHERE user_id = \$1 AND status = 3 AND id <> \$2 AND created_at > \$3\)`).
		WithArgs(int64(12), int64(41), targetPaidAt).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(12), nil, int64(0), int64(0), nil, int64(0), nil,
			int64(5), int64(9), int64(trafficGB), int64(2), int64(0), int64(2), int64(9), int64(40), currentExpiredAt,
		))
	mock.ExpectQuery(`SELECT\s+id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,\s*u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at\s+FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(89)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(89), nil, int64(50), int64(0), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(89), int64(0), int64(-250), int64(0), int64(0), int64(0),
			nil, nil, nil, nil, nil, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_commission_log WHERE trade_no = \$1`).
		WithArgs("T905").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET`).
		WithArgs(
			int64(12), int64(0), int64(0), int64(5), int64(9), int64(trafficGB),
			int64(2), int64(2), int64(9), int64(40), expectedExpiredAt, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, commission_status = \$3, actual_commission_balance = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(41), int64(2), int64(3), nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.RefundManagedOrder(context.Background(), "T905"); err != nil {
		t.Fatalf("refund managed order: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
