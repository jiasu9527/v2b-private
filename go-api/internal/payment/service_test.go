package payment

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	usersvc "forest/go-api/internal/user"

	"github.com/DATA-DOG/go-sqlmock"
)

type recordingPaymentOrderManager struct {
	tradeNo      string
	confirmation usersvc.OrderPaymentConfirmation
}

const checkoutOrderLockPattern = `SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result,\s*checkout_claim,\s*checkout_fingerprint,\s*COALESCE\(checkout_claim IS NOT NULL AND checkout_claim_expires_at > EXTRACT\(EPOCH FROM NOW\(\)\)::BIGINT, FALSE\) AS checkout_claim_active,\s*status\s+FROM v2_order\s+WHERE trade_no = \$1 AND user_id = \$2\s+FOR UPDATE`

var checkoutOrderColumns = []string{
	"id", "user_id", "trade_no", "payment_id", "total_amount", "handling_amount", "checkout_result", "checkout_claim", "checkout_fingerprint", "checkout_claim_active", "status",
}

func checkoutOrderRow(tradeNo string, paymentID any, totalAmount int64, handlingAmount, checkoutResult, claim any, claimActive bool, status int64) *sqlmock.Rows {
	return sqlmock.NewRows(checkoutOrderColumns).AddRow(
		int64(9), int64(5), tradeNo, paymentID, totalAmount, handlingAmount, checkoutResult, claim, nil, claimActive, status,
	)
}

