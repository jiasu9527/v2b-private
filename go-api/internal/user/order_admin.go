package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *DBService) RefundManagedOrder(ctx context.Context, tradeNo string) error {
	if s.db == nil {
		return ErrUnavailable
	}

	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return ErrInvalidParameter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin refund transaction: %w", err)
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
	if order.Status != 3 {
		return ErrRefundCompletedOnly
	}
	if order.PlanID <= 0 || order.Type == 9 {
		return ErrRefundTargetNotSupported
	}

	var hasLater bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_order WHERE user_id = $1 AND status = 3 AND id <> $2 AND created_at > $3)`,
		order.UserID, order.ID, order.CreatedAt).Scan(&hasLater); err != nil {
		return fmt.Errorf("query later completed order: %w", err)
	}
	if hasLater {
		return ErrRefundLatestOnly
	}

	userRow, err := s.lockUserTx(ctx, tx, order.UserID)
	if err != nil {
		return err
	}
	userRow.U = 0
	userRow.D = 0
	userRow.TransferEnable = 0
	userRow.DeviceLimit = sql.NullInt64{}
	userRow.GroupID = sql.NullInt64{}
	userRow.PlanID = sql.NullInt64{}
	userRow.SpeedLimit = sql.NullInt64{}
	userRow.ExpiredAt = sql.NullInt64{}

	if err := s.rollbackCommissionForRefundTx(ctx, tx, order); err != nil {
		return err
	}
	if err := s.updateUserSubscriptionTx(ctx, tx, userRow); err != nil {
		return err
	}

	order.Status = 2
	order.CommissionStatus = 3
	order.ActualCommissionBalance = sql.NullInt64{}
	order.UpdatedAt = time.Now().Unix()
	if err := s.updateOrderRefundStateTx(ctx, tx, order); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit refund transaction: %w", err)
	}
	return nil
}

func (s *DBService) rollbackCommissionForRefundTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	if !order.InviteUserID.Valid || order.CommissionBalance <= 0 {
		return nil
	}

	if order.CommissionStatus == 2 || (order.ActualCommissionBalance.Valid && order.ActualCommissionBalance.Int64 > 0) {
		inviter, err := s.lockUserTx(ctx, tx, order.InviteUserID.Int64)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}

		rollbackAmount := order.CommissionBalance
		if order.ActualCommissionBalance.Valid && order.ActualCommissionBalance.Int64 > 0 {
			rollbackAmount = order.ActualCommissionBalance.Int64
		}

		if inviter.CommissionBalance >= rollbackAmount {
			inviter.CommissionBalance -= rollbackAmount
		} else {
			remaining := rollbackAmount - inviter.CommissionBalance
			inviter.CommissionBalance = 0
			if inviter.Balance < remaining {
				return ErrCommissionRollbackInsufficient
			}
			inviter.Balance -= remaining
		}
		if err := s.updateUserSubscriptionTx(ctx, tx, inviter); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_commission_log WHERE trade_no = $1`, order.TradeNo); err != nil {
		return fmt.Errorf("delete commission log: %w", err)
	}
	return nil
}
