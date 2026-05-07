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
	if order.PlanID <= 0 || order.Type == 9 || order.Period == "reset_price" {
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

	if err := s.rollbackCommissionForRefundTx(ctx, tx, order); err != nil {
		return err
	}
	restoredUser, err := s.rollbackUserSubscriptionForRefundTx(ctx, tx, userRow, order)
	if err != nil {
		return err
	}
	if err := s.updateUserSubscriptionTx(ctx, tx, restoredUser); err != nil {
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

func (s *DBService) rollbackUserSubscriptionForRefundTx(ctx context.Context, tx *sql.Tx, current userRecord, refunded orderRecord) (userRecord, error) {
	ids := parseIDList(nullableString(refunded.SurplusOrderIDs))
	if len(ids) > 0 {
		if err := s.restoreSurplusOrdersForRefundTx(ctx, tx, ids); err != nil {
			return userRecord{}, err
		}
		return s.rebuildSubscriptionFromOrderIDsTx(ctx, tx, current, ids)
	}

	switch refunded.Type {
	case 2:
		return rollbackRenewalSubscription(current, refunded)
	case 3:
		return s.rebuildPreviousSubscriptionBeforeRefundTx(ctx, tx, current, refunded)
	case 1:
		return clearSubscriptionForRefund(current), nil
	default:
		if refunded.Period == "onetime_price" {
			return clearSubscriptionForRefund(current), nil
		}
		return clearSubscriptionForRefund(current), nil
	}
}

func (s *DBService) restoreSurplusOrdersForRefundTx(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	parts := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
		parts = append(parts, fmt.Sprintf("$%d", len(args)))
	}
	args = append(args, time.Now().Unix())
	query := fmt.Sprintf(`UPDATE v2_order SET status = 3, updated_at = $%d WHERE status = 4 AND id IN (%s)`, len(args), strings.Join(parts, ","))
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("restore surplus orders: %w", err)
	}
	return nil
}

func (s *DBService) rebuildSubscriptionFromOrderIDsTx(ctx context.Context, tx *sql.Tx, current userRecord, ids []int64) (userRecord, error) {
	restored := current
	restored.U = 0
	restored.D = 0
	restored.TransferEnable = 0
	restored.DeviceLimit = sql.NullInt64{}
	restored.GroupID = sql.NullInt64{}
	restored.PlanID = sql.NullInt64{}
	restored.SpeedLimit = sql.NullInt64{}
	restored.ExpiredAt = sql.NullInt64{}

	parts := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		parts = append(parts, fmt.Sprintf("$%d", len(args)))
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT
id, plan_id, type, period, surplus_order_ids, paid_at, created_at
FROM v2_order
WHERE id IN (%s)
ORDER BY COALESCE(paid_at, created_at) ASC, id ASC`, strings.Join(parts, ",")), args...)
	if err != nil {
		return userRecord{}, fmt.Errorf("query refund replay orders by ids: %w", err)
	}
	defer rows.Close()

	planCache := make(map[int64]planRecord)
	for rows.Next() {
		var replay orderRecord
		if err := rows.Scan(&replay.ID, &replay.PlanID, &replay.Type, &replay.Period, &replay.SurplusOrderIDs, &replay.PaidAt, &replay.CreatedAt); err != nil {
			return userRecord{}, fmt.Errorf("scan refund replay order: %w", err)
		}

		plan, ok := planCache[replay.PlanID]
		if !ok {
			var err error
			plan, ok, err = s.loadPlanTx(ctx, tx, replay.PlanID)
			if err != nil {
				return userRecord{}, err
			}
			if !ok {
				return userRecord{}, ErrPlanNotFound
			}
			planCache[replay.PlanID] = plan
		}

		applyHistoricalOrder(&restored, replay, plan)
	}
	if err := rows.Err(); err != nil {
		return userRecord{}, fmt.Errorf("iterate refund replay orders: %w", err)
	}

	preserveUserUsage(current, &restored)
	return restored, nil
}

func (s *DBService) rebuildPreviousSubscriptionBeforeRefundTx(ctx context.Context, tx *sql.Tx, current userRecord, refunded orderRecord) (userRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
id, plan_id, type, period, surplus_order_ids, paid_at, created_at
FROM v2_order
WHERE user_id = $1 AND status = 3 AND id <> $2 AND plan_id > 0
AND COALESCE(paid_at, created_at) < $3
ORDER BY COALESCE(paid_at, created_at) ASC, id ASC`, refunded.UserID, refunded.ID, effectiveOrderAt(refunded))
	if err != nil {
		return userRecord{}, fmt.Errorf("query refund previous orders: %w", err)
	}
	defer rows.Close()

	restored := current
	restored.U = 0
	restored.D = 0
	restored.TransferEnable = 0
	restored.DeviceLimit = sql.NullInt64{}
	restored.GroupID = sql.NullInt64{}
	restored.PlanID = sql.NullInt64{}
	restored.SpeedLimit = sql.NullInt64{}
	restored.ExpiredAt = sql.NullInt64{}

	planCache := make(map[int64]planRecord)
	hasRows := false
	for rows.Next() {
		hasRows = true
		var replay orderRecord
		if err := rows.Scan(&replay.ID, &replay.PlanID, &replay.Type, &replay.Period, &replay.SurplusOrderIDs, &replay.PaidAt, &replay.CreatedAt); err != nil {
			return userRecord{}, fmt.Errorf("scan refund previous order: %w", err)
		}
		plan, ok := planCache[replay.PlanID]
		if !ok {
			var err error
			plan, ok, err = s.loadPlanTx(ctx, tx, replay.PlanID)
			if err != nil {
				return userRecord{}, err
			}
			if !ok {
				return userRecord{}, ErrPlanNotFound
			}
			planCache[replay.PlanID] = plan
		}
		applyHistoricalOrder(&restored, replay, plan)
	}
	if err := rows.Err(); err != nil {
		return userRecord{}, fmt.Errorf("iterate refund previous orders: %w", err)
	}
	if !hasRows {
		return clearSubscriptionForRefund(current), nil
	}

	preserveUserUsage(current, &restored)
	return restored, nil
}

