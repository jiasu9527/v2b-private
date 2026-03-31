package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type giftcardRow struct {
	ID          int64
	Code        string
	Type        int64
	Value       sql.NullInt64
	PlanID      sql.NullInt64
	LimitUse    sql.NullInt64
	UsedUserIDs sql.NullString
	StartedAt   int64
	EndedAt     int64
}

func (s *DBService) Transfer(ctx context.Context, userID, amount int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if amount < 1 {
		return false, errors.New("The transfer amount parameter is wrong")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin transfer transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	userRow, err := s.lockUserTx(ctx, tx, userID)
	if err != nil {
		return false, err
	}
	if amount > userRow.CommissionBalance {
		return false, errors.New("Insufficient commission balance")
	}

	order := orderDraft{
		UserID:      userID,
		PlanID:      0,
		Period:      "deposit",
		TradeNo:     generateTradeNo(),
		TotalAmount: amount,
		Status:      3,
	}
	if err := s.setOrderTypeTx(ctx, tx, userRow, &order); err != nil {
		return false, err
	}
	if err := s.setInviteTx(ctx, tx, userRow, &order); err != nil {
		return false, err
	}

	userRow.CommissionBalance -= amount
	userRow.Balance += amount

	order.TotalAmount = 0
	order.SurplusAmount = amount
	order.CallbackNo = sql.NullString{String: "佣金划转 Commission transfer", Valid: true}

	if err := s.insertOrderTx(ctx, tx, &order); err != nil {
		return false, errors.New("Transfer failed")
	}
	if err := s.updateUserSubscriptionTx(ctx, tx, userRow); err != nil {
		return false, errors.New("Transfer failed")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("Transfer failed")
	}

	return true, nil
}

func (s *DBService) NewPeriod(ctx context.Context, userID int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if !s.runtimeValues().AllowNewPeriod {
		return false, errors.New("Renewal is not allowed")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin new period transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	userRow, err := s.lockUserTx(ctx, tx, userID)
	if err != nil {
		return false, err
	}
	if userRow.TransferEnable > userRow.U+userRow.D {
		return false, errors.New("You have not used up your traffic, you cannot renew your subscription")
	}
	if !userRow.PlanID.Valid {
		return false, errors.New("You do not allow to renew the subscription")
	}

	plan, ok, err := s.loadPlanTx(ctx, tx, userRow.PlanID.Int64)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrPlanNotFound
	}

	resetDay := s.calculateResetDayForPlan(plan, userRow.ExpiredAt)
	if resetDay == nil {
		return false, errors.New("You do not allow to renew the subscription")
	}
	resetPeriod := s.calculateResetPeriodForPlan(plan, userRow.ExpiredAt)
	if resetPeriod == nil {
		return false, errors.New("You do not allow to renew the subscription")
	}

	day := *resetDay
	period := *resetPeriod
	switch period {
	case 1:
		day = 30
		period = 30
	case 30:
	case 12:
		day = 365
		period = 365
	case 365:
	default:
		return false, errors.New("Invalid reset period")
	}
	if day <= 0 {
		day = period
	}

	now := time.Now().Unix()
	if !userRow.ExpiredAt.Valid || (period+1)*86400 >= userRow.ExpiredAt.Int64-now {
		return false, errors.New("You do not have enough time to renew your subscription")
	}

	userRow.ExpiredAt = sql.NullInt64{Int64: userRow.ExpiredAt.Int64 - day*86400, Valid: true}
	userRow.U = 0
	userRow.D = 0
	if err := s.updateUserSubscriptionTx(ctx, tx, userRow); err != nil {
		return false, errors.New("Save failed")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("Save failed")
	}

	return true, nil
}

func (s *DBService) RedeemGiftcard(ctx context.Context, userID int64, code string) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("Giftcard cannot be empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin giftcard transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	userRow, err := s.lockUserTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	giftcard, ok, err := s.loadGiftcardTx(ctx, tx, strings.TrimSpace(code))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("The gift card does not exist")
	}

	now := time.Now().Unix()
	if giftcard.StartedAt > 0 && now < giftcard.StartedAt {
		return nil, errors.New("The gift card is not yet valid")
	}
	if giftcard.EndedAt > 0 && now > giftcard.EndedAt {
		return nil, errors.New("The gift card has expired")
	}
	if giftcard.LimitUse.Valid && giftcard.LimitUse.Int64 <= 0 {
		return nil, errors.New("The gift card usage limit has been reached")
	}

	usedUserIDs := parseIDList(giftcard.UsedUserIDs.String)
	if containsInt64(usedUserIDs, userRow.ID) {
		return nil, errors.New("The gift card has already been used by this user")
	}
	usedUserIDs = append(usedUserIDs, userRow.ID)

	giftcardValue := nullableInt64(giftcard.Value)
	switch giftcard.Type {
	case 1:
		userRow.Balance += giftcardValue
	case 2:
		if !userRow.ExpiredAt.Valid {
			return nil, errors.New("Not suitable gift card type")
		}
		if userRow.ExpiredAt.Int64 <= now {
			userRow.ExpiredAt = sql.NullInt64{Int64: now + giftcardValue*86400, Valid: true}
		} else {
			userRow.ExpiredAt = sql.NullInt64{Int64: userRow.ExpiredAt.Int64 + giftcardValue*86400, Valid: true}
		}
	case 3:
		userRow.TransferEnable += giftcardValue * trafficGB
	case 4:
		userRow.U = 0
		userRow.D = 0
	case 5:
		if userRow.PlanID.Valid && (!userRow.ExpiredAt.Valid || userRow.ExpiredAt.Int64 >= now) {
			return nil, errors.New("Not suitable gift card type")
		}
		if !giftcard.PlanID.Valid {
			return nil, ErrPlanNotFound
		}
		plan, ok, err := s.loadPlanTx(ctx, tx, giftcard.PlanID.Int64)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrPlanNotFound
		}
		userRow.PlanID = sql.NullInt64{Int64: plan.ID, Valid: true}
		userRow.GroupID = sql.NullInt64{Int64: plan.GroupID, Valid: true}
		userRow.TransferEnable = plan.TransferEnable * trafficGB
		userRow.DeviceLimit = plan.DeviceLimit
		userRow.U = 0
		userRow.D = 0
		if giftcardValue == 0 {
			userRow.ExpiredAt = sql.NullInt64{}
		} else {
			userRow.ExpiredAt = sql.NullInt64{Int64: now + giftcardValue*86400, Valid: true}
		}
	default:
		return nil, errors.New("Unknown gift card type")
	}

	if giftcard.LimitUse.Valid {
		giftcard.LimitUse = sql.NullInt64{Int64: giftcard.LimitUse.Int64 - 1, Valid: true}
	}

	if err := s.updateUserSubscriptionTx(ctx, tx, userRow); err != nil {
		return nil, errors.New("Save failed")
	}
	if err := s.updateGiftcardTx(ctx, tx, giftcard, usedUserIDs); err != nil {
		return nil, errors.New("Save failed")
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("Save failed")
	}

	return map[string]any{
		"type":  giftcard.Type,
		"value": nullInt64Any(giftcard.Value),
	}, nil
}

