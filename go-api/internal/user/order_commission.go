package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *DBService) handleQueuedCommission(ctx context.Context, tradeNo string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin commission transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	order, ok, err := s.lockOrderByTradeNoTx(ctx, tx, tradeNo)
	if err != nil {
		return err
	}
	if !ok || order.Status != 3 || !order.InviteUserID.Valid || order.CommissionBalance <= 0 {
		return nil
	}

	now := time.Now().Unix()
	cfg := s.currentConfig()
	paidAt := order.CreatedAt
	if order.PaidAt.Valid && order.PaidAt.Int64 > 0 {
		paidAt = order.PaidAt.Int64
	}
	if order.CommissionStatus == 0 {
		if !cfg.CommissionAutoCheckEnable {
			return nil
		}
		delayMinutes := cfg.CommissionAutoCheckMinutes
		if delayMinutes < 0 {
			delayMinutes = 0
		}
		if paidAt > now-(delayMinutes*60) {
			return nil
		}
		order.CommissionStatus = 1
	}
	if order.CommissionStatus != 1 {
		return nil
	}

	inviter, err := s.lockUserTx(ctx, tx, order.InviteUserID.Int64)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	actualAmount := order.CommissionBalance
	if cfg.WithdrawCloseEnable {
		inviter.Balance += actualAmount
	} else {
		inviter.CommissionBalance += actualAmount
	}
	if err := s.updateUserSubscriptionTx(ctx, tx, inviter); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_commission_log (invite_user_id, user_id, trade_no, order_amount, get_amount, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		order.InviteUserID.Int64,
		order.UserID,
		order.TradeNo,
		order.TotalAmount,
		actualAmount,
		now,
		now,
	); err != nil {
		return fmt.Errorf("insert commission log: %w", err)
	}

	order.CommissionStatus = 2
	order.ActualCommissionBalance = sql.NullInt64{Int64: actualAmount, Valid: true}
	order.UpdatedAt = now
	if err := s.updateOrderCommissionStateTx(ctx, tx, order); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit commission transaction: %w", err)
	}
	return nil
}
