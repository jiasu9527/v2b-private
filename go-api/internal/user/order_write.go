package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const trafficGB = int64(1073741824)

func planTransferEnableBytes(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value >= trafficGB {
		return value
	}
	return value * trafficGB
}

var allowedOrderPeriods = map[string]struct{}{
	"month_price":      {},
	"quarter_price":    {},
	"half_year_price":  {},
	"year_price":       {},
	"two_year_price":   {},
	"three_year_price": {},
	"onetime_price":    {},
	"reset_price":      {},
	"deposit":          {},
}

type userRecord struct {
	ID                int64
	InviteUserID      sql.NullInt64
	Balance           int64
	CommissionBalance int64
	Discount          sql.NullInt64
	CommissionType    int64
	CommissionRate    sql.NullInt64
	U                 int64
	D                 int64
	TransferEnable    int64
	DeviceLimit       sql.NullInt64
	Banned            int64
	GroupID           sql.NullInt64
	PlanID            sql.NullInt64
	SpeedLimit        sql.NullInt64
	ExpiredAt         sql.NullInt64
}

type planRecord struct {
	ID                 int64
	GroupID            int64
	TransferEnable     int64
	DeviceLimit        sql.NullInt64
	SpeedLimit         sql.NullInt64
	Show               int64
	Renew              int64
	MonthPrice         sql.NullInt64
	QuarterPrice       sql.NullInt64
	HalfYearPrice      sql.NullInt64
	YearPrice          sql.NullInt64
	TwoYearPrice       sql.NullInt64
	ThreeYearPrice     sql.NullInt64
	OnetimePrice       sql.NullInt64
	ResetPrice         sql.NullInt64
	ResetTrafficMethod sql.NullInt64
	CapacityLimit      sql.NullInt64
}

type couponRecord struct {
	ID               int64
	Type             int64
	Value            int64
	Show             int64
	LimitUse         sql.NullInt64
	LimitUseWithUser sql.NullInt64
	LimitPlanIDs     sql.NullString
	LimitPeriod      sql.NullString
	StartedAt        int64
	EndedAt          int64
}

type paymentMethodRecord struct {
	ID                 int64
	Payment            string
	Enable             int64
	HandlingFeeFixed   sql.NullInt64
	HandlingFeePercent sql.NullFloat64
}

type orderRecord struct {
	ID                           int64
	UserID                       int64
	PlanID                       int64
	CouponID                     sql.NullInt64
	PaymentID                    sql.NullInt64
	Type                         int64
	Period                       string
	TradeNo                      string
	CallbackNo                   sql.NullString
	TotalAmount                  int64
	HandlingAmount               sql.NullInt64
	DiscountAmount               sql.NullInt64
	SurplusAmount                sql.NullInt64
	RefundAmount                 sql.NullInt64
	BalanceAmount                sql.NullInt64
	SurplusOrderIDs              sql.NullString
	Status                       int64
	CommissionStatus             int64
	CommissionBalance            int64
	ActualCommissionBalance      sql.NullInt64
	InviteUserID                 sql.NullInt64
	InviteCampaignID             sql.NullInt64
	InviteCampaignDiscountAmount int64
	PaidAt                       sql.NullInt64
	CreatedAt                    int64
	UpdatedAt                    int64
}

type inviteCampaignRecord struct {
	ID            int64
	PlanID        int64
	Period        string
	CurrentAmount int64
	TargetAmount  int64
	Status        int64
	ExpiredAt     int64
}

type orderDraft struct {
	UserID                       int64
	PlanID                       int64
	CouponID                     sql.NullInt64
	PaymentID                    sql.NullInt64
	Type                         int64
	Period                       string
	TradeNo                      string
	TotalAmount                  int64
	HandlingAmount               sql.NullInt64
	DiscountAmount               int64
	SurplusAmount                int64
	RefundAmount                 int64
	BalanceAmount                int64
	SurplusOrderIDs              []int64
	Status                       int64
	CommissionBalance            int64
	InviteUserID                 sql.NullInt64
	InviteCampaignID             sql.NullInt64
	InviteCampaignDiscountAmount int64
	PaidAt                       sql.NullInt64
	CallbackNo                   sql.NullString
}