func rollbackRenewalSubscription(current userRecord, refunded orderRecord) (userRecord, error) {
	months, ok := periodMonths(refunded.Period)
	if !ok {
		return userRecord{}, ErrRefundTargetNotSupported
	}
	if !current.ExpiredAt.Valid {
		return clearSubscriptionForRefund(current), nil
	}

	restored := current
	restoredAt := time.Unix(current.ExpiredAt.Int64, 0).AddDate(0, -months, 0).Unix()
	restored.ExpiredAt = sql.NullInt64{Int64: restoredAt, Valid: true}
	if restoredAt <= 0 {
		restored.ExpiredAt = sql.NullInt64{}
	}
	return restored, nil
}

func clearSubscriptionForRefund(current userRecord) userRecord {
	restored := current
	restored.U = 0
	restored.D = 0
	restored.TransferEnable = 0
	restored.DeviceLimit = sql.NullInt64{}
	restored.GroupID = sql.NullInt64{}
	restored.PlanID = sql.NullInt64{}
	restored.SpeedLimit = sql.NullInt64{}
	restored.ExpiredAt = sql.NullInt64{}
	return restored
}

func applyHistoricalOrder(userRow *userRecord, order orderRecord, plan planRecord) {
	effectiveAt := effectiveOrderAt(order)

	switch order.Period {
	case "onetime_price":
		transferEnable := plan.TransferEnable
		if !order.SurplusOrderIDs.Valid {
			notUsedTraffic := float64(userRow.TransferEnable-(userRow.U+userRow.D)) / float64(trafficGB)
			if notUsedTraffic > 0 && !userRow.ExpiredAt.Valid {
				transferEnable += int64(notUsedTraffic)
			}
		}
		userRow.U = 0
		userRow.D = 0
		userRow.TransferEnable = planTransferEnableBytes(transferEnable)
		userRow.DeviceLimit = plan.DeviceLimit
		userRow.PlanID = sql.NullInt64{Int64: plan.ID, Valid: true}
		userRow.GroupID = sql.NullInt64{Int64: plan.GroupID, Valid: true}
		userRow.ExpiredAt = sql.NullInt64{}
	case "reset_price":
		userRow.U = 0
		userRow.D = 0
		if plan.SpeedLimit.Valid {
			userRow.SpeedLimit = plan.SpeedLimit
		} else {
			userRow.SpeedLimit = sql.NullInt64{}
		}
	default:
		if order.Type == 3 {
			userRow.ExpiredAt = sql.NullInt64{Int64: effectiveAt, Valid: true}
		}
		userRow.TransferEnable = planTransferEnableBytes(plan.TransferEnable)
		userRow.DeviceLimit = plan.DeviceLimit
		if !userRow.ExpiredAt.Valid || order.Type == 1 {
			userRow.U = 0
			userRow.D = 0
		}
		if order.Type == 2 && userRow.ExpiredAt.Valid {
			expireTime := time.Unix(userRow.ExpiredAt.Int64, 0)
			orderTime := time.Unix(effectiveAt, 0)
			if expireTime.Month() == orderTime.Month() && expireTime.Day() == orderTime.Day() {
				userRow.U = 0
				userRow.D = 0
			}
		}
		userRow.PlanID = sql.NullInt64{Int64: plan.ID, Valid: true}
		userRow.GroupID = sql.NullInt64{Int64: plan.GroupID, Valid: true}
		userRow.ExpiredAt = sql.NullInt64{Int64: nextExpiredAtAt(order.Period, userRow.ExpiredAt, effectiveAt), Valid: true}
	}
	if plan.SpeedLimit.Valid {
		userRow.SpeedLimit = plan.SpeedLimit
	} else {
		userRow.SpeedLimit = sql.NullInt64{}
	}
}

func nextExpiredAtAt(period string, expiredAt sql.NullInt64, effectiveAt int64) int64 {
	base := time.Unix(effectiveAt, 0)
	if expiredAt.Valid && expiredAt.Int64 > effectiveAt {
		base = time.Unix(expiredAt.Int64, 0)
	}
	months, ok := periodMonths(period)
	if !ok {
		return base.Unix()
	}
	return base.AddDate(0, months, 0).Unix()
}

func effectiveOrderAt(order orderRecord) int64 {
	if order.PaidAt.Valid && order.PaidAt.Int64 > 0 {
		return order.PaidAt.Int64
	}
	return order.CreatedAt
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func preserveUserUsage(current userRecord, restored *userRecord) {
	if restored.TransferEnable <= 0 {
		restored.U = 0
		restored.D = 0
		return
	}

	u := current.U
	if u > restored.TransferEnable {
		u = restored.TransferEnable
	}
	remaining := restored.TransferEnable - u
	d := current.D
	if d > remaining {
		d = remaining
	}

	restored.U = u
	restored.D = d
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

		inviter.CommissionBalance -= rollbackAmount
		if inviter.CommissionBalance < 0 && inviter.Balance > 0 {
			recovered := minInt64(inviter.Balance, -inviter.CommissionBalance)
			inviter.Balance -= recovered
			inviter.CommissionBalance += recovered
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
