package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *DBService) MarkOrderPaid(ctx context.Context, tradeNo string, confirmation OrderPaymentConfirmation) error {
	if s.db == nil {
		return ErrUnavailable
	}

	tradeNo = strings.TrimSpace(tradeNo)
	callbackNo := strings.TrimSpace(confirmation.CallbackNo)
	if tradeNo == "" || callbackNo == "" {
		return ErrInvalidParameter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pay order transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	order, ok, err := s.lockOrderByTradeNoTx(ctx, tx, tradeNo)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOrderNotFound
	}
	if err := validateOrderPaymentConfirmation(order, confirmation); err != nil {
		return err
	}
	if err := s.lockPaymentCallbackTx(ctx, tx, tradeNo, callbackNo, confirmation.PaymentID); err != nil {
		return err
	}

	switch order.Status {
	case 1, 3, 4:
		storedCallback := strings.TrimSpace(order.CallbackNo.String)
		if order.CallbackNo.Valid && storedCallback == callbackNo {
			return nil
		}
		// Older runtimes replaced the provider transaction number with trade_no
		// while opening an order, and some migrated rows have no callback at all.
		// Repair only that known legacy shape after the payment method and amount
		// have been re-verified. This path never reopens or credits the order.
		if confirmation.PaymentID != nil && (!order.CallbackNo.Valid || storedCallback == "" || storedCallback == order.TradeNo) {
			order.CallbackNo = sql.NullString{String: callbackNo, Valid: true}
			order.UpdatedAt = time.Now().Unix()
			if err := s.updateOrderPaymentStateTx(ctx, tx, order); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit legacy callback repair: %w", err)
			}
			return nil
		}
		return ErrPaymentConfirmationMismatch
	case 2:
		if !confirmation.AllowCancelled {
			recoverable, err := s.canRecoverCancelledOrderTx(ctx, tx, tradeNo)
			if err != nil {
				return err
			}
			if !recoverable {
				return ErrOrderPaidOrMissing
			}
		}
		if order.BalanceAmount.Valid && order.BalanceAmount.Int64 > 0 {
			userRow, err := s.lockUserTx(ctx, tx, order.UserID)
			if err != nil {
				return err
			}
			userRow.Balance -= order.BalanceAmount.Int64
			if err := s.updateUserBalanceTx(ctx, tx, order.UserID, userRow.Balance); err != nil {
				return err
			}
		}
	case 0:
	default:
		return ErrOrderPaidOrMissing
	}

	now := time.Now().Unix()
	order.CallbackNo = sql.NullString{String: callbackNo, Valid: true}
	order.PaidAt = sql.NullInt64{Int64: now, Valid: true}
	if err := s.openOrderTx(ctx, tx, &order); err != nil {
		return err
	}
	if err := deleteCancelRecoveryTx(ctx, tx, tradeNo); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pay order transaction: %w", err)
	}

	_ = s.notifyOrderPaidAdmins(ctx, adminPaymentNotification{
		TradeNo:     order.TradeNo,
		TotalAmount: order.TotalAmount,
		PaymentID:   order.PaymentID,
		PaidAt:      order.PaidAt,
	})
	return nil
}

func validateOrderPaymentConfirmation(order orderRecord, confirmation OrderPaymentConfirmation) error {
	handlingAmount := nullableInt64(order.HandlingAmount)
	if !validOrderAmountState(order) {
		return ErrPaymentConfirmationMismatch
	}
	if confirmation.Manual {
		if confirmation.PaymentID != nil || confirmation.Amount != nil {
			return ErrPaymentConfirmationMismatch
		}
		return nil
	}
	if confirmation.PaymentID == nil {
		// The only non-manual confirmation without a provider is an actually
		// free order. Do not let a future internal caller accidentally open a
		// positive order by omitting gateway evidence.
		if confirmation.Amount != nil || order.TotalAmount != 0 || handlingAmount != 0 {
			return ErrPaymentConfirmationMismatch
		}
		return nil
	}

	if *confirmation.PaymentID <= 0 || confirmation.Amount == nil || *confirmation.Amount <= 0 || order.TotalAmount <= 0 || !order.PaymentID.Valid || order.PaymentID.Int64 != *confirmation.PaymentID {
		return ErrPaymentConfirmationMismatch
	}
	if order.TotalAmount+handlingAmount != *confirmation.Amount {
		return ErrPaymentConfirmationMismatch
	}
	return nil
}