func (s *DBService) SaveOrder(ctx context.Context, userID int64, req OrderSaveRequest) (string, error) {
	if s.db == nil {
		return "", ErrUnavailable
	}

	req.Period = strings.TrimSpace(req.Period)
	if req.PlanID < 0 || req.Period == "" {
		return "", ErrInvalidParameter
	}
	if _, ok := allowedOrderPeriods[req.Period]; !ok {
		return "", ErrInvalidParameter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin order transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	userRow, err := s.lockUserTx(ctx, tx, userID)
	if err != nil {
		return "", err
	}
	if pending, err := s.hasPendingOrderTx(ctx, tx, userID); err != nil {
		return "", err
	} else if pending {
		return "", ErrPendingOrderExists
	}

	if req.PlanID == 0 {
		tradeNo, err := s.saveDepositOrderTx(ctx, tx, userRow, req.DepositAmount)
		if err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("commit deposit order: %w", err)
		}
		return tradeNo, nil
	}

	plan, ok, err := s.loadPlanTx(ctx, tx, req.PlanID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrPlanNotFound
	}

	if userRow.PlanID.Valid && userRow.PlanID.Int64 != plan.ID && req.Period != "reset_price" {
		hasCapacity, err := s.planHasCapacity(ctx, plan.ID, plan.CapacityLimit)
		if err != nil {
			return "", err
		}
		if !hasCapacity {
			return "", ErrPlanSoldOut
		}
	}

	price, ok := plan.priceForPeriod(req.Period)
	if !ok {
		return "", ErrPeriodUnavailable
	}
	if price < 0 {
		return "", ErrInvalidParameter
	}
	if req.Period == "reset_price" {
		if !userRow.isAvailable(time.Now().Unix()) || !userRow.PlanID.Valid || userRow.PlanID.Int64 != plan.ID {
			return "", ErrResetUnavailable
		}
	}
	if ((!plan.isShown() && !plan.canRenew()) || (!plan.isShown() && (!userRow.PlanID.Valid || userRow.PlanID.Int64 != plan.ID))) && req.Period != "reset_price" {
		return "", ErrSubscriptionSoldOut
	}
	if !plan.canRenew() && userRow.PlanID.Valid && userRow.PlanID.Int64 == plan.ID && req.Period != "reset_price" {
		return "", ErrPlanCannotRenew
	}
	if !plan.isShown() && plan.canRenew() && !userRow.isAvailable(time.Now().Unix()) {
		return "", ErrPlanExpiredChangeRequired
	}

	order := orderDraft{
		UserID:      userID,
		PlanID:      plan.ID,
		Period:      req.Period,
		TradeNo:     generateTradeNo(),
		TotalAmount: price,
		Status:      0,
	}

	if req.CouponCode = strings.TrimSpace(req.CouponCode); req.CouponCode != "" {
		coupon, ok, err := s.loadCouponTx(ctx, tx, req.CouponCode)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrCouponInvalid
		}
		if err := s.applyCouponTx(ctx, tx, coupon, &order); err != nil {
			return "", err
		}
	}

	if userRow.Discount.Valid && userRow.Discount.Int64 > 0 {
		vipDiscount := calcPercent(order.TotalAmount, userRow.Discount.Int64)
		order.DiscountAmount += vipDiscount
		order.TotalAmount -= vipDiscount
	}
	if order.TotalAmount < 0 {
		return "", ErrInvalidParameter
	}

	if err := s.setOrderTypeTx(ctx, tx, userRow, &order); err != nil {
		return "", err
	}
	if err := s.applyInviteCampaignDiscountTx(ctx, tx, userID, &order); err != nil {
		return "", err
	}
	if order.TotalAmount < 0 {
		return "", ErrInvalidParameter
	}

	if userRow.Balance > 0 && order.TotalAmount > 0 {
		if userRow.Balance > order.TotalAmount {
			userRow.Balance -= order.TotalAmount
			order.BalanceAmount = order.TotalAmount
			order.TotalAmount = 0
		} else {
			order.BalanceAmount = userRow.Balance
			order.TotalAmount -= userRow.Balance
			userRow.Balance = 0
		}
		if err := s.updateUserBalanceTx(ctx, tx, userID, userRow.Balance); err != nil {
			if errors.Is(err, ErrInsufficientBalance) {
				return "", err
			}
			return "", fmt.Errorf("deduct user balance: %w", err)
		}
	}

	if err := s.setInviteTx(ctx, tx, userRow, &order); err != nil {
		return "", err
	}
	if err := s.insertOrderTx(ctx, tx, &order); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit order: %w", err)
	}
	return order.TradeNo, nil
}

func (s *DBService) CheckoutOrder(ctx context.Context, userID int64, req OrderCheckoutRequest) (OrderCheckoutResult, error) {
	if s.db == nil {
		return OrderCheckoutResult{}, ErrUnavailable
	}

	req.TradeNo = strings.TrimSpace(req.TradeNo)
	if req.TradeNo == "" {
		return OrderCheckoutResult{}, ErrInvalidParameter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrderCheckoutResult{}, fmt.Errorf("begin checkout transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	order, ok, err := s.lockOrderTx(ctx, tx, userID, req.TradeNo)
	if err != nil {
		return OrderCheckoutResult{}, err
	}
	if !ok || order.Status != 0 {
		return OrderCheckoutResult{}, ErrOrderPaidOrMissing
	}

	if order.TotalAmount < 0 {
		return OrderCheckoutResult{}, ErrInvalidParameter
	}
	if order.TotalAmount == 0 {
		if err := s.openOrderTx(ctx, tx, &order); err != nil {
			return OrderCheckoutResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return OrderCheckoutResult{}, fmt.Errorf("commit free checkout: %w", err)
		}
		return OrderCheckoutResult{Type: -1, Data: true}, nil
	}

	payment, ok, err := s.loadPaymentMethodTx(ctx, tx, req.MethodID)
	if err != nil {
		return OrderCheckoutResult{}, err
	}
	if !ok || payment.Enable != 1 {
		return OrderCheckoutResult{}, ErrPaymentMethodUnavailable
	}

	order.PaymentID = sql.NullInt64{Int64: payment.ID, Valid: true}
	order.HandlingAmount = sql.NullInt64{Valid: false}
	if payment.HandlingFeeFixed.Valid || payment.HandlingFeePercent.Valid {
		amount := math.Round(float64(order.TotalAmount)*(payment.HandlingFeePercent.Float64/100) + float64(payment.HandlingFeeFixed.Int64))
		order.HandlingAmount = sql.NullInt64{Int64: int64(amount), Valid: true}
	}
	if err := s.updateOrderPaymentTx(ctx, tx, order); err != nil {
		return OrderCheckoutResult{}, err
	}

	return OrderCheckoutResult{}, ErrUnsupportedPaymentGateway
}

func (s *DBService) CancelOrder(ctx context.Context, userID int64, tradeNo string) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false, ErrInvalidParameter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin cancel transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	order, ok, err := s.lockOrderTx(ctx, tx, userID, tradeNo)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrOrderNotFound
	}
	if order.Status != 0 {
		return false, ErrCancelPendingOnly
	}
	checkoutUnlocked, err := tryLockCheckoutCreationTx(ctx, tx, tradeNo)
	if err != nil {
		return false, err
	}
	if !checkoutUnlocked {
		return false, ErrCheckoutInProgress
	}

	order.Status = 2
	order.UpdatedAt = time.Now().Unix()
	if err := s.updateOrderStatusTx(ctx, tx, order); err != nil {
		return false, err
	}
	if order.BalanceAmount.Valid && order.BalanceAmount.Int64 > 0 {
		userRow, err := s.lockUserTx(ctx, tx, userID)
		if err != nil {
			return false, err
		}
		userRow.Balance += order.BalanceAmount.Int64
		if err := s.updateUserBalanceTx(ctx, tx, userID, userRow.Balance); err != nil {
			return false, err
		}
	}

	ttl := s.currentConfig().OrderCancelRecoverTTL
	if ttl <= 0 {
		ttl = 1800
	}
	if err := setCancelRecoveryTx(ctx, tx, tradeNo, ttl); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cancel order: %w", err)
	}

	return true, nil
}