func (s *DBService) calculateResetDayForPlan(plan planRecord, expiredAt sql.NullInt64) *int64 {
	if !expiredAt.Valid || expiredAt.Int64 <= time.Now().Unix() {
		return nil
	}

	method := s.runtimeValues().ResetTrafficMethod
	if plan.ResetTrafficMethod.Valid {
		method = plan.ResetTrafficMethod.Int64
	}
	if method == 2 {
		return nil
	}

	var day int64
	switch method {
	case 0:
		day = calcResetDayByMonthFirstDay()
	case 1:
		day = calcResetDayByExpireDay(expiredAt.Int64)
	case 3:
		day = calcResetDayByYearFirstDay()
	case 4:
		day = calcResetDayByYearExpiredAt(expiredAt.Int64)
	default:
		return nil
	}
	return &day
}

func (s *DBService) calculateResetPeriodForPlan(plan planRecord, expiredAt sql.NullInt64) *int64 {
	if !expiredAt.Valid || expiredAt.Int64 <= time.Now().Unix() {
		return nil
	}

	method := s.runtimeValues().ResetTrafficMethod
	if plan.ResetTrafficMethod.Valid {
		method = plan.ResetTrafficMethod.Int64
	}
	if method == 2 {
		return nil
	}

	var period int64
	switch method {
	case 0:
		period = 1
	case 1:
		period = 30
	case 3:
		period = 12
	case 4:
		period = 365
	default:
		return nil
	}
	return &period
}

func (s *DBService) loadGiftcardTx(ctx context.Context, tx *sql.Tx, code string) (giftcardRow, bool, error) {
	var row giftcardRow
	err := tx.QueryRowContext(ctx, `SELECT
id, code, type, value, plan_id, limit_use, used_user_ids, started_at, ended_at
FROM v2_giftcard
WHERE code = $1
FOR UPDATE`, code).Scan(
		&row.ID,
		&row.Code,
		&row.Type,
		&row.Value,
		&row.PlanID,
		&row.LimitUse,
		&row.UsedUserIDs,
		&row.StartedAt,
		&row.EndedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return giftcardRow{}, false, nil
		}
		return giftcardRow{}, false, fmt.Errorf("query giftcard: %w", err)
	}
	return row, true, nil
}

func (s *DBService) updateGiftcardTx(ctx context.Context, tx *sql.Tx, giftcard giftcardRow, usedUserIDs []int64) error {
	encoded, err := json.Marshal(usedUserIDs)
	if err != nil {
		return fmt.Errorf("encode used giftcard users: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_giftcard
SET limit_use = $2, used_user_ids = $3, updated_at = $4
WHERE id = $1`,
		giftcard.ID,
		nullInt64Any(giftcard.LimitUse),
		string(encoded),
		time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("update giftcard: %w", err)
	}
	return nil
}
