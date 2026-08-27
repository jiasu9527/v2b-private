package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
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

	var (
		attempt      orderPaymentAttempt
		attemptFound bool
	)
	if confirmation.PaymentID != nil {
		attempt, attemptFound, err = s.lockOrderPaymentAttemptTx(ctx, tx, order.ID, *confirmation.PaymentID)
		if err != nil {
			return err
		}
	}

	currentConfirmationErr := validateOrderPaymentConfirmation(order, confirmation)
	if attemptFound {
		if err := validateOrderPaymentAttemptConfirmation(order, attempt, confirmation); err != nil {
			return err
		}
	}

	switch order.Status {
	case 1, 3, 4:
		if currentConfirmationErr != nil {
			if !attemptFound {
				return currentConfirmationErr
			}
			if err := s.lockPaymentCallbackTx(ctx, tx, tradeNo, callbackNo, confirmation.PaymentID); err != nil {
				return err
			}
			switch attempt.Status {
			case paymentAttemptPending:
				now := time.Now().Unix()
				if err := markOrderPaymentAttemptTx(ctx, tx, attempt.ID, callbackNo, paymentAttemptDuplicate, now); err != nil {
					return err
				}
				if err := tx.Commit(); err != nil {
					return fmt.Errorf("commit duplicate payment attempt: %w", err)
				}
				log.Printf("duplicate payment recorded for manual refund trade_no=%q payment_id=%d callback_no=%q amount=%d", tradeNo, attempt.PaymentID, callbackNo, attempt.Amount)
				return nil
			case paymentAttemptDuplicate:
				if attempt.CallbackNo.Valid && strings.TrimSpace(attempt.CallbackNo.String) == callbackNo {
					return nil
				}
				return fmt.Errorf("%w: duplicate payment attempt callback differs", ErrPaymentConfirmationMismatch)
			default:
				return fmt.Errorf("%w: completed order references an inconsistent payment attempt", ErrPaymentConfirmationMismatch)
			}
		}
		if err := s.lockPaymentCallbackTx(ctx, tx, tradeNo, callbackNo, confirmation.PaymentID); err != nil {
			return err
		}
		storedCallback := strings.TrimSpace(order.CallbackNo.String)
		if order.CallbackNo.Valid && storedCallback == callbackNo {
			if attemptFound {
				switch attempt.Status {
				case paymentAttemptPending:
					now := time.Now().Unix()
					if err := markOrderPaymentAttemptTx(ctx, tx, attempt.ID, callbackNo, paymentAttemptWinner, now); err != nil {
						return err
					}
					if err := tx.Commit(); err != nil {
						return fmt.Errorf("commit payment attempt replay repair: %w", err)
					}
				case paymentAttemptWinner:
					if !attempt.CallbackNo.Valid || strings.TrimSpace(attempt.CallbackNo.String) != callbackNo {
						return fmt.Errorf("%w: winning payment attempt callback differs", ErrPaymentConfirmationMismatch)
					}
				default:
					return fmt.Errorf("%w: winning payment attempt has invalid status", ErrPaymentConfirmationMismatch)
				}
			}
			return nil
		}
		// Older runtimes replaced the provider transaction number with trade_no
		// while opening an order, and some migrated rows have no callback at all.
		// Repair only that known legacy shape after the payment method and amount
		// have been re-verified. This path never reopens or credits the order.
		if confirmation.PaymentID != nil && (!order.CallbackNo.Valid || storedCallback == "" || storedCallback == order.TradeNo) {
			now := time.Now().Unix()
			if attemptFound {
				switch attempt.Status {
				case paymentAttemptPending:
					if err := markOrderPaymentAttemptTx(ctx, tx, attempt.ID, callbackNo, paymentAttemptWinner, now); err != nil {
						return err
					}
				case paymentAttemptWinner:
					storedAttemptCallback := strings.TrimSpace(attempt.CallbackNo.String)
					if !attempt.CallbackNo.Valid || storedAttemptCallback == "" || storedAttemptCallback == order.TradeNo {
						if err := repairWinningPaymentAttemptCallbackTx(ctx, tx, attempt.ID, order.TradeNo, callbackNo, now); err != nil {
							return err
						}
					} else if storedAttemptCallback != callbackNo {
						return fmt.Errorf("%w: winning payment attempt callback differs", ErrPaymentConfirmationMismatch)
					}
				default:
					return fmt.Errorf("%w: winning payment attempt has invalid status", ErrPaymentConfirmationMismatch)
				}
			}
			order.CallbackNo = sql.NullString{String: callbackNo, Valid: true}
			order.UpdatedAt = now
			if err := s.updateOrderPaymentStateTx(ctx, tx, order); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit legacy callback repair: %w", err)
			}
			return nil
		}
		return fmt.Errorf("%w: completed order callback differs (status=%d)", ErrPaymentConfirmationMismatch, order.Status)
	case 2:
		if currentConfirmationErr != nil {
			if !attemptFound || attempt.Status != paymentAttemptPending {
				return currentConfirmationErr
			}
			order = orderWithPaymentAttempt(order, attempt)
		}
		if err := validateOrderPaymentConfirmation(order, confirmation); err != nil {
			return err
		}
		if err := s.lockPaymentCallbackTx(ctx, tx, tradeNo, callbackNo, confirmation.PaymentID); err != nil {
			return err
		}
		if !confirmation.AllowCancelled {
			recoverable, err := s.canRecoverCancelledOrderTx(ctx, tx, tradeNo)
			if err != nil {
				return err
			}
			if !recoverable {
				return fmt.Errorf("%w: cancelled order recovery window expired", ErrOrderPaidOrMissing)
			}
		}
		if order.BalanceAmount.Valid && order.BalanceAmount.Int64 > 0 {
			userRow, err := s.lockUserTx(ctx, tx, order.UserID)
			if err != nil {
				return err
			}
			userRow.Balance -= order.BalanceAmount.Int64
			if err := s.updateUserBalanceTx(ctx, tx, order.UserID, userRow.Balance); err != nil {
				return fmt.Errorf("recover cancelled payment balance refund: %w", err)
			}
		}
	case 0:
		if currentConfirmationErr != nil {
			if !attemptFound || attempt.Status != paymentAttemptPending {
				return currentConfirmationErr
			}
			order = orderWithPaymentAttempt(order, attempt)
		}
		if err := validateOrderPaymentConfirmation(order, confirmation); err != nil {
			return err
		}
		if err := s.lockPaymentCallbackTx(ctx, tx, tradeNo, callbackNo, confirmation.PaymentID); err != nil {
			return err
		}
	default:
		return ErrOrderPaidOrMissing
	}

	now := time.Now().Unix()
	if attemptFound {
		if attempt.Status != paymentAttemptPending {
			return fmt.Errorf("%w: payment attempt is not pending", ErrPaymentConfirmationMismatch)
		}
		if err := markOrderPaymentAttemptTx(ctx, tx, attempt.ID, callbackNo, paymentAttemptWinner, now); err != nil {
			return err
		}
	}
	if confirmation.PaymentID != nil && (!order.PaymentID.Valid || order.PaymentID.Int64 != *confirmation.PaymentID || nullableInt64(order.HandlingAmount) != *confirmation.Amount-order.TotalAmount) {
		return fmt.Errorf("%w: rebound payment state is inconsistent", ErrPaymentConfirmationMismatch)
	}
	if attemptFound && currentConfirmationErr != nil {
		if err := s.updateOrderPaymentTx(ctx, tx, order); err != nil {
			return err
		}
	}
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

	s.dispatchOrderPaidAdminNotification(adminPaymentNotification{
		TradeNo:     order.TradeNo,
		TotalAmount: order.TotalAmount,
		PaymentID:   order.PaymentID,
		PaidAt:      order.PaidAt,
	})
	return nil
}

