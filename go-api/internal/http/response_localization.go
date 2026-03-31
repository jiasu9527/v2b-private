package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
)

var exactErrorMessages = map[string]string{
	"guest service unavailable":                                         "访客服务不可用",
	"telegram service unavailable":                                      "Telegram 服务不可用",
	"node service unavailable":                                          "节点服务不可用",
	"passport service unavailable":                                      "认证服务不可用",
	"user service unavailable":                                          "用户服务不可用",
	"payment service unavailable":                                       "支付服务不可用",
	"admin service unavailable":                                         "管理服务不可用",
	"session service unavailable":                                       "会话服务不可用",
	"unauthorized":                                                      "未授权",
	"node_id is invalid":                                                "节点 ID 无效",
	"node_type is invalid":                                              "节点类型无效",
	"invalid parameter":                                                 "参数错误",
	"Invalid parameter":                                                 "参数错误",
	"Incorrect auto renewal format":                                     "自动续费格式错误",
	"Incorrect format of expiration reminder":                           "到期提醒格式错误",
	"Incorrect traffic alert format":                                    "流量提醒格式错误",
	"Password must be greater than 8 digits":                            "密码长度不能少于 8 位",
	"Old password cannot be empty":                                      "旧密码不能为空",
	"New password cannot be empty":                                      "新密码不能为空",
	"The old password is wrong":                                         "旧密码错误",
	"The transfer amount cannot be empty":                               "划转金额不能为空",
	"The transfer amount parameter is wrong":                            "划转金额参数错误",
	"Giftcard cannot be empty":                                          "礼品卡不能为空",
	"Coupon cannot be empty":                                            "优惠券不能为空",
	"Notice not found":                                                  "公告不存在",
	"Ticket does not exist":                                             "工单不存在",
	"Ticket subject cannot be empty":                                    "工单标题不能为空",
	"Ticket level cannot be empty":                                      "工单等级不能为空",
	"Incorrect ticket level format":                                     "工单等级格式错误",
	"Message cannot be empty":                                           "消息不能为空",
	"The withdrawal method cannot be empty":                             "提现方式不能为空",
	"The withdrawal account cannot be empty":                            "提现账户不能为空",
	"Article does not exist":                                            "文章不存在",
	"Invite campaign is disabled":                                       "邀请活动未开启",
	"There is already an active invite campaign task":                   "已有进行中的邀请活动任务",
	"Invite campaign task does not exist":                               "邀请活动任务不存在",
	"Too many requests, please try again later.":                        "请求过于频繁，请稍后再试",
	"Sending frequently, please try again later":                        "发送过于频繁，请稍后再试",
	"Email suffix is not in the Whitelist":                              "邮箱后缀不在白名单中",
	"Gmail alias is not supported":                                      "不支持 Gmail 别名邮箱",
	"This email is registered":                                          "该邮箱已被注册",
	"This email is not registered in the system":                        "该邮箱未在系统中注册",
	"Email verification code has been sent, please request again later": "邮箱验证码已发送，请稍后再试",
	"Email verification code cannot be empty":                           "邮箱验证码不能为空",
	"Incorrect email verification code":                                 "邮箱验证码错误",
	"Email already exists":                                              "邮箱已存在",
	"Invalid invitation code":                                           "邀请码无效",
	"You must use the invitation code to register":                      "必须使用邀请码注册",
	"Registration has closed":                                           "注册已关闭",
	"Incorrect email or password":                                       "邮箱或密码错误",
	"Your account has been suspended":                                   "您的账号已被封禁",
	"Reset failed, Please try again later":                              "重置失败，请稍后再试",
	"Reset failed":                                                      "重置失败",
	"Token error":                                                       "令牌错误",
	"Not Found":                                                         "未找到",
	"Invalid code is incorrect":                                         "验证码错误",
	"Email can not be empty":                                            "邮箱不能为空",
	"Email format is incorrect":                                         "邮箱格式错误",
	"Request failed":                                                    "请求失败",
	"Request failed, please try again later":                            "请求失败，请稍后再试",
	"Unbind telegram failed":                                            "解绑 Telegram 失败",
	"Save failed":                                                       "保存失败",
	"Transfer failed":                                                   "划转失败",
	"Failed to open ticket":                                             "创建工单失败",
	"Renewal is not allowed":                                            "不允许续费",
	"You have not used up your traffic, you cannot renew your subscription": "流量尚未用尽，暂时无法续费",
	"You do not allow to renew the subscription":                            "当前订阅不允许续费",
	"Invalid reset period": "重置周期无效",
	"You do not have enough time to renew your subscription": "当前可续费时间不足",
	"The gift card does not exist":                           "礼品卡不存在",
	"The gift card is not yet valid":                         "礼品卡尚未生效",
	"The gift card has expired":                              "礼品卡已过期",
	"The gift card usage limit has been reached":             "礼品卡已达到使用上限",
	"The gift card has already been used by this user":       "该礼品卡已被当前用户使用",
	"Not suitable gift card type":                            "礼品卡类型不适用",
	"Unknown gift card type":                                 "未知的礼品卡类型",
	"Ticket reply failed":                                    "工单回复失败",
	"There are other unresolved tickets":                     "还有未处理的工单",
	"The ticket is closed and cannot be replied":             "工单已关闭，无法回复",
	"Please wait for the technical enginneer to reply":       "请等待技术人员回复",
	"Close failed":                              "关闭失败",
	"Unsupported withdrawal method":             "不支持的提现方式",
	"user.ticket.withdraw.not_support_withdraw": "当前不支持提现",
	"Insufficient commission balance":           "佣金余额不足",
	"Insufficient balance":                      "余额不足",
	"Payment gateway is unsupported":            "不支持当前支付网关",
	"payment gateway unsupported":               "不支持当前支付网关",
	"Payment method is not available":           "支付方式不可用",
	"failed to create order":                    "创建订单失败",
	"checkout failed":                           "下单失败",
	"order not found":                           "订单不存在",
	"Order does not exist":                      "订单不存在",
	"Order does not exist or has been paid":     "订单不存在或已支付",
	"Subscription plan does not exist":          "订阅计划不存在",
	"Subscription has expired or no active subscription, unable to purchase Data Reset Package": "订阅已过期或当前无有效订阅，无法购买流量重置包",
	"This payment period cannot be purchased, please choose another period":                     "当前支付周期不可购买，请选择其他周期",
	"Current product is sold out":                                                "当前商品已售罄",
	"The maximum number of creations has been reached":                           "已达到最大创建次数",
	"This subscription cannot be renewed, please change to another subscription": "当前订阅不支持续费，请改购其他订阅",
	"This subscription has expired, please change to another subscription":       "当前订阅已过期，请改购其他订阅",
	"This subscription has been sold out, please choose another subscription":    "当前订阅已售罄，请选择其他订阅",
	"Failed to create order, deposit amount must be greater than 0":              "创建订单失败，充值金额必须大于 0",
	"Deposit amount too large, please contact the administrator":                 "充值金额过大，请联系管理员",
	"You have an unpaid or pending order, please try again later or cancel it":   "您有待支付或待处理订单，请稍后再试或先取消",
	"Invalid coupon":                                                         "优惠券无效",
	"This coupon is no longer available":                                     "该优惠券当前不可用",
	"This coupon has not yet started":                                        "该优惠券尚未开始使用",
	"This coupon has expired":                                                "该优惠券已过期",
	"The coupon code cannot be used for this subscription":                   "该优惠券不适用于当前订阅",
	"The coupon code cannot be used for this period":                         "该优惠券不适用于当前周期",
	"The coupon can only be used for the allowed number of times per person": "该优惠券已达到单用户可用次数上限",
	"Coupon failed":                                                          "优惠券校验失败",
	"plan not found":                                                         "订阅计划不存在",
	"order does not exist or has been paid":                                  "订单不存在或已支付",
	"pending order exists":                                                   "存在待支付或待处理订单",
	"deposit amount must be greater than 0":                                  "充值金额必须大于 0",
	"deposit amount too large":                                               "充值金额过大",
	"plan sold out":                                                          "当前订阅已售罄",
	"payment period unavailable":                                             "当前支付周期不可购买",
	"reset package unavailable":                                              "当前无法购买流量重置包",
	"subscription sold out":                                                  "当前订阅已售罄",
	"plan cannot renew":                                                      "当前订阅不支持续费",
	"plan change disabled":                                                   "当前订阅不允许变更",
	"expired subscription must change plan":                                  "订阅已过期，请改购其他订阅",
	"invalid coupon":                                                         "优惠券无效",
	"coupon unavailable":                                                     "优惠券不可用",
	"coupon not started":                                                     "优惠券尚未开始使用",
	"coupon expired":                                                         "优惠券已过期",
	"coupon plan restricted":                                                 "优惠券不适用于当前订阅",
	"coupon period restricted":                                               "优惠券不适用于当前周期",
	"coupon user limit reached":                                              "该优惠券已达到单用户使用上限",
	"coupon failed":                                                          "优惠券校验失败",
	"insufficient balance":                                                   "余额不足",
	"payment method unavailable":                                             "支付方式不可用",
	"cancel pending orders only":                                             "只能取消待支付订单",
	"notice not found":                                                       "公告不存在",
	"invite limit reached":                                                   "已达到邀请码生成上限",
	"client token invalid":                                                   "客户端令牌无效",
	"The user does not exist":                                                "用户不存在",
	"The user does not ":                                                     "用户不存在",
	"token is null":                                                          "令牌不能为空",
	"token is error":                                                         "令牌错误",
	"invalid traffic data":                                                   "流量数据无效",
	"invalid integer":                                                        "整数参数格式错误",
	"invalid integer slice":                                                  "整数数组参数格式错误",
	"invalid url":                                                            "URL 格式错误",
}

