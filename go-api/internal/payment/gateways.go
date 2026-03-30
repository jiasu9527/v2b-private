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
		return verifyEPayNotify(cfg, req)
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
		"X-CC-Api-Key": []string{configValue(cfg, "coinbase_api_key")},
		"X-CC-Version": []string{"2018-03-22"},
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
		"Authorization": []string{"token " + configValue(cfg, "btcpay_api_key")},
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

	values := url.Values{}
	values.Set("amount", strconv.FormatInt(stripeAmount(order.Total, exchange), 10))
	values.Set("currency", currency)
	values.Set("type", sourceType)
	values.Set("statement_descriptor", stripeOrderName(order))
	values.Set("metadata[user_id]", strconv.FormatInt(order.UserID, 10))
	values.Set("metadata[out_trade_no]", order.TradeNo)
	values.Set("metadata[identifier]", "")
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
	headers := http.Header{"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")}}
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
	if err := doJSON(ctx, client, http.MethodPost, strings.TrimRight(configValue(cfg, "epusdt_pay_url"), "/")+"/api/v1/order/create-transaction", params, nil, &response); err != nil {
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
	if err := doJSON(ctx, client, http.MethodPost, strings.TrimRight(configValue(cfg, "bepusdt_url"), "/")+"/api/v1/order/create-transaction", params, nil, &response); err != nil {
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
	if err := doForm(ctx, client, http.MethodPost, strings.TrimRight(configValue(cfg, "mgate_url"), "/")+"/v1/gateway/fetch", form, nil, &response); err != nil {
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
	values := url.Values{}
	values.Set("success_url", order.ReturnURL)
	values.Set("cancel_url", order.ReturnURL)
	values.Set("client_reference_id", order.TradeNo)
	values.Set("mode", "payment")
	values.Set("invoice_creation[enabled]", "true")
	values.Set("phone_number_collection[enabled]", "true")
	values.Set("line_items[0][price_data][currency]", currency)
	values.Set("line_items[0][price_data][product_data][name]", order.TradeNo)
	values.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(stripeAmount(order.Total, exchange), 10))
	values.Set("line_items[0][quantity]", "1")

	var response struct {
		URL   string `json:"url"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	headers := http.Header{"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")}}
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
		values.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(stripeAmount(order.Total, exchange), 10))
		values.Set("line_items[0][price_data][product_data][name]", stripeOrderName(order))
		values.Set("line_items[0][quantity]", "1")

		var response struct {
			URL   string `json:"url"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		headers := http.Header{"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")}}
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
		headers := http.Header{"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")}}
		methodValues := url.Values{}
		methodValues.Set("type", paymentMethod)

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
		intentValues.Set("amount", strconv.FormatInt(stripeAmount(order.Total, exchange), 10))
		intentValues.Set("currency", currency)
		intentValues.Set("confirm", "true")
		intentValues.Set("payment_method", paymentMethodResp.ID)
		intentValues.Set("automatic_payment_methods[enabled]", "true")
		intentValues.Set("statement_descriptor", stripeOrderName(order))
		intentValues.Set("metadata[user_id]", strconv.FormatInt(order.UserID, 10))
		intentValues.Set("metadata[out_trade_no]", order.TradeNo)
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
		if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/payment_intents", intentValues, headers, &intentResp); err != nil {
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
	values := url.Values{}
	values.Set("amount", strconv.FormatInt(stripeAmount(order.Total, exchange), 10))
	values.Set("currency", currency)
	values.Set("source", order.Token)
	values.Set("metadata[user_id]", strconv.FormatInt(order.UserID, 10))
	values.Set("metadata[out_trade_no]", order.TradeNo)
	values.Set("metadata[identifier]", "")

	var response struct {
		ID    string `json:"id"`
		Paid  bool   `json:"paid"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	headers := http.Header{"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")}}
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

func verifyEPayNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	sign := strings.TrimSpace(req.Params["sign"])
	if sign == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	params := cloneStringMap(req.Params)
	delete(params, "sign")
	delete(params, "sign_type")
	if sign != md5Hex(decodedQuery(params)+configValue(cfg, "key")) {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:    strings.TrimSpace(req.Params["out_trade_no"]),
		CallbackNo: strings.TrimSpace(req.Params["trade_no"]),
	}, nil
}

func verifyEpusdtNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if strings.TrimSpace(req.Params["status"]) != "2" {
		return notifyResult{}, ErrVerifyFailed
	}
	if epusdtSign(cfg, stringMapToAny(req.Params)) != strings.TrimSpace(req.Params["signature"]) {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:      strings.TrimSpace(req.Params["order_id"]),
		CallbackNo:   strings.TrimSpace(req.Params["trade_id"]),
		CustomResult: "ok",
	}, nil
}

func verifyBEasyNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	sign := strings.TrimSpace(req.Params["signature"])
	if sign == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	params := cloneStringMap(req.Params)
	delete(params, "signature")
	if sign != md5Hex(decodedQuery(params)+configValue(cfg, "bepusdt_apitoken")) {
		return notifyResult{}, ErrVerifyFailed
	}
	if strings.TrimSpace(req.Params["status"]) != "2" {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:      strings.TrimSpace(req.Params["order_id"]),
		CallbackNo:   strings.TrimSpace(req.Params["trade_id"]),
		CustomResult: "ok",
	}, nil
}

func verifyMGateNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	sign := strings.TrimSpace(req.Params["sign"])
	if sign == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	params := cloneStringMap(req.Params)
	delete(params, "sign")
	if sign != md5Hex(encodedQuery(params)+configValue(cfg, "mgate_app_secret")) {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:    strings.TrimSpace(req.Params["out_trade_no"]),
		CallbackNo: strings.TrimSpace(req.Params["trade_no"]),
	}, nil
}

func verifyCoinPaymentsNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if strings.TrimSpace(req.Params["merchant"]) != strings.TrimSpace(configValue(cfg, "coinpayments_merchant_id")) {
		return notifyResult{}, ErrVerifyFailed
	}
	headerSign := strings.TrimSpace(req.Headers.Get("Hmac"))
	if headerSign == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	mac := hmac.New(sha512.New, []byte(configValue(cfg, "coinpayments_ipn_secret")))
	_, _ = mac.Write([]byte(encodedQuery(req.Params)))
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(headerSign)), []byte(strings.ToLower(expected))) != 1 {
		return notifyResult{}, ErrVerifyFailed
	}

	status, err := strconv.ParseInt(strings.TrimSpace(req.Params["status"]), 10, 64)
	if err != nil {
		return notifyResult{}, ErrVerifyFailed
	}
	if status >= 100 || status == 2 {
		return notifyResult{
			TradeNo:      strings.TrimSpace(req.Params["item_number"]),
			CallbackNo:   strings.TrimSpace(req.Params["txn_id"]),
			CustomResult: "IPN OK",
		}, nil
	}
	if status < 0 {
		return notifyResult{}, ErrRequestFailed
	}
	return notifyResult{CustomResult: "IPN OK: pending"}, nil
}

func verifyCoinbaseNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	signature := strings.TrimSpace(req.Headers.Get("X-Cc-Webhook-Signature"))
	if signature == "" || len(req.Body) == 0 {
		return notifyResult{}, ErrVerifyFailed
	}
	mac := hmac.New(sha256.New, []byte(configValue(cfg, "coinbase_webhook_key")))
	_, _ = mac.Write(req.Body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(strings.ToLower(expected))) != 1 {
		return notifyResult{}, ErrVerifyFailed
	}

	var event map[string]any
	if err := json.Unmarshal(req.Body, &event); err != nil {
		return notifyResult{}, fmt.Errorf("%w: decode coinbase event", ErrVerifyFailed)
	}
	tradeNo := nestedString(event, "event", "data", "metadata", "outTradeNo")
	callbackNo := nestedString(event, "event", "id")
	if tradeNo == "" || callbackNo == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{TradeNo: tradeNo, CallbackNo: callbackNo}, nil
}

func verifyBTCPayNotify(ctx context.Context, client *http.Client, cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	signature := strings.TrimSpace(req.Headers.Get("Btcpay-Sig"))
	if signature == "" || len(req.Body) == 0 {
		return notifyResult{}, ErrVerifyFailed
	}
	mac := hmac.New(sha256.New, []byte(configValue(cfg, "btcpay_webhook_key")))
	_, _ = mac.Write(req.Body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(strings.ToLower(expected))) != 1 {
		return notifyResult{}, ErrVerifyFailed
	}

	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
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
		Metadata struct {
			OrderID string `json:"orderId"`
		} `json:"metadata"`
	}
	headers := http.Header{"Authorization": []string{"token " + configValue(cfg, "btcpay_api_key")}}
	endpoint := strings.TrimRight(configValue(cfg, "btcpay_url"), "/") + "/api/v1/stores/" + url.PathEscape(configValue(cfg, "btcpay_storeId")) + "/invoices/" + url.PathEscape(invoiceID)
	if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, headers, &detail); err != nil {
		return notifyResult{}, err
	}
	if strings.TrimSpace(detail.Metadata.OrderID) == "" {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{TradeNo: detail.Metadata.OrderID, CallbackNo: invoiceID}, nil
}

func verifyStripeNotify(ctx context.Context, client *http.Client, gateway string, cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if err := verifyStripeSignature(configValue(cfg, "stripe_webhook_key"), req.Headers.Get("Stripe-Signature"), req.Body); err != nil {
		return notifyResult{}, err
	}
	var event map[string]any
	if err := json.Unmarshal(req.Body, &event); err != nil {
		return notifyResult{}, fmt.Errorf("%w: decode stripe event", ErrVerifyFailed)
	}

	eventType := nestedString(event, "type")
	switch gateway {
	case "StripeCheckout":
		switch eventType {
		case "checkout.session.completed", "checkout.session.async_payment_succeeded":
			object := nestedMap(event, "data", "object")
			if eventType == "checkout.session.completed" && nestedString(object, "payment_status") != "paid" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    nestedString(object, "client_reference_id"),
				CallbackNo: nestedString(object, "payment_intent"),
			}, nil
		}
	case "StripeALL":
		switch eventType {
		case "payment_intent.succeeded":
			object := nestedMap(event, "data", "object")
			return notifyResult{
				TradeNo:    nestedString(object, "metadata", "out_trade_no"),
				CallbackNo: nestedString(object, "id"),
			}, nil
		case "checkout.session.completed", "checkout.session.async_payment_succeeded":
			object := nestedMap(event, "data", "object")
			if eventType == "checkout.session.completed" && nestedString(object, "payment_status") != "paid" {
				return notifyResult{}, ErrVerifyFailed
			}
			return notifyResult{
				TradeNo:    nestedString(object, "client_reference_id"),
				CallbackNo: nestedString(object, "payment_intent"),
			}, nil
		}
	case "StripeCredit":
		if eventType == "charge.succeeded" {
			object := nestedMap(event, "data", "object")
			tradeNo := nestedString(object, "metadata", "out_trade_no")
			if tradeNo == "" {
				tradeNo = nestedString(object, "source", "metadata", "out_trade_no")
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: nestedString(object, "id"),
			}, nil
		}
	case "StripeAlipay", "StripeWepay":
		switch eventType {
		case "source.chargeable":
			object := nestedMap(event, "data", "object")
			headers := http.Header{"Authorization": []string{"Bearer " + configValue(cfg, "stripe_sk_live")}}
			values := url.Values{}
			values.Set("amount", nestedString(object, "amount"))
			values.Set("currency", nestedString(object, "currency"))
			values.Set("source", nestedString(object, "id"))
			if metadata, ok := object["metadata"].(map[string]any); ok {
				for key, value := range metadata {
					values.Set("metadata["+key+"]", strings.TrimSpace(fmt.Sprint(value)))
				}
			}

			var response struct {
				ID    string `json:"id"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := doForm(ctx, client, http.MethodPost, "https://api.stripe.com/v1/charges", values, headers, &response); err != nil {
				return notifyResult{}, err
			}
			if response.ID == "" && response.Error.Message != "" {
				return notifyResult{}, fmt.Errorf("%w: %s", ErrRequestFailed, response.Error.Message)
			}
			return notifyResult{CustomResult: "success"}, nil
		case "charge.succeeded":
			object := nestedMap(event, "data", "object")
			tradeNo := nestedString(object, "metadata", "out_trade_no")
			if tradeNo == "" {
				tradeNo = nestedString(object, "source", "metadata", "out_trade_no")
			}
			return notifyResult{
				TradeNo:    tradeNo,
				CallbackNo: nestedString(object, "id"),
			}, nil
		}
	}
	return notifyResult{}, ErrUnsupportedGateway
}

func verifyAlipayF2FNotify(cfg map[string]string, req NotifyRequest) (notifyResult, error) {
	if strings.TrimSpace(req.Params["trade_status"]) != "TRADE_SUCCESS" {
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
	return notifyResult{
		TradeNo:    strings.TrimSpace(req.Params["out_trade_no"]),
		CallbackNo: strings.TrimSpace(req.Params["trade_no"]),
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
	if wechatPaySign(values, configValue(cfg, "api_key")) != strings.ToUpper(strings.TrimSpace(values["sign"])) {
		return notifyResult{}, ErrVerifyFailed
	}
	return notifyResult{
		TradeNo:      strings.TrimSpace(values["out_trade_no"]),
		CallbackNo:   strings.TrimSpace(values["transaction_id"]),
		CustomResult: `<xml><return_code><![CDATA[SUCCESS]]></return_code><return_msg><![CDATA[OK]]></return_msg></xml>`,
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
	return "V2Board - 订阅"
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
