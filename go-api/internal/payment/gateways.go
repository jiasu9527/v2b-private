package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
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

func buildGatewayCheckout(ctx context.Context, client *http.Client, gateway string, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	switch gateway {
	case "AlipayF2F":
		return buildAlipayF2FCheckout(ctx, client, cfg, order)
	case "CoinPayments":
		return buildCoinPaymentsCheckout(cfg, order), nil
	case "Coinbase":
		return buildCoinbaseCheckout(ctx, client, cfg, order)
	case "EPay":
		return buildEPayCheckout(cfg, order), nil
	case "epaypro":
		paymentType := strings.ToLower(configValue(cfg, "type"))
		if paymentType != "alipay" && paymentType != "wxpay" {
			return CheckoutResult{}, ErrInvalidParameter
		}
		return buildEPayProCheckout(cfg, order), nil
	case "EpusdtPay":
		return buildEpusdtCheckout(ctx, client, cfg, order)
	case "BEasyPaymentUSDT":
		return buildBEasyCheckout(ctx, client, cfg, order)
	case "BTCPay":
		return buildBTCPayCheckout(ctx, client, cfg, order)
	case "MGate":
		return buildMGateCheckout(ctx, client, cfg, order)
	case "StripeCheckout":
		return buildStripeCheckout(ctx, client, cfg, order)
	case "StripeALL":
		return buildStripeAllCheckout(ctx, client, cfg, order)
	case "StripeAlipay":
		return buildStripeSourceCheckout(ctx, client, cfg, order, "alipay")
	case "StripeCredit":
		return buildStripeCreditCheckout(ctx, client, cfg, order)
	case "StripeWepay":
		return buildStripeSourceCheckout(ctx, client, cfg, order, "wechat")
	case "WechatPayNative":
		return buildWechatPayNativeCheckout(ctx, client, cfg, order)
	default:
		return CheckoutResult{}, ErrUnsupportedGateway
	}
}

func verifyGatewayNotify(ctx context.Context, client *http.Client, gateway string, cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	switch gateway {
	case "AlipayF2F":
		return verifyAlipayF2FNotify(cfg, req)
	case "CoinPayments":
		return verifyCoinPaymentsNotify(cfg, req)
	case "Coinbase":
		return verifyCoinbaseNotify(cfg, req)
	case "EPay":
		return verifyEPayNotify(cfg, req, false)
	case "epaypro":
		return verifyEPayNotify(cfg, req, true)
	case "EpusdtPay":
		return verifyEpusdtNotify(cfg, req)
	case "BEasyPaymentUSDT":
		return verifyBEasyNotify(cfg, req)
	case "BTCPay":
		return verifyBTCPayNotify(ctx, client, cfg, req)
	case "MGate":
		return verifyMGateNotify(cfg, req)
	case "StripeCheckout", "StripeALL", "StripeAlipay", "StripeCredit", "StripeWepay":
		return verifyStripeNotify(ctx, client, gateway, cfg, req)
	case "WechatPayNative":
		return verifyWechatPayNativeNotify(cfg, req)
	default:
		return notifyResult{}, ErrUnsupportedGateway
	}
}

func buildAlipayF2FCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	params := map[string]string{
		"app_id":      configValue(cfg, "app_id"),
		"method":      "alipay.trade.precreate",
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"notify_url":  order.NotifyURL,
		"biz_content": fmt.Sprintf(`{"subject":%q,"out_trade_no":%q,"total_amount":%q}`, alipayProductName(cfg), order.TradeNo, formatMoney(order.Total)),
	}

	signature, err := alipaySign(params, configValue(cfg, "private_key"))
	if err != nil {
		return CheckoutResult{}, err
	}
	params["sign"] = signature

	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	var response struct {
		Response struct {
			Code   string `json:"code"`
			Msg    string `json:"msg"`
			SubMsg string `json:"sub_msg"`
			QRCode string `json:"qr_code"`
		} `json:"alipay_trade_precreate_response"`
	}
	if err := doForm(ctx, client, http.MethodPost, "https://openapi.alipay.com/gateway.do", values, nil, &response); err != nil {
		return CheckoutResult{}, err
	}
	if response.Response.Code != "10000" || strings.TrimSpace(response.Response.QRCode) == "" {
		if response.Response.SubMsg != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Response.SubMsg)
		}
		if response.Response.Msg != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Response.Msg)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 0, Data: response.Response.QRCode}, nil
}

func buildCoinPaymentsCheckout(cfg map[string]string, order gatewayOrder) CheckoutResult {
	parsed, err := url.Parse(order.ReturnURL)
	successURL := order.ReturnURL
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		successURL = parsed.Scheme + "://" + parsed.Host
	}

	values := url.Values{}
	values.Set("cmd", "_pay_simple")
	values.Set("reset", "1")
	values.Set("merchant", configValue(cfg, "coinpayments_merchant_id"))
	values.Set("item_name", order.TradeNo)
	values.Set("item_number", order.TradeNo)
	values.Set("want_shipping", "0")
	values.Set("currency", configValue(cfg, "coinpayments_currency"))
	values.Set("amountf", formatMoney(order.Total))
	values.Set("success_url", successURL)
	values.Set("cancel_url", order.ReturnURL)
	values.Set("ipn_url", order.NotifyURL)

	return CheckoutResult{
		Type: 1,
		Data: "https://www.coinpayments.net/index.php?" + values.Encode(),
	}
}

func buildCoinbaseCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	payload := map[string]any{
		"name":         "订阅套餐",
		"description":  "订单号 " + order.TradeNo,
		"pricing_type": "fixed_price",
		"local_price": map[string]any{
			"amount":   formatMoney(order.Total),
			"currency": "CNY",
		},
		"metadata": map[string]any{
			"outTradeNo": order.TradeNo,
		},
	}

	var response struct {
		Data struct {
			HostedURL string `json:"hosted_url"`
		} `json:"data"`
	}
	headers := http.Header{
		"X-CC-Api-Key":         []string{configValue(cfg, "coinbase_api_key")},
		"X-CC-Version":         []string{"2018-03-22"},
		"X-CC-Idempotency-Key": []string{"forest:" + order.TradeNo},
	}
	if err := doJSON(ctx, client, http.MethodPost, configValue(cfg, "coinbase_url"), payload, headers, &response); err != nil {
		return CheckoutResult{}, err
	}
	if strings.TrimSpace(response.Data.HostedURL) == "" {
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 1, Data: response.Data.HostedURL}, nil
}

func buildBTCPayCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	payload := map[string]any{
		"jsonResponse": true,
		"amount":       formatMoney(order.Total),
		"currency":     "CNY",
		"metadata": map[string]any{
			"orderId": order.TradeNo,
		},
	}

	var response struct {
		CheckoutLink string `json:"checkoutLink"`
	}
	headers := http.Header{
		"Authorization":   []string{"token " + configValue(cfg, "btcpay_api_key")},
		"Idempotency-Key": []string{"forest:" + order.TradeNo},
	}
	endpoint := strings.TrimRight(configValue(cfg, "btcpay_url"), "/") + "/api/v1/stores/" + url.PathEscape(configValue(cfg, "btcpay_storeId")) + "/invoices"
	if err := doJSON(ctx, client, http.MethodPost, endpoint, payload, headers, &response); err != nil {
		return CheckoutResult{}, err
	}
	if strings.TrimSpace(response.CheckoutLink) == "" {
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 1, Data: response.CheckoutLink}, nil
}

func buildStripeSourceCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder, sourceType string) (CheckoutResult, error) {
	currency := strings.ToLower(configValue(cfg, "currency"))
	if currency == "" {
		return CheckoutResult{}, ErrRequestFailed
	}
	exchange, err := exchangeRate(ctx, client, "CNY", strings.ToUpper(currency))
	if err != nil {
		return CheckoutResult{}, err
	}
	gatewayAmount := stripeAmount(order.Total, exchange)

	values := url.Values{}
	values.Set("amount", strconv.FormatInt(gatewayAmount, 10))
	values.Set("currency", currency)
	values.Set("type", sourceType)
	values.Set("statement_descriptor", stripeOrderName(order))
	values.Set("metadata[user_id]", strconv.FormatInt(order.UserID, 10))
	values.Set("metadata[out_trade_no]", order.TradeNo)
	values.Set("metadata[identifier]", "")
	setStripeConfirmationMetadata(values, "metadata", order.Total, gatewayAmount, currency)
	values.Set("redirect[return_url]", order.ReturnURL)

	var response struct {
		Redirect struct {
			URL string `json:"url"`
		} `json:"redirect"`
		Wechat struct {
			QRCodeURL string `json:"qr_code_url"`
		} `json:"wechat"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	headers := stripeRequestHeaders(cfg, "source:"+order.TradeNo, values)
	if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/sources", values, headers, &response); err != nil {
		return CheckoutResult{}, err
	}
	switch sourceType {
	case "alipay":
		if response.Redirect.URL == "" {
			if response.Error.Message != "" {
				return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
			}
			return CheckoutResult{}, ErrRequestFailed
		}
		return CheckoutResult{Type: 1, Data: response.Redirect.URL}, nil
	case "wechat":
		if response.Wechat.QRCodeURL == "" {
			if response.Error.Message != "" {
				return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
			}
			return CheckoutResult{}, ErrRequestFailed
		}
		return CheckoutResult{Type: 0, Data: response.Wechat.QRCodeURL}, nil
	default:
		return CheckoutResult{}, ErrUnsupportedGateway
	}
}

func buildWechatPayNativeCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	values := map[string]string{
		"appid":            configValue(cfg, "app_id"),
		"mch_id":           configValue(cfg, "mch_id"),
		"nonce_str":        strconv.FormatInt(time.Now().UnixNano(), 36),
		"body":             order.TradeNo,
		"out_trade_no":     order.TradeNo,
		"total_fee":        strconv.FormatInt(order.Total, 10),
		"spbill_create_ip": "0.0.0.0",
		"fee_type":         "CNY",
		"notify_url":       order.NotifyURL,
		"trade_type":       "NATIVE",
	}
	values["sign"] = wechatPaySign(values, configValue(cfg, "api_key"))

	raw, err := doRaw(ctx, client, http.MethodPost, "https://api.mch.weixin.qq.com/pay/unifiedorder", strings.NewReader(wechatXML(values)), "application/xml; charset=utf-8", nil)
	if err != nil {
		return CheckoutResult{}, err
	}

	var response struct {
		XMLName    xml.Name `xml:"xml"`
		ReturnCode string   `xml:"return_code"`
		ReturnMsg  string   `xml:"return_msg"`
		ResultCode string   `xml:"result_code"`
		ErrCodeDes string   `xml:"err_code_des"`
		CodeURL    string   `xml:"code_url"`
	}
	if err := xml.Unmarshal(raw, &response); err != nil {
		return CheckoutResult{}, fmt.Errorf("decode wechat unifiedorder response: %w", err)
	}
	if response.ReturnCode != "SUCCESS" || response.ResultCode != "SUCCESS" || strings.TrimSpace(response.CodeURL) == "" {
		message := strings.TrimSpace(response.ErrCodeDes)
		if message == "" {
			message = strings.TrimSpace(response.ReturnMsg)
		}
		if message != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, message)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 0, Data: response.CodeURL}, nil
}

func buildEPayCheckout(cfg map[string]string, order gatewayOrder) CheckoutResult {
	params := map[string]string{
		"money":        formatMoney(order.Total),
		"name":         order.TradeNo,
		"notify_url":   order.NotifyURL,
		"return_url":   order.ReturnURL,
		"out_trade_no": order.TradeNo,
		"pid":          configValue(cfg, "pid"),
	}
	sign := md5Hex(decodedQuery(params) + configValue(cfg, "key"))
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	values.Set("sign", sign)
	values.Set("sign_type", "MD5")
	return CheckoutResult{
		Type: 1,
		Data: strings.TrimRight(configValue(cfg, "url"), "/") + "/submit.php?" + values.Encode(),
	}
}

func buildEPayProCheckout(cfg map[string]string, order gatewayOrder) CheckoutResult {
	params := map[string]string{
		"money":        formatMoney(order.Total),
		"name":         order.TradeNo,
		"notify_url":   order.NotifyURL,
		"return_url":   order.ReturnURL,
		"out_trade_no": order.TradeNo,
		"pid":          configValue(cfg, "pid"),
		"type":         configValue(cfg, "type"),
	}
	sign := md5Hex(decodedQuery(params) + configValue(cfg, "key"))
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	values.Set("sign", sign)
	values.Set("sign_type", "MD5")
	return CheckoutResult{
		Type: 1,
		Data: strings.TrimRight(configValue(cfg, "url"), "/") + "/submit.php?" + values.Encode(),
	}
}

func buildEpusdtCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	params := map[string]any{
		"amount":       roundMoney(order.Total),
		"order_id":     order.TradeNo,
		"redirect_url": order.ReturnURL,
		"notify_url":   order.NotifyURL,
	}
	params["signature"] = epusdtSign(cfg, params)

	var response struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			PaymentURL string `json:"payment_url"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := doJSON(ctx, client, http.MethodPost, strings.TrimRight(configValue(cfg, "epusdt_pay_url"), "/")+"/api/v1/order/create-transaction", params, http.Header{"Idempotency-Key": []string{"forest:" + order.TradeNo}}, &response); err != nil {
		return CheckoutResult{}, err
	}
	if response.StatusCode != 200 || strings.TrimSpace(response.Data.PaymentURL) == "" {
		if response.Message != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Message)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 1, Data: response.Data.PaymentURL}, nil
}

func buildBEasyCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	params := map[string]string{
		"amount":       formatMoney(order.Total),
		"trade_type":   configValue(cfg, "bepusdt_trade_type"),
		"notify_url":   order.NotifyURL,
		"order_id":     order.TradeNo,
		"redirect_url": order.ReturnURL,
	}
	params["signature"] = md5Hex(decodedQuery(params) + configValue(cfg, "bepusdt_apitoken"))

	var response struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			PaymentURL string `json:"payment_url"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := doJSON(ctx, client, http.MethodPost, strings.TrimRight(configValue(cfg, "bepusdt_url"), "/")+"/api/v1/order/create-transaction", params, http.Header{"Idempotency-Key": []string{"forest:" + order.TradeNo}}, &response); err != nil {
		return CheckoutResult{}, err
	}
	if response.StatusCode != 200 || strings.TrimSpace(response.Data.PaymentURL) == "" {
		if response.Message != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Message)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 1, Data: response.Data.PaymentURL}, nil
}

func buildMGateCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	params := map[string]string{
		"out_trade_no": order.TradeNo,
		"total_amount": strconv.FormatInt(order.Total, 10),
		"notify_url":   order.NotifyURL,
		"return_url":   order.ReturnURL,
		"app_id":       configValue(cfg, "mgate_app_id"),
	}
	if sourceCurrency := configValue(cfg, "mgate_source_currency"); sourceCurrency != "" {
		params["source_currency"] = sourceCurrency
	}
	params["sign"] = md5Hex(encodedQuery(params) + configValue(cfg, "mgate_app_secret"))

	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}

	var response struct {
		Data struct {
			PayURL string `json:"pay_url"`
		} `json:"data"`
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	}
	if err := doForm(ctx, client, http.MethodPost, strings.TrimRight(configValue(cfg, "mgate_url"), "/")+"/v1/gateway/fetch", form, http.Header{"Idempotency-Key": []string{"forest:" + order.TradeNo}}, &response); err != nil {
		return CheckoutResult{}, err
	}
	if strings.TrimSpace(response.Data.PayURL) == "" {
		if len(response.Errors) > 0 {
			for _, messages := range response.Errors {
				if len(messages) > 0 {
					return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, messages[0])
				}
			}
		}
		if response.Message != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Message)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 1, Data: response.Data.PayURL}, nil
}

func buildStripeCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	currency := strings.ToLower(configValue(cfg, "currency"))
	if currency == "" {
		return CheckoutResult{}, ErrRequestFailed
	}
	exchange, err := exchangeRate(ctx, client, "CNY", strings.ToUpper(currency))
	if err != nil {
		return CheckoutResult{}, err
	}
	gatewayAmount := stripeAmount(order.Total, exchange)
	values := url.Values{}
	values.Set("success_url", order.ReturnURL)
	values.Set("cancel_url", order.ReturnURL)
	values.Set("client_reference_id", order.TradeNo)
	values.Set("mode", "payment")
	values.Set("invoice_creation[enabled]", "true")
	values.Set("phone_number_collection[enabled]", "true")
	values.Set("line_items[0][price_data][currency]", currency)
	values.Set("line_items[0][price_data][product_data][name]", order.TradeNo)
	values.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(gatewayAmount, 10))
	values.Set("line_items[0][quantity]", "1")
	setStripeConfirmationMetadata(values, "metadata", order.Total, gatewayAmount, currency)
	setStripeConfirmationMetadata(values, "payment_intent_data[metadata]", order.Total, gatewayAmount, currency)

	var response struct {
		URL   string `json:"url"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	headers := stripeRequestHeaders(cfg, "checkout-session:"+order.TradeNo, values)
	if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", values, headers, &response); err != nil {
		return CheckoutResult{}, err
	}
	if response.URL == "" {
		if response.Error.Message != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 1, Data: response.URL}, nil
}

func buildStripeAllCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	currency := strings.ToLower(configValue(cfg, "currency"))
	if currency == "" {
		return CheckoutResult{}, ErrRequestFailed
	}
	exchange, err := exchangeRate(ctx, client, "CNY", strings.ToUpper(currency))
	if err != nil {
		return CheckoutResult{}, err
	}
	gatewayAmount := stripeAmount(order.Total, exchange)
	paymentMethod := configValue(cfg, "payment_method")
	switch paymentMethod {
	case "", "cards":
		values := url.Values{}
		values.Set("success_url", order.ReturnURL)
		values.Set("client_reference_id", order.TradeNo)
		values.Set("payment_method_types[0]", "card")
		values.Set("mode", "payment")
		values.Set("invoice_creation[enabled]", "true")
		values.Set("phone_number_collection[enabled]", "false")
		if order.UserEmail != "" {
			values.Set("customer_email", order.UserEmail)
		}
		values.Set("line_items[0][price_data][currency]", currency)
		values.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(gatewayAmount, 10))
		values.Set("line_items[0][price_data][product_data][name]", stripeOrderName(order))
		values.Set("line_items[0][quantity]", "1")
		setStripeConfirmationMetadata(values, "metadata", order.Total, gatewayAmount, currency)
		setStripeConfirmationMetadata(values, "payment_intent_data[metadata]", order.Total, gatewayAmount, currency)

		var response struct {
			URL   string `json:"url"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		headers := stripeRequestHeaders(cfg, "all-session:"+order.TradeNo, values)
		if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", values, headers, &response); err != nil {
			return CheckoutResult{}, err
		}
		if response.URL == "" {
			if response.Error.Message != "" {
				return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
			}
			return CheckoutResult{}, ErrRequestFailed
		}
		return CheckoutResult{Type: 1, Data: response.URL}, nil
	case "alipay", "wechat_pay":
		methodValues := url.Values{}
		methodValues.Set("type", paymentMethod)
		headers := stripeRequestHeaders(cfg, "all-method:"+order.TradeNo, methodValues)

		var paymentMethodResp struct {
			ID    string `json:"id"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/payment_methods", methodValues, headers, &paymentMethodResp); err != nil {
			return CheckoutResult{}, err
		}
		if paymentMethodResp.ID == "" {
			if paymentMethodResp.Error.Message != "" {
				return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, paymentMethodResp.Error.Message)
			}
			return CheckoutResult{}, ErrRequestFailed
		}

		intentValues := url.Values{}
		intentValues.Set("amount", strconv.FormatInt(gatewayAmount, 10))
		intentValues.Set("currency", currency)
		intentValues.Set("confirm", "true")
		intentValues.Set("payment_method", paymentMethodResp.ID)
		intentValues.Set("automatic_payment_methods[enabled]", "true")
		intentValues.Set("statement_descriptor", stripeOrderName(order))
		intentValues.Set("metadata[user_id]", strconv.FormatInt(order.UserID, 10))
		intentValues.Set("metadata[out_trade_no]", order.TradeNo)
		setStripeConfirmationMetadata(intentValues, "metadata", order.Total, gatewayAmount, currency)
		if order.UserEmail != "" {
			intentValues.Set("metadata[customer_email]", order.UserEmail)
		}
		intentValues.Set("return_url", order.ReturnURL)
		if paymentMethod == "wechat_pay" {
			intentValues.Set("payment_method_options[wechat_pay][client]", "web")
		}

		var intentResp struct {
			NextAction struct {
				AlipayHandleRedirect struct {
					URL string `json:"url"`
				} `json:"alipay_handle_redirect"`
				WechatPayDisplayQRCode struct {
					Data string `json:"data"`
				} `json:"wechat_pay_display_qr_code"`
			} `json:"next_action"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		intentHeaders := stripeRequestHeaders(cfg, "all-intent:"+order.TradeNo, intentValues)
		if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/payment_intents", intentValues, intentHeaders, &intentResp); err != nil {
			return CheckoutResult{}, err
		}
		switch paymentMethod {
		case "alipay":
			if intentResp.NextAction.AlipayHandleRedirect.URL == "" {
				if intentResp.Error.Message != "" {
					return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, intentResp.Error.Message)
				}
				return CheckoutResult{}, ErrRequestFailed
			}
			return CheckoutResult{Type: 1, Data: intentResp.NextAction.AlipayHandleRedirect.URL}, nil
		case "wechat_pay":
			if intentResp.NextAction.WechatPayDisplayQRCode.Data == "" {
				if intentResp.Error.Message != "" {
					return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, intentResp.Error.Message)
				}
				return CheckoutResult{}, ErrRequestFailed
			}
			return CheckoutResult{Type: 0, Data: intentResp.NextAction.WechatPayDisplayQRCode.Data}, nil
		}
	}

	return CheckoutResult{}, ErrUnsupportedGateway
}

func buildStripeCreditCheckout(ctx context.Context, client *http.Client, cfg map[string]string, order gatewayOrder) (CheckoutResult, error) {
	if strings.TrimSpace(order.Token) == "" {
		return CheckoutResult{}, ErrInvalidParameter
	}
	currency := strings.ToLower(configValue(cfg, "currency"))
	if currency == "" {
		return CheckoutResult{}, ErrRequestFailed
	}
	exchange, err := exchangeRate(ctx, client, "CNY", strings.ToUpper(currency))
	if err != nil {
		return CheckoutResult{}, err
	}
	gatewayAmount := stripeAmount(order.Total, exchange)
	values := url.Values{}
	values.Set("amount", strconv.FormatInt(gatewayAmount, 10))
	values.Set("currency", currency)
	values.Set("source", order.Token)
	values.Set("metadata[user_id]", strconv.FormatInt(order.UserID, 10))
	values.Set("metadata[out_trade_no]", order.TradeNo)
	values.Set("metadata[identifier]", "")
	setStripeConfirmationMetadata(values, "metadata", order.Total, gatewayAmount, currency)

	var response struct {
		ID    string `json:"id"`
		Paid  bool   `json:"paid"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	headers := stripeRequestHeaders(cfg, "credit-charge:"+order.TradeNo, values)
	if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/charges", values, headers, &response); err != nil {
		return CheckoutResult{}, err
	}
	if !response.Paid {
		if response.Error.Message != "" {
			return CheckoutResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
		}
		return CheckoutResult{}, ErrRequestFailed
	}
	return CheckoutResult{Type: 2, Data: true}, nil
}

const (
	stripeMetaOrderAmount   = "forest_order_amount"
	stripeMetaGatewayAmount = "forest_gateway_amount"
	stripeMetaCurrency      = "forest_gateway_currency"
)

func setStripeConfirmationMetadata(values url.Values, prefix string, orderAmount, gatewayAmount int64, currency string) {
	values.Set(prefix+"["+stripeMetaOrderAmount+"]", strconv.FormatInt(orderAmount, 10))
	values.Set(prefix+"["+stripeMetaGatewayAmount+"]", strconv.FormatInt(gatewayAmount, 10))
	values.Set(prefix+"["+stripeMetaCurrency+"]", strings.ToLower(strings.TrimSpace(currency)))
}

func stripeRequestHeaders(cfg map[string]string, scope string, values url.Values) http.Header {
	_ = values
	return http.Header{
		"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")},
		// The idempotency identity belongs to the station order, not to mutable
		// request parameters. If a provider accepted the first request but the
		// response or our database commit was lost, a changed token, exchange rate
		// or return URL must fail closed at Stripe instead of becoming a second
		// charge for the same order.
		"Idempotency-Key": []string{"forest:" + scope},
	}
}

func stripeConfirmationAmount(object map[string]any, amountField string) (*int64, error) {
	metadata := nestedMap(object, "metadata")
	if len(metadata) == 0 {
		metadata = nestedMap(object, "source", "metadata")
	}
	orderAmount, orderErr := parsePositiveInteger(nestedString(metadata, stripeMetaOrderAmount))
	expectedGatewayAmount, gatewayErr := parsePositiveInteger(nestedString(metadata, stripeMetaGatewayAmount))
	actualGatewayAmount, actualErr := parsePositiveInteger(nestedString(object, amountField))
	metadataCurrency := strings.ToLower(nestedString(metadata, stripeMetaCurrency))
	actualCurrency := strings.ToLower(nestedString(object, "currency"))
	if orderErr != nil || gatewayErr != nil || actualErr != nil || expectedGatewayAmount != actualGatewayAmount || metadataCurrency == "" || actualCurrency != metadataCurrency {
		return nil, ErrVerifyFailed
	}
	return &orderAmount, nil
}

