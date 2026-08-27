package user

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

var paymentSecurityOrderColumns = []string{
	"id", "user_id", "plan_id", "coupon_id", "payment_id", "type", "period", "trade_no", "callback_no",
	"total_amount", "handling_amount", "discount_amount", "surplus_amount", "refund_amount", "balance_amount",
	"surplus_order_ids", "status", "commission_status", "commission_balance", "actual_commission_balance",
	"invite_user_id", "invite_campaign_id", "invite_campaign_discount_amount", "paid_at", "created_at", "updated_at",
}

var paymentSecurityAttemptColumns = []string{
	"id", "order_id", "payment_id", "handling_amount", "amount", "callback_no", "status", "paid_at",
}

const paymentSecurityCallbackConflictPattern = `SELECT EXISTS\(\s*SELECT 1 FROM v2_order WHERE payment_id = \$1 AND callback_no = \$2 AND trade_no <> \$3\s*UNION ALL\s*SELECT 1\s*FROM v2_order_payment_attempt AS attempt\s*JOIN v2_order AS attempt_order ON attempt_order.id = attempt.order_id\s*WHERE attempt.payment_id = \$1 AND attempt.callback_no = \$2 AND attempt_order.trade_no <> \$3\s*\)`

func expectNoPaymentAttempt(mock sqlmock.Sqlmock, orderID, paymentID int64) {
	mock.ExpectQuery(`SELECT id, order_id, payment_id, handling_amount, amount, callback_no, status, paid_at\s+FROM v2_order_payment_attempt\s+WHERE order_id = \$1 AND payment_id = \$2\s+FOR UPDATE`).
		WithArgs(orderID, paymentID).
		WillReturnRows(sqlmock.NewRows(paymentSecurityAttemptColumns))
}

func paymentSecurityAttemptRow(paymentID, handlingAmount, amount, status int64, callback any) *sqlmock.Rows {
	return sqlmock.NewRows(paymentSecurityAttemptColumns).AddRow(
		int64(70), int64(9), paymentID, handlingAmount, amount, callback, status, nil,
	)
}

func paymentSecurityDepositOrderRow(status int64, callback any, paymentID, handlingAmount int64) *sqlmock.Rows {
	return sqlmock.NewRows(paymentSecurityOrderColumns).AddRow(
		int64(9), int64(5), int64(0), nil, paymentID, int64(9), "deposit", "T900", callback,
		int64(1000), handlingAmount, nil, nil, nil, nil, nil,
		status, int64(0), int64(0), nil, nil, nil, int64(0), nil, int64(1200), int64(1250),
	)
}

func paymentSecurityOrderRow(status int64, callback any) *sqlmock.Rows {
	return paymentSecurityOrderRowForPayment(status, callback, 7)
}

func paymentSecurityOrderRowForPayment(status int64, callback any, paymentID int64) *sqlmock.Rows {
	return sqlmock.NewRows(paymentSecurityOrderColumns).AddRow(
		int64(9), int64(5), int64(2), nil, paymentID, int64(1), "month_price", "T900", callback,
		int64(1000), int64(100), nil, nil, nil, int64(200), nil,
		status, int64(0), int64(0), nil, nil, nil, int64(0), int64(1234), int64(1200), int64(1250),
	)
}