const (
	paymentAttemptPending   int64 = 0
	paymentAttemptWinner    int64 = 1
	paymentAttemptDuplicate int64 = 2
)

type orderPaymentAttempt struct {
	ID             int64
	OrderID        int64
	PaymentID      int64
	HandlingAmount int64
	Amount         int64
	CallbackNo     sql.NullString
	Status         int64
	PaidAt         sql.NullInt64
}

func (s *DBService) lockOrderPaymentAttemptTx(ctx context.Context, tx *sql.Tx, orderID, paymentID int64) (orderPaymentAttempt, bool, error) {
	var attempt orderPaymentAttempt
	err := tx.QueryRowContext(ctx, `SELECT id, order_id, payment_id, handling_amount, amount, callback_no, status, paid_at
FROM v2_order_payment_attempt
WHERE order_id = $1 AND payment_id = $2
FOR UPDATE`, orderID, paymentID).Scan(
		&attempt.ID,
		&attempt.OrderID,
		&attempt.PaymentID,
		&attempt.HandlingAmount,
		&attempt.Amount,
		&attempt.CallbackNo,
		&attempt.Status,
		&attempt.PaidAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return orderPaymentAttempt{}, false, nil
		}
		return orderPaymentAttempt{}, false, fmt.Errorf("lock order payment attempt: %w", err)
	}
	return attempt, true, nil
}

