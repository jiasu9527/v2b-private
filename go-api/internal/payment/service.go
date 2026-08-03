package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/config"
	usersvc "forest/go-api/internal/user"
)

var (
	ErrUnavailable              = errors.New("payment service unavailable")
	ErrInvalidParameter         = errors.New("invalid parameter")
	ErrOrderPaidOrMissing       = errors.New("order does not exist or has been paid")
	ErrPaymentMethodUnavailable = errors.New("payment method unavailable")
	ErrPaymentMethodLocked      = errors.New("payment method is already locked for this order")
	ErrCheckoutInProgress       = errors.New("payment checkout is already being created")
	ErrCheckoutBusy             = errors.New("too many payment checkouts are being created")
	ErrUnsupportedGateway       = errors.New("payment gateway unsupported")
	ErrVerifyFailed             = errors.New("payment verify failed")
	ErrRequestFailed            = errors.New("payment request failed")
)

// Provider checkout creation may spend tens of seconds across exchange-rate
// and payment APIs. Bound the number of transactions that can hold a database
// connection during that external work so unrelated application queries keep
// enough of the 64-connection pool available under checkout floods.
const maxConcurrentCheckoutCreations = 8

var checkoutCreationSlots = make(chan struct{}, maxConcurrentCheckoutCreations)

type CheckoutRequest struct {
	TradeNo        string
	MethodID       int64
	Token          string
	RequestBaseURL string
}

type CheckoutResult struct {
	Type int64 `json:"type"`
	Data any   `json:"data"`
}

type NotifyRequest struct {
	Params  map[string]string
	Headers http.Header
	Body    []byte
}

type notifyResult struct {
	TradeNo      string
	CallbackNo   string
	CustomResult string
	Amount       *int64
}

type Service interface {
	Checkout(ctx context.Context, userID int64, req CheckoutRequest) (CheckoutResult, error)
	Notify(ctx context.Context, method, uuid string, req NotifyRequest) (string, error)
}

type orderManager interface {
	MarkOrderPaid(ctx context.Context, tradeNo string, confirmation usersvc.OrderPaymentConfirmation) error
}

type DBService struct {
	cfg     config.Config
	runtime *config.RuntimeState
	db      *sql.DB
	client  *http.Client
	orders  orderManager
}

type paymentRecord struct {
	ID                 int64
	UUID               string
	Payment            string
	Config             string
	NotifyDomain       sql.NullString
	HandlingFeeFixed   sql.NullInt64
	HandlingFeePercent sql.NullFloat64
	Enable             int64
}

type orderRecord struct {
	ID             int64
	UserID         int64
	TradeNo        string
	PaymentID      sql.NullInt64
	TotalAmount    int64
	HandlingAmount sql.NullInt64
	CheckoutResult sql.NullString
	Status         int64
}

type checkoutSnapshot struct {
	Version   int            `json:"version"`
	PaymentID int64          `json:"payment_id"`
	Total     int64          `json:"total"`
	Result    CheckoutResult `json:"result"`
}

type gatewayOrder struct {
	UserID    int64
	UserEmail string
	TradeNo   string
	Total     int64
	NotifyURL string
	ReturnURL string
	Token     string
}

func NewDBService(cfg config.Config, db *sql.DB, orders orderManager) *DBService {
	return &DBService{
		cfg: cfg,
		db:  db,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		orders: orders,
	}
}

func (s *DBService) WithRuntimeConfig(runtime *config.RuntimeState) *DBService {
	s.runtime = runtime
	return s
}

func (s *DBService) currentConfig() config.Config {
	if s == nil {
		return config.Config{}
	}
	if s.runtime == nil {
		return s.cfg
	}
	return s.runtime.CurrentConfig()
}