func TestMarkOrderPaidScopesCallbackToPaymentMethod(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRowForPayment(3, "P900", 8))
	expectNoPaymentAttempt(mock, 9, 8)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:8:P900").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(8), "P900", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P900",
		PaymentID:  testInt64Pointer(8),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("same callback in another payment namespace should remain valid: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidRejectsMismatchedGatewayConfirmation(t *testing.T) {
	tests := []struct {
		name         string
		confirmation OrderPaymentConfirmation
	}{
		{
			name: "payment method",
			confirmation: OrderPaymentConfirmation{
				CallbackNo: "P900",
				PaymentID:  testInt64Pointer(8),
				Amount:     testInt64Pointer(1100),
			},
		},
		{
			name: "amount",
			confirmation: OrderPaymentConfirmation{
				CallbackNo: "P900",
				PaymentID:  testInt64Pointer(7),
				Amount:     testInt64Pointer(1099),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("new sql mock: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
				WithArgs("T900").
				WillReturnRows(paymentSecurityOrderRow(0, nil))
			expectNoPaymentAttempt(mock, 9, *test.confirmation.PaymentID)
			mock.ExpectRollback()

			service := &DBService{db: db}
			err = service.MarkOrderPaid(context.Background(), "T900", test.confirmation)
			if !errors.Is(err, ErrPaymentConfirmationMismatch) {
				t.Fatalf("expected confirmation mismatch, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestMarkOrderPaidRejectsNegativeStoredOrderWithoutGatewayAmount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	row := sqlmock.NewRows(paymentSecurityOrderColumns).AddRow(
		int64(9), int64(5), int64(2), nil, nil, int64(1), "month_price", "TNEG", nil,
		int64(-1), nil, nil, nil, nil, nil, nil,
		int64(0), int64(0), int64(0), nil, nil, nil, int64(0), nil, int64(1200), int64(1250),
	)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("TNEG").
		WillReturnRows(row)
	mock.ExpectRollback()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "TNEG", OrderPaymentConfirmation{CallbackNo: "TNEG"})
	if !errors.Is(err, ErrPaymentConfirmationMismatch) {
		t.Fatalf("expected negative stored order to be rejected, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidRejectsPositiveOrderWithoutGatewayEvidence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRow(0, nil))
	mock.ExpectRollback()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{CallbackNo: "T900"})
	if !errors.Is(err, ErrPaymentConfirmationMismatch) {
		t.Fatalf("expected missing gateway evidence to be rejected, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidRejectsCallbackAlreadyUsedByAnotherOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRow(0, nil))
	expectNoPaymentAttempt(mock, 9, 7)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P900").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P900", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P900",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if !errors.Is(err, ErrPaymentCallbackConflict) {
		t.Fatalf("expected callback conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidRepairsLegacyCallbackWithoutOpeningTwice(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRow(3, "T900"))
	expectNoPaymentAttempt(mock, 9, 7)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P900").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P900", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, callback_no = \$3, paid_at = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(9), int64(3), "P900", int64(1234), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P900",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("repair legacy callback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidRepairsMigratedAttemptLegacyCallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRow(3, "T900"))
	mock.ExpectQuery(`FROM v2_order_payment_attempt\s+WHERE order_id = \$1 AND payment_id = \$2\s+FOR UPDATE`).
		WithArgs(int64(9), int64(7)).
		WillReturnRows(paymentSecurityAttemptRow(7, 100, 1100, paymentAttemptWinner, "T900"))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P900").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P900", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_order_payment_attempt\s+SET callback_no = \$2, updated_at = \$3\s+WHERE id = \$1 AND status = 1`).
		WithArgs(int64(70), "P900", sqlmock.AnyArg(), "T900").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, callback_no = \$3, paid_at = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(9), int64(3), "P900", int64(1234), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P900",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("repair migrated legacy callback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidTreatsExactGatewayReplayAsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRow(3, "P900"))
	expectNoPaymentAttempt(mock, 9, 7)
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P900").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P900", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P900",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidLetsAnEarlierPaymentAttemptWinAfterMethodSwitch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityDepositOrderRow(0, nil, 8, 50))
	mock.ExpectQuery(`FROM v2_order_payment_attempt\s+WHERE order_id = \$1 AND payment_id = \$2\s+FOR UPDATE`).
		WithArgs(int64(9), int64(7)).
		WillReturnRows(paymentSecurityAttemptRow(7, 100, 1100, paymentAttemptPending, nil))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P700").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P700", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_order_payment_attempt\s+SET callback_no = \$2, status = \$3, paid_at = \$4, updated_at = \$4\s+WHERE id = \$1 AND status = 0`).
		WithArgs(int64(70), "P700", paymentAttemptWinner, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET payment_id = \$2, handling_amount = \$3, updated_at = \$4 WHERE id = \$1`).
		WithArgs(int64(9), int64(7), int64(100), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(5), nil, int64(0), int64(0), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_order\s+SET commission_balance = 0, commission_status = 2, actual_commission_balance = 0, updated_at = \$2\s+WHERE id = \$1`).
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET balance = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(5), int64(1000), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, callback_no = \$3, paid_at = \$4, updated_at = \$5 WHERE id = \$1`).
		WithArgs(int64(9), int64(3), "P700", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_runtime_kv WHERE k = \$1`).
		WithArgs("order:cancel:recover:T900").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P700",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("settle earlier payment attempt: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidRecordsLateAlternateAttemptWithoutOpeningTwice(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRowForPayment(3, "P800", 8))
	mock.ExpectQuery(`FROM v2_order_payment_attempt\s+WHERE order_id = \$1 AND payment_id = \$2\s+FOR UPDATE`).
		WithArgs(int64(9), int64(7)).
		WillReturnRows(paymentSecurityAttemptRow(7, 100, 1100, paymentAttemptPending, nil))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P700").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P700", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_order_payment_attempt\s+SET callback_no = \$2, status = \$3, paid_at = \$4, updated_at = \$4\s+WHERE id = \$1 AND status = 0`).
		WithArgs(int64(70), "P700", paymentAttemptDuplicate, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P700",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("record late alternate payment: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMarkOrderPaidTreatsDuplicateAttemptCallbackReplayAsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("T900").
		WillReturnRows(paymentSecurityOrderRowForPayment(3, "P800", 8))
	mock.ExpectQuery(`FROM v2_order_payment_attempt\s+WHERE order_id = \$1 AND payment_id = \$2\s+FOR UPDATE`).
		WithArgs(int64(9), int64(7)).
		WillReturnRows(paymentSecurityAttemptRow(7, 100, 1100, paymentAttemptDuplicate, "P700"))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("payment:7:P700").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(paymentSecurityCallbackConflictPattern).
		WithArgs(int64(7), "P700", "T900").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	service := &DBService{db: db}
	err = service.MarkOrderPaid(context.Background(), "T900", OrderPaymentConfirmation{
		CallbackNo: "P700",
		PaymentID:  testInt64Pointer(7),
		Amount:     testInt64Pointer(1100),
	})
	if err != nil {
		t.Fatalf("replay duplicate payment callback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCancelOrderDoesNotRefundAfterLockedOrderIsAlreadyCancelled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE user_id = \$1 AND trade_no = \$2\s+FOR UPDATE`).
		WithArgs(int64(5), "T900").
		WillReturnRows(paymentSecurityOrderRow(2, nil))
	mock.ExpectRollback()

	service := &DBService{db: db}
	ok, err := service.CancelOrder(context.Background(), 5, "T900")
	if ok || !errors.Is(err, ErrCancelPendingOnly) {
		t.Fatalf("expected already-cancelled order to be rejected without refund, ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCancelOrderCommitsRefundAndRecoveryMarkerTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE user_id = \$1 AND trade_no = \$2\s+FOR UPDATE`).
		WithArgs(int64(5), "T900").
		WillReturnRows(paymentSecurityOrderRow(0, nil))
	mock.ExpectQuery(`SELECT COALESCE\(\s*checkout_claim IS NOT NULL AND checkout_claim_expires_at > EXTRACT\(EPOCH FROM NOW\(\)\)::BIGINT,\s*FALSE\s*\)\s*FROM v2_order\s*WHERE id = \$1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(9), int64(2), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM v2_user\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invite_user_id", "balance", "commission_balance", "discount", "commission_type", "commission_rate",
			"u", "d", "transfer_enable", "device_limit", "banned", "group_id", "plan_id", "speed_limit", "expired_at",
		}).AddRow(
			int64(5), nil, int64(50), int64(0), nil, int64(0), nil,
			int64(0), int64(0), int64(0), nil, int64(0), nil, nil, nil, nil,
		))
	mock.ExpectExec(`UPDATE v2_user SET balance = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(5), int64(250), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
		WithArgs("order:cancel:recover:T900", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	ok, err := service.CancelOrder(context.Background(), 5, "T900")
	if err != nil || !ok {
		t.Fatalf("cancel order: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCancelOrderRefusesWhileProviderCheckoutIsBeingCreated(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE user_id = \$1 AND trade_no = \$2\s+FOR UPDATE`).
		WithArgs(int64(5), "T900").
		WillReturnRows(paymentSecurityOrderRow(0, nil))
	mock.ExpectQuery(`SELECT COALESCE\(\s*checkout_claim IS NOT NULL AND checkout_claim_expires_at > EXTRACT\(EPOCH FROM NOW\(\)\)::BIGINT,\s*FALSE\s*\)\s*FROM v2_order\s*WHERE id = \$1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectRollback()

	service := &DBService{db: db}
	ok, err := service.CancelOrder(context.Background(), 5, "T900")
	if ok || !errors.Is(err, ErrCheckoutInProgress) {
		t.Fatalf("expected checkout-in-progress cancellation refusal, ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestHandleQueuedOrderQuarantinesNegativePaidPendingOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	row := sqlmock.NewRows(paymentSecurityOrderColumns).AddRow(
		int64(9), int64(5), int64(2), nil, int64(7), int64(1), "month_price", "TNEGQ", nil,
		int64(-1), nil, nil, nil, nil, nil, nil,
		int64(1), int64(0), int64(0), nil, nil, nil, int64(0), nil, int64(1200), int64(1250),
	)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_order\s+WHERE trade_no = \$1\s+FOR UPDATE`).
		WithArgs("TNEGQ").
		WillReturnRows(row)
	mock.ExpectExec(`UPDATE v2_order SET status = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(9), int64(2), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	if err := service.handleQueuedOrder(context.Background(), "TNEGQ"); err != nil {
		t.Fatalf("quarantine negative queued order: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func testInt64Pointer(value int64) *int64 {
	return &value
}
