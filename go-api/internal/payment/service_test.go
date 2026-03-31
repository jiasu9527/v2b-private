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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

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
	}
	signBase := "out_trade_no=T402&trade_no=P402secret"
	sum := md5.Sum([]byte(signBase))
	params["sign"] = hex.EncodeToString(sum[:])
	params["sign_type"] = "MD5"

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"EPay",
		map[string]string{"key": "secret"},
		NotifyRequest{Params: params},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T402" || result.CallbackNo != "P402" {
		t.Fatalf("unexpected notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyStripeCheckout(t *testing.T) {
	payload := `{"type":"checkout.session.completed","data":{"object":{"payment_status":"paid","client_reference_id":"T403","payment_intent":"pi_403"}}}`
	secret := "whsec_test"
	timestamp := int64(1700000000)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature := hex.EncodeToString(mac.Sum(nil))

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"StripeCheckout",
		map[string]string{"stripe_webhook_key": secret},
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
	if result.TradeNo != "T403" || result.CallbackNo != "pi_403" {
		t.Fatalf("unexpected stripe notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyCoinPayments(t *testing.T) {
	params := map[string]string{
		"merchant":    "merchant-1",
		"item_number": "T410",
		"txn_id":      "CP-1",
		"status":      "100",
	}
	signed := hmac.New(sha512.New, []byte("ipn-secret"))
	_, _ = signed.Write([]byte(encodedQuery(params)))

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"CoinPayments",
		map[string]string{
			"coinpayments_merchant_id": "merchant-1",
			"coinpayments_ipn_secret":  "ipn-secret",
		},
		NotifyRequest{
			Params: params,
			Headers: http.Header{
				"Hmac": []string{hex.EncodeToString(signed.Sum(nil))},
			},
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T410" || result.CallbackNo != "CP-1" || result.CustomResult != "IPN OK" {
		t.Fatalf("unexpected coinpayments notify result: %#v", result)
	}
}

func TestVerifyGatewayNotifyCoinbase(t *testing.T) {
	payload := `{"event":{"id":"CB-1","data":{"metadata":{"outTradeNo":"T411"}}}}`
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
	if result.TradeNo != "T411" || result.CallbackNo != "CB-1" {
		t.Fatalf("unexpected coinbase notify result: %#v", result)
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
		_, _ = w.Write([]byte(`{"metadata":{"orderId":"T412"}}`))
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
	if result.TradeNo != "T412" || result.CallbackNo != "inv_412" {
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
	payload := `{"type":"source.chargeable","data":{"object":{"id":"src_1","amount":1234,"currency":"usd","metadata":{"out_trade_no":"T413"}}}}`
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

	payload = `{"type":"charge.succeeded","data":{"object":{"id":"ch_413","metadata":{"out_trade_no":"T413"}}}}`
	mac = hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, payload)))
	signature = hex.EncodeToString(mac.Sum(nil))
	result, err = verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"StripeWepay",
		map[string]string{
			"stripe_webhook_key": secret,
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
		map[string]string{"public_key": publicPEM},
		NotifyRequest{Params: params},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.TradeNo != "T416" || result.CallbackNo != "ALI-416" {
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
	}
	fields["sign"] = testWechatSign(fields, "wx-key")
	payload := `<xml><appid>` + fields["appid"] + `</appid><mch_id>` + fields["mch_id"] + `</mch_id><nonce_str>` + fields["nonce_str"] + `</nonce_str><result_code>` + fields["result_code"] + `</result_code><return_code>` + fields["return_code"] + `</return_code><out_trade_no>` + fields["out_trade_no"] + `</out_trade_no><transaction_id>` + fields["transaction_id"] + `</transaction_id><sign>` + fields["sign"] + `</sign></xml>`

	result, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"WechatPayNative",
		map[string]string{"api_key": "wx-key"},
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
}

func TestVerifyGatewayNotifyRejectsBadSignature(t *testing.T) {
	_, err := verifyGatewayNotify(
		context.Background(),
		http.DefaultClient,
		"EPay",
		map[string]string{"key": "secret"},
		NotifyRequest{
			Params: map[string]string{
				"out_trade_no": "T404",
				"trade_no":     "P404",
				"sign":         "bad",
				"sign_type":    "MD5",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("expected verify error, got %v", err)
	}
}