func tryLockCheckoutCreationTx(ctx context.Context, tx *sql.Tx, tradeNo string) (bool, error) {
	var acquired bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0::bigint))`, "checkout:"+strings.TrimSpace(tradeNo)).Scan(&acquired); err != nil {
		return false, fmt.Errorf("lock checkout creation state: %w", err)
	}
	return acquired, nil
}

func (s *DBService) AssignAdminOrder(ctx context.Context, req AdminAssignOrderRequest) (string, error) {
	if s.db == nil {
		return "", ErrUnavailable
	}

	req.Email = strings.TrimSpace(req.Email)
	req.Period = strings.TrimSpace(req.Period)
	if req.Email == "" || req.PlanID <= 0 || req.Period == "" || req.TotalAmount < 0 {
		return "", ErrInvalidParameter
	}
	if _, ok := allowedOrderPeriods[req.Period]; !ok {
		return "", ErrInvalidParameter
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin admin assign transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	userRow, err := s.lockUserByEmailTx(ctx, tx, req.Email)
	if err != nil {
		return "", err
	}
	plan, ok, err := s.loadPlanTx(ctx, tx, req.PlanID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrPlanNotFound
	}
	pending, err := s.hasPendingOrderTx(ctx, tx, userRow.ID)
	if err != nil {
		return "", err
	}
	if pending {
		return "", ErrPendingOrderExists
	}

	order := orderDraft{
		UserID:      userRow.ID,
		PlanID:      plan.ID,
		Period:      req.Period,
		TradeNo:     generateTradeNo(),
		TotalAmount: req.TotalAmount,
		Status:      0,
	}
	now := time.Now().Unix()
	switch {
	case order.Period == "reset_price":
		order.Type = 4
	case userRow.PlanID.Valid && userRow.PlanID.Int64 != order.PlanID:
		order.Type = 3
	case userRow.ExpiredAt.Valid && userRow.ExpiredAt.Int64 > now && userRow.PlanID.Valid && userRow.PlanID.Int64 == order.PlanID:
		order.Type = 2
	default:
		order.Type = 1
	}

	if err := s.setInviteTx(ctx, tx, userRow, &order); err != nil {
		return "", err
	}
	if err := s.insertOrderTx(ctx, tx, &order); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit admin assign transaction: %w", err)
	}
	return order.TradeNo, nil
}

func (s *DBService) saveDepositOrderTx(ctx context.Context, tx *sql.Tx, userRow userRecord, amount int64) (string, error) {
	if amount <= 0 {
		return "", ErrDepositAmountInvalid
	}
	if amount >= 9999999 {
		return "", ErrDepositAmountTooLarge
	}

	order := orderDraft{
		UserID:      userRow.ID,
		PlanID:      0,
		Type:        9,
		Period:      "deposit",
		TradeNo:     generateTradeNo(),
		TotalAmount: amount,
		Status:      0,
	}
	if err := s.setInviteTx(ctx, tx, userRow, &order); err != nil {
		return "", err
	}
	if err := s.insertOrderTx(ctx, tx, &order); err != nil {
		return "", err
	}
	return order.TradeNo, nil
}

func (s *DBService) setOrderTypeTx(ctx context.Context, tx *sql.Tx, userRow userRecord, order *orderDraft) error {
	now := time.Now().Unix()
	switch {
	case order.Period == "deposit":
		order.Type = 9
	case order.Period == "reset_price":
		order.Type = 4
	case userRow.PlanID.Valid && userRow.PlanID.Int64 != order.PlanID && (!userRow.ExpiredAt.Valid || userRow.ExpiredAt.Int64 > now):
		cfg := s.currentConfig()
		if !cfg.PlanChangeEnable {
			return ErrPlanChangeDisabled
		}
		order.Type = 3
		if cfg.SurplusEnable {
			if err := s.applySurplusValueTx(ctx, tx, userRow, order); err != nil {
				return err
			}
			if order.SurplusAmount >= order.TotalAmount {
				order.RefundAmount = order.SurplusAmount - order.TotalAmount
				order.TotalAmount = 0
			} else {
				order.TotalAmount -= order.SurplusAmount
			}
		}
	case userRow.ExpiredAt.Valid && userRow.ExpiredAt.Int64 > now && userRow.PlanID.Valid && userRow.PlanID.Int64 == order.PlanID:
		order.Type = 2
	default:
		order.Type = 1
	}
	return nil
}

func (s *DBService) applySurplusValueTx(ctx context.Context, tx *sql.Tx, userRow userRecord, order *orderDraft) error {
	if !userRow.ExpiredAt.Valid {
		return s.applyOneTimeSurplusTx(ctx, tx, userRow, order)
	}
	return s.applyPeriodSurplusTx(ctx, tx, userRow, order)
}

func (s *DBService) applyOneTimeSurplusTx(ctx context.Context, tx *sql.Tx, userRow userRecord, order *orderDraft) error {
	lastOrder := orderRecord{}
	err := tx.QueryRowContext(ctx, `SELECT id, total_amount, balance_amount