func (s *DBService) Checkout(ctx context.Context, userID int64, req CheckoutRequest) (CheckoutResult, error) {
	if s.db == nil {
		return CheckoutResult{}, ErrUnavailable
	}

	req.TradeNo = strings.TrimSpace(req.TradeNo)
	if req.TradeNo == "" {
		return CheckoutResult{}, ErrInvalidParameter
	}
	if !tryAcquireCheckoutCreationSlot() {
		return CheckoutResult{}, ErrCheckoutBusy
	}
	defer releaseCheckoutCreationSlot()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("begin checkout transaction: %w", err)
	}

	order, ok, err := s.lockPendingOrderTx(ctx, tx, userID, req.TradeNo)
	if err != nil {
		_ = tx.Rollback()
		return CheckoutResult{}, err
	}
	if !ok || order.Status != 0 {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrOrderPaidOrMissing
	}

	if order.TotalAmount < 0 {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrInvalidParameter
	}
	if order.TotalAmount == 0 {
		_ = tx.Rollback()
		if s.orders == nil {
			return CheckoutResult{}, ErrUnavailable
		}
		if err := s.orders.MarkOrderPaid(ctx, req.TradeNo, usersvc.OrderPaymentConfirmation{CallbackNo: req.TradeNo}); err != nil {
			return CheckoutResult{}, err
		}
		return CheckoutResult{Type: -1, Data: true}, nil
	}

	handlingAmount := int64(0)
	if order.HandlingAmount.Valid {
		handlingAmount = order.HandlingAmount.Int64
	}
	if order.PaymentID.Valid {
		// A generated payment link remains payable until the order is cancelled.
		// Do not invalidate it by allowing a later checkout to overwrite the
		// payment method or the amount that was signed for that link.
		if order.PaymentID.Int64 != req.MethodID {
			_ = tx.Rollback()
			return CheckoutResult{}, ErrPaymentMethodLocked
		}
	}
	if handlingAmount < 0 || order.TotalAmount > math.MaxInt64-handlingAmount {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrInvalidParameter
	}
	total := order.TotalAmount + handlingAmount
	if total <= 0 {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrInvalidParameter
	}
	if order.CheckoutResult.Valid && strings.TrimSpace(order.CheckoutResult.String) != "" {
		result, err := decodeCheckoutSnapshot(order.CheckoutResult.String, req.MethodID, total)
		_ = tx.Rollback()
		return result, err
	}

	paymentMethod, ok, err := s.loadPaymentMethodTx(ctx, tx, req.MethodID)
	if err != nil {
		_ = tx.Rollback()
		return CheckoutResult{}, err
	}
	if !ok || paymentMethod.Enable != 1 {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrPaymentMethodUnavailable
	}

	if !order.PaymentID.Valid {
		handlingAmount = 0
		if paymentMethod.HandlingFeeFixed.Valid || paymentMethod.HandlingFeePercent.Valid {
			if paymentMethod.HandlingFeeFixed.Int64 < 0 || paymentMethod.HandlingFeePercent.Float64 < 0 || math.IsNaN(paymentMethod.HandlingFeePercent.Float64) || math.IsInf(paymentMethod.HandlingFeePercent.Float64, 0) {
				_ = tx.Rollback()
				return CheckoutResult{}, ErrInvalidParameter
			}
			handlingAmount = int64(float64(order.TotalAmount)*(paymentMethod.HandlingFeePercent.Float64/100) + float64(paymentMethod.HandlingFeeFixed.Int64) + 0.5)
		}

		if _, err := tx.ExecContext(ctx, `UPDATE v2_order SET payment_id = $2, handling_amount = $3, updated_at = $4 WHERE id = $1`,
			order.ID,
			paymentMethod.ID,
			nullInt64Value(handlingAmount),
			time.Now().Unix(),
		); err != nil {
			_ = tx.Rollback()
			return CheckoutResult{}, fmt.Errorf("update order payment method: %w", err)
		}
		if handlingAmount < 0 || order.TotalAmount > math.MaxInt64-handlingAmount {
			_ = tx.Rollback()
			return CheckoutResult{}, ErrInvalidParameter
		}
		total = order.TotalAmount + handlingAmount
		if total <= 0 {
			_ = tx.Rollback()
			return CheckoutResult{}, ErrInvalidParameter
		}
	}

	if err := tx.Commit(); err != nil {
		return CheckoutResult{}, fmt.Errorf("commit checkout payment selection: %w", err)
	}
	return s.createCheckoutOnce(ctx, userID, req, paymentMethod, total, handlingAmount)
}

