package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"forest/go-api/internal/aiops"
)

// Deliberately explicit: SaveConfig accepts arbitrary keys, so its normal
// business validation alone is not a suitable AI capability boundary.
var panelConfigSections = map[string]string{
	"ticket":          "ticket_status",
	"deposit":         "deposit_bounus",
	"invite":          "invite_force invite_commission invite_gen_limit invite_never_expire invite_campaign_enable invite_campaign_reward_amount invite_campaign_expire_hours invite_campaign_try_out_plan_id invite_campaign_try_out_transfer_gb invite_campaign_try_out_hours commission_first_time_enable commission_auto_check_enable commission_auto_check_minutes commission_withdraw_limit commission_withdraw_method withdraw_close_enable commission_distribution_enable commission_distribution_l1 commission_distribution_l2 commission_distribution_l3",
	"site":            "logo force_https stop_register app_name app_description app_url subscribe_url subscribe_path subscribe_token_in_path try_out_plan_id try_out_hour tos_url currency currency_symbol",
	"subscribe":       "plan_change_enable reset_traffic_method surplus_enable allow_new_period order_keep_days mail_log_keep_days log_keep_days stat_user_keep_days stat_server_keep_days auth_session_keep_days runtime_kv_keep_days failed_jobs_keep_days new_order_event_id renew_order_event_id change_order_event_id show_info_to_server_enable show_subscribe_method show_subscribe_expire",
	"subscribe_guard": "subscribe_guard_enable subscribe_guard_ip_whitelist subscribe_guard_ip_blacklist subscribe_guard_ua_whitelist subscribe_guard_ua_blacklist subscribe_guard_token_blacklist subscribe_guard_block_empty_ua subscribe_guard_block_crawler_ua subscribe_guard_rate_limit_per_minute subscribe_guard_log_keep_days subscribe_guard_sensitive_enable subscribe_guard_sensitive_rules subscribe_guard_sensitive_interval subscribe_guard_sensitive_log_ip subscribe_guard_sensitive_log_keep_days",
	"frontend":        "frontend_theme frontend_theme_sidebar frontend_theme_header frontend_theme_color frontend_background_url",
	"server":          "server_api_url server_token server_pull_interval server_push_interval server_node_report_min_traffic server_device_online_min_traffic device_limit_mode server_cf_api_token server_cf_record_type server_cf_ttl server_cf_proxied server_ddns_interval server_block_check_url server_block_check_keyword server_block_check_timeout server_block_check_threshold server_change_ip_wait server_change_ip_cooldown",
	"email":           "email_template email_host email_port email_username email_password email_encryption email_from_address email_bulk_interval",
	"telegram":        "telegram_bot_enable telegram_bot_token telegram_webhook_url telegram_discuss_link",
	"app":             "windows_version windows_download_url macos_version macos_download_url android_version android_download_url",
	"safe":            "email_verify safe_mode_enable secure_path email_whitelist_enable email_whitelist_suffix email_gmail_limit_enable recaptcha_enable recaptcha_key recaptcha_site_key register_limit_by_ip_enable register_limit_count register_limit_expire password_limit_enable password_limit_count password_limit_expire",
}

func (p *panelOperations) validateConfig(_ context.Context, actor aiops.PanelOperationActor, path string, params map[string]any) error {
	if path == "/config/fetch" {
		key, ok := params["key"].(string)
		if !ok || panelConfigSections[key] == "" || len(params) > 2 {
			return aiops.ErrValidation
		}
		for name, value := range params {
			if name == "key" {
				continue
			}
			flag, valid := value.(bool)
			if name != "include_credentials" || !valid || flag && key != "server" {
				return aiops.ErrValidation
			}
		}
	}
	if path != "/config/save" {
		return nil
	}
	if len(params) == 0 {
		return aiops.ErrValidation
	}
	allowed := map[string]bool{}
	for _, fields := range panelConfigSections {
		for _, key := range strings.Fields(fields) {
			allowed[key] = true
		}
	}
	for key := range params {
		if !allowed[key] {
			return aiops.ErrForbidden
		}
		// Employees may not erase logs by changing automatic retention either.
		if !actor.IsAdmin && strings.HasSuffix(key, "_keep_days") {
			return aiops.ErrForbidden
		}
	}
	return nil
}

func (p *panelOperations) validateUserScope(ctx context.Context, actor aiops.PanelOperationActor, path string, params map[string]any) error {
	if actor.IsAdmin || (!strings.HasPrefix(path, "/user/") && path != "/subscribe-guard/set-user-banned") {
		return nil
	}
	for _, key := range []string{"is_admin", "is_staff"} {
		if value, ok := params[key]; ok && fmt.Sprint(value) != "0" {
			return aiops.ErrForbidden
		}
	}
	switch path {
	case "/user/update", "/user/setInviteUser", "/user/resetSecret", "/user/delUser", "/subscribe-guard/set-user-banned":
		id, ok := panelPositiveID(params["id"])
		if !ok {
			return aiops.ErrValidation
		}
		if p.state.admin == nil {
			return aiops.ErrUnavailable
		}
		user, err := p.state.admin.GetUserInfoByID(ctx, id)
		if err != nil {
			return aiops.ErrUnavailable
		}
		if user == nil || user["deleted_user"] != nil {
			return aiops.ErrNotFound
		}
		if fmt.Sprint(user["is_admin"]) != "0" || fmt.Sprint(user["is_staff"]) != "0" {
			return aiops.ErrForbidden
		}
	}
	return nil
}

