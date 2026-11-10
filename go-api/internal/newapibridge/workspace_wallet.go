package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

type WorkspaceQuoteRequest struct {
	AmountFen int64  `json:"amount_fen"`
	RequestID string `json:"request_id"`
}
type workspaceQuote struct {
	AmountFen       int64      `json:"amount_fen"`
	RemoteAmount    int64      `json:"remote_amount"`
	CreditedQuota   int64      `json:"credited_quota"`
	Unit            int64      `json:"quota_per_unit"`
	Currency        string     `json:"currency"`
	DisplayCredit   string     `json:"display_credit"`
	DisplayCurrency string     `json:"display_currency"`
	DisplayRate     string     `json:"display_rate"`
	RemoteQuoteHash string     `json:"remote_quote_hash,omitempty"`
	PaymentMethod   string     `json:"payment_method"`
	Revision        int64      `json:"revision"`
	Expires         int64      `json:"expires_at"`
	Params          url.Values `json:"params,omitempty"`
	CheckoutID      string     `json:"checkout_id,omitempty"`
}

func workspaceQuoteView(key, state string, q workspaceQuote) map[string]any {
	return map[string]any{"quote_id": key, "status": state, "amount_fen": q.AmountFen, "remote_amount": q.RemoteAmount, "credited_quota": q.CreditedQuota, "quota_per_unit": q.Unit, "currency": q.Currency, "expires_at": q.Expires, "display_credit": q.DisplayCredit, "display_currency": q.DisplayCurrency, "display_rate": q.DisplayRate}
}
func (s *Service) workspaceAmount(ctx context.Context, cfg Settings, c workspaceSession, amount int64) (int64, error) {
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodPost, "/api/user/amount", map[string]any{"amount": amount}, "")
	if e != nil {
		return 0, e
	}
	var value string
	if workspaceDecode(env.Data, &value) != nil {
		return 0, ErrRemoteUnavailable
	}
	return parseMoneyFen(value)
}
func (s *Service) QuoteWorkspaceTransfer(ctx context.Context, id int64, auth string, req WorkspaceQuoteRequest) (any, error) {
	if !workspaceRequestID(req.RequestID) || req.AmountFen <= 0 {
		return nil, ErrInvalid
	}
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	if e = requireWalletSettings(cfg); e != nil {
		return nil, e
	}
	if req.AmountFen < cfg.MinAmountFen || req.AmountFen > cfg.MaxAmountFen {
		return nil, ErrLimitExceeded
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return nil, e
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return nil, e
	}
	defer lock.Rollback()
	var existing string
	e = s.db.QueryRowContext(ctx, `SELECT id FROM v2_newapi_workspace_action WHERE user_id=$1 AND api_url=$2 AND kind='transfer_quote' AND request_id=$3`, id, cfg.APIURL, req.RequestID).Scan(&existing)
	if e == nil {
		var old workspaceQuote
		state, e := s.readWorkspaceAction(ctx, id, cfg.APIURL, existing, "transfer_quote", &old)
		if e != nil {
			return nil, e
		}
		if old.AmountFen != req.AmountFen {
			return nil, ErrConflict
		}
		return workspaceQuoteView(existing, state, old), nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	var uncertain bool
	if e = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_newapi_workspace_action WHERE user_id=$1 AND api_url=$2 AND kind='transfer_quote' AND state='sending')`, id, cfg.APIURL).Scan(&uncertain); e != nil {
		return nil, e
	}
	if uncertain {
		return nil, ErrWorkspaceUnknown
	}
	info, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodGet, "/api/user/topup/info", nil, "")
	if e != nil {
		return nil, e
	}
	var top struct {
		CNYAmountFen bool                   `json:"supports_cny_amount_fen"`
		MinAmountFen int64                  `json:"min_amount_fen"`
		Enabled      bool                   `json:"enable_online_topup"`
		Min          json.Number            `json:"min_topup"`
		Discount     map[string]json.Number `json:"discount"`
		Methods      []struct {
			Type string `json:"type"`
		} `json:"pay_methods"`
	}
	if workspaceDecode(info.Data, &top) != nil || !top.Enabled || len(top.Methods) == 0 {
		return nil, ErrUnavailable
	}
	status, _, e := s.workspaceRequest(ctx, cfg, nil, http.MethodGet, "/api/status", nil, "")
	if e != nil {
		return nil, e
	}
	var display struct {
		Unit int64       `json:"quota_per_unit"`
		Type string      `json:"quota_display_type"`
		Rate json.Number `json:"usd_exchange_rate"`
	}
	if workspaceDecode(status.Data, &display) != nil || display.Unit <= 0 {
		return nil, ErrUnavailable
	}
	if top.CNYAmountFen {
		if display.Type != "CNY" || top.MinAmountFen < 100 || req.AmountFen < top.MinAmountFen {
			return nil, ErrWorkspaceAmount
		}
		quote, e := s.workspaceCNYQuote(ctx, cfg, c, req.AmountFen)
		if e != nil {
			return nil, e
		}
		if quote.QuotaPerUnit != display.Unit || quote.DisplayRate != display.Rate.String() {
			return nil, ErrConflict
		}
		method := ""
		for _, m := range top.Methods {
			if m.Type == "wallet" || m.Type == "forest_wallet" {
				method = m.Type
				break
			}
		}
		if method == "" {
			for _, m := range top.Methods {
				if m.Type == "alipay" || m.Type == "wxpay" {
					method = m.Type
					break
				}
			}
		}
		if method == "" {
			return nil, ErrUnavailable
		}
		q := workspaceQuote{AmountFen: req.AmountFen, CreditedQuota: quote.CreditedQuota, Unit: quote.QuotaPerUnit, Currency: "CNY", DisplayCurrency: "CNY", DisplayCredit: formatMoneyFen(req.AmountFen), DisplayRate: quote.DisplayRate, RemoteQuoteHash: quote.QuoteHash, PaymentMethod: method, Revision: cfg.Revision, Expires: time.Now().Add(10 * time.Minute).Unix()}
		key, e := s.saveWorkspaceAction(ctx, id, cfg.APIURL, "transfer_quote", req.RequestID, q, q.Expires)
		if e != nil {
			return nil, e
		}
		return workspaceQuoteView(key, "ready", q), nil
	}
	minimum, e := strconv.ParseInt(top.Min.String(), 10, 64)
	if e != nil || minimum < 1 {
		return nil, ErrUnavailable
	}
	if display.Type == "TOKENS" {
		if minimum > 1000000000000/display.Unit {
			return nil, ErrInvalid
		}
		minimum *= display.Unit
	}
	// Discount amounts are isolated points; quote those separately. Between
	// them normal Epay pricing is monotonic, so server-side binary search avoids
	// amplifying the two-decimal rounding error of a one-unit sample.
	discountAmounts := []int64{}
	for key := range top.Discount {
		if n, e := strconv.ParseInt(key, 10, 64); e == nil && n >= minimum && n <= 1000000000000 {
			discountAmounts = append(discountAmounts, n)
		}
	}
	if len(discountAmounts) > 50 {
		return nil, ErrWorkspaceAmount
	}
	sort.Slice(discountAmounts, func(i, j int) bool { return discountAmounts[i] < discountAmounts[j] })
	amount, e := workspaceFindAmount(ctx, minimum, req.AmountFen, discountAmounts, func(n int64) (int64, error) { return s.workspaceAmount(ctx, cfg, c, n) })
	if e != nil {
		return nil, e
	}
	if amount == 0 {
		return nil, ErrWorkspaceAmount
	}
	credited := amount
	if display.Type == "TOKENS" {
		credited = (amount / display.Unit) * display.Unit
	} else {
		if amount > 9223372036854775807/display.Unit {
			return nil, ErrInvalid
		}
		credited = amount * display.Unit
	}
	if credited <= 0 {
		return nil, ErrWorkspaceAmount
	}
	method := ""
	for _, m := range top.Methods {
		if m.Type == "wallet" || m.Type == "forest_wallet" {
			method = m.Type
			break
		}
	}
	if method == "" {
		for _, m := range top.Methods {
			if m.Type == "alipay" || m.Type == "wxpay" {
				method = m.Type
				break
			}
		}
	}
	if method == "" {
		return nil, ErrUnavailable
	}
	q := workspaceQuote{AmountFen: req.AmountFen, RemoteAmount: amount, CreditedQuota: credited, Unit: display.Unit, Currency: display.Type, PaymentMethod: method, Revision: cfg.Revision, Expires: time.Now().Add(10 * time.Minute).Unix()}
	q.DisplayCurrency = "quota"
	q.DisplayCredit = strconv.FormatInt(credited, 10)
	q.DisplayRate = "1"
	if display.Type == "USD" {
		q.DisplayCurrency = "USD"
		q.DisplayCredit = workspaceDecimal(credited, display.Unit, "1")
	}
	if display.Type == "CNY" {
		if r, e := strconv.ParseFloat(display.Rate.String(), 64); e == nil && r > 0 {
			q.DisplayCurrency = "CNY"
			q.DisplayRate = display.Rate.String()
			q.DisplayCredit = workspaceDecimal(credited, display.Unit, q.DisplayRate)
		}
	}
	key, e := s.saveWorkspaceAction(ctx, id, cfg.APIURL, "transfer_quote", req.RequestID, q, q.Expires)
	if e != nil {
		return nil, e
	}
	return workspaceQuoteView(key, "ready", q), nil
}
func (s *Service) saveWorkspaceQuote(ctx context.Context, key, state string, q workspaceQuote) error {
	raw, e := json.Marshal(q)
	if e != nil {
		return e
	}
	enc, e := s.seal(raw)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE v2_newapi_workspace_action SET payload_cipher=$2,state=$3 WHERE id=$1`, key, enc, state)
	return e
}
func (s *Service) ConfirmWorkspaceTransfer(ctx context.Context, id int64, auth, key string) (Transfer, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return Transfer{}, e
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return Transfer{}, e
	}
	defer lock.Rollback()
	var q workspaceQuote
	state, e := s.readWorkspaceAction(ctx, id, cfg.APIURL, key, "transfer_quote", &q)
	if e != nil {
		return Transfer{}, e
	}
	if state == "sending" {
		return Transfer{}, ErrWorkspaceUnknown
	}
	if state == "ready" && q.Revision != cfg.Revision {
		return Transfer{}, ErrConflict
	}
	if state == "ready" {
		if e = s.verifyWorkspaceQuoteUnits(ctx, cfg, q); e != nil {
			return Transfer{}, e
		}
		if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
			return Transfer{}, e
		}
		if q.RemoteQuoteHash != "" {
			fresh, e := s.workspaceCNYQuote(ctx, cfg, c, q.AmountFen)
			if e != nil {
				return Transfer{}, e
			}
			if !workspaceCNYQuoteMatches(fresh, q) {
				return Transfer{}, ErrConflict
			}
		} else {
			amount, e := s.workspaceAmount(ctx, cfg, c, q.RemoteAmount)
			if e != nil {
				return Transfer{}, e
			}
			if amount != q.AmountFen {
				return Transfer{}, ErrConflict
			}
		}
		var balance int64
		if e = s.db.QueryRowContext(ctx, `SELECT balance FROM v2_user WHERE id=$1`, id).Scan(&balance); e != nil {
			return Transfer{}, e
		}
		if balance < q.AmountFen {
			return Transfer{}, ErrInsufficientBalance
		}
		if e = s.saveWorkspaceQuote(ctx, key, "sending", q); e != nil {
			return Transfer{}, e
		}
		body := map[string]any{"amount": q.RemoteAmount, "payment_method": q.PaymentMethod}
		if q.RemoteQuoteHash != "" {
			delete(body, "amount")
			body["amount_fen"] = q.AmountFen
			body["expected_quote_hash"] = q.RemoteQuoteHash
		}
		env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodPost, "/api/user/pay", body, "")
		if e != nil {
			return Transfer{}, ErrWorkspaceUnknown
		}
		var params map[string]string
		if workspaceDecode(env.Data, &params) != nil {
			return Transfer{}, ErrWorkspaceUnknown
		}
		q.Params = url.Values{}
		for k, v := range params {
			q.Params.Set(k, v)
		}
		got, e := parseMoneyFen(q.Params.Get("money"))
		if e != nil || got != q.AmountFen {
			return Transfer{}, ErrConflict
		}
		if e = s.saveWorkspaceQuote(ctx, key, "created", q); e != nil {
			return Transfer{}, ErrWorkspaceUnknown
		}
		state = "created"
	}
	if state == "created" {
		if e = s.verifyWorkspaceOriginalAmount(ctx, cfg, c, q, q.Params.Get("out_trade_no")); e != nil {
			return Transfer{}, e
		}
		checkout, e := s.CreateCheckout(ctx, q.Params)
		if e != nil {
			return Transfer{}, e
		}
		if checkout.userID != id || checkout.AmountFen != q.AmountFen {
			return Transfer{}, ErrForbidden
		}
		q.CheckoutID = checkout.ID
		if e = s.saveWorkspaceQuote(ctx, key, "checkout", q); e != nil {
			return Transfer{}, e
		}
	}
	if q.CheckoutID == "" {
		return Transfer{}, ErrConflict
	}
	if state != "done" {
		if e = s.verifyWorkspaceOriginalAmount(ctx, cfg, c, q, q.Params.Get("out_trade_no")); e != nil {
			return Transfer{}, e
		}
	}
	result, e := s.Pay(ctx, id, q.CheckoutID)
	if e != nil {
		return result, e
	}
	_ = s.saveWorkspaceQuote(ctx, key, "done", q)
	return result, nil
}

