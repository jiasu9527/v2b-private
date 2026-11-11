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

type walletLease struct {
	ID         int64
	CheckoutID string
	Token      string
	Attempts   int
}

// Run delivers already-debited wallet orders even after new checkouts have
// been disabled. Each checkout carries encrypted destination credentials;
// changing settings never redirects an existing transfer to another account.
func (s *Service) Run(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = s.ProcessTransfers(ctx, 5)
			_ = s.ProcessRecharges(ctx, 5)
			if time.Since(lastCleanup) >= time.Hour {
				if s.Cleanup(ctx) == nil {
					lastCleanup = time.Now()
				}
			}
			timer.Reset(10 * time.Second)
		}
	}
}

func (s *Service) ProcessTransfers(ctx context.Context, limit int) error {
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
		lease, err := s.claimWalletTransfer(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return walletDBError("claim", err)
		}
		deliveryCtx, cancelDelivery := context.WithTimeout(ctx, 120*time.Second)
		err = s.deliverWalletTransfer(deliveryCtx, lease)
		cancelDelivery()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Only sanitized reason categories are persisted, never remote
			// response bodies, connection URLs, signed parameters or tokens.
			delay := int64(10)
			for n := 1; n < lease.Attempts && delay < 3600; n++ {
				delay *= 2
			}
			if delay > 3600 {
				delay = 3600
			}
			_, saveErr := s.db.ExecContext(ctx, `UPDATE v2_newapi_transfer SET lease_until=0,lease_token='',last_error=$3,next_attempt_at=$4 WHERE id=$1 AND lease_token=$2 AND status='pending'`, lease.ID, lease.Token, walletErrorMessage(err), time.Now().Unix()+delay)
			if saveErr != nil {
				return walletDBError("save pending result", saveErr)
			}
		}
	}
	return nil
}

func (s *Service) claimWalletTransfer(ctx context.Context) (walletLease, error) {
	token, err := walletRandomID()
	if err != nil {
		return walletLease{}, err
	}
	now := time.Now().Unix()
	var lease walletLease
	err = s.db.QueryRowContext(ctx, `WITH next_transfer AS (
 SELECT id FROM v2_newapi_transfer
 WHERE status='pending' AND next_attempt_at <= $1 AND lease_until <= $1
 ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE v2_newapi_transfer t SET lease_token=$2,lease_until=$3,attempts=t.attempts+1
FROM next_transfer n WHERE t.id=n.id
RETURNING t.id,t.checkout_id,t.lease_token,t.attempts`, now, token, now+180).Scan(&lease.ID, &lease.CheckoutID, &lease.Token, &lease.Attempts)
	return lease, err
}

func (s *Service) deliverWalletTransfer(ctx context.Context, lease walletLease) error {
	c, err := scanCheckout(s.db.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE id=$1`, lease.CheckoutID))
	if err != nil {
		return err
	}
	if c.fundingSource != "wallet" {
		return ErrConflict
	}
	raw, err := s.open(c.configCipher)
	if err != nil {
		return ErrConflict
	}
	var cfg remotePaymentConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIURL != c.apiURL || cfg.MerchantID == "" || cfg.MerchantKey == "" || cfg.AdminToken == "" || cfg.AdminUserID <= 0 {
		return ErrConflict
	}
	// Credentials may need rotation while money is already pending. Only
	// refresh credentials for the exact same origin and merchant; never change
	// the saved destination, beneficiary, order, or amount. New-checkout enable
	// state does not cancel an already committed debit.
	if current, loadErr := s.loadSettings(ctx); loadErr == nil && current.APIURL == cfg.APIURL && current.MerchantID == cfg.MerchantID && current.MerchantKey != "" && current.AdminToken != "" && current.AdminUserID > 0 {
		cfg.MerchantKey, cfg.AdminToken, cfg.AdminUserID = current.MerchantKey, current.AdminToken, current.AdminUserID
	}
	topup, err := s.readRemoteTopUp(ctx, cfg, c.RemoteTradeNo)
	if err != nil {
		return err
	}
	if err = verifyTopUpMatchesCheckout(topup, c); err != nil {
		return err
	}
	if topup.Status == "success" {
		return s.completeWalletTransfer(ctx, lease)
	}
	if topup.Status != "pending" {
		return ErrConflict
	}
	user, err := s.readRemoteUser(ctx, cfg, c.RemoteUserID)
	if err != nil {
		return err
	}
	if user.Status != 1 || user.OIDCID != "forest:"+strconv.FormatInt(c.userID, 10) {
		return ErrForbidden
	}
	params := url.Values{
		"pid":          {cfg.MerchantID},
		"trade_no":     {"FOREST" + strconv.FormatInt(lease.ID, 10)},
		"out_trade_no": {c.RemoteTradeNo},
		"type":         {c.paymentMethod},
		"name":         {"V2B wallet transfer"},
		"money":        {formatMoneyFen(c.AmountFen)},
		"trade_status": {"TRADE_SUCCESS"},
		"sign_type":    {"MD5"},
	}
	params.Set("sign", epaySignature(params, cfg.MerchantKey))
	// A missing response is not a failed credit. Read the authoritative order
	// after any callback result, including network failure or a non-200 status.
	_, _ = s.remoteRequest(ctx, cfg, http.MethodPost, "/api/user/epay/notify", nil, params)
	topup, err = s.readRemoteTopUp(ctx, cfg, c.RemoteTradeNo)
	if err != nil {
		return err
	}
	if err = verifyTopUpMatchesCheckout(topup, c); err != nil {
		return err
	}
	if topup.Status != "success" {
		return ErrRemoteUnavailable
	}
	return s.completeWalletTransfer(ctx, lease)
}

func (s *Service) completeWalletTransfer(ctx context.Context, lease walletLease) error {
	result, err := s.db.ExecContext(ctx, `UPDATE v2_newapi_transfer SET status='succeeded',completed_at=$3,last_error='',lease_until=0,lease_token='' WHERE id=$1 AND lease_token=$2 AND status='pending'`, lease.ID, lease.Token, time.Now().Unix())
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}