var prefixErrorMessages = []struct {
	Prefix string
	Label  string
}{
	{"query invite code: ", "查询邀请码失败"},
	{"query invite campaign: ", "查询邀请活动失败"},
	{"query guest plans: ", "查询访客套餐失败"},
	{"plan columns: ", "读取套餐字段失败"},
	{"scan guest plan: ", "读取访客套餐失败"},
	{"iterate guest plans: ", "遍历访客套餐失败"},
	{"increment invite code pv: ", "增加邀请码访问量失败"},
	{"begin invite campaign transaction: ", "开启邀请活动事务失败"},
	{"expire invite campaign: ", "更新邀请活动过期状态失败"},
	{"complete invite campaign: ", "更新邀请活动完成状态失败"},
	{"insert invite campaign: ", "创建邀请活动失败"},
	{"commit invite campaign: ", "提交邀请活动失败"},
	{"query invite campaign records: ", "查询邀请活动记录失败"},
	{"abandon invite campaign: ", "放弃邀请活动失败"},
	{"query latest invite campaign: ", "查询最新邀请活动失败"},
	{"query invite campaign by id: ", "查询邀请活动失败"},
	{"refresh invite campaign status: ", "刷新邀请活动状态失败"},
	{"insert invite campaign record: ", "写入邀请活动记录失败"},
	{"update invite campaign progress: ", "更新邀请活动进度失败"},
	{"query invite campaign user email: ", "查询邀请活动用户邮箱失败"},
	{"begin transaction: ", "开启事务失败"},
	{"query user by email: ", "查询用户失败"},
	{"query user by id: ", "查询用户失败"},
	{"check email exists: ", "检查邮箱是否存在失败"},
	{"hash password: ", "密码加密失败"},
	{"hash reset password: ", "重置密码加密失败"},
	{"create user: ", "创建用户失败"},
	{"commit register transaction: ", "提交注册事务失败"},
	{"ensure v2_runtime_kv: ", "初始化运行时缓存失败"},
	{"ensure v2_runtime_kv index: ", "初始化运行时缓存索引失败"},
	{"ensure v2_auth_session: ", "初始化会话表失败"},
	{"ensure v2_auth_session index: ", "初始化会话索引失败"},
	{"sign auth token: ", "签发登录令牌失败"},
	{"save auth session: ", "保存登录会话失败"},
	{"check auth session: ", "校验登录会话失败"},
	{"remove auth sessions: ", "移除登录会话失败"},
	{"get runtime kv: ", "读取运行时数据失败"},
	{"set runtime kv: ", "写入运行时数据失败"},
	{"delete runtime kv: ", "删除运行时数据失败"},
	{"lock runtime kv: ", "锁定运行时数据失败"},
	{"upsert runtime kv increment: ", "更新运行时计数失败"},
	{"update runtime kv increment: ", "更新运行时计数失败"},
	{"commit runtime kv increment: ", "提交运行时计数失败"},
	{"query plan: ", "查询订阅计划失败"},
	{"lock invite campaign: ", "锁定邀请活动失败"},
	{"check invite campaign record: ", "检查邀请活动记录失败"},
	{"count query failed: ", "统计数据失败"},
	{"read request body: ", "读取请求体失败"},
	{"decode json: ", "解析 JSON 失败"},
	{"parse form: ", "解析表单失败"},
	{"decode traffic payload: ", "解析流量上报数据失败"},
	{"invalid integer value: ", "整数参数格式错误"},
	{"marshal clash profile: ", "生成 Clash 订阅失败"},
	{"read rule template: ", "读取规则模板失败"},
	{"decode rule template: ", "解析规则模板失败"},
	{"decode deepbwork config template: ", "解析 Deepbwork 配置模板失败"},
	{"decode trojan config template: ", "解析 Trojan 配置模板失败"},
	{"decode alive payload: ", "解析在线状态数据失败"},
	{"list auth sessions: ", "查询登录会话列表失败"},
	{"scan auth session: ", "读取登录会话失败"},
	{"iterate auth sessions: ", "遍历登录会话失败"},
	{"remove auth session: ", "移除登录会话失败"},
	{"find auth identity by session: ", "查询会话身份失败"},
}