func (m *recordingPaymentOrderManager) MarkOrderPaid(_ context.Context, tradeNo string, confirmation usersvc.OrderPaymentConfirmation) error {
	m.tradeNo = tradeNo
	m.confirmation = confirmation
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testAlipayKeys(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	privateDER := x509.MarshalPKCS1PrivateKey(privateKey)
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return string(privatePEM), string(publicPEM)
}

func testRawSortedQuery(values map[string]string, exclude ...string) string {
	skip := map[string]struct{}{}
	for _, key := range exclude {
		skip[key] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if _, ok := skip[key]; ok {
			continue
		}
		if strings.TrimSpace(values[key]) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, "&")
}

func testAlipaySignature(t *testing.T, privatePEM string, values map[string]string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(privatePEM))
	if block == nil {
		t.Fatal("decode private pem")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	digest := sha256.Sum256([]byte(testRawSortedQuery(values, "sign", "sign_type")))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign alipay payload: %v", err)
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func testWechatSign(values map[string]string, apiKey string) string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if strings.TrimSpace(value) == "" || key == "sign" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	parts = append(parts, "key="+apiKey)
	return strings.ToUpper(md5Hex(strings.Join(parts, "&")))
}

func TestBuildGatewayCheckoutEPayProIncludesConfiguredType(t *testing.T) {
	result, err := buildGatewayCheckout(
		context.Background(),
		http.DefaultClient,
		"epaypro",
		map[string]string{
			"url":  "https://pay.example.com/",
			"pid":  "10001",
			"key":  "secret",
			"type": "alipay",
		},
		gatewayOrder{
			TradeNo:   "T401",
			Total:     1234,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T401",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 1 {
		t.Fatalf("expected type 1, got %d", result.Type)
	}

	parsed, err := url.Parse(fmt.Sprint(result.Data))
	if err != nil {
		t.Fatalf("parse redirect url: %v", err)
	}
	if parsed.Path != "/submit.php" {
		t.Fatalf("unexpected redirect path: %s", parsed.Path)
	}
	if parsed.Query().Get("type") != "alipay" {
		t.Fatalf("expected configured type, got %q", parsed.Query().Get("type"))
	}
	expectedSign := md5.Sum([]byte("money=12.34&name=T401&notify_url=https://api.example.com/notify&out_trade_no=T401&pid=10001&return_url=https://app.example.com/#/order/T401&type=alipaysecret"))
	if parsed.Query().Get("sign") != hex.EncodeToString(expectedSign[:]) {
		t.Fatalf("unexpected sign: %s", parsed.Query().Get("sign"))
	}
}

func TestGatewayFormEPayProHasTypeField(t *testing.T) {
	form, ok := GatewayForm("epaypro")
	if !ok {
		t.Fatal("expected epaypro form to exist")
	}
	if form["type"].Label != "TYPE" {
		t.Fatalf("expected TYPE field, got %#v", form["type"])
	}
}

func TestVerifyGatewayNotifyEPayPro(t *testing.T) {
	params := map[string]string{
		"out_trade_no": "T405",
		"trade_no":     "P405",
		"type":         "wxpay",
		"money":        "12.34",
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = md5Hex(decodedQuery(params) + "secret")
	params["sign_type"] = "MD5"

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"epaypro",
		map[string]string{"key": "secret", "pid": "10001", "type": "wxpay"},
		NotifyRequest{Params: params},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T405" || result.CallbackNo != "P405" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected notify result: %#v", result)
	}
}

func TestBuildGatewayCheckoutEPay(t *testing.T) {
	result, err := buildGatewayCheckout(
		context.Background(),
		http.DefaultClient,
		"EPay",
		map[string]string{
			"url": "https://pay.example.com",
			"pid": "10001",
			"key": "secret",
		},
		gatewayOrder{
			TradeNo:   "T400",
			Total:     1234,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T400",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 1 {
		t.Fatalf("expected type 1, got %d", result.Type)
	}

	parsed, err := url.Parse(fmt.Sprint(result.Data))
	if err != nil {
		t.Fatalf("parse redirect url: %v", err)
	}
	if parsed.Host != "pay.example.com" {
		t.Fatalf("unexpected redirect host: %s", parsed.Host)
	}
	if parsed.Query().Get("out_trade_no") != "T400" {
		t.Fatalf("unexpected out_trade_no: %s", parsed.Query().Get("out_trade_no"))
	}
	expectedSign := md5.Sum([]byte("money=12.34&name=T400&notify_url=https://api.example.com/notify&out_trade_no=T400&pid=10001&return_url=https://app.example.com/#/order/T400secret"))
	if parsed.Query().Get("sign") != hex.EncodeToString(expectedSign[:]) {
		t.Fatalf("unexpected sign: %s", parsed.Query().Get("sign"))
	}
}

func TestBuildGatewayCheckoutEpusdtPay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/order/create-transaction" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["order_id"] != "T401" {
			t.Fatalf("unexpected order_id: %#v", payload["order_id"])
		}
		_, _ = w.Write([]byte(`{"status_code":200,"data":{"payment_url":"https://pay.example.com/epusdt/T401"}}`))
	}))
	defer server.Close()

	result, err := buildGatewayCheckout(
		context.Background(),
		server.Client(),
		"EpusdtPay",
		map[string]string{
			"epusdt_pay_url":      server.URL,
			"epusdt_pay_apitoken": "secret",
		},
		gatewayOrder{
			TradeNo:   "T401",
			Total:     2500,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T401",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 1 || fmt.Sprint(result.Data) != "https://pay.example.com/epusdt/T401" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestNotifyURLTrimsGatewayAndUUID(t *testing.T) {
	service := &DBService{cfg: config.Config{AppURL: "https://forest.example.com"}}

	got := service.notifyURL(paymentRecord{
		Payment: " EPay ",
		UUID:    " uuid123 ",
	})

	want := "https://forest.example.com/api/v1/guest/payment/notify/EPay/uuid123"
	if got != want {
		t.Fatalf("expected trimmed notify url %q, got %q", want, got)
	}
}

func TestBuildGatewayCheckoutCoinPayments(t *testing.T) {
	result, err := buildGatewayCheckout(
		context.Background(),
		http.DefaultClient,
		"CoinPayments",
		map[string]string{
			"coinpayments_merchant_id": "merchant-1",
			"coinpayments_currency":    "USDT.TRC20",
		},
		gatewayOrder{
			TradeNo:   "T405",
			Total:     1999,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com:8443/#/order/T405",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 1 {
		t.Fatalf("expected type 1, got %d", result.Type)
	}
	parsed, err := url.Parse(fmt.Sprint(result.Data))
	if err != nil {
		t.Fatalf("parse redirect url: %v", err)
	}
	if parsed.Host != "www.coinpayments.net" {
		t.Fatalf("unexpected redirect host: %s", parsed.Host)
	}
	if parsed.Query().Get("merchant") != "merchant-1" || parsed.Query().Get("item_number") != "T405" {
		t.Fatalf("unexpected coinpayments params: %s", parsed.RawQuery)
	}
	if parsed.Query().Get("success_url") != "https://app.example.com:8443" {
		t.Fatalf("unexpected success_url: %s", parsed.Query().Get("success_url"))
	}
}

func TestBuildGatewayCheckoutCoinbase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CC-Api-Key") != "cb-api-key" || r.Header.Get("X-CC-Version") != "2018-03-22" {
			t.Fatalf("unexpected coinbase headers: %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		meta := payload["metadata"].(map[string]any)
		if meta["outTradeNo"] != "T406" {
			t.Fatalf("unexpected metadata: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"data":{"hosted_url":"https://coinbase.example.com/pay/T406"}}`))
	}))
	defer server.Close()

	result, err := buildGatewayCheckout(
		context.Background(),
		server.Client(),
		"Coinbase",
		map[string]string{
			"coinbase_url":     server.URL,
			"coinbase_api_key": "cb-api-key",
		},
		gatewayOrder{
			TradeNo:   "T406",
			Total:     2345,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T406",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 1 || fmt.Sprint(result.Data) != "https://coinbase.example.com/pay/T406" {
		t.Fatalf("unexpected coinbase result: %#v", result)
	}
}

func TestDBServiceReturnURLUsesCurrentAccessDomainBeforeAppURL(t *testing.T) {
	service := NewDBService(config.Config{AppURL: "https://site.example.com"}, nil, nil)

	got := service.returnURL("https://mirror.example.com/path?q=1", "T900")
	if got != "https://mirror.example.com/#/order/T900" {
		t.Fatalf("unexpected return url: %s", got)
	}
}

func TestDBServiceReturnURLFallsBackToAppURLWhenCurrentAccessDomainMissing(t *testing.T) {
	service := NewDBService(config.Config{AppURL: "https://site.example.com"}, nil, nil)

	got := service.returnURL("", "T901")
	if got != "https://site.example.com/#/order/T901" {
		t.Fatalf("unexpected return url: %s", got)
	}
}

func TestDBServiceNotifyURLTrimsPaddedGatewayUUID(t *testing.T) {
	service := NewDBService(config.Config{}, nil, nil)

	got := service.notifyURL(paymentRecord{
		Payment:      "EPay",
		UUID:         "A63Mqhyi                        ",
		NotifyDomain: sql.NullString{String: "https://forest788.com", Valid: true},
	})
	if got != "https://forest788.com/api/v1/guest/payment/notify/EPay/A63Mqhyi" {
		t.Fatalf("unexpected notify url: %q", got)
	}
}

func TestBuildGatewayCheckoutBTCPay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stores/store-1/invoices" {
			t.Fatalf("unexpected btcpay path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token btc-key" {
			t.Fatalf("unexpected btcpay auth header: %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		meta := payload["metadata"].(map[string]any)
		if meta["orderId"] != "T407" {
			t.Fatalf("unexpected metadata: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"checkoutLink":"https://btcpay.example.com/i/T407"}`))
	}))
	defer server.Close()

	result, err := buildGatewayCheckout(
		context.Background(),
		server.Client(),
		"BTCPay",
		map[string]string{
			"btcpay_url":     server.URL + "/",
			"btcpay_storeId": "store-1",
			"btcpay_api_key": "btc-key",
		},
		gatewayOrder{
			TradeNo:   "T407",
			Total:     3456,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T407",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 1 || fmt.Sprint(result.Data) != "https://btcpay.example.com/i/T407" {
		t.Fatalf("unexpected btcpay result: %#v", result)
	}
}

func TestBuildGatewayCheckoutStripeAlipayAndWepay(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "exchangerate-api.com"):
			return jsonResponse(http.StatusOK, `{"rates":{"USD":0.14}}`), nil
		case r.URL.Host == "api.stripe.com" && r.URL.Path == "/v1/sources":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse stripe form: %v", err)
			}
			if !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "forest:source:T40") {
				t.Fatalf("missing stable Stripe idempotency key: %q", r.Header.Get("Idempotency-Key"))
			}
			if r.Form.Get("metadata[forest_order_amount]") == "" || r.Form.Get("metadata[forest_gateway_amount]") == "" || r.Form.Get("metadata[forest_gateway_currency]") != "usd" {
				t.Fatalf("missing Stripe payment confirmation metadata: %#v", r.Form)
			}
			switch r.Form.Get("type") {
			case "alipay":
				return jsonResponse(http.StatusOK, `{"redirect":{"url":"https://stripe.example.com/alipay/T408"}}`), nil
			case "wechat":
				return jsonResponse(http.StatusOK, `{"wechat":{"qr_code_url":"weixin://wxpay/T409"}}`), nil
			default:
				t.Fatalf("unexpected stripe source type: %s", r.Form.Get("type"))
			}
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		return nil, nil
	})}

	alipayResult, err := buildGatewayCheckout(
		context.Background(),
		client,
		"StripeAlipay",
		map[string]string{
			"currency":       "USD",
			"stripe_sk_live": "sk_live_test",
		},
		gatewayOrder{
			UserID:    12,
			TradeNo:   "T408",
			Total:     4567,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T408",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if alipayResult.Type != 1 || fmt.Sprint(alipayResult.Data) != "https://stripe.example.com/alipay/T408" {
		t.Fatalf("unexpected stripe alipay result: %#v", alipayResult)
	}

	wechatResult, err := buildGatewayCheckout(
		context.Background(),
		client,
		"StripeWepay",
		map[string]string{
			"currency":       "USD",
			"stripe_sk_live": "sk_live_test",
		},
		gatewayOrder{
			UserID:    13,
			TradeNo:   "T409",
			Total:     5678,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T409",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if wechatResult.Type != 0 || fmt.Sprint(wechatResult.Data) != "weixin://wxpay/T409" {
		t.Fatalf("unexpected stripe wepay result: %#v", wechatResult)
	}
}

func TestBuildGatewayCheckoutAlipayF2F(t *testing.T) {
	privatePEM, publicPEM := testAlipayKeys(t)
	_ = publicPEM
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "openapi.alipay.com" || r.URL.Path != "/gateway.do" {
			t.Fatalf("unexpected alipay request: %s", r.URL.String())
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse alipay form: %v", err)
		}
		if r.Form.Get("app_id") != "alipay-app" || r.Form.Get("method") != "alipay.trade.precreate" {
			t.Fatalf("unexpected alipay form: %#v", r.Form)
		}
		if strings.TrimSpace(r.Form.Get("sign")) == "" {
			t.Fatalf("expected alipay sign")
		}
		return jsonResponse(http.StatusOK, `{"alipay_trade_precreate_response":{"code":"10000","qr_code":"https://alipay.example.com/qr/T414"}}`), nil
	})}

	result, err := buildGatewayCheckout(
		context.Background(),
		client,
		"AlipayF2F",
		map[string]string{
			"app_id":      "alipay-app",
			"private_key": privatePEM,
			"public_key":  publicPEM,
		},
		gatewayOrder{
			TradeNo:   "T414",
			Total:     6789,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T414",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 0 || fmt.Sprint(result.Data) != "https://alipay.example.com/qr/T414" {
		t.Fatalf("unexpected alipay f2f result: %#v", result)
	}
}