FROM v2_order
WHERE user_id = $1 AND period = 'onetime_price' AND status = 3
ORDER BY id DESC
LIMIT 1`, userRow.ID).Scan(&lastOrder.ID, &lastOrder.TotalAmount, &lastOrder.BalanceAmount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("query last one-time order: %w", err)
	}

	nowUserTraffic := float64(userRow.TransferEnable) / float64(trafficGB)
	if nowUserTraffic == 0 {
		return nil
	}
	paidTotal := float64(lastOrder.TotalAmount + nullableInt64(lastOrder.BalanceAmount))
	if paidTotal == 0 {
		return nil
	}
	notUsedTraffic := nowUserTraffic - (float64(userRow.U+userRow.D) / float64(trafficGB))
	remainingTrafficRatio := notUsedTraffic / nowUserTraffic
	order.SurplusAmount = int64(math.Max(remainingTrafficRatio*paidTotal, 0))

	rows, err := tx.QueryContext(ctx, `SELECT id FROM v2_order WHERE user_id = $1 AND period != 'reset_price' AND status = 3`, userRow.ID)
	if err != nil {
		return fmt.Errorf("query surplus order ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan surplus order id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate surplus order ids: %w", err)
	}
	order.SurplusOrderIDs = ids
	return nil
}

func (s *DBService) applyPeriodSurplusTx(ctx context.Context, tx *sql.Tx, userRow userRecord, order *orderDraft) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, period, created_at, total_amount, balance_amount, surplus_amount, refund_amount
FROM v2_order
WHERE user_id = $1
AND period != 'reset_price'
AND period != 'onetime_price'
AND period != 'deposit'
AND status = 3`, userRow.ID)
	if err != nil {
		return fmt.Errorf("query period orders: %w", err)
	}
	defer rows.Close()

	now := time.Now().Unix()
	var (
		orderIDs       []int64
		orderAmountSum float64
		orderMonthSum  int
		lastValidateAt int64
		hasValidOrder  bool
	)
	for rows.Next() {
		var (
			id            int64
			period        string
			createdAt     int64
			totalAmount   int64
			balanceAmount sql.NullInt64
			surplusAmount sql.NullInt64
			refundAmount  sql.NullInt64
		)
		if err := rows.Scan(&id, &period, &createdAt, &totalAmount, &balanceAmount, &surplusAmount, &refundAmount); err != nil {
			return fmt.Errorf("scan period order: %w", err)
		}
		orderIDs = append(orderIDs, id)

		months, ok := periodMonths(period)
		if !ok {
			continue
		}
		orderEnd := time.Unix(createdAt, 0).AddDate(0, months, 0).Unix()
		if orderEnd < now {
			continue
		}
		hasValidOrder = true
		if createdAt > lastValidateAt {
			lastValidateAt = createdAt
		}
		orderMonthSum += months
		orderAmountSum += float64(totalAmount + nullableInt64(balanceAmount) + nullableInt64(surplusAmount) - nullableInt64(refundAmount))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate period orders: %w", err)
	}
	if !hasValidOrder || !userRow.ExpiredAt.Valid || userRow.ExpiredAt.Int64 < now {
		return nil
	}

	expiredAtByOrder := time.Unix(lastValidateAt, 0).AddDate(0, orderMonthSum, 0).Unix()
	if expiredAtByOrder < now {
		return nil
	}

	orderSurplusSecond := float64(userRow.ExpiredAt.Int64 - now)
	orderRangeSecond := float64(expiredAtByOrder - lastValidateAt)
	if orderRangeSecond <= 0 || userRow.TransferEnable <= 0 {
		return nil
	}
	remainingTrafficRatio := float64(userRow.TransferEnable-(userRow.U+userRow.D)) / float64(userRow.TransferEnable)
	avgPricePerSecond := orderAmountSum / orderRangeSecond
	var orderSurplusAmount float64
	if orderRangeSecond <= 31*86400 {
		remainingExpiredRatio := orderSurplusSecond / orderRangeSecond
		surplusRatio := math.Min(remainingExpiredRatio, remainingTrafficRatio)
		orderSurplusAmount = avgPricePerSecond * orderSurplusSecond * surplusRatio
	} else {
		monthSeconds := float64(30 * 86400)
		firstMonthRemain := math.Mod(orderSurplusSecond, monthSeconds)
		surplusRatio := math.Min(firstMonthRemain/monthSeconds, remainingTrafficRatio)
		laterMonths := orderSurplusSecond - firstMonthRemain
		orderSurplusAmount = avgPricePerSecond*monthSeconds*surplusRatio + avgPricePerSecond*laterMonths
	}

	order.SurplusAmount = int64(math.Max(orderSurplusAmount, 0))
	order.SurplusOrderIDs = orderIDs
	return nil
}

func (s *DBService) applyInviteCampaignDiscountTx(ctx context.Context, tx *sql.Tx, userID int64, order *orderDraft) error {
	campaign, ok, err := s.currentInviteCampaignTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	now := time.Now().Unix()
	if (campaign.Status == 0 || campaign.Status == 1) && campaign.ExpiredAt <= now {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = 2, updated_at = $2 WHERE id = $1`, campaign.ID, now); err != nil {
			return fmt.Errorf("expire invite campaign: %w", err)
		}
		return nil
	}
	if campaign.Status == 0 && campaign.CurrentAmount >= campaign.TargetAmount {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = 1, completed_at = $2, updated_at = $2 WHERE id = $1`, campaign.ID, now); err != nil {
			return fmt.Errorf("complete invite campaign: %w", err)
		}
		campaign.Status = 1
	}
	if campaign.Status != 0 && campaign.Status != 1 {
		return nil
	}
	if campaign.PlanID != order.PlanID || campaign.Period != order.Period {
		return nil
	}

	discountAmount := minInt64(campaign.CurrentAmount, order.TotalAmount)
	if discountAmount <= 0 {
		return nil
	}
	order.InviteCampaignID = sql.NullInt64{Int64: campaign.ID, Valid: true}
	order.InviteCampaignDiscountAmount = discountAmount
	order.TotalAmount -= discountAmount
	return nil
}