func panelScopeUserFilters(path string, params map[string]any) {
	switch path {
	case "/user/ban", "/user/unban", "/user/allDel", "/user/sendMail", "/user/dumpCSV":
		filters, _ := params["filter"].([]any)
		params["filter"] = append(filters,
			map[string]any{"key": "is_admin", "condition": "=", "value": "0"},
			map[string]any{"key": "is_staff", "condition": "=", "value": "0"},
		)
	case "/user/update":
		params["is_admin"], params["is_staff"] = json.Number("0"), json.Number("0")
	}
}

func filterPanelConfigResponse(raw []byte, section string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, aiops.ErrUnavailable
	}
	var data map[string]map[string]any
	if json.Unmarshal(envelope["data"], &data) != nil || data[section] == nil {
		return nil, aiops.ErrUnavailable
	}
	allowed := map[string]bool{}
	for _, key := range strings.Fields(panelConfigSections[section]) {
		allowed[key] = true
	}
	for key := range data[section] {
		if !allowed[key] {
			delete(data[section], key)
		}
	}
	return json.Marshal(map[string]any{"data": map[string]any{section: data[section]}})
}

func filterPanelSystemLogs(raw []byte) ([]byte, error) {
	var envelope map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&envelope) != nil {
		return nil, aiops.ErrUnavailable
	}
	rows, _ := envelope["data"].([]any)
	for _, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok {
			continue
		}
		uri, _ := row["uri"].(string)
		parsed, err := url.Parse(uri)
		blocked := false
		if err != nil {
			row["uri"] = "[无效URI已隐藏]"
		} else {
			row["uri"] = parsed.Path
			for _, part := range strings.Split(strings.ToLower(parsed.Path), "/") {
				if part == "payment" || part == "ai-ops" {
					blocked = true
				}
			}
		}
		for _, key := range []string{"data", "context"} {
			value, exists := row[key]
			if !exists || value == nil {
				continue
			}
			if blocked {
				row[key] = "[敏感配置日志内容已隐藏]"
				continue
			}
			if text, ok := value.(string); ok {
				var structured any
				decodeErr := json.Unmarshal([]byte(text), &structured)
				_, isObject := structured.(map[string]any)
				_, isArray := structured.([]any)
				if decodeErr == nil && (isObject || isArray) {
					row[key] = scrubPanelValue(structured)
				} else {
					row[key] = "[非结构化日志载荷已隐藏]"
				}
			}
		}
		if blocked {
			row["title"] = "[敏感配置操作日志]"
		}
	}
	return json.Marshal(envelope)
}

// Only task-specific credential reads are exempt. General lists/configuration
// remain scrubbed, and no payment setting can enter these explicit paths.
func restorePanelExplicitCredentials(path, method string, serverToken bool, original, sanitized []byte) ([]byte, error) {
	if !(path == "/dns-failover/probes" && method == "POST") && path != "/apple-id/order/credentials" && !(path == "/config/fetch" && serverToken) {
		return sanitized, nil
	}
	var before, after map[string]any
	if json.Unmarshal(original, &before) != nil || json.Unmarshal(sanitized, &after) != nil {
		return nil, aiops.ErrUnavailable
	}
	source, _ := before["data"].(map[string]any)
	target, _ := after["data"].(map[string]any)
	if source == nil || target == nil {
		return nil, aiops.ErrUnavailable
	}
	fields := []string{}
	switch path {
	case "/dns-failover/probes":
		fields = []string{"secret", "install_command"}
	case "/apple-id/order/credentials":
		fields = []string{"password", "credential"}
	case "/config/fetch":
		source, _ = source["server"].(map[string]any)
		target, _ = target["server"].(map[string]any)
		fields = []string{"server_token"}
	}
	if source == nil || target == nil {
		return nil, aiops.ErrUnavailable
	}
	for _, key := range fields {
		if value, ok := source[key]; ok {
			target[key] = value
		}
	}
	return json.Marshal(after)
}

func filterPanelInstallationResponse(raw []byte, id int64, include bool) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, aiops.ErrUnavailable
	}
	var nodes []map[string]json.RawMessage
	if json.Unmarshal(envelope["data"], &nodes) != nil {
		return nil, aiops.ErrUnavailable
	}
	selected := []map[string]json.RawMessage{}
	for _, node := range nodes {
		var nodeID int64
		var kind string
		if json.Unmarshal(node["id"], &nodeID) != nil || json.Unmarshal(node["type"], &kind) != nil {
			continue
		}
		if nodeID == id && (!include || kind == "v2node") {
			selected = append(selected, node)
		}
	}
	if len(selected) == 0 {
		return nil, &aiops.PanelOperationError{Status: 404}
	}
	if include && len(selected) != 1 {
		return nil, aiops.ErrConflict
	}
	encoded, err := json.Marshal(map[string]any{"data": selected})
	if err != nil {
		return nil, aiops.ErrUnavailable
	}
	sanitized, err := sanitizePanelResponse(encoded, "application/json")
	if err != nil || !include {
		return sanitized, err
	}
	var result struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(sanitized, &result) != nil || len(result.Data) != 1 {
		return nil, aiops.ErrUnavailable
	}
	command, exists := selected[0]["install_command"]
	if !exists || string(command) == "null" || string(command) == `""` {
		return nil, &aiops.PanelOperationError{Status: 404}
	}
	result.Data[0]["install_command"] = command
	return json.Marshal(result)
}
