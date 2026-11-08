package newapibridge

import (
	"context"
	"crypto/md5" // Epay protocol compatibility, not password hashing.
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrRemoteUnavailable = errors.New("new api remote verification unavailable")

type remotePaymentConfig struct {
	APIURL      string `json:"api_url"`
	MerchantID  string `json:"merchant_id"`
	MerchantKey string `json:"merchant_key"`
	AdminToken  string `json:"admin_token"`
	AdminUserID int64  `json:"admin_user_id"`
}

func paymentConfig(c Settings) remotePaymentConfig {
	return remotePaymentConfig{APIURL: c.APIURL, MerchantID: c.MerchantID, MerchantKey: c.MerchantKey, AdminToken: c.AdminToken, AdminUserID: c.AdminUserID}
}

type remoteTopUp struct {
	ID              int64       `json:"id"`
	UserID          int64       `json:"user_id"`
	Money           json.Number `json:"money"`
	TradeNo         string      `json:"trade_no"`
	PaymentMethod   string      `json:"payment_method"`
	PaymentProvider string      `json:"payment_provider"`
	Status          string      `json:"status"`
}

type remoteUser struct {
	ID     int64  `json:"id"`
	OIDCID string `json:"oidc_id"`
	Status int    `json:"status"`
}

// Epay signatures bind the complete set of non-empty request fields. Reject
// duplicate keys before verifying so PHP-style parameter ambiguity cannot
// change the meaning of a signed amount, destination or trade number.
func epaySignature(values url.Values, key string) string {
	keys := make([]string, 0, len(values))
	for name, value := range values {
		if name != "sign" && name != "sign_type" && len(value) == 1 && value[0] != "" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, name+"="+values.Get(name))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

func validEpaySignature(values url.Values, key string) bool {
	if key == "" || values.Get("sign_type") != "MD5" || len(values) > 24 {
		return false
	}
	for name, value := range values {
		if name == "" || len(value) != 1 || len(name) > 64 || len(value[0]) > 2048 || strings.ContainsAny(name+value[0], "\x00\r\n") {
			return false
		}
	}
	supplied, err := hex.DecodeString(values.Get("sign"))
	if err != nil || len(supplied) != md5.Size {
		return false
	}
	expected, _ := hex.DecodeString(epaySignature(values, key))
	return subtle.ConstantTimeCompare(supplied, expected) == 1
}

// Monetary input is decimal yuan, always converted to integer fen before any
// balance comparison. No exponent, sign, whitespace or fractional fen is valid.
func parseMoneyFen(value string) (int64, error) {
	if value == "" || len(value) > 18 {
		return 0, ErrInvalid
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrInvalid
	}
	for _, part := range parts {
		if part == "" {
			return 0, ErrInvalid
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, ErrInvalid
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > (math.MaxInt64-99)/100 {
		return 0, ErrInvalid
	}
	frac := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, ErrInvalid
		}
		frac, _ = strconv.ParseInt(parts[1], 10, 64)
		if len(parts[1]) == 1 {
			frac *= 10
		}
	}
	amount := whole*100 + frac
	if amount <= 0 {
		return 0, ErrInvalid
	}
	return amount, nil
}

func formatMoneyFen(value int64) string { return fmt.Sprintf("%d.%02d", value/100, value%100) }

// New API currently persists Money as float64 and formats the Epay request
// using FormatFloat(..., 'f', 2, 64). Reproduce that exact boundary conversion;
// never use binary floats for the local wallet arithmetic.
func remoteMoneyFen(value json.Number) (int64, error) {
	n, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 1e12 {
		return 0, ErrRemoteUnavailable
	}
	amount, err := parseMoneyFen(strconv.FormatFloat(n, 'f', 2, 64))
	if err != nil {
		return 0, ErrRemoteUnavailable
	}
	return amount, nil
}

func (s *Service) remoteRequest(ctx context.Context, cfg remotePaymentConfig, method, path string, query url.Values, form url.Values) ([]byte, error) {
	base, err := url.Parse(cfg.APIURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, ErrInvalid
	}
	base.Path, base.RawPath = strings.TrimRight(base.Path, "/")+path, ""
	base.RawQuery = query.Encode()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, base.String(), body)
	if err != nil {
		return nil, ErrInvalid
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
		req.Header.Set("New-Api-User", strconv.FormatInt(cfg.AdminUserID, 10))
	}
	req.Header.Set("Accept", "application/json")
	client := http.Client{Timeout: 10 * time.Second}
	if s.client != nil {
		client = *s.client
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrRemoteUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrRemoteUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, ErrRemoteUnavailable
	}
	return data, nil
}

func (s *Service) readRemoteTopUp(ctx context.Context, cfg remotePaymentConfig, tradeNo string) (remoteTopUp, error) {
	if tradeNo == "" || len(tradeNo) > 255 || strings.ContainsAny(tradeNo, "\x00\r\n") {
		return remoteTopUp{}, ErrInvalid
	}
	// Search is fuzzy on the remote side. Always check exact equality and fail
	// closed if the result cannot be found within the bounded search window.
	for page := 1; page <= 10; page++ {
		data, err := s.remoteRequest(ctx, cfg, http.MethodGet, "/api/user/topup", url.Values{"keyword": {tradeNo}, "p": {strconv.Itoa(page)}, "page_size": {"100"}}, nil)
		if err != nil {
			return remoteTopUp{}, err
		}
		var envelope struct {
			Success bool `json:"success"`
			Data    struct {
				Total int64         `json:"total"`
				Items []remoteTopUp `json:"items"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &envelope) != nil || !envelope.Success || envelope.Data.Total < 0 || len(envelope.Data.Items) > 100 {
			return remoteTopUp{}, ErrRemoteUnavailable
		}
		for _, topup := range envelope.Data.Items {
			if topup.TradeNo == tradeNo && topup.ID > 0 && topup.UserID > 0 {
				return topup, nil
			}
		}
		if len(envelope.Data.Items) < 100 || int64(page*100) >= envelope.Data.Total {
			break
		}
	}
	return remoteTopUp{}, ErrRemoteUnavailable
}

func (s *Service) readRemoteUser(ctx context.Context, cfg remotePaymentConfig, id int64) (remoteUser, error) {
	if id <= 0 {
		return remoteUser{}, ErrInvalid
	}
	data, err := s.remoteRequest(ctx, cfg, http.MethodGet, "/api/user/"+strconv.FormatInt(id, 10), nil, nil)
	if err != nil {
		return remoteUser{}, err
	}
	var envelope struct {
		Success bool       `json:"success"`
		Data    remoteUser `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || !envelope.Success || envelope.Data.ID != id {
		return remoteUser{}, ErrRemoteUnavailable
	}
	return envelope.Data, nil
}

func localSubjectID(subject string) (int64, error) {
	if !strings.HasPrefix(subject, "forest:") {
		return 0, ErrForbidden
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(subject, "forest:"), 10, 64)
	if err != nil || id <= 0 || subject != "forest:"+strconv.FormatInt(id, 10) {
		return 0, ErrForbidden
	}
	return id, nil
}

type CheckResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (s *Service) CheckConnection(ctx context.Context) (CheckResult, error) {
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	if cfg.APIURL == "" || cfg.AdminToken == "" || cfg.AdminUserID <= 0 {
		return CheckResult{}, ErrInvalid
	}
	data, err := s.remoteRequest(ctx, paymentConfig(cfg), http.MethodGet, "/api/user/topup", url.Values{"p": {"1"}, "page_size": {"1"}}, nil)
	if err != nil {
		return CheckResult{}, err
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Total int64             `json:"total"`
			Items []json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || !envelope.Success {
		return CheckResult{}, ErrRemoteUnavailable
	}
	// Reading the caller's own account proves connectivity but not access to
	// another user's identity. Payment-time checks always re-verify both APIs.
	selfData, err := s.remoteRequest(ctx, paymentConfig(cfg), http.MethodGet, "/api/user/self", nil, nil)
	if err != nil {
		return CheckResult{}, err
	}
	var self struct {
		Success bool `json:"success"`
		Data    struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(selfData, &self) != nil || !self.Success || self.Data.ID != cfg.AdminUserID {
		return CheckResult{}, ErrRemoteUnavailable
	}
	return CheckResult{OK: true, Message: "已连接 New API 并验证充值单读取权限；具体用户身份与订单在支付前再次核对"}, nil
}
