package httpapi

import (
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/usermigration"
)

func panelBusinessOperations() []aiops.PanelOperation {
	out := []aiops.PanelOperation{}
	add := func(name, category, desc, method, path string, write, adminOnly bool, p panelSchema, req ...string) {
		out = append(out, po(name, category, desc, method, path, write, adminOnly, poObj(p, req...)))
	}
	addSchema := func(name, category, desc, method, path string, write, adminOnly bool, schema panelSchema) {
		out = append(out, po(name, category, desc, method, path, write, adminOnly, schema))
	}
	add("plans.list", "plans", "列出全部套餐，含价格和隐藏状态", "GET", "/plan/fetch", false, false, panelSchema{})
	add("plans.save", "plans", "新建或完整保存套餐；价格单位分，transfer_enable沿用后台套餐单位GiB；force_update会同步已购用户，先确认影响", "POST", "/plan/save", true, false, panelPlanFields(), "name", "group_id", "transfer_enable")
	add("plans.toggle", "plans", "修改套餐上下架show或续费renew开关", "POST", "/plan/update", true, false, poFields("id:i show:i renew:i"), "id")
	add("plans.delete", "plans", "删除套餐", "POST", "/plan/drop", true, false, panelSchema{"id": poID()}, "id")
	add("plans.sort", "plans", "排序套餐，plan_ids为目标顺序", "POST", "/plan/sort", true, false, poFields("plan_ids:ids"), "plan_ids")
	add("notices.list", "notices", "列出公告", "GET", "/notice/fetch", false, false, panelSchema{})
	for _, action := range []string{"save", "update"} {
		add("notices."+action, "notices", "新建或更新公告，HTML内容按后台规则保存", "POST", "/notice/"+action, true, false, poFields("id:i title content img_url tags:a"), "title", "content")
	}
	for _, action := range []string{"drop", "show"} {
		add("notices."+action, "notices", map[string]string{"drop": "删除公告", "show": "切换公告显示状态"}[action], "POST", "/notice/"+action, true, false, panelSchema{"id": poID()}, "id")
	}
	add("coupons.list", "coupons", "分页查看优惠券", "GET", "/coupon/fetch", false, false, poPage())
	add("coupons.save", "coupons", "创建/更新优惠券；type=1金额分，type=2比例；started_at/ended_at为Unix秒；批量generate_count最多500", "POST", "/coupon/generate", true, false, poFields("id:i generate_count:i name type:i value:i started_at:i ended_at:i limit_use:i limit_use_with_user:i limit_period:a limit_plan_ids:ids code"), "name", "type", "value", "started_at", "ended_at")
	for _, action := range []string{"drop", "show"} {
		add("coupons."+action, "coupons", map[string]string{"drop": "删除优惠券", "show": "切换优惠券状态"}[action], "POST", "/coupon/"+action, true, false, panelSchema{"id": poID()}, "id")
	}
	add("giftcards.list", "giftcards", "分页查询礼品卡", "GET", "/giftcard/fetch", false, false, poPage())
	add("giftcards.save", "giftcards", "创建/更新礼品卡；type范围1至5，type=5需plan_id；批量generate_count最多500，生效时间Unix秒", "POST", "/giftcard/generate", true, false, poFields("id:i generate_count:i name type:i value:i plan_id:i started_at:i ended_at:i limit_use:i code"), "name", "type", "started_at", "ended_at")
	add("giftcards.delete", "giftcards", "删除礼品卡", "POST", "/giftcard/drop", true, false, panelSchema{"id": poID()}, "id")
	add("knowledge.list", "knowledge", "查询知识库或指定id文章", "GET", "/knowledge/fetch", false, false, poFields("id:i"))
	add("knowledge.categories", "knowledge", "查询知识库分类", "GET", "/knowledge/getCategory", false, false, panelSchema{})
	add("knowledge.save", "knowledge", "新增或更新知识库文章", "POST", "/knowledge/save", true, false, poFields("id:i title category language body"), "title", "category", "language", "body")
	for _, action := range []string{"drop", "show"} {
		add("knowledge."+action, "knowledge", map[string]string{"drop": "删除文章", "show": "切换文章显示状态"}[action], "POST", "/knowledge/"+action, true, false, panelSchema{"id": poID()}, "id")
	}
	add("knowledge.sort", "knowledge", "排序文章", "POST", "/knowledge/sort", true, false, poFields("knowledge_ids:ids"), "knowledge_ids")
	add("invites.campaigns", "invites", "查询邀请活动，支持filter组合筛选", "GET", "/invite/campaign/fetch", false, false, poPage())
	add("invites.detail", "invites", "查询邀请活动详情", "POST", "/invite/campaign/detail", false, false, panelSchema{"id": poID()}, "id")
	add("invites.records", "invites", "分页查询邀请活动记录", "GET", "/invite/campaign/records", false, false, poMerge(poSmallPage(), poFields("campaign_id:i")), "campaign_id")
	apple := "/apple-id"
	add("apple.products", "apple_id", "查询独享Apple ID商品、价格、库存和上下架", "GET", apple+"/product/fetch", false, false, panelSchema{})
	add("apple.product_save", "apple_id", "创建或更新独享Apple ID商品；price单位分", "POST", apple+"/product/save", true, false, poFields("id:i name region owned_shadowrocket:b price:i after_sales enabled:b"), "name", "region", "price")
	for _, action := range []string{"show", "drop"} {
		add("apple.product_"+action, "apple_id", map[string]string{"show": "切换Apple ID商品上下架", "drop": "删除Apple ID商品"}[action], "POST", apple+"/product/"+action, true, false, panelSchema{"id": poID()}, "id")
	}
	add("apple.inventory", "apple_id", "分页查询Apple ID库存，状态0可售/1预留/2已售/3停用，敏感凭据不直接回显给模型", "GET", apple+"/inventory/fetch", false, false, poMerge(poSmallPage(), poFields("product_id:i status:i")))
	add("apple.inventory_import", "apple_id", "批量导入Apple ID库存；每行作为一条credential原文，可包含密保；也兼容account/password字段", "POST", apple+"/inventory/import", true, false, panelSchema{"product_id": poID(), "items": poArray(poObj(poFields("credential account password")))}, "product_id", "items")
	add("apple.inventory_disable", "apple_id", "停用指定库存账号", "POST", apple+"/inventory/disable", true, false, panelSchema{"id": poID()}, "id")
	add("apple.orders", "apple_id", "分页查询Apple ID订单，与套餐订单独立", "GET", apple+"/order/fetch", false, false, poMerge(poSmallPage(), poFields("product_id:i status:i email trade_no")))
	add("apple.order_detail", "apple_id", "读取Apple ID订单详情", "GET", apple+"/order/detail", false, false, panelSchema{"id": poID()}, "id")
	add("apple.order_credentials", "apple_id", "读取指定已交付Apple ID订单的完整账号凭据，包括credential/password；仅在明确要求处理该订单账号时使用，普通订单与库存列表仍隐藏凭据", "POST", apple+"/order/credentials", false, false, panelSchema{"id": poID()}, "id")
	add("apple.order_replace", "apple_id", "给指定Apple ID订单换号；inventory_id不传按后台分配可售库存", "POST", apple+"/order/replace", true, false, poFields("id:i inventory_id:i reason"), "id", "reason")
	add("apple.order_refund", "apple_id", "将Apple ID订单标记已退款，沿用后台业务流程，不修改支付通道", "POST", apple+"/order/refund", true, false, poFields("id:i reason"), "id", "reason")
	add("apple.order_cancel", "apple_id", "取消未支付Apple ID订单并释放预留库存", "POST", apple+"/order/cancel", true, false, poFields("id:i reason"), "id", "reason")
	add("apple.order_audits", "apple_id", "分页查询Apple ID订单操作记录", "GET", apple+"/order/audits", false, false, poMerge(poSmallPage(), panelSchema{"id": poID()}), "id")
	for _, action := range []string{"stats", "transactions"} {
		add("apple.finance_"+action, "apple_id", "查询Apple ID财务统计或流水，日期YYYY-MM-DD", "GET", apple+"/finance/"+action, false, false, poMerge(poSmallPage(), poFields("start_date end_date product_id:i")))
	}
	sg := "/subscribe-guard"
	add("subscribe_guard.stats", "subscribe_guard", "查询订阅防护统计与用户排行", "GET", sg+"/stats", false, false, poFields("user_rank_sort"))
	add("subscribe_guard.search", "subscribe_guard", "组合筛选订阅用户：邮箱/ID、UA、当前命中入口规则和拉取时间；按当前规则计算，不是历史快照。start_at/end_at为Unix秒，结束时间不含边界", "GET", sg+"/search", false, false, poMerge(poSmallPage(), panelSchema{"account": poText("邮箱或数字ID"), "account_match": poEnum("账号匹配", "contains", "exact"), "ua": poText("User-Agent"), "ua_match": poEnum("UA匹配", "contains", "equals", "not_contains"), "result": poEnum("请求结果", "all", "allowed", "blocked"), "sort": poEnum("排序", "last_at", "count")}, poFields("policy_id:i group_id:i start_at:i end_at:i")))
	add("subscribe_guard.user_search", "subscribe_guard", "按邮箱或ID搜索订阅用户", "GET", sg+"/user-search", false, false, poFields("keyword"), "keyword")
	add("subscribe_guard.ua_search", "subscribe_guard", "按UA搜索订阅用户", "GET", sg+"/ua-search", false, false, poMerge(poSmallPage(), poFields("keyword")), "keyword")
	add("subscribe_guard.user_detail", "subscribe_guard", "查看指定用户订阅访问详情", "GET", sg+"/user-detail", false, false, panelSchema{"id": poID()}, "id")
	add("subscribe_guard.ban_user", "subscribe_guard", "设置指定用户封禁状态", "POST", sg+"/set-user-banned", true, false, poFields("id:i banned:i"), "id", "banned")
	migration := "/migration/xboard"
	addSchema("migration.inspect", "migration", "只读检查Xboard源数据库用户与套餐，凭据只写不回显", "POST", migration+"/inspect", false, false, poReflect(usermigration.SourceConfig{}))
	addSchema("migration.preview", "migration", "预览用户迁移与套餐映射；确认待解锁佣金策略，建议regenerate_tokens/regenerate_uuids都为true", "POST", migration+"/preview", true, false, poReflect(usermigration.PreviewRequest{}))
	addSchema("migration.apply", "migration", "执行已预览的用户迁移，必须preview_id及confirm=true", "POST", migration+"/apply", true, false, poReflect(usermigration.ApplyRequest{}))
	for _, action := range []string{"receipt", "mail/status"} {
		name := "migration.receipt"
		if action == "mail/status" {
			name = "migration.mail_status"
		}
		add(name, "migration", "查询迁移批次结果或邮件进度", "GET", migration+"/"+action, false, false, poFields("preview_id"), "preview_id")
	}
	addSchema("migration.mail_send", "migration", "给某迁移批次实际新增用户群发邮件，沿用后台发送频率；仅明确要求时执行", "POST", migration+"/mail/send", true, false, poReflect(usermigration.MailRequest{}))
	add("config.read", "config", "读取指定非支付配置分组；必须指定key，默认隐藏凭据。仅明确部署节点需要时，可在key=server时传include_credentials=true取得server_token；其他凭据仍隐藏。工单、注册白名单、订阅防护、邮件等都可管理", "GET", "/config/fetch", false, false, panelSchema{"key": poEnum("配置分组", "ticket", "deposit", "invite", "site", "subscribe", "subscribe_guard", "frontend", "server", "email", "telegram", "app", "safe"), "include_credentials": poBool("默认false；仅key=server且部署节点明确需要时可设true，精确开放server_token，不开放其他凭据")}, "key")
	add("config.save", "config", "保存明确的非支付站点配置；仅声明字段可改，留空凭据会清空而非自动保留；先读取当前分组再修改。支付配置永远不可访问。日志保留期限只允许管理员修改。", "POST", "/config/save", true, false, panelConfigFields())
	add("config.email_templates", "config", "列出可用邮件模板", "GET", "/config/getEmailTemplate", false, false, panelSchema{})
	add("config.telegram_status", "config", "查询Telegram通知状态", "GET", "/config/telegramStatus", false, false, panelSchema{})
	add("config.telegram_webhook", "config", "配置Telegram webhook，token仅写入", "POST", "/config/setTelegramWebhook", true, false, poFields("telegram_bot_token telegram_webhook_url app_url"))
	add("config.telegram_test", "config", "给当前操作人发送Telegram测试消息", "POST", "/config/testTelegram", true, false, panelSchema{})
	add("config.email_test", "config", "给当前操作人发送邮件测试", "POST", "/config/testSendMail", true, false, panelSchema{})
	return out
}