func (s *DBService) setInviteTx(ctx context.Context, tx *sql.Tx, userRow userRecord, order *orderDraft) error {
	if userRow.InviteUserID.Valid {
		order.InviteUserID = userRow.InviteUserID
	}
	if order.Period == "deposit" || order.Type == 9 {
		return nil
	}
	if userRow.InviteUserID.Valid && order.TotalAmount <= 0 {
		return nil
	}
	if !userRow.InviteUserID.Valid {
		return nil
	}

	inviter, err := s.lockUserTx(ctx, tx, userRow.InviteUserID.Int64)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	isCommission := false
	switch inviter.CommissionType {
	case 0:
		hasValidOrder, err := s.haveValidOrderTx(ctx, tx, userRow.ID)
		if err != nil {
			return err
		}
		isCommission = !s.currentConfig().CommissionFirstTime || !hasValidOrder
	case 1:
		isCommission = true
	case 2:
		hasValidOrder, err := s.haveValidOrderTx(ctx, tx, userRow.ID)
		if err != nil {
			return err
		}
		isCommission = !hasValidOrder
	}
	if !isCommission {
		return nil
	}

	rate := s.currentConfig().InviteCommission
	if inviter.CommissionRate.Valid && inviter.CommissionRate.Int64 > 0 {
		rate = inviter.CommissionRate.Int64
	}
	order.CommissionBalance = calcPercent(order.TotalAmount, rate)
	return nil
}

func (s *DBService) openOrderTx(ctx context.Context, tx *sql.Tx, order *orderRecord) error {
	if !validOrderAmountState(*order) {
		return ErrInvalidParameter
	}
	now := time.Now().Unix()
	if !order.CallbackNo.Valid || strings.TrimSpace(order.CallbackNo.String) == "" {
		order.CallbackNo = sql.NullString{String: order.TradeNo, Valid: true}
	}
	if !order.PaidAt.Valid || order.PaidAt.Int64 <= 0 {
		order.PaidAt = sql.NullInt64{Int64: now, Valid: true}
	}

	userRow, err := s.lockUserTx(ctx, tx, order.UserID)
	if err != nil {
		return err
	}

	if order.Type == 9 {
		if err := s.disableDepositCommissionTx(ctx, tx, order, now); err != nil {
			return err
		}
		userRow.Balance += order.TotalAmount
		if err := s.updateUserBalanceTx(ctx, tx, userRow.ID, userRow.Balance); err != nil {
			return err
		}
		order.Status = 3
		order.UpdatedAt = now
		return s.updateOrderPaymentStateTx(ctx, tx, *order)
	}

	plan, ok, err := s.loadPlanTx(ctx, tx, order.PlanID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPlanNotFound
	}

	if order.RefundAmount.Valid && order.RefundAmount.Int64 > 0 {
		userRow.Balance += order.RefundAmount.Int64
	}
	if err := s.markSurplusOrdersClosedTx(ctx, tx, order.SurplusOrderIDs); err != nil {
		return err
	}

	switch order.Period {
	case "onetime_price":
		applyOrderOneTime(&userRow, *order, plan)
	case "reset_price":
		userRow.U = 0
		userRow.D = 0
	default:
		applyOrderPeriod(&userRow, *order, plan)
	}
	if plan.SpeedLimit.Valid {
		userRow.SpeedLimit = plan.SpeedLimit
	} else {
		userRow.SpeedLimit = sql.NullInt64{}
	}
	if err := s.updateUserSubscriptionTx(ctx, tx, userRow); err != nil {
		return err
	}
	if order.InviteCampaignID.Valid {
		if err := s.markInviteCampaignUsedTx(ctx, tx, order.InviteCampaignID.Int64); err != nil {
			return err
		}
	}

	order.Status = 3
	order.UpdatedAt = now
	return s.updateOrderPaymentStateTx(ctx, tx, *order)
}

func validOrderAmountState(order orderRecord) bool {
	handlingAmount := nullableInt64(order.HandlingAmount)
	return order.TotalAmount >= 0 && handlingAmount >= 0 && order.TotalAmount <= math.MaxInt64-handlingAmount
}

func applyOrderPeriod(userRow *userRecord, order orderRecord, plan planRecord) {
	now := time.Now().Unix()
	if order.Type == 3 {
		userRow.ExpiredAt = sql.NullInt64{Int64: now, Valid: true}
	}
	userRow.TransferEnable = planTransferEnableBytes(plan.TransferEnable)
	userRow.DeviceLimit = plan.DeviceLimit
	if !userRow.ExpiredAt.Valid {
		userRow.U = 0
		userRow.D = 0
	}
	if order.Type == 1 {
		userRow.U = 0
		userRow.D = 0
	}
	if order.Type == 2 && userRow.ExpiredAt.Valid {
		expireTime := time.Unix(userRow.ExpiredAt.Int64, 0)
		nowTime := time.Now()
		if expireTime.Month() == nowTime.Month() && expireTime.Day() == nowTime.Day() {
			userRow.U = 0
			userRow.D = 0
		}
	}
	userRow.PlanID = sql.NullInt64{Int64: plan.ID, Valid: true}
	userRow.GroupID = sql.NullInt64{Int64: plan.GroupID, Valid: true}
	userRow.ExpiredAt = sql.NullInt64{Int64: nextExpiredAt(order.Period, userRow.ExpiredAt), Valid: true}
}