func verifyEPayNotify(cfg map[string]string, req NotifyRequest, requireType bool) (notifyResult, error) {
	if strings.TrimSpace(req.Params["trade_status"]) != "TRADE_SUCCESS" {
		return notifyResult{}, ErrVerifyFailed
	}
	if !strings.EqualFold(strings.TrimSpace(req.Params["sign_type"]), "MD5") {
		return notifyResult{}, ErrVerifyFailed
	}
	expectedPID := configValue(cfg, "pid")
	key := configValue(cfg, "key")
	if expectedPID == "" || key == "" || strings.TrimSpace(req.Params["pid"]) != expectedPID {
		return notifyResult{}, ErrVerifyFailed
	}
	if requireType {
		expectedType := strings.ToLower(configValue(cfg, "type"))
		if (expectedType != "alipay" && expectedType != "wxpay") || !strings.EqualFold(strings.TrimSpace(req.Params["type"]), expectedType) {
			return notifyResult{}, ErrVerifyFailed
		}
	}
	tradeNo := strings.TrimSpace(req.Params["out_trade_no"])
	callbackNo := strings.TrimSpace(req.Params["trade_no"])
	if tradeNo == "" || callbackNo == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	amount, err := parsePositiveMoneyCents(req.Params["money"])
	if err != nil {
		return notifyResult{}, ErrVerifyFailed
	}

	sign := strings.TrimSpace(req.Params["sign"])
	if sign == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	params := cloneStringMap(req.Params)
	delete(params, "sign")
	delete(params, "sign_type")
	expectedSign := md5Hex(decodedQuery(params) + key)
	if !constantTimeEqualFold(sign, expectedSign) {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:    tradeNo,
		CallbackNo: callbackNo,
		Amount:     &amount,
	}, nil
}

func parsePositiveMoneyCents(raw string) (int64, error) {
	return parsePositiveMoneyCentsWithPrecision(raw, false)
}

// Some providers serialize a two-decimal fiat amount using a fixed-width
// decimal (for example, CoinPayments commonly sends "12.34000000"). Accept
// that representation only when every digit beyond cents is zero. A value
// carrying any real sub-cent precision still fails closed.
func parsePositiveMoneyCentsAllowTrailingZeros(raw string) (int64, error) {
	return parsePositiveMoneyCentsWithPrecision(raw, true)
}

func parsePositiveMoneyCentsWithPrecision(raw string, allowTrailingZeros bool) (int64, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if raw == "" || len(parts) > 2 || parts[0] == "" {
		return 0, ErrVerifyFailed
	}
	for _, char := range parts[0] {
		if char < '0' || char > '9' {
			return 0, ErrVerifyFailed
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, ErrVerifyFailed
	}

	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) < 1 {
			return 0, ErrVerifyFailed
		}
		for _, char := range parts[1] {
			if char < '0' || char > '9' {
				return 0, ErrVerifyFailed
			}
		}
		if len(parts[1]) > 2 {
			if !allowTrailingZeros || strings.Trim(parts[1][2:], "0") != "" {
				return 0, ErrVerifyFailed
			}
			parts[1] = parts[1][:2]
		}
		fraction, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, ErrVerifyFailed
		}
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	if whole > (math.MaxInt64-fraction)/100 {
		return 0, ErrVerifyFailed
	}
	amount := whole*100 + fraction
	if amount <= 0 {
		return 0, ErrVerifyFailed
	}
	return amount, nil
}

func parsePositiveInteger(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ErrVerifyFailed
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, ErrVerifyFailed
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, ErrVerifyFailed
	}
	return value, nil
}

func verifyEpusdtNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if strings.TrimSpace(req.Params["status"]) != "2" {
		return notifyResult{}, ErrVerifyFailed
	}
	if configValue(cfg, "epusdt_pay_apitoken") == "" || !constantTimeEqualFold(req.Params["signature"], epusdtSign(cfg, stringMapToAny(req.Params))) {
		return notifyResult{}, ErrVerifyFailed
	}
	tradeNo := strings.TrimSpace(req.Params["order_id"])
	callbackNo := strings.TrimSpace(req.Params["trade_id"])
	amount, err := parsePositiveMoneyCentsAllowTrailingZeros(req.Params["amount"])
	if tradeNo == "" || callbackNo == "" || err != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:      tradeNo,
		CallbackNo:   callbackNo,
		CustomResult: "ok",
		Amount:       &amount,
	}, nil
}

func verifyBEasyNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	sign := strings.TrimSpace(req.Params["signature"])
	token := configValue(cfg, "bepusdt_apitoken")
	if sign == "" || token == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	params := cloneStringMap(req.Params)
	delete(params, "signature")
	if !constantTimeEqualFold(sign, md5Hex(decodedQuery(params)+token)) {
		return notifyResult{}, ErrVerifyFailed
	}
	if strings.TrimSpace(req.Params["status"]) != "2" {
		return notifyResult{}, ErrVerifyFailed
	}
	tradeNo := strings.TrimSpace(req.Params["order_id"])
	callbackNo := strings.TrimSpace(req.Params["trade_id"])
	amount, err := parsePositiveMoneyCentsAllowTrailingZeros(req.Params["amount"])
	if tradeNo == "" || callbackNo == "" || err != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:      tradeNo,
		CallbackNo:   callbackNo,
		CustomResult: "ok",
		Amount:       &amount,
	}, nil
}

func verifyMGateNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	sign := strings.TrimSpace(req.Params["sign"])
	secret := configValue(cfg, "mgate_app_secret")
	if sign == "" || secret == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	params := cloneStringMap(req.Params)
	delete(params, "sign")
	if !constantTimeEqualFold(sign, md5Hex(encodedQuery(params)+secret)) {
		return notifyResult{}, ErrVerifyFailed
	}
	tradeNo := strings.TrimSpace(req.Params["out_trade_no"])
	callbackNo := strings.TrimSpace(req.Params["trade_no"])
	amount, err := parsePositiveInteger(req.Params["total_amount"])
	if tradeNo == "" || callbackNo == "" || err != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:    tradeNo,
		CallbackNo: callbackNo,
		Amount:     &amount,
	}, nil
}

func verifyCoinPaymentsNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	expectedMerchant := strings.TrimSpace(configValue(cfg, "coinpayments_merchant_id"))
	secret := configValue(cfg, "coinpayments_ipn_secret")
	if expectedMerchant == "" || secret == "" || strings.TrimSpace(req.Params["merchant"]) != expectedMerchant {
		return notifyResult{}, ErrVerifyFailed
	}
	headerSign := strings.TrimSpace(req.Headers.Get("Hmac"))
	if headerSign == "" || len(req.Body) == 0 {
		return notifyResult{}, ErrVerifyFailed
	}
	mac := hmac.New(sha512.New, []byte(secret))
	// CoinPayments defines the IPN HMAC over the exact raw POST body. Rebuilding
	// it from parsed form values can change field ordering, escaping, duplicate
	// keys, or spaces and would reject a legitimate notification.
	_, _ = mac.Write(req.Body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !constantTimeEqualFold(headerSign, expected) {
		return notifyResult{}, ErrVerifyFailed
	}

	status, err := strconv.ParseInt(strings.TrimSpace(req.Params["status"]), 10, 64)
	if err != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	if status >= 100 || status == 2 {
		tradeNo := strings.TrimSpace(req.Params["item_number"])
		callbackNo := strings.TrimSpace(req.Params["txn_id"])
		currency := strings.TrimSpace(req.Params["currency1"])
		expectedCurrency := strings.TrimSpace(configValue(cfg, "coinpayments_currency"))
		amount, amountErr := parsePositiveMoneyCentsAllowTrailingZeros(req.Params["amount1"])
		if tradeNo == "" || callbackNo == "" || expectedCurrency == "" || !strings.EqualFold(currency, expectedCurrency) || amountErr != nil {
			return notifyResult{}, ErrVerifyFailed
		}
		return notifyResult{
			TradeNo:      tradeNo,
			CallbackNo:   callbackNo,
			CustomResult: "IPN OK",
			Amount:       &amount,
		}, nil
	}
	if status < 0 {
		return notifyResult{}, ErrRequestFailed
	}
	return notifyResult{CustomResult: "IPN OK: pending"}, nil
}

func verifyCoinbaseNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	signature := strings.TrimSpace(req.Headers.Get("X-Cc-Webhook-Signature"))
	secret := configValue(cfg, "coinbase_webhook_key")
	if signature == "" || secret == "" || len(req.Body) == 0 {
		return notifyResult{}, ErrVerifyFailed
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(req.Body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !constantTimeEqualFold(signature, expected) {
		return notifyResult{}, ErrVerifyFailed
	}

	var event map[string]any
	if err := decodeJSONPreserveNumbers(req.Body, &event); err != nil {
		return notifyResult{}, fmt.Errorf("%w: decode coinbase event", ErrVerifyFailed)
	}
	eventType := nestedString(event, "event", "type")
	if eventType != "charge:confirmed" && eventType != "charge:resolved" {
		return notifyResult{}, ErrVerifyFailed
	}
	tradeNo := nestedString(event, "event", "data", "metadata", "outTradeNo")
	callbackNo := nestedString(event, "event", "data", "id")
	currency := nestedString(event, "event", "data", "pricing", "local", "currency")
	amount, amountErr := parsePositiveMoneyCentsAllowTrailingZeros(nestedString(event, "event", "data", "pricing", "local", "amount"))
	if tradeNo == "" || callbackNo == "" || !strings.EqualFold(currency, "CNY") || amountErr != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{TradeNo: tradeNo, CallbackNo: callbackNo, Amount: &amount}, nil
}

func verifyBTCPayNotify(ctx context.Context, client *http.Client, cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	signature := strings.TrimSpace(req.Headers.Get("Btcpay-Sig"))
	secret := configValue(cfg, "btcpay_webhook_key")
	if signature == "" || secret == "" || configValue(cfg, "btcpay_url") == "" || configValue(cfg, "btcpay_storeId") == "" || configValue(cfg, "btcpay_api_key") == "" || len(req.Body) == 0 {
		return notifyResult{}, ErrVerifyFailed
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(req.Body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !constantTimeEqualFold(signature, expected) {
		return notifyResult{}, ErrVerifyFailed
	}

	var payload map[string]any
	if err := decodeJSONPreserveNumbers(req.Body, &payload); err != nil {
		return notifyResult{}, fmt.Errorf("%w: decode btcpay payload", ErrVerifyFailed)
	}
	invoiceID := nestedString(payload, "invoiceId")
	if invoiceID == "" {
		invoiceID = strings.TrimSpace(fmt.Sprint(payload["invoiceId"]))
	}
	if invoiceID == "" {
		return notifyResult{}, ErrVerifyFailed
	}

	var detail struct {
		Status   string      `json:"status"`
		Amount   json.Number `json:"amount"`
		Currency string      `json:"currency"`
		Metadata struct {
			OrderID string `json:"orderId"`
		} `json:"metadata"`
	}
	headers := http.Header{"Authorization": []string{"token " + configValue(cfg, "btcpay_api_key")}}
	endpoint := strings.TrimRight(configValue(cfg, "btcpay_url"), "/") + "/api/v1/stores/" + url.PathEscape(configValue(cfg, "btcpay_storeId")) + "/invoices/" + url.PathEscape(invoiceID)
	if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, headers, &detail); err != nil {
		return notifyResult{}, err
	}
	amount, amountErr := parsePositiveMoneyCentsAllowTrailingZeros(detail.Amount.String())
	if strings.TrimSpace(detail.Metadata.OrderID) == "" || !strings.EqualFold(strings.TrimSpace(detail.Status), "Settled") || !strings.EqualFold(strings.TrimSpace(detail.Currency), "CNY") || amountErr != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{TradeNo: detail.Metadata.OrderID, CallbackNo: invoiceID, Amount: &amount}, nil
}

func verifyStripeNotify(ctx context.Context, client *http.Client, gateway string, cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if err := verifyStripeSignature(configValue(cfg, "stripe_webhook_key"), req.Headers.Get("Stripe-Signature"), req.Body); err != nil {
		return notifyResult{}, err
	}
	var event map[string]any
	if err := decodeJSONPreserveNumbers(req.Body, &event); err != nil {
		return notifyResult{}, fmt.Errorf("%w: decode stripe event", ErrVerifyFailed)
	}

	eventType := nestedString(event, "type")
	switch gateway {
	case "StripeCheckout":
		switch eventType {
		case "checkout.session.completed", "checkout.session.async_payment_succeeded":
			object := nestedMap(event, "data", "object")
			if nestedString(object, "payment_status") != "paid" {
				return notifyResult{}, ErrVerifyFailed
			}
			amount, err := stripeConfirmationAmount(object, "amount_total")
			tradeNo := nestedString(object, "client_reference_id")
			callbackNo := nestedString(object, "payment_intent")
			if err != nil || tradeNo == "" || callbackNo == "" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: callbackNo,
				Amount:     amount,
			}, nil
		}
	case "StripeALL":
		switch eventType {
		case "payment_intent.succeeded":
			object := nestedMap(event, "data", "object")
			amount, err := stripeConfirmationAmount(object, "amount_received")
			tradeNo := nestedString(object, "metadata", "out_trade_no")
			callbackNo := nestedString(object, "id")
			if err != nil || tradeNo == "" || callbackNo == "" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: callbackNo,
				Amount:     amount,
			}, nil
		case "checkout.session.completed", "checkout.session.async_payment_succeeded":
			object := nestedMap(event, "data", "object")
			if nestedString(object, "payment_status") != "paid" {
				return notifyResult{}, ErrVerifyFailed
			}
			amount, err := stripeConfirmationAmount(object, "amount_total")
			tradeNo := nestedString(object, "client_reference_id")
			callbackNo := nestedString(object, "payment_intent")
			if err != nil || tradeNo == "" || callbackNo == "" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: callbackNo,
				Amount:     amount,
			}, nil
		}
	case "StripeCredit":
		if eventType == "charge.succeeded" {
			object := nestedMap(event, "data", "object")
			tradeNo := nestedString(object, "metadata", "out_trade_no")
			if tradeNo == "" {
				tradeNo = nestedString(object, "source", "metadata", "out_trade_no")
			}
			callbackNo := nestedString(object, "id")
			amount, err := stripeConfirmationAmount(object, "amount")
			if err != nil || tradeNo == "" || callbackNo == "" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: callbackNo,
				Amount:     amount,
			}, nil
		}
	case "StripeAlipay", "StripeWepay":
		switch eventType {
		case "source.chargeable":
			object := nestedMap(event, "data", "object")
			if _, err := stripeConfirmationAmount(object, "amount"); err != nil {
				return notifyResult{}, err
			}
			sourceID := nestedString(object, "id")
			if sourceID == "" {
				return notifyResult{}, ErrVerifyFailed
			}
			values := url.Values{}
			values.Set("amount", nestedString(object, "amount"))
			values.Set("currency", nestedString(object, "currency"))
			values.Set("source", sourceID)
			if metadata, ok := object["metadata"].(map[string]any); ok {
				for key, value := range metadata {
					values.Set("metadata["+key+"]", strings.TrimSpace(fmt.Sprint(value)))
				}
			}
			headers := stripeRequestHeaders(cfg, "source-charge:"+sourceID, values)

			var response struct {
				ID    string `json:"id"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/charges", values, headers, &response); err != nil {
				return notifyResult{}, err
			}
			if response.ID == "" {
				if response.Error.Message != "" {
					return notifyResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
				}
				return notifyResult{}, ErrRequestFailed
			}
			return notifyResult{CustomResult: "success"}, nil
		case "charge.succeeded":
			object := nestedMap(event, "data", "object")
			tradeNo := nestedString(object, "metadata", "out_trade_no")
			if tradeNo == "" {
				tradeNo = nestedString(object, "source", "metadata", "out_trade_no")
			}
			callbackNo := nestedString(object, "id")
			amount, err := stripeConfirmationAmount(object, "amount")
			if err != nil || tradeNo == "" || callbackNo == "" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: callbackNo,
				Amount:     amount,
			}, nil
		}
	}
	return notifyResult{}, ErrUnsupportedGateway
}

func verifyAlipayF2FNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if strings.TrimSpace(req.Params["trade_status"]) != "TRADE_SUCCESS" {
		return notifyResult{}, ErrVerifyFailed
	}
	if expectedAppID := configValue(cfg, "app_id"); expectedAppID == "" || strings.TrimSpace(req.Params["app_id"]) != expectedAppID {
		return notifyResult{}, ErrVerifyFailed
	}
	signature := strings.TrimSpace(req.Params["sign"])
	if signature == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	ok, err := alipayVerify(req.Params, signature, configValue(cfg, "public_key"))
	if err != nil {
		return notifyResult{}, err
	}
	if !ok {
		return notifyResult{}, ErrVerifyFailed
	}
	tradeNo := strings.TrimSpace(req.Params["out_trade_no"])
	callbackNo := strings.TrimSpace(req.Params["trade_no"])
	amount, amountErr := parsePositiveMoneyCents(req.Params["total_amount"])
	if tradeNo == "" || callbackNo == "" || amountErr != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:    tradeNo,
		CallbackNo: callbackNo,
		Amount:     &amount,
	}, nil
}

func verifyWechatPayNativeNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	values, err := parseSimpleXMLValues(req.Body)
	if err != nil {
		return notifyResult{}, err
	}
	if strings.TrimSpace(values["return_code"]) != "SUCCESS" || strings.TrimSpace(values["result_code"]) != "SUCCESS" {
		return notifyResult{}, ErrVerifyFailed
	}
	apiKey := configValue(cfg, "api_key")
	if apiKey == "" || !constantTimeEqualFold(values["sign"], wechatPaySign(values, apiKey)) {
		return notifyResult{}, ErrVerifyFailed
	}
	if expectedAppID := configValue(cfg, "app_id"); expectedAppID == "" || strings.TrimSpace(values["appid"]) != expectedAppID {
		return notifyResult{}, ErrVerifyFailed
	}
	if expectedMerchantID := configValue(cfg, "mch_id"); expectedMerchantID == "" || strings.TrimSpace(values["mch_id"]) != expectedMerchantID {
		return notifyResult{}, ErrVerifyFailed
	}
	if feeType := strings.TrimSpace(values["fee_type"]); feeType != "" && !strings.EqualFold(feeType, "CNY") {
		return notifyResult{}, ErrVerifyFailed
	}
	tradeNo := strings.TrimSpace(values["out_trade_no"])
	callbackNo := strings.TrimSpace(values["transaction_id"])
	amount, amountErr := parsePositiveInteger(values["total_fee"])
	if tradeNo == "" || callbackNo == "" || amountErr != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:      tradeNo,
		CallbackNo:   callbackNo,
		CustomResult: `<xml><return_code><![CDATA[SUCCESS]]></return_code><return_msg><![CDATA[OK]]></return_msg></xml>`,
		Amount:       &amount,
	}, nil
}

func verifyStripeSignature(secret, header string, body []byte) error {
	secret = strings.TrimSpace(secret)
	header = strings.TrimSpace(header)
	if secret == "" || header == "" || len(body) == 0 {
		return ErrVerifyFailed
	}

	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "t=") {
			timestamp = strings.TrimPrefix(part, "t=")
		}
		if strings.HasPrefix(part, "v1=") {
			signatures = append(signatures, strings.TrimPrefix(part, "v1="))
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return ErrVerifyFailed
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "." + string(body)))
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		decoded, err := hex.DecodeString(signature)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(decoded, expected) == 1 {
			return nil
		}
	}
	return ErrVerifyFailed
}

func constantTimeEqualFold(left, right string) bool {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func stripeOrderName(order gatewayOrder) string {
	suffix := order.TradeNo
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return "user-#" + strconv.FormatInt(order.UserID, 10) + "-" + suffix
}

func stripeAmount(total int64, exchange float64) int64 {
	return int64(math.Floor(float64(total) * exchange))
}

func exchangeRate(ctx context.Context, client *http.Client, from, to string) (float64, error) {
	type exchangeResponse struct {
		Rates map[string]float64 `json:"rates"`
	}

	var response exchangeResponse
	if err := doJSON(ctx, client, http.MethodGet, "https://api.exchangerate-api.com/v4/latest/"+from, nil, nil, &response); err == nil {
		if rate, ok := response.Rates[to]; ok && rate > 0 {
			return rate, nil
		}
	}
	response = exchangeResponse{}
	if err := doJSON(ctx, client, http.MethodGet, "https://api.frankfurter.app/latest?from="+url.QueryEscape(from)+"&to="+url.QueryEscape(to), nil, nil, &response); err != nil {
		return 0, ErrRequestFailed
	}
	rate, ok := response.Rates[to]
	if !ok || rate <= 0 {
		return 0, ErrRequestFailed
	}
	return rate, nil
}

func epusdtSign(cfg map[string]string, params map[string]any) string {
	keys := sortedAnyKeys(params, "signature")
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(params[key]))
		if value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	return md5Hex(strings.Join(parts, "&") + configValue(cfg, "epusdt_pay_apitoken"))
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint string, payload any, headers http.Header, target any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode json request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	copyHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if target == nil {
		return nil
	}
	if len(raw) == 0 {
		return ErrRequestFailed
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response json: %w", err)
	}
	return nil
}

func decodeJSONPreserveNumbers(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func doForm(ctx context.Context, client *http.Client, method, endpoint string, values url.Values, headers http.Header, target any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create form request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	copyHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if target == nil {
		return nil
	}
	if len(raw) == 0 {
		return ErrRequestFailed
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response json: %w", err)
	}
	return nil
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func formatMoney(total int64) string {
	return strconv.FormatFloat(float64(total)/100, 'f', 2, 64)
}

func roundMoney(total int64) float64 {
	return math.Round((float64(total)/100)*100) / 100
}

func decodedQuery(values map[string]string) string {
	query := encodedQuery(values)
	decoded, err := url.QueryUnescape(query)
	if err != nil {
		return query
	}
	return decoded
}

func encodedQuery(values map[string]string) string {
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	return form.Encode()
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func stringMapToAny(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func sortedAnyKeys(values map[string]any, exclude ...string) []string {
	skip := make(map[string]struct{}, len(exclude))
	for _, value := range exclude {
		skip[value] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if _, ok := skip[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func nestedMap(values map[string]any, path ...string) map[string]any {
	current := values
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		current = next
	}
	return current
}

func nestedString(values map[string]any, path ...string) string {
	current := values
	for index, key := range path {
		value, ok := current[key]
		if !ok {
			return ""
		}
		if index == len(path)-1 {
			return strings.TrimSpace(fmt.Sprint(value))
		}
		next, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

func doRaw(ctx context.Context, client *http.Client, method, endpoint string, body io.Reader, contentType string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	copyHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if len(raw) == 0 {
		return nil, ErrRequestFailed
	}
	return raw, nil
}

func rawSortedQuery(values map[string]string, exclude ...string) string {
	skip := make(map[string]struct{}, len(exclude))
	for _, key := range exclude {
		skip[key] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if _, ok := skip[key]; ok {
			continue
		}
		if strings.TrimSpace(value) == "" {
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

func alipayProductName(cfg map[string]string) string {
	if value := strings.TrimSpace(configValue(cfg, "product_name")); value != "" {
		return value
	}
	return "Forest - 订阅"
}

func alipaySign(values map[string]string, privateKey string) (string, error) {
	key, err := parseAlipayPrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(rawSortedQuery(values, "sign", "sign_type")))
	signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign alipay payload: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func alipayVerify(values map[string]string, signature, publicKey string) (bool, error) {
	key, err := parseAlipayPublicKey(publicKey)
	if err != nil {
		return false, err
	}
	rawSignature, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, fmt.Errorf("decode alipay signature: %w", err)
	}
	digest := sha256.Sum256([]byte(rawSortedQuery(values, "sign", "sign_type")))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], rawSignature); err != nil {
		return false, nil
	}
	return true, nil
}

func parseAlipayPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		block, _ = pem.Decode([]byte(wrapPEM("PRIVATE KEY", raw)))
	}
	if block == nil {
		block, _ = pem.Decode([]byte(wrapPEM("RSA PRIVATE KEY", raw)))
	}
	if block == nil {
		return nil, fmt.Errorf("decode alipay private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse alipay private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse alipay private key: unexpected type")
	}
	return key, nil
}

func parseAlipayPublicKey(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		block, _ = pem.Decode([]byte(wrapPEM("PUBLIC KEY", raw)))
	}
	if block == nil {
		return nil, fmt.Errorf("decode alipay public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse alipay public key: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("parse alipay public key: unexpected type")
	}
	return key, nil
}

func wrapPEM(label, raw string) string {
	cleaned := strings.TrimSpace(raw)
	return "-----BEGIN " + label + "-----\n" + cleaned + "\n-----END " + label + "-----"
}

func wechatPaySign(values map[string]string, apiKey string) string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if key == "sign" || strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	parts = append(parts, "key="+apiKey)
	return strings.ToUpper(md5Hex(strings.Join(parts, "&")))
}

func wechatXML(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("<xml>")
	for _, key := range keys {
		builder.WriteString("<")
		builder.WriteString(key)
		builder.WriteString(">")
		builder.WriteString("<![CDATA[")
		builder.WriteString(values[key])
		builder.WriteString("]]>")
		builder.WriteString("</")
		builder.WriteString(key)
		builder.WriteString(">")
	}
	builder.WriteString("</xml>")
	return builder.String()
}

func parseSimpleXMLValues(raw []byte) (map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	values := map[string]string{}
	var current string
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode xml: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local != "xml" {
				current = typed.Name.Local
			}
		case xml.CharData:
			if current != "" {
				values[current] = strings.TrimSpace(string(typed))
			}
		case xml.EndElement:
			if typed.Name.Local == current {
				current = ""
			}
		}
	}
	return values, nil
}
