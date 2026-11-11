package newapibridge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInsufficientBalance = errors.New("insufficient V2B wallet balance")
	ErrLimitExceeded       = errors.New("new api transfer amount limit exceeded")
	ErrExpired             = errors.New("new api checkout expired")
)

type Checkout struct {
	ID             string `json:"id"`
	RemoteTradeNo  string `json:"remote_trade_no"`
	RemoteUserID   int64  `json:"remote_user_id"`
	AmountFen      int64  `json:"amount_fen"`
	BalanceFen     int64  `json:"balance_fen"`
	Status         string `json:"status"`
	ExpiresAt      int64  `json:"expires_at"`
	ReturnURL      string `json:"return_url"`
	TransferID     int64  `json:"transfer_id,omitempty"`
	apiURL         string
	userID         int64
	paymentMethod  string
	configRevision int64
	configCipher   string
	fundingSource  string
}

type Transfer struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"user_id"`
	RemoteUserID  int64  `json:"remote_user_id"`
	RemoteTradeNo string `json:"remote_trade_no"`
	AmountFen     int64  `json:"amount_fen"`
	BalanceBefore int64  `json:"balance_before"`
	BalanceAfter  int64  `json:"balance_after"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	LastError     string `json:"last_error"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   int64  `json:"completed_at"`
	LastRetryBy   int64  `json:"last_retry_by"`
}

type TransferQuery struct {
	Current  int    `json:"current"`
	PageSize int    `json:"page_size"`
	UserID   int64  `json:"user_id"`
	Status   string `json:"status"`
	TradeNo  string `json:"trade_no"`
}

type TransferPage struct {
	Items    []Transfer `json:"items"`
	Total    int64      `json:"total"`
	Current  int        `json:"current"`
	PageSize int        `json:"page_size"`
}

const checkoutColumns = `id,api_url,remote_trade_no,user_id,remote_user_id,amount_fen,payment_method,config_revision,config_cipher,expires_at,funding_source`
const transferColumns = `id,user_id,remote_user_id,remote_trade_no,amount_fen,balance_before,balance_after,status,attempts,last_error,created_at,completed_at,last_retry_by`

type walletScanner interface{ Scan(...any) error }

func scanCheckout(row walletScanner) (Checkout, error) {
	var c Checkout
	err := row.Scan(&c.ID, &c.apiURL, &c.RemoteTradeNo, &c.userID, &c.RemoteUserID, &c.AmountFen, &c.paymentMethod, &c.configRevision, &c.configCipher, &c.ExpiresAt, &c.fundingSource)
	c.Status = "awaiting_confirmation"
	c.ReturnURL = strings.TrimRight(c.apiURL, "/") + "/usage-logs"
	return c, err
}

func scanTransfer(row walletScanner) (Transfer, error) {
	var t Transfer
	err := row.Scan(&t.ID, &t.UserID, &t.RemoteUserID, &t.RemoteTradeNo, &t.AmountFen, &t.BalanceBefore, &t.BalanceAfter, &t.Status, &t.Attempts, &t.LastError, &t.CreatedAt, &t.CompletedAt, &t.LastRetryBy)
	return t, err
}

func walletRandomID() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validCheckoutID(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 32 && id == strings.ToLower(id)
}

func requireWalletSettings(c Settings) error {
	if !c.Enabled {
		return ErrDisabled
	}
	if c.APIURL == "" || c.SiteURL == "" || c.MerchantID == "" || c.MerchantKey == "" || c.AdminToken == "" || c.AdminUserID <= 0 || c.MinAmountFen <= 0 || c.MaxAmountFen < c.MinAmountFen || c.DailyLimitFen < c.MinAmountFen {
		return ErrInvalid
	}
	return nil
}

func validRemoteReturnURL(value, base string) bool {
	u, err := url.Parse(value)
	b, baseErr := url.Parse(base)
	return err == nil && baseErr == nil && u.Scheme == b.Scheme && u.Host == b.Host && u.User == nil && u.Fragment == "" && u.Opaque == "" && strings.HasPrefix(u.Path, "/")
}

func (s *Service) CreateCheckout(ctx context.Context, params url.Values) (Checkout, error) {
	return s.createCheckoutForSource(ctx, params, "wallet")
}

func (s *Service) createCheckoutForSource(ctx context.Context, params url.Values, source string) (Checkout, error) {
	if source != "wallet" && source != "direct" {
		return Checkout{}, ErrInvalid
	}
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		return Checkout{}, err
	}
	if err = requireWalletSettings(cfg); err != nil {
		return Checkout{}, err
	}
	if !validEpaySignature(params, cfg.MerchantKey) || params.Get("pid") != cfg.MerchantID || params.Get("notify_url") != strings.TrimRight(cfg.APIURL, "/")+"/api/user/epay/notify" || !validRemoteReturnURL(params.Get("return_url"), cfg.APIURL) {
		return Checkout{}, ErrForbidden
	}
	tradeNo := params.Get("out_trade_no")
	method := params.Get("type")
	if tradeNo == "" || len(tradeNo) > 255 || method == "" || len(method) > 32 {
		return Checkout{}, ErrInvalid
	}
	for _, c := range method {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return Checkout{}, ErrInvalid
		}
	}
	amount, err := parseMoneyFen(params.Get("money"))
	if err != nil {
		return Checkout{}, err
	}
	if amount < cfg.MinAmountFen || amount > cfg.MaxAmountFen || amount > cfg.DailyLimitFen {
		return Checkout{}, ErrLimitExceeded
	}
	remoteCfg := paymentConfig(cfg)
	topup, err := s.readRemoteTopUp(ctx, remoteCfg, tradeNo)
	if err != nil {
		return Checkout{}, err
	}
	remoteAmount, err := remoteMoneyFen(topup.Money)
	if err != nil {
		return Checkout{}, err
	}
	if topup.PaymentProvider != "epay" || remoteAmount != amount || topup.PaymentMethod != method || (topup.Status != "pending" && topup.Status != "success") {
		return Checkout{}, ErrConflict
	}
	remoteAccount, err := s.readRemoteUser(ctx, remoteCfg, topup.UserID)
	if err != nil {
		return Checkout{}, err
	}
	userID, err := localSubjectID(remoteAccount.OIDCID)
	if err != nil || remoteAccount.Status != 1 {
		return Checkout{}, ErrForbidden
	}
	var banned int64
	if err = s.db.QueryRowContext(ctx, `SELECT banned FROM v2_user WHERE id=$1`, userID).Scan(&banned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Checkout{}, ErrForbidden
		}
		return Checkout{}, err
	}
	if banned != 0 {
		return Checkout{}, ErrForbidden
	}
	// Replayed signed checkout requests return the same opaque confirmation ID.
	existing, err := scanCheckout(s.db.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE api_url=$1 AND remote_trade_no=$2`, cfg.APIURL, tradeNo))
	if err == nil {
		if existing.userID != userID || existing.RemoteUserID != topup.UserID || existing.AmountFen != amount || existing.paymentMethod != method || existing.fundingSource != source {
			return Checkout{}, ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Checkout{}, err
	}
	if topup.Status != "pending" {
		return Checkout{}, ErrConflict
	}
	id, err := walletRandomID()
	if err != nil {
		return Checkout{}, err
	}
	raw, err := json.Marshal(remoteCfg)
	if err != nil {
		return Checkout{}, err
	}
	cipher, err := s.seal(raw)
	if err != nil {
		return Checkout{}, err
	}
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO v2_newapi_checkout(id,api_url,remote_trade_no,user_id,remote_user_id,amount_fen,payment_method,config_revision,config_cipher,created_at,expires_at,funding_source) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(api_url,remote_trade_no) DO NOTHING`, id, cfg.APIURL, tradeNo, userID, topup.UserID, amount, method, cfg.Revision, cipher, now, now+1800, source)
	if err != nil {
		return Checkout{}, err
	}
	checkout, err := scanCheckout(s.db.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE api_url=$1 AND remote_trade_no=$2`, cfg.APIURL, tradeNo))
	if err != nil {
		return Checkout{}, err
	}
	if checkout.userID != userID || checkout.RemoteUserID != topup.UserID || checkout.AmountFen != amount || checkout.paymentMethod != method || checkout.fundingSource != source {
		return Checkout{}, ErrConflict
	}
	return checkout, nil
}

