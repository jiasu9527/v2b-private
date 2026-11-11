package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"forest/go-api/internal/payment"
)

var _ payment.ExternalOrderManager = (*Service)(nil)

func (s *Service) CheckoutWorkspaceRecharge(ctx context.Context, userID int64, req WorkspaceRechargeCheckoutRequest, base string) (RechargeCheckout, error) {
	if req.PaymentID <= 0 || !validRechargeTrade(req.TradeNo) || userID <= 0 {
		return RechargeCheckout{}, ErrInvalid
	}
	if s.rechargePayments == nil {
		return RechargeCheckout{}, ErrUnavailable
	}
	result, err := s.rechargePayments.Checkout(ctx, userID, payment.CheckoutRequest{TradeNo: req.TradeNo, MethodID: req.PaymentID, RequestBaseURL: base})
	if err != nil {
		return RechargeCheckout{}, err
	}
	order, err := s.WorkspaceRechargeDetail(ctx, userID, req.TradeNo)
	return RechargeCheckout{Order: order, Checkout: result}, err
}

func (s *Service) LookupExternalOrder(ctx context.Context, userID int64, trade string) (payment.ExternalOrder, error) {
	if userID <= 0 || !validRechargeTrade(trade) {
		return payment.ExternalOrder{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return payment.ExternalOrder{}, err
	}
	var o payment.ExternalOrder
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT r.trade_no,r.amount_fen,r.handling_amount_fen,r.payment_id,r.status,COALESCE(u.email,'') FROM v2_newapi_recharge r JOIN v2_user u ON u.id=r.user_id WHERE r.trade_no=$1 AND r.user_id=$2`, trade, userID).Scan(&o.TradeNo, &o.TotalAmount, &o.HandlingAmount, &o.PaymentID, &state, &o.UserEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrForbidden
	}
	if err != nil {
		return o, err
	}
	if state != "pending" {
		o.Status = 1
	}
	o.ReturnPath = "/ai?tab=records&recharge=" + trade
	return o, nil
}
func (s *Service) SetExternalPayment(ctx context.Context, userID int64, trade string, paymentID, handling int64) error {
	if userID <= 0 || !validRechargeTrade(trade) || paymentID <= 0 || handling < 0 {
		return ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	var gateway string
	var enabled int64
	if err := s.db.QueryRowContext(ctx, `SELECT payment,enable FROM v2_payment WHERE id=$1`, paymentID).Scan(&gateway, &enabled); errors.Is(err, sql.ErrNoRows) {
		return payment.ErrPaymentMethodUnavailable
	} else if err != nil {
		return err
	}
	if enabled != 1 || !rechargeGatewayAllowed(gateway) {
		return payment.ErrPaymentMethodUnavailable
	}
	// Immutable first payment channel and fee. An old payment URL must retain
	// exactly the same settlement identity even if the user tries another method.
	res, err := s.db.ExecContext(ctx, `UPDATE v2_newapi_recharge SET payment_id=$3,handling_amount_fen=$4 WHERE trade_no=$1 AND user_id=$2 AND status='pending' AND amount_fen<=$5 AND (payment_id=0 OR (payment_id=$3 AND handling_amount_fen=$4))`, trade, userID, paymentID, handling, int64(math.MaxInt64)-handling)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) MarkExternalPaid(ctx context.Context, trade, callback string, _ bool, paymentID, amount *int64) error {
	if !validRechargeTrade(trade) || strings.TrimSpace(callback) == "" || len(callback) > 255 || paymentID == nil || *paymentID <= 0 || amount == nil || *amount <= 0 {
		return ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var order RechargeOrder
	order, err = scanRecharge(tx.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM v2_newapi_recharge WHERE trade_no=$1 FOR UPDATE`, trade))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	// Validate all duplicate callbacks too; never acknowledge a different amount
	// or channel merely because another callback has already completed the order.
	if order.PaymentID != *paymentID || order.AmountFen <= 0 || order.HandlingAmountFen < 0 || order.AmountFen > math.MaxInt64-order.HandlingAmountFen || order.TotalAmountFen != *amount {
		return ErrConflict
	}
	if order.Status == "paid_pending_credit" || order.Status == "succeeded" {
		return nil
	}
	if order.Status != "pending" {
		return ErrConflict
	}
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `UPDATE v2_newapi_recharge SET status='paid_pending_credit',paid_at=$2,callback_no=$3,next_attempt_at=$2,last_error='' WHERE trade_no=$1 AND status='pending' AND checkout_id IS NOT NULL`, trade, now, strings.TrimSpace(callback))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// CheckoutExternalOnce is called from payment.Checkout, including the generic
// checkout endpoint. Thus callers cannot bypass replay protection using another
// route. The advisory transaction is only a mutex; state is durably committed
// before any outbound gateway call and callbacks use a different row lock.
func (s *Service) CheckoutExternalOnce(ctx context.Context, userID int64, req payment.CheckoutRequest, create func(context.Context) (payment.CheckoutResult, error)) (payment.CheckoutResult, error) {
	if userID <= 0 || !validRechargeTrade(req.TradeNo) || req.MethodID <= 0 {
		return payment.CheckoutResult{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return payment.CheckoutResult{}, err
	}
	lock, err := s.workspaceLock(ctx, userID, "recharge/"+req.TradeNo)
	if err != nil {
		return payment.CheckoutResult{}, err
	}
	defer lock.Rollback()

	var o RechargeOrder
	var encoded string
	o, err = s.WorkspaceRechargeDetail(ctx, userID, req.TradeNo)
	if err != nil {
		return payment.CheckoutResult{}, err
	}
	if o.PaymentID > 0 && o.PaymentID != req.MethodID {
		return payment.CheckoutResult{}, ErrConflict
	}
	if o.Status == "paid_pending_credit" || o.Status == "succeeded" {
		return payment.CheckoutResult{Type: -1, Data: true}, nil
	}
	if o.Status != "pending" {
		return payment.CheckoutResult{}, ErrConflict
	}
	if o.CheckoutState == "ready" {
		if err = s.db.QueryRowContext(ctx, `SELECT checkout_cipher FROM v2_newapi_recharge WHERE trade_no=$1 AND user_id=$2`, req.TradeNo, userID).Scan(&encoded); err != nil {
			return payment.CheckoutResult{}, err
		}
		raw, e := s.open(encoded)
		var cached payment.CheckoutResult
		if e != nil || json.Unmarshal(raw, &cached) != nil {
			return payment.CheckoutResult{}, ErrUnavailable
		}
		return cached, nil
	}
	if o.CheckoutState == "sending" || o.CheckoutState == "unknown" {
		return payment.CheckoutResult{}, ErrWorkspaceUnknown
	}
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		return payment.CheckoutResult{}, err
	}
	if err = requireWalletSettings(cfg); err != nil {
		return payment.CheckoutResult{}, err
	}
	// Re-read the exact remote order before creating a new payment URL. Detect
	// externally completed or modified originals before accepting cash.
	if err = s.verifyRechargeBeforePayment(ctx, req.TradeNo, cfg); err != nil {
		return payment.CheckoutResult{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE v2_newapi_recharge SET checkout_state='sending' WHERE trade_no=$1 AND user_id=$2 AND status='pending' AND checkout_state=''`, req.TradeNo, userID)
	if err != nil {
		return payment.CheckoutResult{}, err
	}
	result, createErr := create(ctx)
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if createErr != nil {
		state, message := "unknown", "付款链接创建结果待核对，请勿重复付款"
		// These errors are guaranteed preflight failures in checkoutExternal.
		if errors.Is(createErr, payment.ErrPaymentMethodUnavailable) || errors.Is(createErr, payment.ErrUnsupportedGateway) || errors.Is(createErr, payment.ErrInvalidParameter) || errors.Is(createErr, payment.ErrOrderPaidOrMissing) || errors.Is(createErr, ErrConflict) {
			state = ""
			message = ""
		}
		_, _ = s.db.ExecContext(persist, `UPDATE v2_newapi_recharge SET checkout_state=$2,last_error=$3 WHERE trade_no=$1 AND status='pending'`, req.TradeNo, state, message)
		return payment.CheckoutResult{}, createErr
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return payment.CheckoutResult{}, ErrWorkspaceUnknown
	}
	encoded, err = s.seal(raw)
	if err != nil {
		return payment.CheckoutResult{}, ErrWorkspaceUnknown
	}
	_, err = s.db.ExecContext(persist, `UPDATE v2_newapi_recharge SET checkout_state='ready',checkout_cipher=$2,last_error='' WHERE trade_no=$1`, req.TradeNo, encoded)
	if err != nil {
		return payment.CheckoutResult{}, ErrWorkspaceUnknown
	}
	return result, nil
}