func TestBuildGatewayCheckoutWechatPayNative(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.mch.weixin.qq.com" || r.URL.Path != "/pay/unifiedorder" {
			t.Fatalf("unexpected wechat request: %s", r.URL.String())
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read wechat request body: %v", err)
		}
		var request struct {
			OutTradeNo string `xml:"out_trade_no"`
			TradeType  string `xml:"trade_type"`
		}
		if err := xml.Unmarshal(raw, &request); err != nil {
			t.Fatalf("decode wechat request xml: %v", err)
		}
		if request.OutTradeNo != "T415" || request.TradeType != "NATIVE" {
			t.Fatalf("unexpected wechat body: %s", string(raw))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(`<xml><return_code>SUCCESS</return_code><result_code>SUCCESS</result_code><code_url>weixin://wxpay/T415</code_url></xml>`)),
		}, nil
	})}

	result, err := buildGatewayCheckout(
		context.Background(),
		client,
		"WechatPayNative",
		map[string]string{
			"app_id":  "wx-app",
			"mch_id":  "wx-mch",
			"api_key": "wx-key",
		},
		gatewayOrder{
			TradeNo:   "T415",
			Total:     7890,
			NotifyURL: "https://api.example.com/notify",
			ReturnURL: "https://app.example.com/#/order/T415",
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Type != 0 || fmt.Sprint(result.Data) != "weixin://wxpay/T415" {
		t.Fatalf("unexpected wechat native result: %#v", result)
	}
}

func TestVerifyGatewayNotifyEPay(t *testing.T) {
	params := map[string]string{
		"out_trade_no": "T402",
		"trade_no":     "P402",
		"money":        "12.34",
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = md5Hex(decodedQuery(params) + "secret")
	params["sign_type"] = "MD5"

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"EPay",
		map[string]string{"key": "secret", "pid": "10001"},
		NotifyRequest{Params: params},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T402" || result.CallbackNo != "P402" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyEPayRejectsInvalidBindingFields(t *testing.T) {
	base := map[string]string{
		"out_trade_no": "T402",
		"trade_no":     "P402",
		"money":        "12.34",
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
		"sign_type":    "MD5",
	}
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "failed trade status", mutate: func(values map[string]string) { values["trade_status"] = "WAIT_BUYER_PAY" }},
		{name: "wrong merchant", mutate: func(values map[string]string) { values["pid"] = "other" }},
		{name: "zero amount", mutate: func(values map[string]string) { values["money"] = "0.00" }},
		{name: "excess precision", mutate: func(values map[string]string) { values["money"] = "12.345" }},
		{name: "wrong sign type", mutate: func(values map[string]string) { values["sign_type"] = "SHA256" }},
		{name: "missing callback", mutate: func(values map[string]string) { delete(values, "trade_no") }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := cloneStringMap(base)
			test.mutate(params)
			unsigned := cloneStringMap(params)
			delete(unsigned, "sign_type")
			params["sign"] = md5Hex(decodedQuery(unsigned) + "secret")
			if _, err := verifyGatewayNotify(
				context.Background(),
				http.DefaultClient,
				"EPay",
				map[string]string{"key": "secret", "pid": "10001"},
				NotifyRequest{Params: params},
			); !errors.Is(err, ErrVerifyFailed) {
				t.Fatalf("expected verification failure, got %v", err)
			}
		})
	}
}

func TestVerifyGatewayNotifyRejectsMissingSigningSecret(t *testing.T) {
	params := map[string]string{
		"out_trade_no": "T402",
		"trade_no":     "P402",
		"money":        "12.34",
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = md5Hex(decodedQuery(params))
	params["sign_type"] = "MD5"

	_, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"EPay",
		map[string]string{"pid": "10001"},
		NotifyRequest{Params: params},
	)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected an empty configured secret to fail closed, got %v", err)
	}
}