func (s *Service) GetCheckout(ctx context.Context, userID int64, id string) (Checkout, error) {
	if userID <= 0 || !validCheckoutID(id) {
		return Checkout{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return Checkout{}, err
	}
	c, err := scanCheckout(s.db.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE id=$1 AND user_id=$2`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Checkout{}, ErrForbidden
	}
	if err != nil {
		return Checkout{}, err
	}
	if c.fundingSource != "wallet" {
		return Checkout{}, ErrForbidden
	}
	var banned int64
	if err = s.db.QueryRowContext(ctx, `SELECT balance,banned FROM v2_user WHERE id=$1`, userID).Scan(&c.BalanceFen, &banned); err != nil {
		return Checkout{}, err
	}
	if banned != 0 {
		return Checkout{}, ErrForbidden
	}
	err = s.db.QueryRowContext(ctx, `SELECT id,status FROM v2_newapi_transfer WHERE checkout_id=$1 AND user_id=$2`, id, userID).Scan(&c.TransferID, &c.Status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Checkout{}, err
	}
	if c.Status == "awaiting_confirmation" && c.ExpiresAt <= time.Now().Unix() {
		c.Status = "expired"
	}
	return c, nil
}

func (s *Service) Pay(ctx context.Context, userID int64, id string) (Transfer, error) {
	if userID <= 0 || !validCheckoutID(id) {
		return Transfer{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return Transfer{}, err
	}
	// Already-paid requests are a status read even if the integration was since
	// disabled. They never create a second transfer or deduct the wallet again.
	existing, err := scanTransfer(s.db.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM v2_newapi_transfer WHERE checkout_id=$1 AND user_id=$2`, id, userID))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, err
	}
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		return Transfer{}, err
	}
	if err = requireWalletSettings(cfg); err != nil {
		return Transfer{}, err
	}
	c, err := scanCheckout(s.db.QueryRowContext(ctx, `SELECT `+checkoutColumns+` FROM v2_newapi_checkout WHERE id=$1 AND user_id=$2`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrForbidden
	}
	if err != nil {
		return Transfer{}, err
	}
	if c.ExpiresAt <= time.Now().Unix() {
		return Transfer{}, ErrExpired
	}
	if c.fundingSource != "wallet" {
		return Transfer{}, ErrForbidden
	}
	if c.apiURL != cfg.APIURL || c.configRevision != cfg.Revision {
		return Transfer{}, ErrConflict
	}
	if c.AmountFen < cfg.MinAmountFen || c.AmountFen > cfg.MaxAmountFen || c.AmountFen > cfg.DailyLimitFen {
		return Transfer{}, ErrLimitExceeded
	}
	topup, err := s.readRemoteTopUp(ctx, paymentConfig(cfg), c.RemoteTradeNo)
	if err != nil {
		return Transfer{}, err
	}
	if err = verifyTopUpMatchesCheckout(topup, c); err != nil {
		return Transfer{}, err
	}
	if topup.Status != "pending" {
		return Transfer{}, ErrConflict
	}
	account, err := s.readRemoteUser(ctx, paymentConfig(cfg), c.RemoteUserID)
	if err != nil {
		return Transfer{}, err
	}
	if account.Status != 1 || account.OIDCID != "forest:"+strconv.FormatInt(userID, 10) {
		return Transfer{}, ErrForbidden
	}
	// HTTP calls are finished before the short local wallet transaction starts.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback()
	// Configuration is locked before the wallet, matching SaveConfig's lock
	// order. A disable/rotation during remote verification cannot sneak a new
	// debit through using a stale checkout.
	var currentRevision int64
	var settingsCipher string
	if err = tx.QueryRowContext(ctx, `SELECT revision,config_cipher FROM v2_newapi_settings WHERE id=1 FOR SHARE`).Scan(&currentRevision, &settingsCipher); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Transfer{}, ErrDisabled
		}
		return Transfer{}, err
	}
	if currentRevision != cfg.Revision {
		return Transfer{}, ErrConflict
	}
	settingsRaw, err := s.open(settingsCipher)
	if err != nil {
		return Transfer{}, err
	}
	var current Settings
	if json.Unmarshal(settingsRaw, &current) != nil {
		return Transfer{}, ErrUnavailable
	}
	if !current.Enabled {
		return Transfer{}, ErrDisabled
	}
	var balance, banned int64
	if err = tx.QueryRowContext(ctx, `SELECT balance,banned FROM v2_user WHERE id=$1 FOR UPDATE`, userID).Scan(&balance, &banned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Transfer{}, ErrForbidden
		}
		return Transfer{}, err
	}
	if banned != 0 {
		return Transfer{}, ErrForbidden
	}
	existing, err = scanTransfer(tx.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM v2_newapi_transfer WHERE checkout_id=$1 AND user_id=$2`, id, userID))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, err
	}
	now := time.Now().Unix()
	if c.ExpiresAt <= now {
		return Transfer{}, ErrExpired
	}
	if balance < c.AmountFen {
		return Transfer{}, ErrInsufficientBalance
	}
	// The user row lock serializes all of this user's transfers, including
	// separate checkout IDs, and concurrent existing wallet consumers.
	var dailyUsed int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_fen),0) FROM v2_newapi_transfer WHERE user_id=$1 AND created_at >= $2`, userID, now-86400).Scan(&dailyUsed); err != nil {
		return Transfer{}, err
	}
	if dailyUsed < 0 || dailyUsed > cfg.DailyLimitFen-c.AmountFen {
		return Transfer{}, ErrLimitExceeded
	}
	result, err := tx.ExecContext(ctx, `UPDATE v2_user SET balance=balance-$2,updated_at=$3 WHERE id=$1 AND banned=0 AND balance >= $2`, userID, c.AmountFen, now)
	if err != nil {
		return Transfer{}, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return Transfer{}, ErrConflict
	}
	transfer, err := scanTransfer(tx.QueryRowContext(ctx, `INSERT INTO v2_newapi_transfer(checkout_id,api_url,remote_trade_no,user_id,remote_user_id,amount_fen,balance_before,balance_after,status,next_attempt_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$9) RETURNING `+transferColumns, id, c.apiURL, c.RemoteTradeNo, userID, c.RemoteUserID, c.AmountFen, balance, balance-c.AmountFen, now))
	if err != nil {
		return Transfer{}, err
	}
	if err = tx.Commit(); err != nil {
		return Transfer{}, err
	}
	return transfer, nil
}