func localizeResponsePayload(status int, payload any) any {
	body, ok := payload.(map[string]any)
	if !ok {
		return payload
	}

	isError := status >= http.StatusBadRequest || responseHasLegacyError(body)
	if !isError {
		return payload
	}

	if message, ok := body["message"].(string); ok {
		body["message"] = localizeErrorText(message)
	}
	if msg, ok := body["msg"].(string); ok {
		body["msg"] = localizeErrorText(msg)
	}
	return payload
}

func responseHasLegacyError(body map[string]any) bool {
	rawRet, ok := body["ret"]
	if !ok {
		return false
	}
	switch typed := rawRet.(type) {
	case int:
		return typed == 0
	case int64:
		return typed == 0
	case float64:
		return typed == 0
	case json.Number:
		v, err := typed.Int64()
		return err == nil && v == 0
	case string:
		return strings.TrimSpace(typed) == "0"
	default:
		return false
	}
}

func localizeErrorText(message string) string {
	message = strings.TrimSpace(message)
	if message == "" || containsChinese(message) {
		return message
	}

	if translated, ok := exactErrorMessages[message]; ok {
		return translated
	}

	for _, item := range prefixErrorMessages {
		if !strings.HasPrefix(message, item.Prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(message, item.Prefix))
		if rest == "" {
			return item.Label
		}
		localRest := localizeErrorText(rest)
		if localRest == "" || localRest == genericChineseError {
			return item.Label
		}
		return item.Label + "：" + localRest
	}

	if strings.HasPrefix(message, "The current required minimum withdrawal commission is ") {
		value := strings.TrimSpace(strings.TrimPrefix(message, "The current required minimum withdrawal commission is "))
		return "当前最低可提现佣金为 " + value
	}
	if strings.HasPrefix(message, "Register frequently, please try again after ") && strings.HasSuffix(message, " minute") {
		value := strings.TrimSuffix(strings.TrimPrefix(message, "Register frequently, please try again after "), " minute")
		value = strings.TrimSpace(value)
		return "注册过于频繁，请在 " + value + " 分钟后再试"
	}
	if strings.HasPrefix(message, "There are too many password errors, please try again after ") && strings.HasSuffix(message, " minutes.") {
		value := strings.TrimSuffix(strings.TrimPrefix(message, "There are too many password errors, please try again after "), " minutes.")
		value = strings.TrimSpace(value)
		return "密码错误次数过多，请在 " + value + " 分钟后再试"
	}

	switch {
	case strings.HasSuffix(message, " cannot be empty"):
		subject := strings.TrimSuffix(message, " cannot be empty")
		translated := subject + "不能为空"
		if containsASCIIWord(translated) {
			return genericChineseError
		}
		return translated
	case strings.HasSuffix(message, " does not exist"):
		subject := strings.TrimSuffix(message, " does not exist")
		translated := subject + "不存在"
		if containsASCIIWord(translated) {
			return genericChineseError
		}
		return translated
	case strings.HasSuffix(message, " failed"):
		subject := strings.TrimSuffix(message, " failed")
		translated := subject + "失败"
		if containsASCIIWord(translated) {
			return genericChineseError
		}
		return translated
	case strings.HasSuffix(message, " is invalid"):
		subject := strings.TrimSuffix(message, " is invalid")
		translated := subject + "无效"
		if containsASCIIWord(translated) {
			return genericChineseError
		}
		return translated
	case strings.HasSuffix(message, " is incorrect"):
		subject := strings.TrimSuffix(message, " is incorrect")
		translated := subject + "错误"
		if containsASCIIWord(translated) {
			return genericChineseError
		}
		return translated
	}

	if containsASCIIWord(message) {
		return genericChineseError
	}
	return message
}

const genericChineseError = "系统繁忙，请稍后重试"

func containsChinese(message string) bool {
	for _, r := range message {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func containsASCIIWord(message string) bool {
	for _, r := range message {
		if unicode.IsLetter(r) && r <= unicode.MaxASCII {
			return true
		}
	}
	return false
}