func TestParsePositiveMoneyCentsProviderPrecision(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		tolerant  bool
		want      int64
		wantError bool
	}{
		{name: "ordinary cents", raw: "12.34", want: 1234},
		{name: "one decimal", raw: "12.3", want: 1230},
		{name: "strict extra precision", raw: "12.34000000", wantError: true},
		{name: "provider trailing zero precision", raw: "12.34000000", tolerant: true, want: 1234},
		{name: "provider real sub-cent precision", raw: "12.34000001", tolerant: true, wantError: true},
		{name: "provider third decimal", raw: "12.34500000", tolerant: true, wantError: true},
		{name: "zero remains invalid", raw: "0.00000000", tolerant: true, wantError: true},
		{name: "exponent remains invalid", raw: "1.234e1", tolerant: true, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				got int64
				err error
			)
			if test.tolerant {
				got, err = parsePositiveMoneyCentsAllowTrailingZeros(test.raw)
			} else {
				got, err = parsePositiveMoneyCents(test.raw)
			}
			if test.wantError {
				if !errors.Is(err, ErrVerifyFailed) {
					t.Fatalf("expected verification failure, got amount=%d err=%v", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got amount=%d err=%v, want %d", got, err, test.want)
			}
		})
	}
}

func TestStripeIdempotencyKeyRemainsStableWhenRetryParametersChange(t *testing.T) {
	first := url.Values{"amount": {"1000"}, "currency": {"usd"}}
	same := url.Values{"currency": {"usd"}, "amount": {"1000"}}
	changed := url.Values{"amount": {"1001"}, "currency": {"usd"}}

	firstKey := stripeRequestHeaders(map[string]string{"stripe_sk_live": "sk_test"}, "checkout:T1", first).Get("Idempotency-Key")
	sameKey := stripeRequestHeaders(map[string]string{"stripe_sk_live": "sk_test"}, "checkout:T1", same).Get("Idempotency-Key")
	changedKey := stripeRequestHeaders(map[string]string{"stripe_sk_live": "sk_test"}, "checkout:T1", changed).Get("Idempotency-Key")
	if firstKey == "" || firstKey != sameKey {
		t.Fatalf("identical canonical requests should share an idempotency key: first=%q same=%q", firstKey, sameKey)
	}
	if firstKey != changedKey {
		t.Fatalf("one station order must keep one Stripe idempotency key across retries: first=%q changed=%q", firstKey, changedKey)
	}
}

func TestVerifyGatewayNotifyEPayProRejectsWrongConfiguredType(t *testing.T) {
	params := map[string]string{
		"out_trade_no": "T405",
		"trade_no":     "P405",
		"type":         "alipay",
		"money":        "12.34",
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = md5Hex(decodedQuery(params) + "secret")
	params["sign_type"] = "MD5"

	_, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"epaypro",
		map[string]string{"key": "secret", "pid": "10001", "type": "wxpay"},
		NotifyRequest{Params: params},
	)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected configured type mismatch, got %v", err)
	}

	_, err = verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"epaypro",
		map[string]string{"key": "secret", "pid": "10001"},
		NotifyRequest{Params: params},
	)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected missing epaypro type configuration to fail closed, got %v", err)
	}
}

func TestVerifyGatewayNotifyBindsAmountsForSignedFormGateways(t *testing.T) {
	tests := []struct {
		name    string
		gateway string
		config  map[string]string
		params  map[string]string
		sign    func(map[string]string, map[string]string) string
	}{
		{
			name:    "epusdt",
			gateway: "EpusdtPay",
			config:  map[string]string{"epusdt_pay_apitoken": "secret"},
			params:  map[string]string{"status": "2", "order_id": "T-EPU", "trade_id": "P-EPU", "amount": "12.34"},
			sign: func(cfg, params map[string]string) string {
				return epusdtSign(cfg, stringMapToAny(params))
			},
		},
		{
			name:    "beasy",
			gateway: "BEasyPaymentUSDT",
			config:  map[string]string{"bepusdt_apitoken": "secret"},
			params:  map[string]string{"status": "2", "order_id": "T-BE", "trade_id": "P-BE", "amount": "12.34"},
			sign: func(cfg, params map[string]string) string {
				return md5Hex(decodedQuery(params) + cfg["bepusdt_apitoken"])
			},
		},
		{
			name:    "mgate",
			gateway: "MGate",
			config:  map[string]string{"mgate_app_secret": "secret"},
			params:  map[string]string{"out_trade_no": "T-MG", "trade_no": "P-MG", "total_amount": "1234"},
			sign: func(cfg, params map[string]string) string {
				return md5Hex(encodedQuery(params) + cfg["mgate_app_secret"])
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := cloneStringMap(test.params)
			signature := test.sign(test.config, params)
			if test.gateway == "MGate" {
				params["sign"] = signature
			} else {
				params["signature"] = signature
			}
			result, err := verifyGatewayNotify(context.Background(), http.DefaultClient, test.gateway, test.config, NotifyRequest{Params: params})
			if err != nil {
				t.Fatalf("verify callback: %v", err)
			}
			if result.TradeNo == "" || result.CallbackNo == "" || result.Amount == nil || *result.Amount != 1234 {
				t.Fatalf("callback fields were not bound: %#v", result)
			}
		})
	}
}

