package newapibridge

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"forest/go-api/internal/payment"
)

// Direct top-ups are a separate financial ledger. They never pass through the
// subscription order ledger, wallet balance, or invitation commission engine.
var rechargeSchema = []string{
	`ALTER TABLE v2_newapi_checkout ADD COLUMN IF NOT EXISTS funding_source TEXT NOT NULL DEFAULT 'wallet' CHECK(funding_source IN ('wallet','direct'))`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_recharge (
 trade_no TEXT PRIMARY KEY, user_id BIGINT NOT NULL, api_url TEXT NOT NULL, request_id TEXT NOT NULL,
 amount_fen BIGINT NOT NULL CHECK(amount_fen>0), handling_amount_fen BIGINT NOT NULL DEFAULT 0 CHECK(handling_amount_fen>=0),
 payment_id BIGINT NOT NULL DEFAULT 0, status TEXT NOT NULL CHECK(status IN ('preparing','prepare_unknown','pending','paid_pending_credit','succeeded')),
 checkout_id TEXT UNIQUE REFERENCES v2_newapi_checkout(id), quote_cipher TEXT NOT NULL,
 config_cipher TEXT NOT NULL, config_revision BIGINT NOT NULL, callback_no TEXT NOT NULL DEFAULT '',
 checkout_state TEXT NOT NULL DEFAULT '', checkout_cipher TEXT NOT NULL DEFAULT '',
 created_at BIGINT NOT NULL, paid_at BIGINT NOT NULL DEFAULT 0, completed_at BIGINT NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at BIGINT NOT NULL DEFAULT 0,
 lease_until BIGINT NOT NULL DEFAULT 0, lease_token TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
 UNIQUE(user_id,request_id))`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_recharge_user ON v2_newapi_recharge(user_id,created_at DESC,trade_no DESC)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_recharge_pending ON v2_newapi_recharge(status,next_attempt_at,lease_until)`,
}

type WorkspaceRechargeRequest struct {
	AmountFen int64  `json:"amount_fen"`
	RequestID string `json:"request_id"`
}
type WorkspaceRechargeCheckoutRequest struct {
	TradeNo   string `json:"trade_no"`
	PaymentID int64  `json:"payment_id"`
}
type RechargeOrder struct {
	TradeNo           string `json:"trade_no"`
	AmountFen         int64  `json:"amount_fen"`
	HandlingAmountFen int64  `json:"handling_amount_fen"`
	TotalAmountFen    int64  `json:"total_amount_fen"`
	Currency          string `json:"currency"`
	Status            string `json:"status"`
	PaymentID         int64  `json:"payment_id"`
	CreatedAt         int64  `json:"created_at"`
	PaidAt            int64  `json:"paid_at"`
	CompletedAt       int64  `json:"completed_at"`
	LastError         string `json:"last_error"`
	CheckoutState     string `json:"checkout_state,omitempty"`
}
type RechargePage struct {
	Items    []RechargeOrder `json:"items"`
	Total    int64           `json:"total"`
	Current  int             `json:"current"`
	PageSize int             `json:"page_size"`
}
type RechargeCheckout struct {
	Order    RechargeOrder          `json:"order"`
	Checkout payment.CheckoutResult `json:"checkout"`
}

const rechargeColumns = `trade_no,amount_fen,handling_amount_fen,status,payment_id,created_at,paid_at,completed_at,last_error,checkout_state`

func scanRecharge(row walletScanner) (RechargeOrder, error) {
	var o RechargeOrder
	err := row.Scan(&o.TradeNo, &o.AmountFen, &o.HandlingAmountFen, &o.Status, &o.PaymentID, &o.CreatedAt, &o.PaidAt, &o.CompletedAt, &o.LastError, &o.CheckoutState)
	o.Currency = "CNY"
	o.TotalAmountFen = o.AmountFen + o.HandlingAmountFen
	return o, err
}
func validRechargeTrade(value string) bool {
	if !strings.HasPrefix(value, "AIT") || len(value) != 31 {
		return false
	}
	raw, err := hex.DecodeString(value[3:])
	return err == nil && len(raw) == 14 && value[3:] == strings.ToLower(value[3:])
}
func (s *Service) WithRechargePayments(p payment.Service) *Service { s.rechargePayments = p; return s }
func (s *Service) OwnsExternalTradeNo(trade string) bool {
	return strings.HasPrefix(strings.TrimSpace(trade), "AIT")
}

