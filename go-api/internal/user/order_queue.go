package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const orderHandleBatchLimit = 500
const trafficResetMarkerTTLSeconds = 400 * 24 * 3600

type TrafficResetSweepResult struct {
	Scanned    int64
	Reset      int64
	MarkedOnly int64
	Skipped    int64
}

type trafficResetCandidate struct {
	UserID                 int64
	PlanID                 int64
	U                      int64
	D                      int64
	ExpiredAt              int64
	UpdatedAt              int64
	PlanResetTrafficMethod sql.NullInt64
}

type trafficResetCycle struct {
	Marker  string
	StartAt int64
}

type trafficResetApplyResult int

const (
	trafficResetApplySkipped trafficResetApplyResult = iota
	trafficResetApplyMarkedOnly
	trafficResetApplyReset
)

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
	if cfg.CommissionAutoCheckEnable {
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
	}

	if err := s.cleanupExpiredOrders(ctx); err != nil {
		return err
	}
	if _, err := s.runTrafficResetSweep(ctx, true, false); err != nil {
		return err
	}
	return nil
}

func (s *DBService) SweepTrafficResets(ctx context.Context) (TrafficResetSweepResult, error) {
	return s.runTrafficResetSweep(ctx, false, true)
}

func (s *DBService) runTrafficResetSweep(ctx context.Context, limited bool, forceBackfill bool) (TrafficResetSweepResult, error) {
	now := time.Now()
	query := `SELECT u.id, u.plan_id, u.u, u.d, COALESCE(u.expired_at, 0), u.updated_at, p.reset_traffic_method
FROM v2_user u
JOIN v2_plan p ON p.id = u.plan_id
WHERE u.plan_id IS NOT NULL AND (u.expired_at > $1 OR u.expired_at IS NULL OR u.expired_at <= 0)
ORDER BY u.id ASC`

	var (
		rows   *sql.Rows
		err    error
		result TrafficResetSweepResult
	)
	if limited {
		rows, err = s.db.QueryContext(ctx, query+`
LIMIT $2`, now.Unix(), orderHandleBatchLimit)
	} else {
		rows, err = s.db.QueryContext(ctx, query, now.Unix())
	}
	if err != nil {
		return TrafficResetSweepResult{}, fmt.Errorf("query traffic reset candidates: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item trafficResetCandidate
		if err := rows.Scan(
			&item.UserID,
			&item.PlanID,
			&item.U,
			&item.D,
			&item.ExpiredAt,
			&item.UpdatedAt,
			&item.PlanResetTrafficMethod,
		); err != nil {
			return TrafficResetSweepResult{}, fmt.Errorf("scan traffic reset candidate: %w", err)
		}
		result.Scanned++
		applied, err := s.applyTrafficResetCandidate(ctx, item, now, forceBackfill)
		if err != nil {
			return TrafficResetSweepResult{}, err
		}
		switch applied {
		case trafficResetApplyReset:
			result.Reset++
		case trafficResetApplyMarkedOnly:
			result.MarkedOnly++
		default:
			result.Skipped++
		}
	}
	if err := rows.Err(); err != nil {
		return TrafficResetSweepResult{}, fmt.Errorf("iterate traffic reset candidates: %w", err)
	}
	return result, nil
}

func (s *DBService) applyTrafficResetCandidate(ctx context.Context, item trafficResetCandidate, now time.Time, forceBackfill bool) (trafficResetApplyResult, error) {
	method := s.runtimeValues().ResetTrafficMethod
	if item.PlanResetTrafficMethod.Valid {
		method = item.PlanResetTrafficMethod.Int64
	}
	cycle, ok := trafficResetCurrentCycle(item.PlanID, method, item.ExpiredAt, now)
	if !ok {
		return trafficResetApplySkipped, nil
	}

	markerKey := trafficResetCycleKVKey(item.UserID)
	lastMarker, found, err := s.kvGet(ctx, markerKey)
	if err != nil {
		return trafficResetApplySkipped, fmt.Errorf("load traffic reset marker for user %d: %w", item.UserID, err)
	}
	if found && strings.TrimSpace(lastMarker) == cycle.Marker {
		return trafficResetApplySkipped, nil
	}

	applied := trafficResetApplyMarkedOnly
	if (forceBackfill || item.UpdatedAt < cycle.StartAt) && (item.U > 0 || item.D > 0) {
		if err := s.resetUserTrafficUsage(ctx, item.UserID); err != nil {
			return trafficResetApplySkipped, err
		}
		applied = trafficResetApplyReset
	}
	if err := s.kvSet(ctx, markerKey, cycle.Marker, trafficResetMarkerTTLSeconds); err != nil {
		return trafficResetApplySkipped, fmt.Errorf("save traffic reset marker for user %d: %w", item.UserID, err)
	}
	return applied, nil
}

func (s *DBService) resetUserTrafficUsage(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE v2_user SET u = 0, d = 0, updated_at = $2 WHERE id = $1`, userID, time.Now().Unix()); err != nil {
		return fmt.Errorf("reset user traffic usage: %w", err)
	}
	return nil
}

func trafficResetCycleKVKey(userID int64) string {
	return fmt.Sprintf("TRAFFIC_RESET_CYCLE_USER_%d", userID)
}

func trafficResetCurrentCycle(planID, method, expiredAt int64, now time.Time) (trafficResetCycle, bool) {
	if expiredAt <= 0 && method != 0 && method != 3 {
		return trafficResetCycle{}, false
	}

	var start time.Time
	switch method {
	case 0:
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	case 1:
		start = trafficResetMonthlyAnchor(expiredAt, now)
	case 2:
		return trafficResetCycle{}, false
	case 3:
		start = time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, now.Location())
	case 4:
		start = trafficResetYearlyAnchor(expiredAt, now)
	default:
		return trafficResetCycle{}, false
	}

	return trafficResetCycle{
		Marker:  fmt.Sprintf("plan:%d|method:%d|start:%d", planID, method, start.Unix()),
		StartAt: start.Unix(),
	}, true
}

func trafficResetMonthlyAnchor(expiredAt int64, now time.Time) time.Time {
	target := time.Unix(expiredAt, 0).In(now.Location())
	candidate := trafficResetClippedDate(now.Year(), now.Month(), target.Day(), now.Location())
	if candidate.After(now) {
		prev := now.AddDate(0, -1, 0)
		return trafficResetClippedDate(prev.Year(), prev.Month(), target.Day(), now.Location())
	}
	return candidate
}

func trafficResetYearlyAnchor(expiredAt int64, now time.Time) time.Time {
	target := time.Unix(expiredAt, 0).In(now.Location())
	candidate := trafficResetClippedDate(now.Year(), target.Month(), target.Day(), now.Location())
	if candidate.After(now) {
		return trafficResetClippedDate(now.Year()-1, target.Month(), target.Day(), now.Location())
	}
	return candidate
}

func trafficResetClippedDate(year int, month time.Month, day int, loc *time.Location) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, loc)
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

type orderCleanupCandidate struct {
	ID              int64
	UserID          int64
	Status          int64
	SurplusOrderIDs sql.NullString
}

type latestCompletedOrderRef struct {
	ID            int64
	UpdatedAt     int64
	ProtectedRefs map[int64]struct{}
}

func (s *DBService) cleanupExpiredOrders(ctx context.Context) error {
	cfg := s.currentConfig()
	if cfg.OrderKeepDays <= 0 {
		return nil
	}

	cutoff := time.Now().Unix() - (cfg.OrderKeepDays * 86400)
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, status, surplus_order_ids
FROM v2_order
WHERE updated_at <= $2 AND status IN (2, 3, 4)
ORDER BY updated_at ASC
LIMIT $1`, orderHandleBatchLimit, cutoff)
	if err != nil {
		return fmt.Errorf("query order cleanup candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]orderCleanupCandidate, 0)
	for rows.Next() {
		var item orderCleanupCandidate
		if err := rows.Scan(&item.ID, &item.UserID, &item.Status, &item.SurplusOrderIDs); err != nil {
			return fmt.Errorf("scan order cleanup candidate: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate order cleanup candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil
	}

	latestCache := make(map[int64]latestCompletedOrderRef)
	deletable := make([]int64, 0, len(candidates))
	for _, item := range candidates {
		if item.Status == 2 {
			deletable = append(deletable, item.ID)
			continue
		}

		latest, ok := latestCompletedOrderRef{}, false
		if cached, exists := latestCache[item.UserID]; exists {
			latest = cached
			ok = true
		} else {
			var err error
			latest, ok, err = s.latestCompletedOrderForCleanup(ctx, item.UserID)
			if err != nil {
				return err
			}
		}
		if ok {
			latestCache[item.UserID] = latest
		}
		if !ok {
			deletable = append(deletable, item.ID)
			continue
		}
		if latest.UpdatedAt <= cutoff {
			deletable = append(deletable, item.ID)
			continue
		}
		if item.ID == latest.ID {
			continue
		}
		if _, protected := latest.ProtectedRefs[item.ID]; protected {
			continue
		}
		deletable = append(deletable, item.ID)
	}
	if len(deletable) == 0 {
		return nil
	}

	query, args := buildInt64InQuery(`DELETE FROM v2_order WHERE id IN (%s)`, deletable)
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("cleanup expired orders: %w", err)
	}
	return nil
}

func (s *DBService) latestCompletedOrderForCleanup(ctx context.Context, userID int64) (latestCompletedOrderRef, bool, error) {
	var (
		item latestCompletedOrderRef
		raw  sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, updated_at, surplus_order_ids
FROM v2_order
WHERE user_id = $1 AND status = 3
ORDER BY COALESCE(paid_at, created_at) DESC, id DESC LIMIT 1`, userID).Scan(&item.ID, &item.UpdatedAt, &raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return latestCompletedOrderRef{}, false, nil
		}
		return latestCompletedOrderRef{}, false, fmt.Errorf("query latest completed order for cleanup: %w", err)
	}
	item.ProtectedRefs = make(map[int64]struct{})
	for _, id := range parseIDList(nullableString(raw)) {
		item.ProtectedRefs[id] = struct{}{}
	}
	return item, true, nil
}