func TestPaymentNotifyBindsMethodAndAmountToOrderConfirmation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT id, uuid, payment, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable\s+FROM v2_payment\s+WHERE payment = \$1 AND uuid = \$2`).
		WithArgs("EPay", "pay-uuid").
		WillReturnRows(sqlmock.NewRows([]string{"id", "uuid", "payment", "config", "notify_domain", "handling_fee_fixed", "handling_fee_percent", "enable"}).
			AddRow(int64(7), "pay-uuid", "EPay", `{"pid":"10001","key":"secret"}`, nil, nil, nil, int64(1)))

	params := map[string]string{
		"out_trade_no": "T402",
		"trade_no":     "P402",
		"money":        "12.34",
		"pid":          "10001",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = md5Hex(decodedQuery(params) + "secret")
	params["sign_type"] = "MD5"

	orders := &recordingPaymentOrderManager{}
	service := NewDBService(config.Config{}, db, orders)
	result, err := service.Notify(context.Background(), "EPay", "pay-uuid", NotifyRequest{Params: params})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if result != "success" || orders.tradeNo != "T402" {
		t.Fatalf("unexpected notify result=%q trade=%q", result, orders.tradeNo)
	}
	if orders.confirmation.PaymentID == nil || *orders.confirmation.PaymentID != 7 {
		t.Fatalf("payment method was not bound: %#v", orders.confirmation)
	}
	if orders.confirmation.Amount == nil || *orders.confirmation.Amount != 1234 || orders.confirmation.CallbackNo != "P402" {
		t.Fatalf("amount or callback was not bound: %#v", orders.confirmation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutRejectsNegativeOrderInsteadOfOpeningForFree(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TNEG", int64(5)).
		WillReturnRows(checkoutOrderRow("TNEG", nil, -1, nil, nil, nil, false, 0))
	mock.ExpectRollback()

	orders := &recordingPaymentOrderManager{}
	service := NewDBService(config.Config{}, db, orders)
	_, err = service.Checkout(context.Background(), 5, CheckoutRequest{TradeNo: "TNEG"})
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("expected negative order to be rejected, got %v", err)
	}
	if orders.tradeNo != "" {
		t.Fatalf("negative order reached free-open path: %q", orders.tradeNo)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutDoesNotReplaceAnExistingPaymentMethod(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TLOCKED", int64(5)).
		WillReturnRows(checkoutOrderRow("TLOCKED", int64(7), 1000, int64(100), nil, nil, false, 0))
	mock.ExpectRollback()

	service := NewDBService(config.Config{}, db, &recordingPaymentOrderManager{})
	_, err = service.Checkout(context.Background(), 5, CheckoutRequest{TradeNo: "TLOCKED", MethodID: 8})
	if !errors.Is(err, ErrPaymentMethodLocked) {
		t.Fatalf("expected payment method lock error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutReusesLockedMethodAndStoredHandlingAmount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()
	retryMethod := paymentRecord{
		ID: 7, UUID: "epay", Payment: "EPay",
		Config: `{"url":"https://pay.example.com","pid":"10001","key":"secret"}`,
	}
	retryRequest := CheckoutRequest{TradeNo: "TRETRY", MethodID: 7}
	retryFingerprint := checkoutFingerprint(
		retryMethod,
		retryRequest,
		5,
		1100,
		"https://app.example.com/api/v1/guest/payment/notify/EPay/epay",
		"https://app.example.com/#/order/TRETRY",
	)

	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TRETRY", int64(5)).
		WillReturnRows(sqlmock.NewRows(checkoutOrderColumns).AddRow(
			int64(9), int64(5), "TRETRY", int64(7), int64(1000), int64(100), nil, "expired-claim", retryFingerprint, false, int64(0),
		))
	mock.ExpectQuery(`SELECT id, uuid, payment, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable\s+FROM v2_payment\s+WHERE id = \$1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "uuid", "payment", "config", "notify_domain", "handling_fee_fixed", "handling_fee_percent", "enable"}).
			AddRow(int64(7), "epay", "EPay", `{"url":"https://pay.example.com","pid":"10001","key":"secret"}`, nil, int64(9999), float64(99), int64(1)))
	mock.ExpectExec(`UPDATE v2_order\s+SET payment_id = \$2,\s+handling_amount = \$3,\s+checkout_claim = \$4,\s+checkout_claim_expires_at = EXTRACT\(EPOCH FROM NOW\(\)\)::BIGINT \+ \$5,\s+checkout_fingerprint = \$6,\s+updated_at = \$7\s+WHERE id = \$1`).
		WithArgs(int64(9), int64(7), int64(100), "claim-retry", int64(120), retryFingerprint, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result, checkout_claim, checkout_fingerprint, status\s+FROM v2_order\s+WHERE trade_no = \$1 AND user_id = \$2\s+FOR UPDATE`).
		WithArgs("TRETRY", int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "trade_no", "payment_id", "total_amount", "handling_amount", "checkout_result", "checkout_claim", "checkout_fingerprint", "status"}).
			AddRow(int64(9), int64(5), "TRETRY", int64(7), int64(1000), int64(100), nil, "claim-retry", retryFingerprint, int64(0)))
	mock.ExpectExec(`UPDATE v2_order\s+SET checkout_result = \$2, checkout_claim = NULL, checkout_claim_expires_at = NULL, updated_at = \$3\s+WHERE id = \$1`).
		WithArgs(int64(9), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := NewDBService(config.Config{AppURL: "https://app.example.com"}, db, &recordingPaymentOrderManager{})
	service.claimFn = func() (string, error) { return "claim-retry", nil }
	result, err := service.Checkout(context.Background(), 5, retryRequest)
	if err != nil {
		t.Fatalf("retry checkout: %v", err)
	}
	parsed, err := url.Parse(fmt.Sprint(result.Data))
	if err != nil {
		t.Fatalf("parse checkout URL: %v", err)
	}
	if got := parsed.Query().Get("money"); got != "11.00" {
		t.Fatalf("expected stored handling amount to remain 1.00, got money=%q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutDoesNotHoldDatabaseConnectionDuringProviderRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()
	outsideMethod := paymentRecord{
		ID: 7, UUID: "epusdt", Payment: "EpusdtPay",
		Config: `{"epusdt_pay_url":"https://pay.example.test","epusdt_pay_apitoken":"secret"}`,
	}
	outsideRequest := CheckoutRequest{TradeNo: "TOUTSIDE", MethodID: 7}
	outsideFingerprint := checkoutFingerprint(
		outsideMethod,
		outsideRequest,
		5,
		1000,
		"https://app.example.test/api/v1/guest/payment/notify/EpusdtPay/epusdt",
		"https://app.example.test/#/order/TOUTSIDE",
	)

	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TOUTSIDE", int64(5)).
		WillReturnRows(checkoutOrderRow("TOUTSIDE", nil, 1000, nil, nil, nil, false, 0))
	mock.ExpectQuery(`SELECT id, uuid, payment, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable\s+FROM v2_payment\s+WHERE id = \$1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "uuid", "payment", "config", "notify_domain", "handling_fee_fixed", "handling_fee_percent", "enable"}).
			AddRow(int64(7), "epusdt", "EpusdtPay", `{"epusdt_pay_url":"https://pay.example.test","epusdt_pay_apitoken":"secret"}`, nil, nil, nil, int64(1)))
	mock.ExpectExec(`UPDATE v2_order\s+SET payment_id = \$2,\s+handling_amount = \$3,\s+checkout_claim = \$4,\s+checkout_claim_expires_at = EXTRACT\(EPOCH FROM NOW\(\)\)::BIGINT \+ \$5,\s+checkout_fingerprint = \$6,\s+updated_at = \$7\s+WHERE id = \$1`).
		WithArgs(int64(9), int64(7), nil, "claim-outside", int64(120), outsideFingerprint, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result, checkout_claim, checkout_fingerprint, status\s+FROM v2_order\s+WHERE trade_no = \$1 AND user_id = \$2\s+FOR UPDATE`).
		WithArgs("TOUTSIDE", int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "trade_no", "payment_id", "total_amount", "handling_amount", "checkout_result", "checkout_claim", "checkout_fingerprint", "status"}).
			AddRow(int64(9), int64(5), "TOUTSIDE", int64(7), int64(1000), nil, nil, "claim-outside", outsideFingerprint, int64(0)))
	mock.ExpectExec(`UPDATE v2_order\s+SET checkout_result = \$2, checkout_claim = NULL, checkout_claim_expires_at = NULL, updated_at = \$3\s+WHERE id = \$1`).
		WithArgs(int64(9), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	providerCalled := false
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	service := NewDBService(config.Config{AppURL: "https://app.example.test"}, db, &recordingPaymentOrderManager{})
	service.claimFn = func() (string, error) { return "claim-outside", nil }
	service.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		providerCalled = true
		if inUse := db.Stats().InUse; inUse != 0 {
			t.Fatalf("provider HTTP request held %d database connections", inUse)
		}
		cancelRequest()
		return jsonResponse(http.StatusOK, `{"status_code":200,"data":{"payment_url":"https://pay.example.test/order/TOUTSIDE"}}`), nil
	})}

	result, err := service.Checkout(requestCtx, 5, outsideRequest)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if !providerCalled || result.Data != "https://pay.example.test/order/TOUTSIDE" {
		t.Fatalf("unexpected provider result: called=%v result=%#v", providerCalled, result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutReturnsImmediatelyWhenCreationIsAlreadyInProgress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TBUSY", int64(5)).
		WillReturnRows(checkoutOrderRow("TBUSY", int64(7), 1000, int64(100), nil, "active-claim", true, 0))
	mock.ExpectRollback()

	service := NewDBService(config.Config{}, db, &recordingPaymentOrderManager{})
	_, err = service.Checkout(context.Background(), 5, CheckoutRequest{TradeNo: "TBUSY", MethodID: 7})
	if !errors.Is(err, ErrCheckoutInProgress) {
		t.Fatalf("expected in-progress checkout error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutRejectsChangedMerchantConfigurationAfterAttempt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TCHANGED", int64(5)).
		WillReturnRows(sqlmock.NewRows(checkoutOrderColumns).AddRow(
			int64(9), int64(5), "TCHANGED", int64(7), int64(1000), int64(100), nil, "expired-claim", "old-merchant-fingerprint", false, int64(0),
		))
	mock.ExpectQuery(`SELECT id, uuid, payment, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable\s+FROM v2_payment\s+WHERE id = \$1`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "uuid", "payment", "config", "notify_domain", "handling_fee_fixed", "handling_fee_percent", "enable"}).
			AddRow(int64(7), "epay", "EPay", `{"url":"https://new-merchant.example","pid":"new","key":"new-secret"}`, nil, nil, nil, int64(1)))
	mock.ExpectRollback()

	service := NewDBService(config.Config{AppURL: "https://app.example.test"}, db, &recordingPaymentOrderManager{})
	_, err = service.Checkout(context.Background(), 5, CheckoutRequest{TradeNo: "TCHANGED", MethodID: 7})
	if !errors.Is(err, ErrCheckoutConfigChanged) {
		t.Fatalf("expected changed merchant configuration to fail closed, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPersistCheckoutResultRejectsExpiredOwnerAfterNewClaimTakesOver(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id, trade_no, payment_id, total_amount, handling_amount, checkout_result, checkout_claim, checkout_fingerprint, status\s+FROM v2_order\s+WHERE trade_no = \$1 AND user_id = \$2\s+FOR UPDATE`).
		WithArgs("TFENCE", int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "trade_no", "payment_id", "total_amount", "handling_amount", "checkout_result", "checkout_claim", "checkout_fingerprint", "status"}).
			AddRow(int64(9), int64(5), "TFENCE", int64(7), int64(1000), int64(100), nil, "new-claim", "same-fingerprint", int64(0)))
	mock.ExpectRollback()

	service := NewDBService(config.Config{}, db, &recordingPaymentOrderManager{})
	_, err = service.persistCheckoutResult(
		context.Background(), 5, "TFENCE", 7, 1100, 100,
		"old-claim", "same-fingerprint", `{"version":1}`, CheckoutResult{Type: 1, Data: "https://pay.example/old"},
	)
	if !errors.Is(err, ErrCheckoutInProgress) {
		t.Fatalf("expected stale checkout owner to be fenced out, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPaymentCheckoutReturnsPersistedResultWithoutCreatingAnotherProviderOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sql mock: %v", err)
	}
	defer db.Close()

	persisted, err := encodeCheckoutSnapshot(7, 1100, CheckoutResult{Type: 1, Data: "https://pay.example.com/original"})
	if err != nil {
		t.Fatalf("encode persisted checkout: %v", err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(checkoutOrderLockPattern).
		WithArgs("TCACHED", int64(5)).
		WillReturnRows(checkoutOrderRow("TCACHED", int64(7), 1000, int64(100), persisted, nil, false, 0))
	mock.ExpectRollback()

	service := NewDBService(config.Config{}, db, &recordingPaymentOrderManager{})
	result, err := service.Checkout(context.Background(), 5, CheckoutRequest{TradeNo: "TCACHED", MethodID: 7})
	if err != nil {
		t.Fatalf("cached checkout: %v", err)
	}
	if result.Type != 1 || result.Data != "https://pay.example.com/original" {
		t.Fatalf("unexpected cached result: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestVerifyGatewayNotifyStripeCheckout(t *testing.T) {
	payload := `{"type":"checkout.session.completed","data":{"object":{"payment_status":"paid","client_reference_id":"T403","payment_intent":"pi_403","amount_total":1234,"currency":"usd","metadata":{"forest_order_amount":"1234","forest_gateway_amount":"1234","forest_gateway_currency":"usd"}}}}`
	secret := "whsec_test"
	timestamp := int64(1700000000)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature := hex.EncodeToString(mac.Sum(nil))

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"StripeCheckout",
		map[string]string{"stripe_webhook_key": secret, "currency": "EUR"},
		NotifyRequest{
			Headers: http.Header{
				"Stripe-Signature": []string{fmt.Sprintf("t=%d,v1=%s", timestamp, signature)},
			},
			Body: []byte(payload),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T403" || result.CallbackNo != "pi_403" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected stripe notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyStripeRejectsProviderAmountMismatch(t *testing.T) {
	payload := `{"type":"checkout.session.completed","data":{"object":{"payment_status":"paid","client_reference_id":"T403","payment_intent":"pi_403","amount_total":1200,"currency":"usd","metadata":{"forest_order_amount":"1234","forest_gateway_amount":"1234","forest_gateway_currency":"usd"}}}}`
	secret := "whsec_test"
	timestamp := int64(1700000000)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature := hex.EncodeToString(mac.Sum(nil))

	_, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"StripeCheckout",
		map[string]string{"stripe_webhook_key": secret, "currency": "USD"},
		NotifyRequest{
			Headers: http.Header{"Stripe-Signature": []string{fmt.Sprintf("t=%d,v1=%s", timestamp, signature)}},
			Body:    []byte(payload),
		},
	)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected Stripe amount mismatch to fail, got %v", err)
	}
}

func TestVerifyGatewayNotifyStripePreservesLargeIntegerAmounts(t *testing.T) {
	payload := `{"type":"checkout.session.completed","data":{"object":{"payment_status":"paid","client_reference_id":"T-LARGE","payment_intent":"pi_large","amount_total":1000000,"currency":"usd","metadata":{"forest_order_amount":"2147483647","forest_gateway_amount":"1000000","forest_gateway_currency":"usd"}}}}`
	secret := "whsec_test"
	timestamp := int64(1700000000)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature := hex.EncodeToString(mac.Sum(nil))

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"StripeCheckout",
		map[string]string{"stripe_webhook_key": secret, "currency": "USD"},
		NotifyRequest{
			Headers: http.Header{"Stripe-Signature": []string{fmt.Sprintf("t=%d,v1=%s", timestamp, signature)}},
			Body:    []byte(payload),
		},
	)
	if err != nil {
		t.Fatalf("verify large Stripe callback: %v", err)
	}
	if result.Amount == nil || *result.Amount != int64(2147483647) {
		t.Fatalf("large integer amount lost precision: %#v", result.Amount)
	}
}

func TestVerifyGatewayNotifyCoinPayments(t *testing.T) {
	params := map[string]string{
		"merchant":    "merchant-1",
		"item_number": "T410",
		"txn_id":      "CP-1",
		"status":      "100",
		"amount1":     "12.34000000",
		"currency1":   "CNY",
	}
	rawBody := []byte("status=100&currency1=CNY&txn_id=CP-1&item_number=T410&amount1=12.34000000&merchant=merchant-1")
	signed := hmac.New(sha512.New, []byte("ipn-secret"))
	_, _ = signed.Write(rawBody)

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"CoinPayments",
		map[string]string{
			"coinpayments_merchant_id": "merchant-1",
			"coinpayments_ipn_secret":  "ipn-secret",
			"coinpayments_currency":    "CNY",
		},
		NotifyRequest{
			Params: params,
			Body:   rawBody,
			Headers: http.Header{
				"Hmac": []string{hex.EncodeToString(signed.Sum(nil))},
			},
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T410" || result.CallbackNo != "CP-1" || result.CustomResult != "IPN OK" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected coinpayments notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyCoinbase(t *testing.T) {
	payload := `{"event":{"id":"event-CB-1","type":"charge:confirmed","data":{"id":"CB-1","metadata":{"outTradeNo":"T411"},"pricing":{"local":{"amount":"12.34","currency":"CNY"}}}}}`
	signatureMac := hmac.New(sha256.New, []byte("cb-webhook"))
	_, _ = signatureMac.Write([]byte(payload))

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"Coinbase",
		map[string]string{"coinbase_webhook_key": "cb-webhook"},
		NotifyRequest{
			Headers: http.Header{
				"X-Cc-Webhook-Signature": []string{hex.EncodeToString(signatureMac.Sum(nil))},
			},
			Body: []byte(payload),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T411" || result.CallbackNo != "CB-1" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected coinbase notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyCoinbaseRejectsNonFinalEvent(t *testing.T) {
	payload := `{"event":{"id":"event-CB-1","type":"charge:pending","data":{"id":"CB-1","metadata":{"outTradeNo":"T411"},"pricing":{"local":{"amount":"12.34","currency":"CNY"}}}}}`
	signatureMac := hmac.New(sha256.New, []byte("cb-webhook"))
	_, _ = signatureMac.Write([]byte(payload))

	_, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"Coinbase",
		map[string]string{"coinbase_webhook_key": "cb-webhook"},
		NotifyRequest{
			Headers: http.Header{"X-Cc-Webhook-Signature": []string{hex.EncodeToString(signatureMac.Sum(nil))}},
			Body:    []byte(payload),
		},
	)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected pending Coinbase event to fail, got %v", err)
	}
}

func TestVerifyGatewayNotifyBTCPay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stores/store-1/invoices/inv_412" {
			t.Fatalf("unexpected btcpay detail path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token btc-key" {
			t.Fatalf("unexpected btcpay detail auth: %#v", r.Header)
		}
		_, _ = w.Write([]byte(`{"status":"Settled","amount":"12.34","currency":"CNY","metadata":{"orderId":"T412"}}`))
	}))
	defer server.Close()

	payload := `{"invoiceId":"inv_412"}`
	signatureMac := hmac.New(sha256.New, []byte("btc-webhook"))
	_, _ = signatureMac.Write([]byte(payload))

	result, err := verifyGatewayNotify(
		context.Background(),
		server.Client(),
		"BTCPay",
		map[string]string{
			"btcpay_url":         server.URL + "/",
			"btcpay_storeId":     "store-1",
			"btcpay_api_key":     "btc-key",
			"btcpay_webhook_key": "btc-webhook",
		},
		NotifyRequest{
			Headers: http.Header{
				"Btcpay-Sig": []string{"sha256=" + hex.EncodeToString(signatureMac.Sum(nil))},
			},
			Body: []byte(payload),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T412" || result.CallbackNo != "inv_412" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected btcpay notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyStripeAlipayAndWepay(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.stripe.com" && r.URL.Path == "/v1/charges" {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse stripe charge form: %v", err)
			}
			return jsonResponse(http.StatusOK, `{"id":"ch_test","paid":true}`), nil
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		return nil, nil
	})}

	secret := "whsec_test"
	timestamp := int64(1700000000)
	payload := `{"type":"source.chargeable","data":{"object":{"id":"src_1","amount":1234,"currency":"usd","metadata":{"out_trade_no":"T413","forest_order_amount":"1234","forest_gateway_amount":"1234","forest_gateway_currency":"usd"}}}}`
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature := hex.EncodeToString(mac.Sum(nil))

	result, err := verifyGatewayNotify(
		context.Background(),
		client,
		"StripeAlipay",
		map[string]string{
			"stripe_sk_live":     "sk_live_test",
			"stripe_webhook_key": secret,
			"currency":           "USD",
		},
		NotifyRequest{
			Headers: http.Header{
				"Stripe-Signature": []string{fmt.Sprintf("t=%d,v1=%s", timestamp, signature)},
			},
			Body: []byte(payload),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.CustomResult != "success" || result.TradeNo != "" || result.CallbackNo != "" {
		t.Fatalf("unexpected stripe alipay chargeable result: %#v", result)
	}

	payload = `{"type":"charge.succeeded","data":{"object":{"id":"ch_413","amount":1234,"currency":"usd","metadata":{"out_trade_no":"T413","forest_order_amount":"1234","forest_gateway_amount":"1234","forest_gateway_currency":"usd"}}}}`
	mac = hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature = hex.EncodeToString(mac.Sum(nil))
	result, err = verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"StripeWepay",
		map[string]string{
			"stripe_webhook_key": secret,
			"currency":           "USD",
		},
		NotifyRequest{
			Headers: http.Header{
				"Stripe-Signature": []string{fmt.Sprintf("t=%d,v1=%s", timestamp, signature)},
			},
			Body: []byte(payload),
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T413" || result.CallbackNo != "ch_413" {
		t.Fatalf("unexpected stripe wepay notify result: %#v", result)
	}
	if result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("expected stripe order amount 1234, got %#v", result.Amount)
	}
}