func applyOrderOneTime(userRow *userRecord, order orderRecord, plan planRecord) {
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
}

func nextExpiredAt(period string, expiredAt sql.NullInt64) int64 {
	base := time.Now()
	if expiredAt.Valid && expiredAt.Int64 > base.Unix() {
		base = time.Unix(expiredAt.Int64, 0)
	}
	months, ok := periodMonths(period)
	if !ok {
		return base.Unix()
	}
	return base.AddDate(0, months, 0).Unix()
}

func periodMonths(period string) (int, bool) {
	switch period {
	case "month_price":
		return 1, true
	case "quarter_price":
		return 3, true
	case "half_year_price":
		return 6, true
	case "year_price":
		return 12, true
	case "two_year_price":
		return 24, true
	case "three_year_price":
		return 36, true
	default:
		return 0, false
	}
}

func (s *DBService) applyCouponTx(ctx context.Context, tx *sql.Tx, coupon couponRecord, order *orderDraft) error {
	now := time.Now().Unix()
	switch {
	case coupon.Show == 0:
		return ErrCouponInvalid
	case coupon.LimitUse.Valid && coupon.LimitUse.Int64 <= 0:
		return ErrCouponUnavailable
	case now < coupon.StartedAt:
		return ErrCouponNotStarted
	case now > coupon.EndedAt:
		return ErrCouponExpired
	}

	if coupon.LimitPlanIDs.Valid && coupon.LimitPlanIDs.String != "" {
		if !containsInt64(parseIDString(coupon.LimitPlanIDs.String), order.PlanID) {
			return ErrCouponPlanRestricted
		}
	}
	if coupon.LimitPeriod.Valid && coupon.LimitPeriod.String != "" {
		if !containsString(parseStringList(coupon.LimitPeriod.String), order.Period) {
			return ErrCouponPeriodRestricted
		}
	}
	if coupon.LimitUseWithUser.Valid {
		var usedCount int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_order WHERE coupon_id = $1 AND user_id = $2 AND status NOT IN (0, 2)`, coupon.ID, order.UserID).Scan(&usedCount); err != nil {
			return fmt.Errorf("query coupon usage count: %w", err)
		}
		if usedCount >= coupon.LimitUseWithUser.Int64 {
			return ErrCouponUserLimit
		}
	}

	var discount int64
	switch coupon.Type {
	case 1:
		discount = coupon.Value
	case 2:
		discount = calcPercent(order.TotalAmount, coupon.Value)
	default:
		return ErrCouponFailed
	}
	if discount > order.TotalAmount {
		discount = order.TotalAmount
	}
	order.DiscountAmount += discount
	order.TotalAmount -= discount
	order.CouponID = sql.NullInt64{Int64: coupon.ID, Valid: true}

	if coupon.LimitUse.Valid {
		nextLimit := coupon.LimitUse.Int64 - 1
		if nextLimit < 0 {
			return ErrCouponFailed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_coupon SET limit_use = $2, updated_at = $3 WHERE id = $1`, coupon.ID, nextLimit, now); err != nil {
			return fmt.Errorf("update coupon limit: %w", err)
		}
	}
	return nil
}

func (s *DBService) lockUserTx(ctx context.Context, tx *sql.Tx, userID int64) (userRecord, error) {
	var row userRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,
u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at
FROM v2_user
WHERE id = $1
FOR UPDATE`, userID).Scan(
		&row.ID,
		&row.InviteUserID,
		&row.Balance,
		&row.CommissionBalance,
		&row.Discount,
		&row.CommissionType,
		&row.CommissionRate,
		&row.U,
		&row.D,
		&row.TransferEnable,
		&row.DeviceLimit,
		&row.Banned,
		&row.GroupID,
		&row.PlanID,
		&row.SpeedLimit,
		&row.ExpiredAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return userRecord{}, ErrNotFound
		}
		return userRecord{}, fmt.Errorf("lock user: %w", err)
	}
	row.ExpiredAt = normalizeNullableExpiry(row.ExpiredAt)
	return row, nil
}

func (s *DBService) lockUserByEmailTx(ctx context.Context, tx *sql.Tx, email string) (userRecord, error) {
	var row userRecord
	email = strings.TrimSpace(strings.ToLower(email))
	err := tx.QueryRowContext(ctx, `SELECT
id, invite_user_id, balance, commission_balance, discount, commission_type, commission_rate,
u, d, transfer_enable, device_limit, banned, group_id, plan_id, speed_limit, expired_at
FROM v2_user
WHERE LOWER(email) = $1
FOR UPDATE`, email).Scan(
		&row.ID,
		&row.InviteUserID,
		&row.Balance,
		&row.CommissionBalance,
		&row.Discount,
		&row.CommissionType,
		&row.CommissionRate,
		&row.U,
		&row.D,
		&row.TransferEnable,
		&row.DeviceLimit,
		&row.Banned,
		&row.GroupID,
		&row.PlanID,
		&row.SpeedLimit,
		&row.ExpiredAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return userRecord{}, ErrNotFound
		}
		return userRecord{}, fmt.Errorf("lock user by email: %w", err)
	}
	row.ExpiredAt = normalizeNullableExpiry(row.ExpiredAt)
	return row, nil
}

func (u userRecord) isAvailable(now int64) bool {
	return u.Banned == 0 && u.TransferEnable > 0 && (!u.ExpiredAt.Valid || u.ExpiredAt.Int64 > now)
}

func (p planRecord) isShown() bool {
	return p.Show != 0
}

func (p planRecord) canRenew() bool {
	return p.Renew != 0
}

func (p planRecord) priceForPeriod(period string) (int64, bool) {
	switch period {
	case "month_price":
		return nullableInt64WithOK(p.MonthPrice)
	case "quarter_price":
		return nullableInt64WithOK(p.QuarterPrice)
	case "half_year_price":
		return nullableInt64WithOK(p.HalfYearPrice)
	case "year_price":
		return nullableInt64WithOK(p.YearPrice)
	case "two_year_price":
		return nullableInt64WithOK(p.TwoYearPrice)
	case "three_year_price":
		return nullableInt64WithOK(p.ThreeYearPrice)
	case "onetime_price":
		return nullableInt64WithOK(p.OnetimePrice)
	case "reset_price":
		return nullableInt64WithOK(p.ResetPrice)
	default:
		return 0, false
	}
}

func (s *DBService) hasPendingOrderTx(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_order WHERE user_id = $1 AND status IN (0, 1))`, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("query pending orders: %w", err)
	}
	return exists, nil
}

