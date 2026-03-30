package payment

import "sort"

type FormField struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Value       string `json:"value,omitempty"`
}

var gatewayForms = map[string]map[string]FormField{
	"AlipayF2F": {
		"app_id":       {Label: "支付宝APPID", Description: "", Type: "input"},
		"private_key":  {Label: "支付宝私钥", Description: "", Type: "input"},
		"public_key":   {Label: "支付宝公钥", Description: "", Type: "input"},
		"product_name": {Label: "自定义商品名称", Description: "将会体现在支付宝账单中", Type: "input"},
	},
	"BEasyPaymentUSDT": {
		"bepusdt_url":        {Label: "API 地址", Description: "您的 BEPUSDT API 接口地址(例如: https://xxx.com)", Type: "input"},
		"bepusdt_apitoken":   {Label: "API Token", Description: "您的 BEPUSDT API Token", Type: "input"},
		"bepusdt_trade_type": {Label: "交易类型", Description: "您的 BEPUSDT 交易类型", Type: "input"},
	},
	"BTCPay": {
		"btcpay_url":         {Label: "API接口所在网址(包含最后的斜杠)", Description: "", Type: "input"},
		"btcpay_storeId":     {Label: "storeId", Description: "", Type: "input"},
		"btcpay_api_key":     {Label: "API KEY", Description: "个人设置中的API KEY(非商店设置中的)", Type: "input"},
		"btcpay_webhook_key": {Label: "WEBHOOK KEY", Description: "", Type: "input"},
	},
	"CoinPayments": {
		"coinpayments_merchant_id": {Label: "Merchant ID", Description: "商户 ID，填写您在 Account Settings 中得到的 ID", Type: "input"},
		"coinpayments_ipn_secret":  {Label: "IPN Secret", Description: "通知密钥，填写您在 Merchant Settings 中自行设置的值", Type: "input"},
		"coinpayments_currency":    {Label: "货币代码", Description: "填写您的货币代码（大写），建议与 Merchant Settings 中的值相同", Type: "input"},
	},
	"Coinbase": {
		"coinbase_url":         {Label: "接口地址", Description: "", Type: "input"},
		"coinbase_api_key":     {Label: "API KEY", Description: "", Type: "input"},
		"coinbase_webhook_key": {Label: "WEBHOOK KEY", Description: "", Type: "input"},
	},
	"EPay": {
		"url": {Label: "URL", Description: "", Type: "input"},
		"pid": {Label: "PID", Description: "", Type: "input"},
		"key": {Label: "KEY", Description: "", Type: "input"},
	},
	"EpusdtPay": {
		"epusdt_pay_url":      {Label: "API 地址", Description: "您的 EpusdtPay API 接口地址(例如: https://epusdt-pay.xxx.com)", Type: "input"},
		"epusdt_pay_apitoken": {Label: "API Token", Description: "您的 EpusdtPay API Token", Type: "input"},
	},
	"MGate": {
		"mgate_url":             {Label: "API地址", Description: "", Type: "input"},
		"mgate_app_id":          {Label: "APPID", Description: "", Type: "input"},
		"mgate_app_secret":      {Label: "AppSecret", Description: "", Type: "input"},
		"mgate_source_currency": {Label: "源货币", Description: "默认CNY", Type: "input"},
	},
	"StripeALL": {
		"currency":           {Label: "货币单位", Description: "请使用符合ISO 4217标准的三位字母，例如GBP", Type: "input"},
		"stripe_sk_live":     {Label: "SK_LIVE", Description: "", Type: "input"},
		"stripe_webhook_key": {Label: "WebHook密钥签名", Description: "whsec_....", Type: "input"},
		"payment_method":     {Label: "支付方式", Description: "请输入alipay, wechat_pay, cards", Type: "input"},
	},
	"StripeAlipay": {
		"currency":           {Label: "货币单位", Description: "", Type: "input"},
		"stripe_sk_live":     {Label: "SK_LIVE", Description: "", Type: "input"},
		"stripe_webhook_key": {Label: "WebHook密钥签名", Description: "", Type: "input"},
	},
	"StripeCheckout": {
		"currency":                 {Label: "货币单位", Description: "", Type: "input"},
		"stripe_sk_live":           {Label: "SK_LIVE", Description: "API 密钥", Type: "input"},
		"stripe_pk_live":           {Label: "PK_LIVE", Description: "API 公钥", Type: "input"},
		"stripe_webhook_key":       {Label: "WebHook 密钥签名", Description: "", Type: "input"},
		"stripe_custom_field_name": {Label: "自定义字段名称", Description: "例如可设置为“联系方式”，以便及时与客户取得联系", Type: "input"},
	},
	"StripeCredit": {
		"currency":           {Label: "货币单位", Description: "", Type: "input"},
		"stripe_sk_live":     {Label: "SK_LIVE", Description: "", Type: "input"},
		"stripe_pk_live":     {Label: "PK_LIVE", Description: "", Type: "input"},
		"stripe_webhook_key": {Label: "WebHook密钥签名", Description: "", Type: "input"},
	},
	"StripeWepay": {
		"currency":           {Label: "货币单位", Description: "", Type: "input"},
		"stripe_sk_live":     {Label: "SK_LIVE", Description: "", Type: "input"},
		"stripe_webhook_key": {Label: "WebHook密钥签名", Description: "", Type: "input"},
	},
	"WechatPayNative": {
		"app_id":  {Label: "APPID", Description: "绑定微信支付商户的APPID", Type: "input"},
		"mch_id":  {Label: "商户号", Description: "微信支付商户号", Type: "input"},
		"api_key": {Label: "APIKEY(v1)", Description: "", Type: "input"},
	},
}

func SupportedGateways() []string {
	names := make([]string, 0, len(gatewayForms))
	for name := range gatewayForms {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func GatewayForm(name string) (map[string]FormField, bool) {
	form, ok := gatewayForms[name]
	if !ok {
		return nil, false
	}
	cloned := make(map[string]FormField, len(form))
	for key, field := range form {
		cloned[key] = field
	}
	return cloned, true
}