func verifyTopUpMatchesCheckout(topup remoteTopUp, c Checkout) error {
	amount, err := remoteMoneyFen(topup.Money)
	if err != nil {
		return err
	}
	if topup.TradeNo != c.RemoteTradeNo || topup.UserID != c.RemoteUserID || topup.PaymentProvider != "epay" || amount != c.AmountFen || topup.PaymentMethod != c.paymentMethod {
		return ErrConflict
	}
	return nil
}

func (s *Service) ListTransfers(ctx context.Context, q TransferQuery) (TransferPage, error) {
	if q.Current == 0 {
		q.Current = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 20
	}
	if q.Current < 1 || q.Current > 1000000 || q.PageSize < 1 || q.PageSize > 100 || q.UserID < 0 || len(q.TradeNo) > 255 || (q.Status != "" && q.Status != "pending" && q.Status != "succeeded") {
		return TransferPage{}, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return TransferPage{}, err
	}
	where := ` WHERE ($1::bigint=0 OR user_id=$1) AND ($2::text='' OR status=$2) AND ($3::text='' OR remote_trade_no=$3)`
	page := TransferPage{Items: []Transfer{}, Current: q.Current, PageSize: q.PageSize}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_newapi_transfer`+where, q.UserID, q.Status, q.TradeNo).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+transferColumns+` FROM v2_newapi_transfer`+where+` ORDER BY id DESC LIMIT $4 OFFSET $5`, q.UserID, q.Status, q.TradeNo, q.PageSize, (q.Current-1)*q.PageSize)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, t)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if err = tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}

func (s *Service) RetryTransfer(ctx context.Context, id, operatorID int64) error {
	if id <= 0 || operatorID <= 0 {
		return ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE v2_newapi_transfer SET next_attempt_at=$2,last_retry_by=$3 WHERE id=$1 AND status='pending' AND lease_until<=$2`, id, now, operatorID)
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

func walletErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrConflict):
		return "远端订单信息或状态变化，待人工核对；未重复扣款"
	case errors.Is(err, ErrForbidden):
		return "目标账号身份或状态不符，待人工核对；未重复扣款"
	case errors.Is(err, ErrRemoteUnavailable):
		return "远端核验或到账通知暂未确认，系统将按原订单重试；请勿重复充值或退款"
	default:
		return "到账状态待核对，系统将按原订单重试；未重复扣款"
	}
}

func walletDBError(operation string, err error) error {
	return fmt.Errorf("new api wallet %s: %w", operation, err)
}