func (s *DBService) createCheckoutOnce(ctx context.Context, userID int64, req CheckoutRequest, paymentMethod paymentRecord, total, handlingAmount int64) (CheckoutResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("begin checkout creation transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var acquired bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0::bigint))`, "checkout:"+req.TradeNo).Scan(&acquired); err != nil {
		return CheckoutResult{}, fmt.Errorf("lock checkout creation: %w", err)
	}
	if !acquired {
		return CheckoutResult{}, ErrCheckoutInProgress
	}

	var current orderRecord
	err = tx.QueryRowContext(ctx, `SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result, status
FROM v2_order
WHERE trade_no = $1 AND user_id = $2`, req.TradeNo, userID).Scan(
		&current.ID,
		&current.UserID,
		&current.TradeNo,
		&current.PaymentID,
		&current.TotalAmount,
		&current.HandlingAmount,
		&current.CheckoutResult,
		&current.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CheckoutResult{}, ErrOrderPaidOrMissing
		}
		return CheckoutResult{}, fmt.Errorf("reload checkout order: %w", err)
	}
	if current.Status != 0 || !current.PaymentID.Valid || current.PaymentID.Int64 != paymentMethod.ID || current.TotalAmount != total-handlingAmount || nullablePaymentInt64(current.HandlingAmount) != handlingAmount {
		return CheckoutResult{}, ErrOrderPaidOrMissing
	}
	if current.CheckoutResult.Valid && strings.TrimSpace(current.CheckoutResult.String) != "" {
		return decodeCheckoutSnapshot(current.CheckoutResult.String, paymentMethod.ID, total)
	}

	gatewayConfig, err := parseGatewayConfig(paymentMethod.Config)
	if err != nil {
		return CheckoutResult{}, err
	}

	userEmail := ""
	if needsUserEmail(paymentMethod.Payment) {
		userEmail, err = findUserEmailTx(ctx, tx, userID)
		if err != nil {
			return CheckoutResult{}, err
		}
	}

	result, err := buildGatewayCheckout(ctx, s.client, paymentMethod.Payment, gatewayConfig, gatewayOrder{
		UserID:    userID,
		UserEmail: userEmail,
		TradeNo:   current.TradeNo,
		Total:     total,
		NotifyURL: s.notifyURL(paymentMethod),
		ReturnURL: s.returnURL(req.RequestBaseURL, current.TradeNo),
		Token:     strings.TrimSpace(req.Token),
	})
	if err != nil {
		return CheckoutResult{}, err
	}

	encoded, err := encodeCheckoutSnapshot(paymentMethod.ID, total, result)
	if err != nil {
		return CheckoutResult{}, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE v2_order
SET checkout_result = $2, updated_at = $3
WHERE id = $1 AND status = 0 AND payment_id = $4 AND total_amount = $5
  AND COALESCE(handling_amount, 0) = $6 AND checkout_result IS NULL`,
		current.ID,
		encoded,
		time.Now().Unix(),
		paymentMethod.ID,
		current.TotalAmount,
		handlingAmount,
	)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("persist checkout result: %w", err)
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("count persisted checkout result: %w", err)
	}
	if affected != 1 {
		// A provider can synchronously deliver its signed callback before the
		// checkout-create response reaches us. In that case the order is already
		// safely paid and the status guard above intentionally prevents writing a
		// now-useless payment link. Return the provider result instead of showing a
		// false checkout failure to the user.
		var status int64
		if queryErr := tx.QueryRowContext(ctx, `SELECT status FROM v2_order WHERE id = $1`, current.ID).Scan(&status); queryErr == nil && (status == 1 || status == 3 || status == 4) {
			if commitErr := tx.Commit(); commitErr != nil {
				return CheckoutResult{}, fmt.Errorf("commit completed checkout observation: %w", commitErr)
			}
			return result, nil
		}
		return CheckoutResult{}, ErrOrderPaidOrMissing
	}
	if err := tx.Commit(); err != nil {
		return CheckoutResult{}, fmt.Errorf("commit checkout result: %w", err)
	}
	return result, nil
}

func tryAcquireCheckoutCreationSlot() bool {
	select {
	case checkoutCreationSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseCheckoutCreationSlot() {
	<-checkoutCreationSlots
}

func (s *DBService) Notify(ctx context.Context, method, uuid string, req NotifyRequest) (string, error) {
	if s.db == nil {
		return "", ErrUnavailable
	}
	method = strings.TrimSpace(method)
	uuid = strings.TrimSpace(uuid)
	if method == "" || uuid == "" {
		return "", ErrInvalidParameter
	}

	paymentMethod, ok, err := s.loadPaymentByGateway(ctx, method, uuid)
	if err != nil {
		return "", err
	}
	// Disabling a method stops new checkouts, but a previously issued payment
	// link can still settle. Keep accepting its cryptographically verified
	// callback as long as the configured payment record still exists.
	if !ok {
		return "", ErrPaymentMethodUnavailable
	}

	gatewayConfig, err := parseGatewayConfig(paymentMethod.Config)
	if err != nil {
		return "", err
	}

	result, err := verifyGatewayNotify(ctx, s.client, paymentMethod.Payment, gatewayConfig, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result.TradeNo) == "" {
		if result.CustomResult != "" {
			return result.CustomResult, nil
		}
		return "success", nil
	}
	if result.Amount == nil {
		return "", ErrVerifyFailed
	}
	if s.orders == nil {
		return "", ErrUnavailable
	}
	paymentID := paymentMethod.ID
	if err := s.orders.MarkOrderPaid(ctx, result.TradeNo, usersvc.OrderPaymentConfirmation{
		CallbackNo: result.CallbackNo,
		PaymentID:  &paymentID,
		Amount:     result.Amount,
	}); err != nil {
		return "", err
	}
	if result.CustomResult != "" {
		return result.CustomResult, nil
	}
	return "success", nil
}

func (s *DBService) lockPendingOrderTx(ctx context.Context, tx *sql.Tx, userID int64, tradeNo string) (orderRecord, bool, error) {
	var order orderRecord
	err := tx.QueryRowContext(ctx, `SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result, status
FROM v2_order
WHERE trade_no = $1 AND user_id = $2
FOR UPDATE`, tradeNo, userID).Scan(
		&order.ID,
		&order.UserID,
		&order.TradeNo,
		&order.PaymentID,
		&order.TotalAmount,
		&order.HandlingAmount,
		&order.CheckoutResult,
		&order.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return orderRecord{}, false, nil
		}
		return orderRecord{}, false, fmt.Errorf("lock order for checkout: %w", err)
	}
	return order, true, nil
}

func encodeCheckoutSnapshot(paymentID, total int64, result CheckoutResult) (string, error) {
	if paymentID <= 0 || total <= 0 || !validCheckoutResult(result) {
		return "", fmt.Errorf("%w: invalid checkout result", ErrRequestFailed)
	}
	encoded, err := json.Marshal(checkoutSnapshot{
		Version:   1,
		PaymentID: paymentID,
		Total:     total,
		Result:    result,
	})
	if err != nil {
		return "", fmt.Errorf("encode checkout result: %w", err)
	}
	return string(encoded), nil
}

func decodeCheckoutSnapshot(raw string, paymentID, total int64) (CheckoutResult, error) {
	var snapshot checkoutSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return CheckoutResult{}, fmt.Errorf("%w: decode checkout result", ErrRequestFailed)
	}
	if snapshot.Version != 1 || snapshot.PaymentID != paymentID || snapshot.Total != total || !validCheckoutResult(snapshot.Result) {
		return CheckoutResult{}, fmt.Errorf("%w: checkout result does not match order", ErrRequestFailed)
	}
	return snapshot.Result, nil
}

func validCheckoutResult(result CheckoutResult) bool {
	switch result.Type {
	case 0, 1:
		value, ok := result.Data.(string)
		return ok && strings.TrimSpace(value) != ""
	case 2:
		value, ok := result.Data.(bool)
		return ok && value
	default:
		return false
	}
}

func nullablePaymentInt64(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func (s *DBService) loadPaymentMethodTx(ctx context.Context, tx *sql.Tx, methodID int64) (paymentRecord, bool, error) {
	var row paymentRecord
	err := tx.QueryRowContext(ctx, `SELECT id, uuid, payment, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable
FROM v2_payment
WHERE id = $1
LIMIT 1`, methodID).Scan(
		&row.ID,
		&row.UUID,
		&row.Payment,
		&row.Config,
		&row.NotifyDomain,
		&row.HandlingFeeFixed,
		&row.HandlingFeePercent,
		&row.Enable,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return paymentRecord{}, false, nil
		}
		return paymentRecord{}, false, fmt.Errorf("query payment method: %w", err)
	}
	return row, true, nil
}

func (s *DBService) loadPaymentByGateway(ctx context.Context, method, uuid string) (paymentRecord, bool, error) {
	var row paymentRecord
	err := s.db.QueryRowContext(ctx, `SELECT id, uuid, payment, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable
FROM v2_payment
WHERE payment = $1 AND uuid = $2
LIMIT 1`, method, uuid).Scan(
		&row.ID,
		&row.UUID,
		&row.Payment,
		&row.Config,
		&row.NotifyDomain,
		&row.HandlingFeeFixed,
		&row.HandlingFeePercent,
		&row.Enable,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return paymentRecord{}, false, nil
		}
		return paymentRecord{}, false, fmt.Errorf("query payment method by gateway: %w", err)
	}
	return row, true, nil
}

func parseGatewayConfig(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("decode payment config: %w", err)
	}
	result := make(map[string]string, len(payload))
	for key, value := range payload {
		switch typed := value.(type) {
		case nil:
			result[key] = ""
		case string:
			result[key] = strings.TrimSpace(typed)
		default:
			result[key] = strings.TrimSpace(fmt.Sprint(typed))
		}
	}
	return result, nil
}

func (s *DBService) notifyURL(paymentMethod paymentRecord) string {
	path := "/api/v1/guest/payment/notify/" + strings.TrimSpace(paymentMethod.Payment) + "/" + strings.TrimSpace(paymentMethod.UUID)
	if paymentMethod.NotifyDomain.Valid && strings.TrimSpace(paymentMethod.NotifyDomain.String) != "" {
		return strings.TrimRight(strings.TrimSpace(paymentMethod.NotifyDomain.String), "/") + path
	}
	base := strings.TrimRight(strings.TrimSpace(s.currentConfig().AppURL), "/")
	if base == "" {
		base = "http://127.0.0.1"
	}
	return base + path
}

func (s *DBService) returnURL(requestBaseURL, tradeNo string) string {
	base := strings.TrimRight(normalizePublicBase(requestBaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(s.currentConfig().AppURL), "/")
	}
	if base == "" {
		base = "http://127.0.0.1"
	}
	return base + "/#/order/" + tradeNo
}

func normalizePublicBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Scheme + "://" + parsed.Host
	default:
		return ""
	}
}

func findUserEmailTx(ctx context.Context, tx *sql.Tx, userID int64) (string, error) {
	var email string
	if err := tx.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("query user email: %w", err)
	}
	return email, nil
}

func needsUserEmail(gateway string) bool {
	switch gateway {
	case "StripeALL":
		return true
	default:
		return false
	}
}

func nullInt64Value(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func configValue(values map[string]string, key string) string {
	return strings.TrimSpace(values[key])
}

func configInt(values map[string]string, key string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(values[key]), 10, 64)
	return parsed
}