// Review is read-only. An uncertain create is never matched solely by amount:
// the account owner selects the original order explicitly before reconciliation.
func (s *Service) ReviewWorkspaceTransfer(ctx context.Context, id int64, auth, key string) (any, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	var q workspaceQuote
	state, e := s.readWorkspaceAction(ctx, id, cfg.APIURL, key, "transfer_quote", &q)
	if e != nil {
		return nil, e
	}
	view := workspaceQuoteView(key, state, q)
	view["candidates"] = []map[string]any{}
	if q.CheckoutID != "" {
		transfer, e := scanTransfer(s.db.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM v2_newapi_transfer WHERE checkout_id=$1 AND user_id=$2`, q.CheckoutID, id))
		if e == nil {
			view["transfer"] = transfer
		} else if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
	}

	if state != "sending" {
		return view, nil
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return nil, e
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodGet, "/api/user/topup/self?p=1&page_size=100", nil, "")
	if e != nil {
		return nil, e
	}
	var data struct {
		Items []struct {
			remoteTopUp
			Amount     int64 `json:"amount"`
			CreateTime int64 `json:"create_time"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if workspaceDecode(env.Data, &data) != nil {
		return nil, ErrRemoteUnavailable
	}
	candidates := []map[string]any{}
	for _, order := range data.Items {
		money, e := remoteMoneyFen(order.Money)
		if e == nil && money == q.AmountFen && order.UserID == c.RemoteID && order.Status == "pending" && order.PaymentProvider == "epay" && order.PaymentMethod == q.PaymentMethod && workspaceOrderQuotaMatches(order.remoteTopUp, order.Amount, q) {
			candidates = append(candidates, map[string]any{"trade_no": order.TradeNo, "amount_fen": money, "created_at": order.CreateTime})
		}
	}
	view["candidates"] = candidates
	view["candidates_truncated"] = data.Total > 100
	return view, nil
}

type WorkspaceResolveRequest struct {
	QuoteID       string `json:"quote_id"`
	RemoteTradeNo string `json:"remote_trade_no"`
}

func (s *Service) ResolveWorkspaceTransfer(ctx context.Context, id int64, auth string, req WorkspaceResolveRequest) (Transfer, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return Transfer{}, e
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return Transfer{}, e
	}
	defer lock.Rollback()
	var q workspaceQuote
	state, e := s.readWorkspaceAction(ctx, id, cfg.APIURL, req.QuoteID, "transfer_quote", &q)
	if e != nil {
		return Transfer{}, e
	}
	if state != "sending" {
		return Transfer{}, ErrConflict
	}
	if q.Revision != cfg.Revision {
		return Transfer{}, ErrConflict
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return Transfer{}, e
	}
	if e = s.verifyWorkspaceOriginalAmount(ctx, cfg, c, q, req.RemoteTradeNo); e != nil {
		return Transfer{}, e
	}
	top, e := s.readRemoteTopUp(ctx, paymentConfig(cfg), req.RemoteTradeNo)
	if e != nil {
		return Transfer{}, e
	}
	amount, e := remoteMoneyFen(top.Money)
	if e != nil || amount != q.AmountFen || top.UserID != c.RemoteID || top.PaymentMethod != q.PaymentMethod || top.PaymentProvider != "epay" || top.Status != "pending" {
		return Transfer{}, ErrConflict
	}
	// Existing confirmed checkout identity remains authoritative; never reuse a
	// different user's order or a remotely completed payment.
	params := url.Values{"pid": {cfg.MerchantID}, "out_trade_no": {top.TradeNo}, "money": {formatMoneyFen(amount)}, "type": {top.PaymentMethod}, "notify_url": {cfg.APIURL + "/api/user/epay/notify"}, "return_url": {cfg.APIURL + "/usage-logs"}, "name": {"Forest wallet transfer"}, "sign_type": {"MD5"}}
	params.Set("sign", epaySignature(params, cfg.MerchantKey))
	checkout, e := s.CreateCheckout(ctx, params)
	if e != nil {
		return Transfer{}, e
	}
	if checkout.userID != id {
		return Transfer{}, ErrForbidden
	}
	q.Params = params
	q.CheckoutID = checkout.ID
	if e = s.saveWorkspaceQuote(ctx, req.QuoteID, "checkout", q); e != nil {
		return Transfer{}, e
	}
	result, e := s.Pay(ctx, id, checkout.ID)
	if e != nil {
		return result, e
	}
	_ = s.saveWorkspaceQuote(ctx, req.QuoteID, "done", q)
	return result, nil
}

func (s *Service) verifyWorkspaceQuoteUnits(ctx context.Context, cfg Settings, q workspaceQuote) error {
	env, _, e := s.workspaceRequest(ctx, cfg, nil, http.MethodGet, "/api/status", nil, "")
	if e != nil {
		return e
	}
	var status struct {
		Unit int64       `json:"quota_per_unit"`
		Type string      `json:"quota_display_type"`
		Rate json.Number `json:"usd_exchange_rate"`
	}
	if workspaceDecode(env.Data, &status) != nil {
		return ErrRemoteUnavailable
	}
	if status.Unit != q.Unit || status.Type != q.Currency {
		return ErrConflict
	}
	if q.Currency == "CNY" && status.Rate.String() != q.DisplayRate {
		return ErrConflict
	}
	return nil
}
func (s *Service) verifyWorkspaceOriginalAmount(ctx context.Context, cfg Settings, c workspaceSession, q workspaceQuote, trade string) error {
	if e := s.verifyWorkspaceQuoteUnits(ctx, cfg, q); e != nil {
		return e
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodGet, "/api/user/topup/self?keyword="+url.QueryEscape(trade)+"&p=1&page_size=100", nil, "")
	if e != nil {
		return e
	}
	var data struct {
		Items []struct {
			remoteTopUp
			Amount int64 `json:"amount"`
		} `json:"items"`
	}
	if workspaceDecode(env.Data, &data) != nil {
		return ErrRemoteUnavailable
	}
	for _, order := range data.Items {
		if order.TradeNo == trade {
			money, e := remoteMoneyFen(order.Money)
			if e != nil || money != q.AmountFen || order.UserID != c.RemoteID || order.PaymentMethod != q.PaymentMethod || order.PaymentProvider != "epay" || !workspaceOrderQuotaMatches(order.remoteTopUp, order.Amount, q) {
				return ErrConflict
			}
			return nil
		}
	}
	return ErrRemoteUnavailable
}

func workspaceFindAmount(ctx context.Context, minimum, target int64, discounts []int64, quote func(int64) (int64, error)) (int64, error) {
	excluded := map[int64]bool{}
	for _, n := range discounts {
		excluded[n] = true
		value, e := quote(n)
		if e != nil {
			return 0, e
		}
		if value == target {
			return n, nil
		}
	}
	next := func(n int64) int64 {
		for excluded[n] && n <= 1000000000000 {
			n++
		}
		return n
	}
	previous := func(n int64) int64 {
		for excluded[n] && n >= minimum {
			n--
		}
		return n
	}
	low := next(minimum)
	high := low
	for calls := 0; calls < 48; calls++ {
		if e := ctx.Err(); e != nil {
			return 0, e
		}
		value, e := quote(high)
		if e != nil {
			return 0, e
		}
		if value == target {
			return high, nil
		}
		if value > target {
			break
		}
		low = next(high + 1)
		if high > 500000000000 {
			return 0, ErrWorkspaceAmount
		}
		high = next(high * 2)
	}
	for calls := 0; low <= high && calls < 48; calls++ {
		if e := ctx.Err(); e != nil {
			return 0, e
		}
		mid := next(low + (high-low)/2)
		if mid > high {
			high = previous(low + (high-low)/2)
			continue
		}
		value, e := quote(mid)
		if e != nil {
			return 0, e
		}
		if value == target {
			return mid, nil
		}
		if value < target {
			low = next(mid + 1)
		} else {
			high = previous(mid - 1)
		}
	}
	return 0, ErrWorkspaceAmount
}
