package user

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const orderHandleBatchLimit = 500

func (s *DBService) HandlePendingOrders(ctx context.Context) error {
	if s.db == nil {
		return ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT trade_no
FROM v2_order
WHERE status IN (0, 1)
ORDER BY created_at ASC
LIMIT $1`, orderHandleBatchLimit)
	if err != nil {
		return fmt.Errorf("query order handle candidates: %w", err)
	}
	defer rows.Close()

	tradeNos := make([]string, 0)
	for rows.Next() {
		var tradeNo string
		if err := rows.Scan(&tradeNo); err != nil {
			return fmt.Errorf("scan order handle candidate: %w", err)
		}
		tradeNos = append(tradeNos, tradeNo)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate order handle candidates: %w", err)
	}

	for _, tradeNo := range tradeNos {
		if err := s.handleQueuedOrder(ctx, tradeNo); err != nil {
			return err
		}
	}

	cfg := s.currentConfig()
	if !cfg.CommissionAutoCheckEnable {
		return nil
	}

	cutoff := time.Now().Unix() - (cfg.CommissionAutoCheckMinutes * 60)
	commissionRows, err := s.db.QueryContext(ctx, `SELECT trade_no
FROM v2_order
WHERE status = 3 AND commission_balance > 0 AND invite_user_id IS NOT NULL AND commission_status IN (0, 1)
AND paid_at <= $2
ORDER BY paid_at ASC
LIMIT $1`, orderHandleBatchLimit, cutoff)
	if err != nil {
		return fmt.Errorf("query commission handle candidates: %w", err)
	}
	defer commissionRows.Close()

	commissionTradeNos := make([]string, 0)
	for commissionRows.Next() {
		var tradeNo string
		if err := commissionRows.Scan(&tradeNo); err != nil {
			return fmt.Errorf("scan commission handle candidate: %w", err)
		}
		commissionTradeNos = append(commissionTradeNos, tradeNo)
	}
	if err := commissionRows.Err(); err != nil {
		return fmt.Errorf("iterate commission handle candidates: %w", err)
	}

	for _, tradeNo := range commissionTradeNos {
		if err := s.handleQueuedCommission(ctx, tradeNo); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBService) handleQueuedOrder(ctx context.Context, tradeNo string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin queued order transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	order, ok, err := s.lockOrderByTradeNoTx(ctx, tx, tradeNo)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	switch {
	case shouldAutoCancelOrder(order.Status, order.CreatedAt, time.Now().Unix()):
		order.Status = 2
		order.UpdatedAt = time.Now().Unix()
		if err := s.updateOrderStatusTx(ctx, tx, order); err != nil {
			return err
		}
		if order.BalanceAmount.Valid && order.BalanceAmount.Int64 > 0 {
			userRow, err := s.lockUserTx(ctx, tx, order.UserID)
			if err != nil {
				return err
			}
			userRow.Balance += order.BalanceAmount.Int64
			if err := s.updateUserBalanceTx(ctx, tx, order.UserID, userRow.Balance); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit queued cancel order: %w", err)
		}
		ttl := s.currentConfig().OrderCancelRecoverTTL
		if ttl <= 0 {
			ttl = 1800
		}
		_ = s.kvSet(ctx, cancelRecoveryKey(tradeNo), strconv.FormatInt(time.Now().Unix(), 10), ttl)
	case order.Status == 1:
		if err := s.openOrderTx(ctx, tx, &order); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit queued open order: %w", err)
		}
	default:
		return nil
	}

	return nil
}

func shouldAutoCancelOrder(status, createdAt, now int64) bool {
	return status == 0 && createdAt <= now-(2*3600)
}