func (s *DBService) loadPlanTx(ctx context.Context, tx *sql.Tx, planID int64) (planRecord, bool, error) {
	var row planRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, group_id, transfer_enable, device_limit, speed_limit, "show", renew,
month_price, quarter_price, half_year_price, year_price, two_year_price, three_year_price,
onetime_price, reset_price, reset_traffic_method, capacity_limit
FROM v2_plan
WHERE id = $1
LIMIT 1`, planID).Scan(
		&row.ID,
		&row.GroupID,
		&row.TransferEnable,
		&row.DeviceLimit,
		&row.SpeedLimit,
		&row.Show,
		&row.Renew,
		&row.MonthPrice,
		&row.QuarterPrice,
		&row.HalfYearPrice,
		&row.YearPrice,
		&row.TwoYearPrice,
		&row.ThreeYearPrice,
		&row.OnetimePrice,
		&row.ResetPrice,
		&row.ResetTrafficMethod,
		&row.CapacityLimit,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return planRecord{}, false, nil
		}
		return planRecord{}, false, fmt.Errorf("query plan: %w", err)
	}
	return row, true, nil
}

func (s *DBService) planHasCapacity(ctx context.Context, planID int64, capacity sql.NullInt64) (bool, error) {
	if !capacity.Valid {
		return true, nil
	}
	counts, err := s.activePlanCounts(ctx)
	if err != nil {
		return false, err
	}
	return capacity.Int64-counts[planID] > 0, nil
}

func (s *DBService) loadCouponTx(ctx context.Context, tx *sql.Tx, code string) (couponRecord, bool, error) {
	var row couponRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, type, value, "show", limit_use, limit_use_with_user, limit_plan_ids, limit_period, started_at, ended_at
FROM v2_coupon
WHERE code = $1
FOR UPDATE`, code).Scan(
		&row.ID,
		&row.Type,
		&row.Value,
		&row.Show,
		&row.LimitUse,
		&row.LimitUseWithUser,
		&row.LimitPlanIDs,
		&row.LimitPeriod,
		&row.StartedAt,
		&row.EndedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return couponRecord{}, false, nil
		}
		return couponRecord{}, false, fmt.Errorf("query coupon: %w", err)
	}
	return row, true, nil
}

func (s *DBService) haveValidOrderTx(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_order WHERE user_id = $1 AND status NOT IN (0, 2))`, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("query valid orders: %w", err)
	}
	return exists, nil
}

func (s *DBService) currentInviteCampaignTx(ctx context.Context, tx *sql.Tx, userID int64) (inviteCampaignRecord, bool, error) {
	var row inviteCampaignRecord
	err := tx.QueryRowContext(ctx, `SELECT id, plan_id, period, current_amount, target_amount, status, expired_at
FROM v2_invite_campaign
WHERE user_id = $1 AND status IN (0, 1)
ORDER BY id DESC
LIMIT 1`, userID).Scan(
		&row.ID,
		&row.PlanID,
		&row.Period,
		&row.CurrentAmount,
		&row.TargetAmount,
		&row.Status,
		&row.ExpiredAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return inviteCampaignRecord{}, false, nil
		}
		return inviteCampaignRecord{}, false, fmt.Errorf("query invite campaign: %w", err)
	}
	return row, true, nil
}

func (s *DBService) insertOrderTx(ctx context.Context, tx *sql.Tx, order *orderDraft) error {
	now := time.Now().Unix()
	surplusJSON := sql.NullString{}
	if len(order.SurplusOrderIDs) > 0 {
		encoded, err := json.Marshal(order.SurplusOrderIDs)
		if err != nil {
			return fmt.Errorf("encode surplus order ids: %w", err)
		}
		surplusJSON = sql.NullString{String: string(encoded), Valid: true}
	}

	_, err := tx.ExecContext(ctx, `INSERT INTO v2_order (
invite_user_id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,
total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,
surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance,
invite_campaign_id, invite_campaign_discount_amount, paid_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, NULL, $9, NULL, $10, $11, $12, $13, $14, $15, 0, $16, NULL, $17, $18, NULL, $19, $19
)`,
		nullInt64Any(order.InviteUserID),
		order.UserID,
		order.PlanID,
		nullInt64Any(order.CouponID),
		nil,
		order.Type,
		order.Period,
		order.TradeNo,
		order.TotalAmount,
		nullZeroInt64(order.DiscountAmount),
		nullZeroInt64(order.SurplusAmount),
		nullZeroInt64(order.RefundAmount),
		nullZeroInt64(order.BalanceAmount),
		nullStringAny(surplusJSON),
		order.Status,
		order.CommissionBalance,
		nullInt64Any(order.InviteCampaignID),
		order.InviteCampaignDiscountAmount,
		now,
	)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	return nil
}

func (s *DBService) updateUserBalanceTx(ctx context.Context, tx *sql.Tx, userID int64, balance int64) error {
	if balance < 0 {
		return ErrInsufficientBalance
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_user SET balance = $2, updated_at = $3 WHERE id = $1`, userID, balance, time.Now().Unix()); err != nil {
		return fmt.Errorf("update user balance: %w", err)
	}
	return nil
}