func TestVerifyGatewayNotifyStripeSourceDoesNotAcknowledgeEmptyCharge(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.stripe.com" || r.URL.Path != "/v1/charges" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		return jsonResponse(http.StatusOK, `{}`), nil
	})}

	secret := "whsec_test"
	timestamp := int64(1700000000)
	payload := `{"type":"source.chargeable","data":{"object":{"id":"src_empty","amount":1234,"currency":"usd","metadata":{"out_trade_no":"T-EMPTY","forest_order_amount":"1234","forest_gateway_amount":"1234","forest_gateway_currency":"usd"}}}}`
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature := hex.EncodeToString(mac.Sum(nil))

	_, err := verifyGatewayNotify(
		context.Background(),
		client,
		"StripeAlipay",
		map[string]string{
			"stripe_sk_live":     "sk_live_test",
			"stripe_webhook_key": secret,
		},
		NotifyRequest{
			Headers: http.Header{"Stripe-Signature": []string{fmt.Sprintf("t=%d,v1=%s", timestamp, signature)}},
			Body:    []byte(payload),
		},
	)
	if !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("empty Stripe charge must remain retryable, got %v", err)
	}
}

func TestVerifyGatewayNotifyAlipayF2F(t *testing.T) {
	privatePEM, publicPEM := testAlipayKeys(t)
	params := map[string]string{
		"app_id":       "alipay-app",
		"out_trade_no": "T416",
		"trade_no":     "ALI-416",
		"trade_status": "TRADE_SUCCESS",
		"total_amount": "12.34",
		"notify_time":  "2026-03-29 06:30:00",
		"notify_type":  "trade_status_sync",
		"notify_id":    "notify-416",
		"charset":      "utf-8",
		"version":      "1.0",
		"sign_type":    "RSA2",
	}
	params["sign"] = testAlipaySignature(t, privatePEM, params)

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"AlipayF2F",
		map[string]string{"app_id": "alipay-app", "public_key": publicPEM},
		NotifyRequest{Params: params},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T416" || result.CallbackNo != "ALI-416" || result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("unexpected alipay notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyWechatPayNative(t *testing.T) {
	fields := map[string]string{
		"appid":          "wx-app",
		"mch_id":         "wx-mch",
		"nonce_str":      "nonce-417",
		"result_code":    "SUCCESS",
		"return_code":    "SUCCESS",
		"out_trade_no":   "T417",
		"transaction_id": "WX-417",
		"total_fee":      "1234",
		"fee_type":       "CNY",
	}
	fields["sign"] = testWechatSign(fields, "wx-key")
	payload := `<xml><appid>` + fields["appid"] + `</appid><mch_id>` + fields["mch_id"] + `</mch_id><nonce_str>` + fields["nonce_str"] + `</nonce_str><result_code>` + fields["result_code"] + `</result_code><return_code>` + fields["return_code"] + `</return_code><out_trade_no>` + fields["out_trade_no"] + `</out_trade_no><transaction_id>` + fields["transaction_id"] + `</transaction_id><total_fee>` + fields["total_fee"] + `</total_fee><fee_type>` + fields["fee_type"] + `</fee_type><sign>` + fields["sign"] + `</sign></xml>`

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"WechatPayNative",
		map[string]string{"app_id": "wx-app", "mch_id": "wx-mch", "api_key": "wx-key"},
		NotifyRequest{Body: []byte(payload)},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T417" || result.CallbackNo != "WX-417" {
		t.Fatalf("unexpected wechat notify result: %#v", result)
	}
	if !strings.Contains(result.CustomResult, "<return_code><![CDATA[SUCCESS]]></return_code>") {
		t.Fatalf("expected custom xml success response, got %q", result.CustomResult)
	}
	if result.Amount == nil || *result.Amount != 1234 {
		t.Fatalf("expected callback amount 1234, got %#v", result.Amount)
	}
}

func TestVerifyGatewayNotifyRejectsBadSignature(t *testing.T) {
	_, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"EPay",
		map[string]string{"key": "secret", "pid": "10001"},
		NotifyRequest{
			Params: map[string]string{
				"out_trade_no": "T404",
				"trade_no":     "P404",
				"money":        "12.34",
				"pid":          "10001",
				"trade_status": "TRADE_SUCCESS",
				"sign":         "bad",
				"sign_type":    "MD5",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("expected verify error, got %v", err)
	}
}