func (s *DBService) lockPaymentCallbackTx(ctx context.Context, tx *sql.Tx, tradeNo, callbackNo string, paymentID *int64) error {
	lockKey := "internal:" + callbackNo
	if paymentID != nil {
		lockKey = fmt.Sprintf("payment:%d:%s", *paymentID, callbackNo)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0::bigint))`, lockKey); err != nil {
		return fmt.Errorf("lock payment callback: %w", err)
	}
	// Internal confirmations derive their callback from the globally unique
	// station order number. Gateway transaction numbers are only guaranteed to
	// be unique inside one configured payment channel, so scope the conflict
	// check to payment_id instead of treating callback_no as globally unique.
	if paymentID == nil {
		return nil
	}

	var conflict bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM v2_order WHERE payment_id = $1 AND callback_no = $2 AND trade_no <> $3
)`, *paymentID, callbackNo, tradeNo).Scan(&conflict); err != nil {
		return fmt.Errorf("check payment callback conflict: %w", err)
	}
	if conflict {
		return ErrPaymentCallbackConflict
	}
	return nil
}

func (s *DBService) canRecoverCancelledOrderTx(ctx context.Context, tx *sql.Tx, tradeNo string) (bool, error) {
	var recoverable bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM v2_runtime_kv
WHERE k = $1 AND (expire_at = 0 OR expire_at > $2)
)`, cancelRecoveryKey(tradeNo), time.Now().Unix()).Scan(&recoverable); err != nil {
		return false, fmt.Errorf("query cancel recovery state: %w", err)
	}
	return recoverable, nil
}

func deleteCancelRecoveryTx(ctx context.Context, tx *sql.Tx, tradeNo string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE k = $1`, cancelRecoveryKey(tradeNo))
	if err != nil {
		return fmt.Errorf("delete cancel recovery state: %w", err)
	}
	return nil
}

func setCancelRecoveryTx(ctx context.Context, tx *sql.Tx, tradeNo string, ttl int64) error {
	now := time.Now().Unix()
	expireAt := int64(0)
	if ttl > 0 {
		expireAt = now + ttl
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
		cancelRecoveryKey(tradeNo),
		fmt.Sprintf("%d", now),
		expireAt,
		now,
	); err != nil {
		return fmt.Errorf("set cancel recovery state: %w", err)
	}
	return nil
}

func (s *DBService) lockOrderByTradeNoTx(ctx context.Context, tx *sql.Tx, tradeNo string) (orderRecord, bool, error) {
	var row orderRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,
total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,
surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,
invite_campaign_discount_amount, paid_at, created_at, updated_at
FROM v2_order
WHERE trade_no = $1
FOR UPDATE`, tradeNo).Scan(
		&row.ID,
		&row.UserID,
		&row.PlanID,
		&row.CouponID,
		&row.PaymentID,
		&row.Type,
		&row.Period,
		&row.TradeNo,
		&row.CallbackNo,
		&row.TotalAmount,
		&row.HandlingAmount,
		&row.DiscountAmount,
		&row.SurplusAmount,
		&row.RefundAmount,
		&row.BalanceAmount,
		&row.SurplusOrderIDs,
		&row.Status,
		&row.CommissionStatus,
		&row.CommissionBalance,
		&row.ActualCommissionBalance,
		&row.InviteUserID,
		&row.InviteCampaignID,
		&row.InviteCampaignDiscountAmount,
		&row.PaidAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return orderRecord{}, false, nil
		}
		return orderRecord{}, false, fmt.Errorf("lock order by trade no: %w", err)
	}
	return row, true, nil
}