func validateOrderPaymentAttemptConfirmation(order orderRecord, attempt orderPaymentAttempt, confirmation OrderPaymentConfirmation) error {
	if confirmation.PaymentID == nil || confirmation.Amount == nil ||
		attempt.OrderID != order.ID || attempt.PaymentID != *confirmation.PaymentID ||
		attempt.Amount != *confirmation.Amount || attempt.HandlingAmount < 0 {
		return fmt.Errorf("%w: payment attempt differs from callback", ErrPaymentConfirmationMismatch)
	}
	candidate := orderWithPaymentAttempt(order, attempt)
	if err := validateOrderPaymentConfirmation(candidate, confirmation); err != nil {
		return err
	}
	return nil
}

func orderWithPaymentAttempt(order orderRecord, attempt orderPaymentAttempt) orderRecord {
	order.PaymentID = sql.NullInt64{Int64: attempt.PaymentID, Valid: true}
	order.HandlingAmount = sql.NullInt64{Int64: attempt.HandlingAmount, Valid: attempt.HandlingAmount != 0}
	return order
}

func markOrderPaymentAttemptTx(ctx context.Context, tx *sql.Tx, attemptID int64, callbackNo string, status, now int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE v2_order_payment_attempt
SET callback_no = $2, status = $3, paid_at = $4, updated_at = $4
WHERE id = $1 AND status = 0`, attemptID, callbackNo, status, now)
	if err != nil {
		return fmt.Errorf("mark order payment attempt: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count marked order payment attempt: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: payment attempt changed concurrently", ErrPaymentConfirmationMismatch)
	}
	return nil
}

func repairWinningPaymentAttemptCallbackTx(ctx context.Context, tx *sql.Tx, attemptID int64, tradeNo, callbackNo string, now int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE v2_order_payment_attempt
SET callback_no = $2, updated_at = $3
WHERE id = $1 AND status = 1
  AND (callback_no IS NULL OR BTRIM(callback_no) = '' OR callback_no = $4)`, attemptID, callbackNo, now, tradeNo)
	if err != nil {
		return fmt.Errorf("repair winning payment attempt callback: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count repaired winning payment attempt callback: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: winning payment attempt callback changed concurrently", ErrPaymentConfirmationMismatch)
	}
	return nil
}

func validateOrderPaymentConfirmation(order orderRecord, confirmation OrderPaymentConfirmation) error {
	handlingAmount := nullableInt64(order.HandlingAmount)
	if !validOrderAmountState(order) {
		return fmt.Errorf("%w: stored order amount state is invalid", ErrPaymentConfirmationMismatch)
	}
	if confirmation.Manual {
		if confirmation.PaymentID != nil || confirmation.Amount != nil {
			return fmt.Errorf("%w: manual confirmation contains gateway evidence", ErrPaymentConfirmationMismatch)
		}
		return nil
	}
	if confirmation.PaymentID == nil {
		// The only non-manual confirmation without a provider is an actually
		// free order. Do not let a future internal caller accidentally open a
		// positive order by omitting gateway evidence.
		if confirmation.Amount != nil || order.TotalAmount != 0 || handlingAmount != 0 {
			return fmt.Errorf("%w: positive order is missing gateway evidence", ErrPaymentConfirmationMismatch)
		}
		return nil
	}

	if *confirmation.PaymentID <= 0 || confirmation.Amount == nil || *confirmation.Amount <= 0 || order.TotalAmount <= 0 {
		return fmt.Errorf("%w: gateway confirmation amount or payment ID is invalid", ErrPaymentConfirmationMismatch)
	}
	if !order.PaymentID.Valid {
		return fmt.Errorf("%w: order has no selected payment method", ErrPaymentConfirmationMismatch)
	}
	if order.PaymentID.Int64 != *confirmation.PaymentID {
		return fmt.Errorf("%w: payment method differs (order=%d callback=%d)", ErrPaymentConfirmationMismatch, order.PaymentID.Int64, *confirmation.PaymentID)
	}
	if order.TotalAmount+handlingAmount != *confirmation.Amount {
		return fmt.Errorf("%w: amount differs (order=%d callback=%d)", ErrPaymentConfirmationMismatch, order.TotalAmount+handlingAmount, *confirmation.Amount)
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
UNION ALL
SELECT 1
FROM v2_order_payment_attempt AS attempt
JOIN v2_order AS attempt_order ON attempt_order.id = attempt.order_id
WHERE attempt.payment_id = $1 AND attempt.callback_no = $2 AND attempt_order.trade_no <> $3
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
