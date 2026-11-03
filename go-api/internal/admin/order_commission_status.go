package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrOrderCommissionInvalid = errors.New("佣金状态只能设置为 0（待确认）、1（发放中）或 3（已驳回）；2（已发放）由结算任务维护")
var ErrOrderCommissionSettled = errors.New("该订单已有佣金结算记录，不能重置发放状态；隐藏返佣记录不撤回余额，也不能重新发放")

func editableOrderCommissionStatus(value int64) bool {
	return value == 0 || value == 1 || value == 3
}

func (s *DBService) updateOrderCommissionStatus(ctx context.Context, req OrderUpdateRequest) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrUnavailable
	}
	req.TradeNo = strings.TrimSpace(req.TradeNo)
	if req.TradeNo == "" || req.CommissionStatus == nil || !editableOrderCommissionStatus(*req.CommissionStatus) {
		return false, ErrOrderCommissionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin order commission status update: %w", err)
	}
	defer tx.Rollback()
	// Settlement acquires this same order lock before changing balance/history.
	// Query ledger after locking: a subquery in the locking SELECT could still
	// see a pre-wait statement snapshot during a concurrent settlement commit.
	var current, credited int64
	err = tx.QueryRowContext(ctx, `SELECT commission_status, COALESCE(actual_commission_balance, 0)
FROM v2_order WHERE trade_no = $1 FOR UPDATE`, req.TradeNo).Scan(&current, &credited)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("订单不存在")
		}
		return false, fmt.Errorf("lock order commission status: %w", err)
	}
	if current == 2 || credited > 0 {
		return false, ErrOrderCommissionSettled
	}
	var hasLedger bool
	// Do not filter hidden rows: hiding a log is not a reversal.
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM v2_commission_log WHERE trade_no = $1)`, req.TradeNo).Scan(&hasLedger); err != nil {
		return false, fmt.Errorf("check order commission history: %w", err)
	}
	if hasLedger {
		return false, ErrOrderCommissionSettled
	}
	if !editableOrderCommissionStatus(current) {
		return false, ErrOrderCommissionInvalid
	}
	if current != *req.CommissionStatus {
		if _, err = tx.ExecContext(ctx, `UPDATE v2_order
SET commission_status = $2, updated_at = $3
WHERE trade_no = $1`, req.TradeNo, *req.CommissionStatus, time.Now().Unix()); err != nil {
			return false, fmt.Errorf("update order commission status: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit order commission status: %w", err)
	}
	// Repeated same-target requests are no-ops, not repeated balance effects.
	return true, nil
}