type rechargeTopupInfo struct {
	Enabled bool  `json:"enable_online_topup"`
	CNY     bool  `json:"supports_cny_amount_fen"`
	Minimum int64 `json:"min_amount_fen"`
	Methods []struct {
		Type string `json:"type"`
	} `json:"pay_methods"`
}

func rechargeRemoteMethod(top rechargeTopupInfo) string {
	for _, types := range [][]string{{"wallet", "forest_wallet"}, {"alipay", "wxpay"}} {
		for _, method := range top.Methods {
			for _, kind := range types {
				if method.Type == kind {
					return kind
				}
			}
		}
	}
	return ""
}

func (s *Service) WorkspaceRechargeOptions(ctx context.Context, userID int64, auth string) (any, error) {
	cfg, cred, err := s.workspaceCredentials(ctx, userID, auth)
	if err != nil {
		return nil, err
	}
	if err = requireWalletSettings(cfg); err != nil {
		return nil, err
	}
	if _, err = s.workspaceSelf(ctx, cfg, cred, userID); err != nil {
		return nil, err
	}
	info, _, err := s.workspaceRequest(ctx, cfg, &cred, http.MethodGet, "/api/user/topup/info", nil, "")
	if err != nil {
		return nil, err
	}
	var top rechargeTopupInfo
	if workspaceDecode(info.Data, &top) != nil {
		return nil, ErrRemoteUnavailable
	}
	minimum := cfg.MinAmountFen
	if minimum < 100 {
		minimum = 100
	}
	if top.Minimum > minimum {
		minimum = top.Minimum
	}
	methods, err := s.rechargePaymentMethods(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": top.Enabled && top.CNY && rechargeRemoteMethod(top) != "" && len(methods) > 0 && minimum <= cfg.MaxAmountFen, "currency": "CNY", "min_amount_fen": minimum, "max_amount_fen": cfg.MaxAmountFen, "daily_limit_fen": cfg.DailyLimitFen, "payment_methods": methods}, nil
}

// Token-based card entry is intentionally not part of the direct top-up form.
func rechargeGatewayAllowed(gateway string) bool {
	switch gateway {
	case "AlipayF2F", "CoinPayments", "Coinbase", "EPay", "epaypro", "EpusdtPay", "BEasyPaymentUSDT", "BTCPay", "MGate", "StripeCheckout", "StripeALL", "StripeAlipay", "StripeWepay", "WechatPayNative":
		return true
	}
	return false
}
func (s *Service) rechargePaymentMethods(ctx context.Context) ([]map[string]any, error) {
	methods := []map[string]any{}
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(name,''),payment,COALESCE(icon,''),COALESCE(handling_fee_fixed,0),COALESCE(handling_fee_percent,0) FROM v2_payment WHERE enable=1 ORDER BY sort ASC NULLS LAST,id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, fixed int64
		var name, gateway, icon string
		var percent float64
		if err = rows.Scan(&id, &name, &gateway, &icon, &fixed, &percent); err != nil {
			return nil, err
		}
		if !rechargeGatewayAllowed(gateway) || fixed < 0 || percent < 0 {
			continue
		}
		methods = append(methods, map[string]any{"id": id, "name": name, "payment": gateway, "icon": icon, "handling_fee_fixed": fixed, "handling_fee_percent": percent})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return methods, nil
}

func (s *Service) CreateWorkspaceRecharge(ctx context.Context, userID int64, auth string, req WorkspaceRechargeRequest) (RechargeOrder, error) {
	if userID <= 0 || !workspaceRequestID(req.RequestID) || req.AmountFen < 100 {
		return RechargeOrder{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return RechargeOrder{}, err
	}
	// Idempotency survives configuration rotation, remote downtime and expired
	// sessions. Replaying a request is a local status read, never a second create.
	existing, err := scanRecharge(s.db.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM v2_newapi_recharge WHERE user_id=$1 AND request_id=$2`, userID, req.RequestID))
	if err == nil {
		if existing.AmountFen != req.AmountFen {
			return RechargeOrder{}, ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RechargeOrder{}, err
	}
	cfg, cred, err := s.workspaceCredentials(ctx, userID, auth)
	if err != nil {
		return RechargeOrder{}, err
	}
	if err = requireWalletSettings(cfg); err != nil {
		return RechargeOrder{}, err
	}
	methods, err := s.rechargePaymentMethods(ctx)
	if err != nil {
		return RechargeOrder{}, err
	}
	if len(methods) == 0 {
		return RechargeOrder{}, ErrUnavailable
	}
	if req.AmountFen < cfg.MinAmountFen || req.AmountFen > cfg.MaxAmountFen || req.AmountFen > cfg.DailyLimitFen {
		return RechargeOrder{}, ErrLimitExceeded
	}
	if _, err = s.workspaceSelf(ctx, cfg, cred, userID); err != nil {
		return RechargeOrder{}, err
	}
	lock, err := s.workspaceLock(ctx, userID, cfg.APIURL)
	if err != nil {
		return RechargeOrder{}, err
	}
	defer lock.Rollback()
	existing, err = scanRecharge(s.db.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM v2_newapi_recharge WHERE user_id=$1 AND request_id=$2`, userID, req.RequestID))
	if err == nil {
		if existing.AmountFen != req.AmountFen {
			return RechargeOrder{}, ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RechargeOrder{}, err
	}
	info, _, err := s.workspaceRequest(ctx, cfg, &cred, http.MethodGet, "/api/user/topup/info", nil, "")
	if err != nil {
		return RechargeOrder{}, err
	}
	var top rechargeTopupInfo
	if workspaceDecode(info.Data, &top) != nil || !top.Enabled || !top.CNY {
		return RechargeOrder{}, ErrUnavailable
	}
	if req.AmountFen < top.Minimum {
		return RechargeOrder{}, ErrWorkspaceAmount
	}
	method := rechargeRemoteMethod(top)
	if method == "" {
		return RechargeOrder{}, ErrUnavailable
	}
	fresh, err := s.workspaceCNYQuote(ctx, cfg, cred, req.AmountFen)
	if err != nil {
		return RechargeOrder{}, err
	}
	q := workspaceQuote{AmountFen: req.AmountFen, CreditedQuota: fresh.CreditedQuota, Unit: fresh.QuotaPerUnit, Currency: "CNY", DisplayCredit: formatMoneyFen(req.AmountFen), DisplayCurrency: "CNY", DisplayRate: fresh.DisplayRate, RemoteQuoteHash: fresh.QuoteHash, PaymentMethod: method, Revision: cfg.Revision, Expires: time.Now().Add(10 * time.Minute).Unix()}
	if err = s.verifyWorkspaceQuoteUnits(ctx, cfg, q); err != nil {
		return RechargeOrder{}, err
	}
	id, err := walletRandomID()
	if err != nil {
		return RechargeOrder{}, err
	}
	trade := "AIT" + id[:28]
	raw, _ := json.Marshal(q)
	quoteCipher, err := s.seal(raw)
	if err != nil {
		return RechargeOrder{}, err
	}
	raw, _ = json.Marshal(paymentConfig(cfg))
	cfgCipher, err := s.seal(raw)
	if err != nil {
		return RechargeOrder{}, err
	}
	now := time.Now().Unix()
	// Lock settings before the user, matching SaveConfig and wallet Pay. Reserving
	// pending amounts prevents concurrent creations bypassing the rolling cap.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RechargeOrder{}, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM v2_newapi_settings WHERE id=1 FOR SHARE`).Scan(&revision); err != nil {
		return RechargeOrder{}, err
	}
	if revision != cfg.Revision {
		return RechargeOrder{}, ErrConflict
	}
	var banned int64
	if err = tx.QueryRowContext(ctx, `SELECT banned FROM v2_user WHERE id=$1 FOR UPDATE`, userID).Scan(&banned); err != nil {
		return RechargeOrder{}, err
	}
	if banned != 0 {
		return RechargeOrder{}, ErrForbidden
	}
	var reserved int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_fen),0) FROM v2_newapi_recharge WHERE user_id=$1 AND created_at >= $2`, userID, now-86400).Scan(&reserved); err != nil {
		return RechargeOrder{}, err
	}
	if reserved < 0 || reserved > cfg.DailyLimitFen-req.AmountFen {
		return RechargeOrder{}, ErrLimitExceeded
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_newapi_recharge(trade_no,user_id,api_url,request_id,amount_fen,status,quote_cipher,config_cipher,config_revision,created_at,last_error) VALUES($1,$2,$3,$4,$5,'prepare_unknown',$6,$7,$8,$9,'远端充值单创建中，请勿重复付款')`, trade, userID, cfg.APIURL, req.RequestID, req.AmountFen, quoteCipher, cfgCipher, cfg.Revision, now)
	if err != nil {
		return RechargeOrder{}, err
	}
	if err = tx.Commit(); err != nil {
		return RechargeOrder{}, err
	}
	// Persist uncertainty BEFORE the network boundary. A missing response must
	// not permit repeating creation; no payment link exists until verified.
	env, _, err := s.workspaceRequest(ctx, cfg, &cred, http.MethodPost, "/api/user/pay", map[string]any{"amount_fen": req.AmountFen, "expected_quote_hash": q.RemoteQuoteHash, "payment_method": method}, "")
	if err != nil {
		return s.rechargePrepareFailure(ctx, userID, trade, "AI 充值单创建结果待核对，尚未发起支付")
	}
	var params map[string]string
	if workspaceDecode(env.Data, &params) != nil {
		return s.rechargePrepareFailure(ctx, userID, trade, "AI 充值单返回无效，尚未发起支付")
	}
	q.Params = url.Values{}
	for k, v := range params {
		q.Params.Set(k, v)
	}
	if err = s.verifyWorkspaceOriginalAmount(ctx, cfg, cred, q, q.Params.Get("out_trade_no")); err != nil {
		return s.rechargePrepareFailure(ctx, userID, trade, "AI 充值单金额或归属待核对，尚未发起支付")
	}
	checkout, err := s.createCheckoutForSource(ctx, q.Params, "direct")
	if err != nil {
		return s.rechargePrepareFailure(ctx, userID, trade, "AI 充值单关联待核对，尚未发起支付")
	}
	if checkout.userID != userID || checkout.AmountFen != req.AmountFen || checkout.RemoteUserID != cred.RemoteID {
		return s.rechargePrepareFailure(ctx, userID, trade, "AI 充值单归属不一致，尚未发起支付")
	}
	// Signed parameters are unnecessary after the exact original is pinned.
	q.Params = nil
	q.CheckoutID = checkout.ID
	raw, _ = json.Marshal(q)
	quoteCipher, err = s.seal(raw)
	if err != nil {
		return RechargeOrder{}, err
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(persist, `UPDATE v2_newapi_recharge SET checkout_id=$2,quote_cipher=$3,status='pending',last_error='' WHERE trade_no=$1 AND status='prepare_unknown'`, trade, checkout.ID, quoteCipher)
	if err != nil {
		return RechargeOrder{}, ErrWorkspaceUnknown
	}
	return s.WorkspaceRechargeDetail(persist, userID, trade)
}
func (s *Service) rechargePrepareFailure(ctx context.Context, userID int64, trade, message string) (RechargeOrder, error) {
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(persist, `UPDATE v2_newapi_recharge SET last_error=$2 WHERE trade_no=$1 AND status='prepare_unknown'`, trade, message)
	if err != nil {
		return RechargeOrder{}, ErrWorkspaceUnknown
	}
	return s.WorkspaceRechargeDetail(persist, userID, trade)
}
func (s *Service) WorkspaceRechargeDetail(ctx context.Context, userID int64, trade string) (RechargeOrder, error) {
	if userID <= 0 || !validRechargeTrade(trade) {
		return RechargeOrder{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return RechargeOrder{}, err
	}
	o, err := scanRecharge(s.db.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM v2_newapi_recharge WHERE trade_no=$1 AND user_id=$2`, trade, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return RechargeOrder{}, ErrForbidden
	}
	return o, err
}
func (s *Service) WorkspaceRecharges(ctx context.Context, userID int64, current, pageSize int) (RechargePage, error) {
	page := RechargePage{Items: []RechargeOrder{}, Current: current, PageSize: pageSize}
	if userID <= 0 || current < 1 || current > 100000 || pageSize < 1 || pageSize > 100 {
		return page, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_newapi_recharge WHERE user_id=$1`, userID).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+rechargeColumns+` FROM v2_newapi_recharge WHERE user_id=$1 ORDER BY created_at DESC,trade_no DESC LIMIT $2 OFFSET $3`, userID, pageSize, (current-1)*pageSize)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		o, e := scanRecharge(rows)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, o)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	return page, tx.Commit()
}