// Explicit fields mirrored from FetchConfig; arbitrary configuration keys are never accepted.
func panelConfigFields() panelSchema {
	return poFields(`ticket_status:i deposit_bounus:a invite_force:i invite_commission:i invite_gen_limit:i invite_never_expire:i
invite_campaign_enable:i invite_campaign_reward_amount:i invite_campaign_expire_hours:i invite_campaign_try_out_plan_id:i invite_campaign_try_out_transfer_gb:n invite_campaign_try_out_hours:n
commission_first_time_enable:i commission_auto_check_enable:i commission_auto_check_minutes:i commission_withdraw_limit:i commission_withdraw_method:a withdraw_close_enable:i
commission_distribution_enable:i commission_distribution_l1 commission_distribution_l2 commission_distribution_l3 logo force_https:i
stop_register:i app_name app_description app_url subscribe_url subscribe_path
subscribe_token_in_path:i try_out_plan_id:i try_out_hour:i tos_url currency currency_symbol
plan_change_enable:i reset_traffic_method:i surplus_enable:i allow_new_period:i order_keep_days:i mail_log_keep_days:i
log_keep_days:i stat_user_keep_days:i stat_server_keep_days:i auth_session_keep_days:i runtime_kv_keep_days:i failed_jobs_keep_days:i
new_order_event_id:i renew_order_event_id:i change_order_event_id:i show_info_to_server_enable:i show_subscribe_method:i show_subscribe_expire:i
subscribe_guard_enable:i subscribe_guard_ip_whitelist:a subscribe_guard_ip_blacklist:a subscribe_guard_ua_whitelist:a subscribe_guard_ua_blacklist:a subscribe_guard_token_blacklist:a
subscribe_guard_block_empty_ua:i subscribe_guard_block_crawler_ua:i subscribe_guard_rate_limit_per_minute:i subscribe_guard_log_keep_days:i subscribe_guard_sensitive_enable:i subscribe_guard_sensitive_rules:a
subscribe_guard_sensitive_interval:i subscribe_guard_sensitive_log_ip:i subscribe_guard_sensitive_log_keep_days:i frontend_theme frontend_theme_sidebar frontend_theme_header
frontend_theme_color frontend_background_url server_api_url server_token server_pull_interval:i server_push_interval:i
server_node_report_min_traffic:i server_device_online_min_traffic:i device_limit_mode:i server_cf_api_token server_cf_record_type server_cf_ttl:i
server_cf_proxied:i server_ddns_interval:i server_block_check_url server_block_check_keyword server_block_check_timeout:i server_block_check_threshold:i
server_change_ip_wait:i server_change_ip_cooldown:i email_template email_host email_port email_username
email_password email_encryption email_from_address email_bulk_interval:i telegram_bot_enable:i telegram_bot_token
telegram_webhook_url telegram_discuss_link windows_version windows_download_url macos_version macos_download_url
android_version android_download_url email_verify:i safe_mode_enable:i secure_path email_whitelist_enable:i
email_whitelist_suffix:a email_gmail_limit_enable:i recaptcha_enable:i recaptcha_key recaptcha_site_key register_limit_by_ip_enable:i
register_limit_count:i register_limit_expire:i password_limit_enable:i password_limit_count:i password_limit_expire:i`)
}

func panelPlanFields() panelSchema {
	p := poFields("id:i name content group_id:i transfer_enable:i device_limit:i show:i renew:i month_price:i quarter_price:i half_year_price:i year_price:i two_year_price:i three_year_price:i onetime_price:i reset_price:i reset_traffic_method:i capacity_limit:i speed_limit:i force_update:b")
	for _, key := range []string{"content", "device_limit", "month_price", "quarter_price", "half_year_price", "year_price", "two_year_price", "three_year_price", "onetime_price", "reset_price", "reset_traffic_method", "capacity_limit", "speed_limit"} {
		p[key] = poNullable(p[key].(panelSchema))
	}
	return p
}