func (s *DBService) lockOrderTx(ctx context.Context, tx *sql.Tx, userID int64, tradeNo string) (orderRecord, bool, error) {
	var row orderRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, user_id, plan_id, coupon_id, payment_id, type, period, trade_no, callback_no,
total_amount, handling_amount, discount_amount, surplus_amount, refund_amount, balance_amount,
surplus_order_ids, status, commission_status, commission_balance, actual_commission_balance, invite_user_id, invite_campaign_id,
invite_campaign_discount_amount, paid_at, created_at, updated_at
FROM v2_order
WHERE user_id = $1 AND trade_no = $2
FOR UPDATE`, userID, tradeNo).Scan(
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
		return orderRecord{}, false, fmt.Errorf("lock order: %w", err)
	}
	return row, true, nil
}

func (s *DBService) loadPaymentMethodTx(ctx context.Context, tx *sql.Tx, methodID int64) (paymentMethodRecord, bool, error) {
	var row paymentMethodRecord
	err := tx.QueryRowContext(ctx, `SELECT id, payment, enable, handling_fee_fixed, handling_fee_percent::float8
FROM v2_payment
WHERE id = $1
LIMIT 1`, methodID).Scan(
		&row.ID,
		&row.Payment,
		&row.Enable,
		&row.HandlingFeeFixed,
		&row.HandlingFeePercent,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return paymentMethodRecord{}, false, nil
		}
		return paymentMethodRecord{}, false, fmt.Errorf("query payment method: %w", err)
	}
	return row, true, nil
}

func (s *DBService) updateOrderPaymentTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_order SET payment_id = $2, handling_amount = $3, updated_at = $4 WHERE id = $1`,
		order.ID, nullInt64Any(order.PaymentID), nullInt64Any(order.HandlingAmount), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("update order payment: %w", err)
	}
	return nil
}

func (s *DBService) updateOrderStatusTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_order SET status = $2, updated_at = $3 WHERE id = $1`, order.ID, order.Status, order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update order status: %w", err)
	}
	return nil
}

func (s *DBService) updateOrderPaymentStateTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_order SET status = $2, callback_no = $3, paid_at = $4, updated_at = $5 WHERE id = $1`,
		order.ID, order.Status, nullStringAny(order.CallbackNo), nullInt64Any(order.PaidAt), order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update order payment state: %w", err)
	}
	return nil
}

func (s *DBService) updateOrderCommissionStateTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_order SET commission_status = $2, actual_commission_balance = $3, updated_at = $4 WHERE id = $1`,
		order.ID, order.CommissionStatus, nullInt64Any(order.ActualCommissionBalance), order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update order commission state: %w", err)
	}
	return nil
}

func (s *DBService) updateOrderRefundStateTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_order SET status = $2, commission_status = $3, actual_commission_balance = $4, updated_at = $5 WHERE id = $1`,
		order.ID, order.Status, order.CommissionStatus, nullInt64Any(order.ActualCommissionBalance), order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update order refund state: %w", err)
	}
	return nil
}

func (s *DBService) updateUserSubscriptionTx(ctx context.Context, tx *sql.Tx, userRow userRecord) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_user SET
balance = $2, commission_balance = $3, u = $4, d = $5, transfer_enable = $6, device_limit = $7, group_id = $8,
plan_id = $9, speed_limit = $10, expired_at = $11, updated_at = $12
WHERE id = $1`,
		userRow.ID,
		userRow.Balance,
		userRow.CommissionBalance,
		userRow.U,
		userRow.D,
		userRow.TransferEnable,
		nullInt64Any(userRow.DeviceLimit),
		nullInt64Any(userRow.GroupID),
		nullInt64Any(userRow.PlanID),
		nullInt64Any(userRow.SpeedLimit),
		nullInt64Any(userRow.ExpiredAt),
		time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("update user subscription: %w", err)
	}
	return nil
}

func (s *DBService) markSurplusOrdersClosedTx(ctx context.Context, tx *sql.Tx, raw sql.NullString) error {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	ids := parseIDList(raw.String)
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
	query := fmt.Sprintf(`UPDATE v2_order SET status = 4, updated_at = $%d WHERE id IN (%s)`, len(args), strings.Join(parts, ","))
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("close surplus orders: %w", err)
	}
	return nil
}

func (s *DBService) markInviteCampaignUsedTx(ctx context.Context, tx *sql.Tx, campaignID int64) error {
	now := time.Now().Unix()
	_, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign SET status = 4, used_at = $2, updated_at = $2 WHERE id = $1 AND status != 4`, campaignID, now)
	if err != nil {
		return fmt.Errorf("mark invite campaign used: %w", err)
	}
	return nil
}

func cancelRecoveryKey(tradeNo string) string {
	return "order:cancel:recover:" + tradeNo
}

func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var values []string
		_ = json.Unmarshal([]byte(raw), &values)
		return values
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		raw = strings.TrimPrefix(strings.TrimSuffix(raw, "}"), "{")
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func calcPercent(amount, rate int64) int64 {
	return amount * rate / 100
}

func nullableInt64(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func nullableInt64WithOK(value sql.NullInt64) (int64, bool) {
	if !value.Valid {
		return 0, false
	}
	return value.Int64, true
}

func nullZeroInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullInt64Any(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullStringAny(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func generateTradeNo() string {
	now := time.Now()
	return now.Format("20060102150405") + fmt.Sprintf("%06d%05d", now.Nanosecond()/1000%1000000, now.UnixNano()%100000)
}
