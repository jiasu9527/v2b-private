package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/config"
)

var (
	ErrUnavailable              = errors.New("payment service unavailable")
	ErrInvalidParameter         = errors.New("invalid parameter")
	ErrOrderPaidOrMissing       = errors.New("order does not exist or has been paid")
	ErrPaymentMethodUnavailable = errors.New("payment method unavailable")
	ErrUnsupportedGateway       = errors.New("payment gateway unsupported")
	ErrVerifyFailed             = errors.New("payment verify failed")
	ErrRequestFailed            = errors.New("payment request failed")
)

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
}

type Service interface {
	Checkout(ctx context.Context, userID int64, req CheckoutRequest) (CheckoutResult, error)
	Notify(ctx context.Context, method, uuid string, req NotifyRequest) (string, error)
}

type orderManager interface {
	MarkOrderPaid(ctx context.Context, tradeNo, callbackNo string, allowCancelled bool) error
}

type DBService struct {
	cfg    config.Config
	db     *sql.DB
	client *http.Client
	orders orderManager
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
	TotalAmount    int64
	HandlingAmount sql.NullInt64
	Status         int64
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

func (s *DBService) Checkout(ctx context.Context, userID int64, req CheckoutRequest) (CheckoutResult, error) {
	if s.db == nil {
		return CheckoutResult{}, ErrUnavailable
	}

	req.TradeNo = strings.TrimSpace(req.TradeNo)
	if req.TradeNo == "" {
		return CheckoutResult{}, ErrInvalidParameter
	}

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

	if order.TotalAmount <= 0 {
		_ = tx.Rollback()
		if s.orders == nil {
			return CheckoutResult{}, ErrUnavailable
		}
		if err := s.orders.MarkOrderPaid(ctx, req.TradeNo, req.TradeNo, false); err != nil {
			return CheckoutResult{}, err
		}
		return CheckoutResult{Type: -1, Data: true}, nil
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

	handlingAmount := int64(0)
	if paymentMethod.HandlingFeeFixed.Valid || paymentMethod.HandlingFeePercent.Valid {
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

	if err := tx.Commit(); err != nil {
		return CheckoutResult{}, fmt.Errorf("commit checkout payment selection: %w", err)
	}

	gatewayConfig, err := parseGatewayConfig(paymentMethod.Config)
	if err != nil {
		return CheckoutResult{}, err
	}

	userEmail := ""
	if needsUserEmail(paymentMethod.Payment) {
		userEmail, err = s.findUserEmail(ctx, userID)
		if err != nil {
			return CheckoutResult{}, err
		}
	}

	total := order.TotalAmount + handlingAmount
	return buildGatewayCheckout(ctx, s.client, paymentMethod.Payment, gatewayConfig, gatewayOrder{
		UserID:    userID,
		UserEmail: userEmail,
		TradeNo:   order.TradeNo,
		Total:     total,
		NotifyURL: s.notifyURL(paymentMethod),
		ReturnURL: s.returnURL(req.RequestBaseURL, order.TradeNo),
		Token:     strings.TrimSpace(req.Token),
	})
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
	if !ok || paymentMethod.Enable != 1 {
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
	if s.orders == nil {
		return "", ErrUnavailable
	}
	if err := s.orders.MarkOrderPaid(ctx, result.TradeNo, result.CallbackNo, false); err != nil {
		return "", err
	}
	if result.CustomResult != "" {
		return result.CustomResult, nil
	}
	return "success", nil
}

func (s *DBService) lockPendingOrderTx(ctx context.Context, tx *sql.Tx, userID int64, tradeNo string) (orderRecord, bool, error) {
	var order orderRecord
	err := tx.QueryRowContext(ctx, `SELECT id, user_id, trade_no, total_amount, handling_amount, status
FROM v2_order
WHERE trade_no = $1 AND user_id = $2
FOR UPDATE`, tradeNo, userID).Scan(
		&order.ID,
		&order.UserID,
		&order.TradeNo,
		&order.TotalAmount,
		&order.HandlingAmount,
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
	base := strings.TrimRight(strings.TrimSpace(s.cfg.AppURL), "/")
	if base == "" {
		base = "http://127.0.0.1"
	}
	return base + path
}

func (s *DBService) returnURL(requestBaseURL, tradeNo string) string {
	base := strings.TrimRight(normalizePublicBase(requestBaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(s.cfg.AppURL), "/")
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

func (s *DBService) findUserEmail(ctx context.Context, userID int64) (string, error) {
	var email string
	if err := s.db.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&email); err != nil {
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
