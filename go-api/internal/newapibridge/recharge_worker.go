package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type rechargeDelivery struct {
	Order    RechargeOrder
	Checkout Checkout
	Quote    workspaceQuote
	Config   remotePaymentConfig
}

func (s *Service) readRechargeDelivery(ctx context.Context, trade string) (rechargeDelivery, error) {
	var d rechargeDelivery
	var quoteCipher, configCipher, checkoutID string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(checkout_id,''),quote_cipher,config_cipher FROM v2_newapi_recharge WHERE trade_no=$1`, trade).Scan(&checkoutID, &quoteCipher, &configCipher)
	if err != nil {
		return d, err
	}
	if !validCheckoutID(checkoutID) {
		return d, ErrConflict
	}
	d.Order, err = scanRecharge(s.db.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM v2_newapi_recharge WHERE trade_no=$1`, trade))
	if err != nil {
		return d, err
	}
	d.Checkout, err = scanCheckout(s.db.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE id=$1`, checkoutID))
	if err != nil {
		return d, err
	}
	if d.Checkout.fundingSource != "direct" || d.Checkout.AmountFen != d.Order.AmountFen {
		return d, ErrConflict
	}
	raw, err := s.open(quoteCipher)
	if err != nil || json.Unmarshal(raw, &d.Quote) != nil {
		return d, ErrConflict
	}
	raw, err = s.open(configCipher)
	if err != nil || json.Unmarshal(raw, &d.Config) != nil {
		return d, ErrConflict
	}
	if d.Config.APIURL != d.Checkout.apiURL || d.Config.MerchantID == "" || d.Config.MerchantKey == "" || d.Config.AdminToken == "" || d.Config.AdminUserID <= 0 || d.Quote.CheckoutID != checkoutID || d.Quote.AmountFen != d.Order.AmountFen || d.Quote.RemoteQuoteHash == "" {
		return d, ErrConflict
	}
	// Only credentials for the original destination may rotate. Disabling new
	// orders does not cancel an already paid customer's outstanding credit.
	if cfg, e := s.loadSettings(ctx); e == nil && cfg.APIURL == d.Config.APIURL && cfg.MerchantID == d.Config.MerchantID && cfg.MerchantKey != "" && cfg.AdminToken != "" && cfg.AdminUserID > 0 {
		d.Config.MerchantKey = cfg.MerchantKey
		d.Config.AdminToken = cfg.AdminToken
		d.Config.AdminUserID = cfg.AdminUserID
	}
	return d, nil
}
func (s *Service) verifyRechargeRemote(ctx context.Context, d rechargeDelivery) (remoteTopUp, error) {
	top, err := s.readRemoteTopUp(ctx, d.Config, d.Checkout.RemoteTradeNo)
	if err != nil {
		return top, err
	}
	if err = verifyTopUpMatchesCheckout(top, d.Checkout); err != nil {
		return top, err
	}
	if !workspaceOrderQuotaMatches(top, 0, d.Quote) {
		return top, ErrConflict
	}
	user, err := s.readRemoteUser(ctx, d.Config, d.Checkout.RemoteUserID)
	if err != nil {
		return top, err
	}
	if user.Status != 1 || user.OIDCID != "forest:"+strconv.FormatInt(d.Checkout.userID, 10) {
		return top, ErrForbidden
	}
	return top, nil
}
func (s *Service) verifyRechargeBeforePayment(ctx context.Context, trade string, cfg Settings) error {
	d, err := s.readRechargeDelivery(ctx, trade)
	if err != nil {
		return err
	}
	if cfg.APIURL != d.Config.APIURL || cfg.MerchantID != d.Config.MerchantID {
		return ErrConflict
	}
	if err = s.verifyWorkspaceQuoteUnits(ctx, cfg, d.Quote); err != nil {
		return err
	}
	top, err := s.verifyRechargeRemote(ctx, d)
	if err != nil {
		return err
	}
	if top.Status != "pending" {
		return ErrConflict
	}
	return nil
}

type rechargeLease struct {
	TradeNo  string
	Token    string
	Attempts int
}

func (s *Service) claimRecharge(ctx context.Context) (rechargeLease, error) {
	var lease rechargeLease
	token, err := walletRandomID()
	if err != nil {
		return lease, err
	}
	now := time.Now().Unix()
	err = s.db.QueryRowContext(ctx, `WITH next_recharge AS (
 SELECT trade_no FROM v2_newapi_recharge WHERE status='paid_pending_credit' AND next_attempt_at<=$1 AND lease_until<=$1 ORDER BY next_attempt_at,trade_no FOR UPDATE SKIP LOCKED LIMIT 1
 ) UPDATE v2_newapi_recharge r SET lease_token=$2,lease_until=$3,attempts=r.attempts+1 FROM next_recharge n WHERE r.trade_no=n.trade_no RETURNING r.trade_no,r.lease_token,r.attempts`, now, token, now+180).Scan(&lease.TradeNo, &lease.Token, &lease.Attempts)
	return lease, err
}
func (s *Service) ProcessRecharges(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lease, err := s.claimRecharge(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		delivery, cancel := context.WithTimeout(ctx, 120*time.Second)
		err = s.deliverRecharge(delivery, lease)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			delay := int64(10)
			for n := 1; n < lease.Attempts && delay < 3600; n++ {
				delay *= 2
			}
			if delay > 3600 {
				delay = 3600
			}
			_, saveErr := s.db.ExecContext(ctx, `UPDATE v2_newapi_recharge SET lease_until=0,lease_token='',last_error=$3,next_attempt_at=$4 WHERE trade_no=$1 AND lease_token=$2 AND status='paid_pending_credit'`, lease.TradeNo, lease.Token, walletErrorMessage(err), time.Now().Unix()+delay)
			if saveErr != nil {
				return saveErr
			}
		}
	}
	return nil
}
func (s *Service) deliverRecharge(ctx context.Context, lease rechargeLease) error {
	d, err := s.readRechargeDelivery(ctx, lease.TradeNo)
	if err != nil {
		return err
	}
	if d.Order.Status != "paid_pending_credit" {
		return ErrConflict
	}
	top, err := s.verifyRechargeRemote(ctx, d)
	if err != nil {
		return err
	}
	if top.Status != "success" {
		if top.Status != "pending" {
			return ErrConflict
		}
		params := url.Values{"pid": {d.Config.MerchantID}, "trade_no": {lease.TradeNo}, "out_trade_no": {d.Checkout.RemoteTradeNo}, "type": {d.Checkout.paymentMethod}, "name": {"Forest direct AI recharge"}, "money": {formatMoneyFen(d.Order.AmountFen)}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
		params.Set("sign", epaySignature(params, d.Config.MerchantKey))
		_, _ = s.remoteRequest(ctx, d.Config, http.MethodPost, "/api/user/epay/notify", nil, params)
		top, err = s.verifyRechargeRemote(ctx, d)
		if err != nil {
			return err
		}
		if top.Status != "success" {
			return ErrRemoteUnavailable
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_newapi_recharge SET status='succeeded',completed_at=$3,last_error='',lease_until=0,lease_token='' WHERE trade_no=$1 AND lease_token=$2 AND status='paid_pending_credit'`, lease.TradeNo, lease.Token, time.Now().Unix())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
