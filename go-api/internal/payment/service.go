package payment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	ErrCheckoutInProgress       = errors.New("payment checkout is already being created")
	ErrCheckoutConfigChanged    = errors.New("payment checkout configuration changed after the first attempt")
	ErrUnsupportedGateway       = errors.New("payment gateway unsupported")
	ErrVerifyFailed             = errors.New("payment verify failed")
	ErrRequestFailed            = errors.New("payment request failed")
)

const checkoutClaimLease = 2 * time.Minute

const paymentCallbackSettlementTimeout = 10 * time.Second

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
	claimFn func() (string, error)
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
	CheckoutClaim  sql.NullString
	Fingerprint    sql.NullString
	ClaimActive    bool
	Status         int64
}

type paymentAttemptRecord struct {
	ID             int64
	OrderID        int64
	PaymentID      int64
	HandlingAmount int64
	Amount         int64
	CheckoutResult sql.NullString
	CheckoutClaim  sql.NullString
	Fingerprint    sql.NullString
	ClaimActive    bool
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
	switchingMethod := order.PaymentID.Valid && order.PaymentID.Int64 != req.MethodID
	if !switchingMethod {
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
	}
	if order.ClaimActive {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrCheckoutInProgress
	}

	var selectedAttempt paymentAttemptRecord
	if switchingMethod {
		if err := archiveCurrentPaymentAttemptTx(ctx, tx, order); err != nil {
			_ = tx.Rollback()
			return CheckoutResult{}, err
		}
		selectedAttempt, ok, err = loadPaymentAttemptTx(ctx, tx, order.ID, req.MethodID)
		if err != nil {
			_ = tx.Rollback()
			return CheckoutResult{}, err
		}
		if ok {
			if selectedAttempt.Status != 0 {
				_ = tx.Rollback()
				return CheckoutResult{}, ErrOrderPaidOrMissing
			}
			if selectedAttempt.ClaimActive {
				_ = tx.Rollback()
				return CheckoutResult{}, ErrCheckoutInProgress
			}
			handlingAmount = selectedAttempt.HandlingAmount
			if !validAttemptAmount(order.TotalAmount, handlingAmount, selectedAttempt.Amount) {
				_ = tx.Rollback()
				return CheckoutResult{}, ErrInvalidParameter
			}
			if selectedAttempt.CheckoutResult.Valid && strings.TrimSpace(selectedAttempt.CheckoutResult.String) != "" {
				result, decodeErr := decodeCheckoutSnapshot(selectedAttempt.CheckoutResult.String, req.MethodID, selectedAttempt.Amount)
				if decodeErr != nil {
					_ = tx.Rollback()
					return CheckoutResult{}, decodeErr
				}
				if err := selectCachedPaymentAttemptTx(ctx, tx, order.ID, selectedAttempt, time.Now().Unix()); err != nil {
					_ = tx.Rollback()
					return CheckoutResult{}, err
				}
				if err := tx.Commit(); err != nil {
					return CheckoutResult{}, fmt.Errorf("commit cached payment method switch: %w", err)
				}
				return result, nil
			}
		}
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

	if !order.PaymentID.Valid || (switchingMethod && selectedAttempt.ID == 0) {
		handlingAmount = 0
		if paymentMethod.HandlingFeeFixed.Valid || paymentMethod.HandlingFeePercent.Valid {
			if paymentMethod.HandlingFeeFixed.Int64 < 0 || paymentMethod.HandlingFeePercent.Float64 < 0 || math.IsNaN(paymentMethod.HandlingFeePercent.Float64) || math.IsInf(paymentMethod.HandlingFeePercent.Float64, 0) {
				_ = tx.Rollback()
				return CheckoutResult{}, ErrInvalidParameter
			}
			handlingAmount = int64(float64(order.TotalAmount)*(paymentMethod.HandlingFeePercent.Float64/100) + float64(paymentMethod.HandlingFeeFixed.Int64) + 0.5)
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
	notifyURL := s.notifyURL(paymentMethod)
	returnURL := s.returnURL(req.RequestBaseURL, req.TradeNo)
	fingerprint := checkoutFingerprint(paymentMethod, req, userID, total, notifyURL, returnURL)
	existingFingerprint := order.Fingerprint
	if switchingMethod && selectedAttempt.ID != 0 {
		existingFingerprint = selectedAttempt.Fingerprint
	}
	if existingFingerprint.Valid && strings.TrimSpace(existingFingerprint.String) != "" && existingFingerprint.String != fingerprint {
		_ = tx.Rollback()
		return CheckoutResult{}, ErrCheckoutConfigChanged
	}

	claim, err := s.newCheckoutClaim()
	if err != nil {
		_ = tx.Rollback()
		return CheckoutResult{}, err
	}
	if err := claimPaymentAttemptTx(ctx, tx, order.ID, paymentMethod.ID, handlingAmount, total, claim, fingerprint, time.Now().Unix()); err != nil {
		_ = tx.Rollback()
		return CheckoutResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_order
SET payment_id = $2,
    handling_amount = $3,
    checkout_claim = $4,
    checkout_claim_expires_at = EXTRACT(EPOCH FROM NOW())::BIGINT + $5,
    checkout_fingerprint = $6,
    checkout_result = NULL,
    updated_at = $7
WHERE id = $1`,
		order.ID,
		paymentMethod.ID,
		nullInt64Value(handlingAmount),
		claim,
		int64(checkoutClaimLease/time.Second),
		fingerprint,
		time.Now().Unix(),
	); err != nil {
		_ = tx.Rollback()
		return CheckoutResult{}, fmt.Errorf("claim checkout creation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return CheckoutResult{}, fmt.Errorf("commit checkout creation claim: %w", err)
	}
	return s.createCheckoutOnce(ctx, userID, req, paymentMethod, total, handlingAmount, claim, fingerprint, notifyURL, returnURL)
}

func archiveCurrentPaymentAttemptTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	if !order.PaymentID.Valid || order.PaymentID.Int64 <= 0 {
		return nil
	}
	handlingAmount := nullablePaymentInt64(order.HandlingAmount)
	if handlingAmount < 0 || order.TotalAmount <= 0 || order.TotalAmount > math.MaxInt64-handlingAmount {
		return ErrInvalidParameter
	}
	amount := order.TotalAmount + handlingAmount
	if amount <= 0 {
		return ErrInvalidParameter
	}
	now := time.Now().Unix()
	_, err := tx.ExecContext(ctx, `INSERT INTO v2_order_payment_attempt (
order_id, payment_id, handling_amount, amount, checkout_result,
checkout_claim, checkout_claim_expires_at, checkout_fingerprint,
status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, NULL, NULL, $6, 0, $7, $7)
ON CONFLICT (order_id, payment_id) DO UPDATE SET
    handling_amount = EXCLUDED.handling_amount,
    amount = EXCLUDED.amount,
    checkout_result = COALESCE(v2_order_payment_attempt.checkout_result, EXCLUDED.checkout_result),
    checkout_claim = NULL,
    checkout_claim_expires_at = NULL,
    checkout_fingerprint = COALESCE(v2_order_payment_attempt.checkout_fingerprint, EXCLUDED.checkout_fingerprint),
    updated_at = EXCLUDED.updated_at
WHERE v2_order_payment_attempt.status = 0`,
		order.ID,
		order.PaymentID.Int64,
		handlingAmount,
		amount,
		nullableStringValue(order.CheckoutResult),
		nullableStringValue(order.Fingerprint),
		now,
	)
	if err != nil {
		return fmt.Errorf("archive current payment attempt: %w", err)
	}
	return nil
}

func loadPaymentAttemptTx(ctx context.Context, tx *sql.Tx, orderID, paymentID int64) (paymentAttemptRecord, bool, error) {
	var attempt paymentAttemptRecord
	err := tx.QueryRowContext(ctx, `SELECT
id, order_id, payment_id, handling_amount, amount, checkout_result,
checkout_claim, checkout_fingerprint,
COALESCE(checkout_claim IS NOT NULL AND checkout_claim_expires_at > EXTRACT(EPOCH FROM NOW())::BIGINT, FALSE) AS checkout_claim_active,
status
FROM v2_order_payment_attempt
WHERE order_id = $1 AND payment_id = $2
FOR UPDATE`, orderID, paymentID).Scan(
		&attempt.ID,
		&attempt.OrderID,
		&attempt.PaymentID,
		&attempt.HandlingAmount,
		&attempt.Amount,
		&attempt.CheckoutResult,
		&attempt.CheckoutClaim,
		&attempt.Fingerprint,
		&attempt.ClaimActive,
		&attempt.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return paymentAttemptRecord{}, false, nil
		}
		return paymentAttemptRecord{}, false, fmt.Errorf("load payment attempt: %w", err)
	}
	return attempt, true, nil
}

func selectCachedPaymentAttemptTx(ctx context.Context, tx *sql.Tx, orderID int64, attempt paymentAttemptRecord, now int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE v2_order
SET payment_id = $2,
    handling_amount = $3,
    checkout_result = $4,
    checkout_claim = NULL,
    checkout_claim_expires_at = NULL,
    checkout_fingerprint = $5,
    updated_at = $6
WHERE id = $1 AND status = 0`,
		orderID,
		attempt.PaymentID,
		nullInt64Value(attempt.HandlingAmount),
		nullableStringValue(attempt.CheckoutResult),
		nullableStringValue(attempt.Fingerprint),
		now,
	); err != nil {
		return fmt.Errorf("select cached payment attempt: %w", err)
	}
	return nil
}

func claimPaymentAttemptTx(ctx context.Context, tx *sql.Tx, orderID, paymentID, handlingAmount, amount int64, claim, fingerprint string, now int64) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO v2_order_payment_attempt (
order_id, payment_id, handling_amount, amount, checkout_result,
checkout_claim, checkout_claim_expires_at, checkout_fingerprint,
status, created_at, updated_at
) VALUES ($1, $2, $3, $4, NULL, $5, $6 + $7, $8, 0, $6, $6)
ON CONFLICT (order_id, payment_id) DO UPDATE SET
    handling_amount = EXCLUDED.handling_amount,
    amount = EXCLUDED.amount,
    checkout_result = NULL,
    checkout_claim = EXCLUDED.checkout_claim,
    checkout_claim_expires_at = EXCLUDED.checkout_claim_expires_at,
    checkout_fingerprint = EXCLUDED.checkout_fingerprint,
    updated_at = EXCLUDED.updated_at
WHERE v2_order_payment_attempt.status = 0
  AND COALESCE(
    v2_order_payment_attempt.checkout_claim IS NULL
      OR v2_order_payment_attempt.checkout_claim_expires_at <= $6,
    TRUE
  )`,
		orderID,
		paymentID,
		handlingAmount,
		amount,
		claim,
		now,
		int64(checkoutClaimLease/time.Second),
		fingerprint,
	)
	if err != nil {
		return fmt.Errorf("claim payment attempt: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count claimed payment attempt: %w", err)
	}
	if affected != 1 {
		return ErrCheckoutInProgress
	}
	return nil
}

func validAttemptAmount(orderAmount, handlingAmount, amount int64) bool {
	return orderAmount > 0 && handlingAmount >= 0 && orderAmount <= math.MaxInt64-handlingAmount && orderAmount+handlingAmount == amount
}

func (s *DBService) createCheckoutOnce(ctx context.Context, userID int64, req CheckoutRequest, paymentMethod paymentRecord, total, handlingAmount int64, claim, fingerprint, notifyURL, returnURL string) (CheckoutResult, error) {
	gatewayConfig, err := parseGatewayConfig(paymentMethod.Config)
	if err != nil {
		s.releaseCheckoutClaim(userID, req.TradeNo, claim)
		return CheckoutResult{}, err
	}

	userEmail := ""
	if needsUserEmail(paymentMethod.Payment) {
		userEmail, err = s.findUserEmail(ctx, userID)
		if err != nil {
			s.releaseCheckoutClaim(userID, req.TradeNo, claim)
			return CheckoutResult{}, err
		}
	}

	result, err := buildGatewayCheckout(ctx, s.client, paymentMethod.Payment, gatewayConfig, gatewayOrder{
		UserID:    userID,
		UserEmail: userEmail,
		TradeNo:   req.TradeNo,
		Total:     total,
		NotifyURL: notifyURL,
		ReturnURL: returnURL,
		Token:     strings.TrimSpace(req.Token),
	})
	if err != nil {
		s.releaseCheckoutClaim(userID, req.TradeNo, claim)
		return CheckoutResult{}, err
	}

	encoded, err := encodeCheckoutSnapshot(paymentMethod.ID, total, result)
	if err != nil {
		s.releaseCheckoutClaim(userID, req.TradeNo, claim)
		return CheckoutResult{}, err
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.persistCheckoutResult(persistCtx, userID, req.TradeNo, paymentMethod.ID, total, handlingAmount, claim, fingerprint, encoded, result)
}

func checkoutFingerprint(paymentMethod paymentRecord, req CheckoutRequest, userID, total int64, notifyURL, returnURL string) string {
	tokenHash := sha256.Sum256([]byte(strings.TrimSpace(req.Token)))
	payload := struct {
		PaymentID int64  `json:"payment_id"`
		Gateway   string `json:"gateway"`
		Config    string `json:"config"`
		UserID    int64  `json:"user_id"`
		TradeNo   string `json:"trade_no"`
		Total     int64  `json:"total"`
		NotifyURL string `json:"notify_url"`
		ReturnURL string `json:"return_url"`
		TokenHash string `json:"token_hash"`
	}{
		PaymentID: paymentMethod.ID,
		Gateway:   strings.TrimSpace(paymentMethod.Payment),
		Config:    strings.TrimSpace(paymentMethod.Config),
		UserID:    userID,
		TradeNo:   strings.TrimSpace(req.TradeNo),
		Total:     total,
		NotifyURL: strings.TrimSpace(notifyURL),
		ReturnURL: strings.TrimSpace(returnURL),
		TokenHash: hex.EncodeToString(tokenHash[:]),
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s *DBService) newCheckoutClaim() (string, error) {
	if s.claimFn != nil {
		return s.claimFn()
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate checkout claim: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (s *DBService) releaseCheckoutClaim(userID int64, tradeNo, claim string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(ctx, `UPDATE v2_order_payment_attempt AS attempt
SET checkout_claim = NULL,
    checkout_claim_expires_at = NULL,
    checkout_fingerprint = NULL,
    updated_at = $4
FROM v2_order AS orders
WHERE attempt.order_id = orders.id
  AND orders.user_id = $1 AND orders.trade_no = $2
  AND attempt.checkout_claim = $3
  AND attempt.status = 0 AND attempt.checkout_result IS NULL`, userID, tradeNo, claim, time.Now().Unix())
	_, _ = s.db.ExecContext(ctx, `UPDATE v2_order
SET checkout_claim = NULL,
    checkout_claim_expires_at = NULL,
    checkout_fingerprint = NULL,
    updated_at = $4
WHERE user_id = $1 AND trade_no = $2 AND checkout_claim = $3
  AND status = 0 AND checkout_result IS NULL`, userID, tradeNo, claim, time.Now().Unix())
}

func (s *DBService) persistCheckoutResult(ctx context.Context, userID int64, tradeNo string, paymentID, total, handlingAmount int64, claim, fingerprint, encoded string, result CheckoutResult) (CheckoutResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("begin checkout result transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var current orderRecord
	err = tx.QueryRowContext(ctx, `SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result, checkout_claim, checkout_fingerprint, status
FROM v2_order
WHERE trade_no = $1 AND user_id = $2
FOR UPDATE`, tradeNo, userID).Scan(
		&current.ID,
		&current.UserID,
		&current.TradeNo,
		&current.PaymentID,
		&current.TotalAmount,
		&current.HandlingAmount,
		&current.CheckoutResult,
		&current.CheckoutClaim,
		&current.Fingerprint,
		&current.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CheckoutResult{}, ErrOrderPaidOrMissing
		}
		return CheckoutResult{}, fmt.Errorf("lock checkout result order: %w", err)
	}
	// Persist the provider result in its own attempt row first. This keeps an
	// older link usable when the user has already switched the order to another
	// payment method while the provider request was in flight.
	attemptPersisted := false
	if attemptUpdate, updateErr := tx.ExecContext(ctx, `UPDATE v2_order_payment_attempt
SET checkout_result = $4,
    checkout_claim = NULL,
    checkout_claim_expires_at = NULL,
    updated_at = $5
WHERE order_id = $1 AND payment_id = $2 AND checkout_claim = $3
  AND checkout_fingerprint = $6 AND checkout_result IS NULL AND status IN (0, 1)`,
		current.ID, paymentID, claim, encoded, time.Now().Unix(), fingerprint); updateErr != nil {
		return CheckoutResult{}, fmt.Errorf("persist payment attempt result: %w", updateErr)
	} else if affected, affectedErr := attemptUpdate.RowsAffected(); affectedErr != nil {
		return CheckoutResult{}, fmt.Errorf("count payment attempt result: %w", affectedErr)
	} else {
		attemptPersisted = affected == 1
	}
	if current.Status == 1 || current.Status == 3 || current.Status == 4 {
		if current.CheckoutClaim.Valid && current.CheckoutClaim.String == claim {
			if _, err := tx.ExecContext(ctx, `UPDATE v2_order SET checkout_claim = NULL, checkout_claim_expires_at = NULL WHERE id = $1`, current.ID); err != nil {
				return CheckoutResult{}, fmt.Errorf("clear completed checkout claim: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return CheckoutResult{}, fmt.Errorf("commit completed checkout observation: %w", err)
		}
		return CheckoutResult{Type: -1, Data: true}, nil
	}
	if current.CheckoutResult.Valid && strings.TrimSpace(current.CheckoutResult.String) != "" {
		return decodeCheckoutSnapshot(current.CheckoutResult.String, paymentID, total)
	}
	if current.Status != 0 || !current.PaymentID.Valid || current.PaymentID.Int64 != paymentID || current.TotalAmount != total-handlingAmount || nullablePaymentInt64(current.HandlingAmount) != handlingAmount || !current.Fingerprint.Valid || current.Fingerprint.String != fingerprint {
		if attemptPersisted {
			if err := tx.Commit(); err != nil {
				return CheckoutResult{}, fmt.Errorf("commit switched payment attempt result: %w", err)
			}
		}
		return CheckoutResult{}, ErrOrderPaidOrMissing
	}
	if !current.CheckoutClaim.Valid || current.CheckoutClaim.String != claim {
		if attemptPersisted {
			if err := tx.Commit(); err != nil {
				return CheckoutResult{}, fmt.Errorf("commit stale payment attempt result: %w", err)
			}
		}
		return CheckoutResult{}, ErrCheckoutInProgress
	}

	if !attemptPersisted {
		// Legacy orders created before the attempt table are still supported;
		// create their attempt record when the first result is successfully
		// persisted.
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_order_payment_attempt (
order_id, payment_id, handling_amount, amount, checkout_result,
checkout_claim, checkout_claim_expires_at, checkout_fingerprint,
status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, NULL, NULL, $6, 0, $7, $7)
ON CONFLICT (order_id, payment_id) DO UPDATE SET
    handling_amount = EXCLUDED.handling_amount,
    amount = EXCLUDED.amount,
    checkout_result = EXCLUDED.checkout_result,
    checkout_claim = NULL,
    checkout_claim_expires_at = NULL,
    checkout_fingerprint = EXCLUDED.checkout_fingerprint,
    updated_at = EXCLUDED.updated_at`,
			current.ID, paymentID, handlingAmount, total, encoded, fingerprint, time.Now().Unix()); err != nil {
			return CheckoutResult{}, fmt.Errorf("create legacy payment attempt result: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_order
SET checkout_result = $2, checkout_claim = NULL, checkout_claim_expires_at = NULL, updated_at = $3
WHERE id = $1`, current.ID, encoded, time.Now().Unix()); err != nil {
		return CheckoutResult{}, fmt.Errorf("persist checkout result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CheckoutResult{}, fmt.Errorf("commit checkout result: %w", err)
	}
	return result, nil
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
		return "", fmt.Errorf("load payment callback method=%q uuid=%q: %w", method, uuid, err)
	}
	// Disabling a method stops new checkouts, but a previously issued payment
	// link can still settle. Keep accepting its cryptographically verified
	// callback as long as the configured payment record still exists.
	if !ok {
		return "", fmt.Errorf("load payment callback method=%q uuid=%q: %w", method, uuid, ErrPaymentMethodUnavailable)
	}

	gatewayConfig, err := parseGatewayConfig(paymentMethod.Config)
	if err != nil {
		return "", fmt.Errorf("parse payment callback configuration payment_id=%d gateway=%q: %w", paymentMethod.ID, paymentMethod.Payment, err)
	}

	result, err := verifyGatewayNotify(ctx, s.client, paymentMethod.Payment, gatewayConfig, req)
	if err != nil {
		return "", fmt.Errorf("verify payment callback payment_id=%d gateway=%q: %w", paymentMethod.ID, paymentMethod.Payment, err)
	}
	if strings.TrimSpace(result.TradeNo) == "" {
		if result.CustomResult != "" {
			return result.CustomResult, nil
		}
		return "success", nil
	}
	if result.Amount == nil {
		return "", fmt.Errorf("verify payment callback payment_id=%d gateway=%q: missing trusted amount: %w", paymentMethod.ID, paymentMethod.Payment, ErrVerifyFailed)
	}
	if s.orders == nil {
		return "", fmt.Errorf("settle payment callback trade_no=%q: %w", result.TradeNo, ErrUnavailable)
	}
	paymentID := paymentMethod.ID
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), paymentCallbackSettlementTimeout)
	defer cancel()
	if err := s.orders.MarkOrderPaid(settleCtx, result.TradeNo, usersvc.OrderPaymentConfirmation{
		CallbackNo: result.CallbackNo,
		PaymentID:  &paymentID,
		Amount:     result.Amount,
	}); err != nil {
		return "", fmt.Errorf("settle payment callback trade_no=%q callback_no=%q payment_id=%d amount=%d: %w", result.TradeNo, result.CallbackNo, paymentID, *result.Amount, err)
	}
	if result.CustomResult != "" {
		return result.CustomResult, nil
	}
	return "success", nil
}

func (s *DBService) lockPendingOrderTx(ctx context.Context, tx *sql.Tx, userID int64, tradeNo string) (orderRecord, bool, error) {
	var order orderRecord
	err := tx.QueryRowContext(ctx, `SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result,
	       checkout_claim,
	       checkout_fingerprint,
	       COALESCE(checkout_claim IS NOT NULL AND checkout_claim_expires_at > EXTRACT(EPOCH FROM NOW())::BIGINT, FALSE) AS checkout_claim_active,
       status
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
		&order.CheckoutClaim,
		&order.Fingerprint,
		&order.ClaimActive,
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

func nullableStringValue(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}

func configValue(values map[string]string, key string) string {
	return strings.TrimSpace(values[key])
}

func configInt(values map[string]string, key string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(values[key]), 10, 64)
	return parsed
}
