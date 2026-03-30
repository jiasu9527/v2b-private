package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *DBService) MarkOrderPaid(ctx context.Context, tradeNo, callbackNo string, allowCancelled bool) error {
	if s.db == nil {
		return ErrUnavailable
	}

	tradeNo = strings.TrimSpace(tradeNo)
	callbackNo = strings.TrimSpace(callbackNo)
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

	switch order.Status {
	case 1, 3, 4:
		return nil
	case 2:
		if !allowCancelled {
			recoverable, err := s.canRecoverCancelledOrder(ctx, tradeNo)
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pay order transaction: %w", err)
	}

	_ = s.kvDelete(ctx, cancelRecoveryKey(tradeNo))
	_ = s.notifyOrderPaidAdmins(ctx, adminPaymentNotification{
		TradeNo:     order.TradeNo,
		TotalAmount: order.TotalAmount,
	})
	return nil
}

func (s *DBService) canRecoverCancelledOrder(ctx context.Context, tradeNo string) (bool, error) {
	_, ok, err := s.kvGet(ctx, cancelRecoveryKey(tradeNo))
	if err != nil {
		return false, err
	}
	return ok, nil
}

func (s *DBService) kvDelete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE k = $1`, key)
	if err != nil {
		return fmt.Errorf("delete runtime kv: %w", err)
	}
	return nil
}

func (s *DBService) lockOrderByTradeNoTx(ctx context.Context, tx *sql.Tx, tradeNo string) (orderRecord, bool, error) {
	var row orderRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,
total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,
surplus_order_ids, status, commission_balance, invite_user_id, invite_campaign_id,
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
		&row.CommissionBalance,
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
