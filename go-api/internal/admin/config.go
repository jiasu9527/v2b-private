package admin

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	cfgpkg "forest/go-api/internal/config"
	"forest/go-api/internal/platform/smtpcompat"
)

var (
	configKeyPattern     = regexp.MustCompile(`^'((?:\\.|[^'])*)'\s*=>\s*(.*)$`)
	configArrayItemRegex = regexp.MustCompile(`^\d+\s*=>\s*(.*),$`)
	configBonusPattern   = regexp.MustCompile(`^\d+(\.\d+)?:\d+(\.\d+)?$`)
)

type phpConfigKind int

const (
	phpConfigScalar phpConfigKind = iota
	phpConfigNil
	phpConfigArray
	phpConfigRaw
)

type phpConfigValue struct {
	kind   phpConfigKind
	scalar string
	array  []phpConfigValue
}

type phpConfigFile struct {
	order  []string
	values map[string]phpConfigValue
}

var adminProjectRoot = detectAdminProjectRoot()

func (s *DBService) FetchConfig(_ context.Context, key string) (map[string]any, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return nil, err
	}

	data := map[string]any{
		"ticket": map[string]any{
			"ticket_status": cfg.int64Value("ticket_status", 0),
		},
		"deposit": map[string]any{
			"deposit_bounus": cfg.stringSliceValue("deposit_bounus", []string{}),
		},
		"invite": map[string]any{
			"invite_force":                        cfg.int64Value("invite_force", 0),
			"invite_commission":                   cfg.int64Value("invite_commission", 10),
			"invite_gen_limit":                    cfg.int64Value("invite_gen_limit", 5),
			"invite_never_expire":                 cfg.int64Value("invite_never_expire", 0),
			"invite_campaign_enable":              cfg.int64Value("invite_campaign_enable", 1),
			"invite_campaign_reward_amount":       cfg.int64Value("invite_campaign_reward_amount", 1000),
			"invite_campaign_expire_hours":        cfg.int64Value("invite_campaign_expire_hours", 48),
			"invite_campaign_try_out_plan_id":     cfg.int64Value("invite_campaign_try_out_plan_id", 0),
			"invite_campaign_try_out_transfer_gb": cfg.float64Value("invite_campaign_try_out_transfer_gb", 0),
			"invite_campaign_try_out_hours":       cfg.float64Value("invite_campaign_try_out_hours", 0),
			"commission_first_time_enable":        cfg.int64Value("commission_first_time_enable", 1),
			"commission_auto_check_enable":        cfg.int64Value("commission_auto_check_enable", 1),
			"commission_auto_check_minutes":       cfg.int64Value("commission_auto_check_minutes", 4320),
			"commission_withdraw_limit":           cfg.int64Value("commission_withdraw_limit", 100),
			"commission_withdraw_method":          cfg.stringSliceValue("commission_withdraw_method", []string{"支付宝", "USDT", "Paypal"}),
			"withdraw_close_enable":               cfg.int64Value("withdraw_close_enable", 0),
			"commission_distribution_enable":      cfg.int64Value("commission_distribution_enable", 0),
			"commission_distribution_l1":          cfg.nullableNumericString("commission_distribution_l1"),
			"commission_distribution_l2":          cfg.nullableNumericString("commission_distribution_l2"),
			"commission_distribution_l3":          cfg.nullableNumericString("commission_distribution_l3"),
		},
		"site": map[string]any{
			"logo":                    cfg.nullableStringValue("logo"),
			"force_https":             cfg.int64Value("force_https", 0),
			"stop_register":           cfg.int64Value("stop_register", 0),
			"app_name":                cfg.stringValue("app_name", "Forest"),
			"app_description":         cfg.stringValue("app_description", ""),
			"app_url":                 cfg.nullableStringValue("app_url"),
			"subscribe_url":           cfg.nullableStringValue("subscribe_url"),
			"subscribe_path":          cfg.nullableStringValue("subscribe_path"),
			"subscribe_token_in_path": cfg.int64Value("subscribe_token_in_path", 0),
			"try_out_plan_id":         cfg.int64Value("try_out_plan_id", 0),
			"try_out_hour":            cfg.int64Value("try_out_hour", 1),
			"tos_url":                 cfg.nullableStringValue("tos_url"),
			"currency":                cfg.stringValue("currency", "CNY"),
			"currency_symbol":         cfg.stringValue("currency_symbol", "¥"),
		},
		"subscribe": map[string]any{
			"plan_change_enable":         cfg.int64Value("plan_change_enable", 1),
			"reset_traffic_method":       cfg.int64Value("reset_traffic_method", 0),
			"surplus_enable":             cfg.int64Value("surplus_enable", 1),
			"allow_new_period":           cfg.int64Value("allow_new_period", 0),
			"order_keep_days":            cfg.int64Value("order_keep_days", 0),
			"mail_log_keep_days":         cfg.int64Value("mail_log_keep_days", 0),
			"log_keep_days":              cfg.int64Value("log_keep_days", 0),
			"stat_user_keep_days":        cfg.int64Value("stat_user_keep_days", 0),
			"stat_server_keep_days":      cfg.int64Value("stat_server_keep_days", 0),
			"auth_session_keep_days":     cfg.int64Value("auth_session_keep_days", 0),
			"runtime_kv_keep_days":       cfg.int64Value("runtime_kv_keep_days", 0),
			"failed_jobs_keep_days":      cfg.int64Value("failed_jobs_keep_days", 0),
			"new_order_event_id":         cfg.int64Value("new_order_event_id", 0),
			"renew_order_event_id":       cfg.int64Value("renew_order_event_id", 0),
			"change_order_event_id":      cfg.int64Value("change_order_event_id", 0),
			"show_info_to_server_enable": cfg.int64Value("show_info_to_server_enable", 0),
			"show_subscribe_method":      cfg.int64Value("show_subscribe_method", 0),
			"show_subscribe_expire":      cfg.int64Value("show_subscribe_expire", 5),
		},
		"frontend": map[string]any{
			"frontend_theme":          cfg.stringValue("frontend_theme", "forest"),
			"frontend_theme_sidebar":  cfg.stringValue("frontend_theme_sidebar", "light"),
			"frontend_theme_header":   cfg.stringValue("frontend_theme_header", "dark"),
			"frontend_theme_color":    cfg.stringValue("frontend_theme_color", "default"),
			"frontend_background_url": cfg.nullableStringValue("frontend_background_url"),
		},
		"server": map[string]any{
			"server_api_url":                   cfg.nullableStringValue("server_api_url"),
			"server_token":                     cfg.nullableStringValue("server_token"),
			"server_pull_interval":             cfg.int64Value("server_pull_interval", 60),
			"server_push_interval":             cfg.int64Value("server_push_interval", 60),
			"server_node_report_min_traffic":   cfg.int64Value("server_node_report_min_traffic", 0),
			"server_device_online_min_traffic": cfg.int64Value("server_device_online_min_traffic", 0),
			"device_limit_mode":                cfg.int64Value("device_limit_mode", 0),
			"server_cf_api_token":              cfg.nullableStringValue("server_cf_api_token"),
			"server_cf_record_type":            cfg.stringValue("server_cf_record_type", "A"),
			"server_cf_ttl":                    cfg.int64Value("server_cf_ttl", 1),
			"server_cf_proxied":                cfg.int64Value("server_cf_proxied", 0),
			"server_ddns_interval":             cfg.int64Value("server_ddns_interval", 1),
			"server_block_check_url":           cfg.stringValue("server_block_check_url", "https://www.baidu.com/"),
			"server_block_check_keyword":       cfg.nullableStringValue("server_block_check_keyword"),
			"server_block_check_timeout":       cfg.int64Value("server_block_check_timeout", 10),
			"server_block_check_threshold":     cfg.int64Value("server_block_check_threshold", 3),
			"server_change_ip_wait":            cfg.int64Value("server_change_ip_wait", 60),
			"server_change_ip_cooldown":        cfg.int64Value("server_change_ip_cooldown", 1800),
		},
		"email": map[string]any{
			"email_template":      cfg.stringValue("email_template", "default"),
			"email_host":          cfg.nullableStringValue("email_host"),
			"email_port":          cfg.nullableStringValue("email_port"),
			"email_username":      cfg.nullableStringValue("email_username"),
			"email_password":      cfg.nullableStringValue("email_password"),
			"email_encryption":    cfg.nullableStringValue("email_encryption"),
			"email_from_address":  cfg.nullableStringValue("email_from_address"),
			"email_bulk_interval": cfg.int64Value("email_bulk_interval", 0),
		},
		"telegram": map[string]any{
			"telegram_bot_enable":   cfg.int64Value("telegram_bot_enable", 0),
			"telegram_bot_token":    cfg.nullableStringValue("telegram_bot_token"),
			"telegram_discuss_link": cfg.nullableStringValue("telegram_discuss_link"),
		},
		"app": map[string]any{
			"windows_version":      cfg.nullableStringValue("windows_version"),
			"windows_download_url": cfg.nullableStringValue("windows_download_url"),
			"macos_version":        cfg.nullableStringValue("macos_version"),
			"macos_download_url":   cfg.nullableStringValue("macos_download_url"),
			"android_version":      cfg.nullableStringValue("android_version"),
			"android_download_url": cfg.nullableStringValue("android_download_url"),
		},
		"safe": map[string]any{
			"email_verify":                cfg.int64Value("email_verify", 0),
			"safe_mode_enable":            cfg.int64Value("safe_mode_enable", 0),
			"secure_path":                 cfg.stringValue("secure_path", fallbackAdminPath(s.currentConfig().AdminPath)),
			"email_whitelist_enable":      cfg.int64Value("email_whitelist_enable", 0),
			"email_whitelist_suffix":      cfg.stringSliceValue("email_whitelist_suffix", []string{"gmail.com", "qq.com", "163.com", "yahoo.com", "sina.com", "126.com", "outlook.com", "yeah.net", "foxmail.com"}),
			"email_gmail_limit_enable":    cfg.int64Value("email_gmail_limit_enable", 0),
			"recaptcha_enable":            cfg.int64Value("recaptcha_enable", 0),
			"recaptcha_key":               cfg.nullableStringValue("recaptcha_key"),
			"recaptcha_site_key":          cfg.nullableStringValue("recaptcha_site_key"),
			"register_limit_by_ip_enable": cfg.int64Value("register_limit_by_ip_enable", 0),
			"register_limit_count":        cfg.int64Value("register_limit_count", 3),
			"register_limit_expire":       cfg.int64Value("register_limit_expire", 60),
			"password_limit_enable":       cfg.int64Value("password_limit_enable", 1),
			"password_limit_count":        cfg.int64Value("password_limit_count", 5),
			"password_limit_expire":       cfg.int64Value("password_limit_expire", 60),
		},
	}

	key = strings.TrimSpace(key)
	if key != "" {
		if item, ok := data[key]; ok {
			return map[string]any{key: item}, nil
		}
	}
	return data, nil
}

func (s *DBService) SaveConfig(_ context.Context, values map[string]any) (bool, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return false, err
	}

	newKeys := make([]string, 0)
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || key == "auth_data" {
			continue
		}
		normalized, err := normalizeConfigValue(value)
		if err != nil {
			return false, errors.New("参数错误")
		}
		if err := validateConfigValue(key, normalized); err != nil {
			return false, err
		}
		if _, exists := cfg.values[key]; !exists {
			newKeys = append(newKeys, key)
		}
		cfg.values[key] = normalized
	}
	sort.Strings(newKeys)
	cfg.order = appendMissingConfigKeys(cfg.order, newKeys, cfg.values)

	if err := writeJSONConfigFile(adminConfigPath(), cfg); err != nil {
		return false, errors.New("修改失败")
	}
	if s.runtime != nil {
		s.runtime.Reload()
	}
	return true, nil
}

func (s *DBService) ListThemes(_ context.Context) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s *DBService) GetThemeConfig(_ context.Context, name string) (map[string]any, error) {
	_ = strings.TrimSpace(name)
	return map[string]any{}, nil
}

func (s *DBService) SaveThemeConfig(_ context.Context, name string, values map[string]any) (map[string]any, error) {
	_ = strings.TrimSpace(name)
	return cloneMap(values), nil
}

func (s *DBService) ListEmailTemplates(_ context.Context) ([]string, error) {
	return listTemplateEntries(adminMailTemplatePath())
}

func (s *DBService) ListThemeTemplates(_ context.Context) ([]string, error) {
	return []string{}, nil
}

func (s *DBService) SetTelegramWebhook(_ context.Context, token string) (bool, error) {
	token = strings.TrimSpace(token)
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return false, err
	}
	if token == "" {
		token = cfg.stringValue("telegram_bot_token", "")
	}
	if token == "" {
		return false, errors.New("参数错误")
	}

	appURL := strings.TrimSpace(valueToString(cfg.values["app_url"]))
	if appURL == "" {
		appURL = strings.TrimSpace(s.currentConfig().AppURL)
	}
	if appURL == "" {
		return false, errors.New("站点URL格式不正确，必须携带http(s)://")
	}
	parsedURL, err := url.Parse(appURL)
	if err != nil || parsedURL.Host == "" {
		return false, errors.New("站点URL格式不正确，必须携带http(s)://")
	}
	parsedURL.Scheme = "https"
	baseURL := strings.TrimRight(parsedURL.String(), "/")
	hookURL := baseURL + "/api/v1/guest/telegram/webhook?access_token=" + fmt.Sprintf("%x", md5.Sum([]byte(token)))

	client := &http.Client{Timeout: 10 * time.Second}
	if err := telegramAPICall(client, token, "getMe", nil); err != nil {
		return false, err
	}
	if err := telegramAPICall(client, token, "setWebhook", map[string]string{"url": hookURL}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *DBService) TestSendMail(_ context.Context, email string) (ConfigMailTestLog, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return nil, err
	}

	email = strings.TrimSpace(email)
	mailConfig, err := s.loadBulkMailConfig()
	if err != nil {
		return nil, err
	}
	if mailConfig.from == "" {
		mailConfig.from = email
	}
	if mailConfig.from == "" {
		mailConfig.from = "noreply@example.com"
	}
	appName := strings.TrimSpace(valueToString(cfg.values["app_name"]))
	if appName == "" {
		appName = mailConfig.appName
	}
	subject := appName + "测试邮件"

	log := ConfigMailTestLog{
		"email":         email,
		"subject":       subject,
		"template_name": "mail." + cfg.stringValue("email_template", "default") + ".notify",
		"config": map[string]any{
			"host":       mailConfig.host,
			"port":       mailConfig.port,
			"encryption": mailConfig.encryption,
			"username":   mailConfig.username,
		},
	}

	if mailConfig.host == "" || email == "" {
		log["error"] = "邮件服务未配置"
		return log, nil
	}

	body := renderAdminMailBody(mailConfig, "notify", "This is a Forest test email", map[string]string{
		"name":    mailConfig.appName,
		"url":     mailConfig.appURL,
		"content": "This is a Forest test email",
	})
	if err := sendMail(mailConfig.host, int(mailConfig.port), mailConfig.encryption, mailConfig.username, mailConfig.password, mailConfig.from, mailConfig.fromName, email, subject, body); err != nil {
		log["error"] = err.Error()
	}
	return log, nil
}

func detectAdminProjectRoot() string {
	adminConfig := cfgpkg.ResolveProjectConfigPath("admin.json")
	if strings.TrimSpace(adminConfig) != "" {
		return filepath.Dir(filepath.Dir(adminConfig))
	}
	legacyConfig := cfgpkg.ResolveLegacyPHPConfigPath()
	if strings.TrimSpace(legacyConfig) != "" {
		return filepath.Dir(filepath.Dir(legacyConfig))
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Clean(cwd)
	}
	return "."
}

func adminConfigPath() string {
	return filepath.Join(adminProjectRoot, "config", "admin.json")
}

func legacyAdminConfigPath() string {
	return cfgpkg.ResolveLegacyPHPConfigPathFromRoot(adminProjectRoot)
}

func adminMailTemplatePath() string {
	return filepath.Join(adminProjectRoot, "resources", "views", "mail")
}

func fallbackAdminPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return "localadmin"
	}
	return path
}

func loadPHPConfigFile(path string) (*phpConfigFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &phpConfigFile{values: map[string]phpConfigValue{}}, nil
		}
		return nil, err
	}

	lines := strings.Split(string(raw), "\n")
	result := &phpConfigFile{
		order:  make([]string, 0),
		values: make(map[string]phpConfigValue),
	}

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		matches := configKeyPattern.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}

		key := unescapePHPString(matches[1])
		rest := strings.TrimSpace(matches[2])
		if rest == "" && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next == "array (" {
				rest = next
				i++
			}
		}

		var value phpConfigValue
		if rest == "array (" {
			items := make([]phpConfigValue, 0)
			for i = i + 1; i < len(lines); i++ {
				itemLine := strings.TrimSpace(lines[i])
				if itemLine == ")," || itemLine == ")" {
					break
				}
				itemMatches := configArrayItemRegex.FindStringSubmatch(itemLine)
				if len(itemMatches) != 2 {
					continue
				}
				items = append(items, parsePHPScalar(strings.TrimSpace(itemMatches[1])))
			}
			value = phpConfigValue{kind: phpConfigArray, array: items}
		} else {
			value = parsePHPScalar(strings.TrimSuffix(rest, ","))
		}

		result.order = append(result.order, key)
		result.values[key] = value
	}
	return result, nil
}

func parsePHPScalar(token string) phpConfigValue {
	token = strings.TrimSpace(token)
	switch {
	case token == "NULL":
		return phpConfigValue{kind: phpConfigNil}
	case strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'") && len(token) >= 2:
		return phpConfigValue{kind: phpConfigScalar, scalar: unescapePHPString(token[1 : len(token)-1])}
	default:
		return phpConfigValue{kind: phpConfigRaw, scalar: token}
	}
}

func (f *phpConfigFile) marshal() string {
	var builder strings.Builder
	builder.WriteString("<?php\n return array (\n")
	for _, key := range f.order {
		value, ok := f.values[key]
		if !ok {
			continue
		}
		writePHPConfigEntry(&builder, key, value)
	}
	builder.WriteString(") ;\n")
	return builder.String()
}

func writePHPConfigEntry(builder *strings.Builder, key string, value phpConfigValue) {
	switch value.kind {
	case phpConfigArray:
		builder.WriteString("  '")
		builder.WriteString(escapePHPString(key))
		builder.WriteString("' => \n")
		builder.WriteString("  array (\n")
		for idx, item := range value.array {
			builder.WriteString("    ")
			builder.WriteString(strconv.Itoa(idx))
			builder.WriteString(" => ")
			builder.WriteString(marshalPHPScalar(item))
			builder.WriteString(",\n")
		}
		builder.WriteString("  ),\n")
	default:
		builder.WriteString("  '")
		builder.WriteString(escapePHPString(key))
		builder.WriteString("' => ")
		builder.WriteString(marshalPHPScalar(value))
		builder.WriteString(",\n")
	}
}

func marshalPHPScalar(value phpConfigValue) string {
	switch value.kind {
	case phpConfigNil:
		return "NULL"
	case phpConfigRaw:
		return value.scalar
	default:
		return "'" + escapePHPString(value.scalar) + "'"
	}
}

func appendMissingConfigKeys(order, newKeys []string, values map[string]phpConfigValue) []string {
	seen := make(map[string]struct{}, len(order))
	for _, key := range order {
		seen[key] = struct{}{}
	}
	for _, key := range newKeys {
		if _, ok := seen[key]; ok {
			continue
		}
		if _, exists := values[key]; exists {
			order = append(order, key)
		}
	}
	return order
}

func normalizeConfigValue(value any) (phpConfigValue, error) {
	switch typed := value.(type) {
	case nil:
		return phpConfigValue{kind: phpConfigNil}, nil
	case string:
		return phpConfigValue{kind: phpConfigScalar, scalar: typed}, nil
	case json.Number:
		return phpConfigValue{kind: phpConfigScalar, scalar: typed.String()}, nil
	case float64:
		return phpConfigValue{kind: phpConfigScalar, scalar: strconv.FormatFloat(typed, 'f', -1, 64)}, nil
	case float32:
		return phpConfigValue{kind: phpConfigScalar, scalar: strconv.FormatFloat(float64(typed), 'f', -1, 64)}, nil
	case int:
		return phpConfigValue{kind: phpConfigScalar, scalar: strconv.Itoa(typed)}, nil
	case int64:
		return phpConfigValue{kind: phpConfigScalar, scalar: strconv.FormatInt(typed, 10)}, nil
	case int32:
		return phpConfigValue{kind: phpConfigScalar, scalar: strconv.FormatInt(int64(typed), 10)}, nil
	case bool:
		if typed {
			return phpConfigValue{kind: phpConfigScalar, scalar: "1"}, nil
		}
		return phpConfigValue{kind: phpConfigScalar, scalar: "0"}, nil
	case []string:
		items := make([]phpConfigValue, 0, len(typed))
		for _, item := range typed {
			items = append(items, phpConfigValue{kind: phpConfigScalar, scalar: item})
		}
		return phpConfigValue{kind: phpConfigArray, array: items}, nil
	case []any:
		items := make([]phpConfigValue, 0, len(typed))
		for _, item := range typed {
			next, err := normalizeConfigValue(item)
			if err != nil {
				return phpConfigValue{}, err
			}
			if next.kind == phpConfigArray {
				return phpConfigValue{}, errors.New("nested array")
			}
			items = append(items, next)
		}
		return phpConfigValue{kind: phpConfigArray, array: items}, nil
	default:
		return phpConfigValue{}, fmt.Errorf("unsupported config value %T", value)
	}
}

func validateConfigValue(key string, value phpConfigValue) error {
	switch key {
	case "deposit_bounus":
		if value.kind != phpConfigArray {
			return errors.New("充值奖励格式不正确，必须为充值金额:奖励金额")
		}
		for _, item := range value.array {
			raw := strings.TrimSpace(valueToString(item))
			if raw == "" {
				continue
			}
			if !configBonusPattern.MatchString(raw) {
				return errors.New("充值奖励格式不正确，必须为充值金额:奖励金额")
			}
		}
	case "logo":
		if err := validateOptionalURL(value, "LOGO URL格式不正确，必须携带https(s)://"); err != nil {
			return err
		}
	case "app_url":
		if err := validateOptionalURL(value, "站点URL格式不正确，必须携带http(s)://"); err != nil {
			return err
		}
	case "tos_url":
		if err := validateOptionalURL(value, "服务条款URL格式不正确，必须携带http(s)://"); err != nil {
			return err
		}
	case "telegram_discuss_link":
		if err := validateOptionalURL(value, "Telegram群组地址必须为URL格式，必须携带http(s)://"); err != nil {
			return err
		}
	case "subscribe_path":
		if raw := strings.TrimSpace(valueToString(value)); raw != "" && !strings.HasPrefix(raw, "/") {
			return errors.New("订阅路径必须以/开头")
		}
	case "server_token":
		if raw := strings.TrimSpace(valueToString(value)); raw != "" && len(raw) < 16 {
			return errors.New("通讯密钥长度必须大于16位")
		}
	case "server_cf_record_type":
		raw := strings.ToUpper(strings.TrimSpace(valueToString(value)))
		if raw != "" && raw != "A" && raw != "AAAA" {
			return errors.New("Cloudflare记录类型只支持A或AAAA")
		}
	case "server_block_check_url":
		if err := validateOptionalURL(value, "墙检测URL格式不正确，必须携带http(s)://"); err != nil {
			return err
		}
	case "secure_path":
		raw := strings.TrimSpace(valueToString(value))
		if len(raw) < 8 {
			return errors.New("后台路径长度最小为8位")
		}
		if matched, _ := regexp.MatchString(`^[\w-]*$`, raw); !matched {
			return errors.New("后台路径只能为字母或数字")
		}
	case "email_bulk_interval":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("群发速率限制必须为大于等于0的整数")
		}
	case "commission_auto_check_minutes":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("佣金自动确认时间必须为大于等于0的整数")
		}
	case "order_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("订单保留天数必须为大于等于0的整数")
		}
	case "mail_log_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("邮件日志保留天数必须为大于等于0的整数")
		}
	case "log_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("系统日志保留天数必须为大于等于0的整数")
		}
	case "stat_user_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("用户流量统计保留天数必须为大于等于0的整数")
		}
	case "stat_server_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("节点流量统计保留天数必须为大于等于0的整数")
		}
	case "auth_session_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("登录会话保留天数必须为大于等于0的整数")
		}
	case "runtime_kv_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("运行时缓存保留天数必须为大于等于0的整数")
		}
	case "failed_jobs_keep_days":
		raw := strings.TrimSpace(valueToString(value))
		if raw == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return errors.New("失败任务保留天数必须为大于等于0的整数")
		}
	}
	return nil
}

func validateOptionalURL(value phpConfigValue, message string) error {
	raw := strings.TrimSpace(valueToString(value))
	if raw == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New(message)
	}
	return nil
}

func (f *phpConfigFile) stringValue(key, fallback string) string {
	raw := strings.TrimSpace(valueToString(f.values[key]))
	if raw == "" {
		return fallback
	}
	return raw
}

func (f *phpConfigFile) nullableStringValue(key string) any {
	value, ok := f.values[key]
	if !ok || value.kind == phpConfigNil {
		return nil
	}
	return valueToString(value)
}

func (f *phpConfigFile) int64Value(key string, fallback int64) int64 {
	raw := strings.TrimSpace(valueToString(f.values[key]))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func (f *phpConfigFile) float64Value(key string, fallback float64) float64 {
	raw := strings.TrimSpace(valueToString(f.values[key]))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func (f *phpConfigFile) stringSliceValue(key string, fallback []string) []string {
	value, ok := f.values[key]
	if !ok {
		return append([]string(nil), fallback...)
	}
	switch value.kind {
	case phpConfigArray:
		result := make([]string, 0, len(value.array))
		for _, item := range value.array {
			raw := strings.TrimSpace(valueToString(item))
			if raw != "" {
				result = append(result, raw)
			}
		}
		return result
	case phpConfigScalar, phpConfigRaw:
		raw := strings.TrimSpace(value.scalar)
		if raw == "" {
			return append([]string(nil), fallback...)
		}
		return []string{raw}
	default:
		return append([]string(nil), fallback...)
	}
}

func (f *phpConfigFile) nullableNumericString(key string) any {
	value, ok := f.values[key]
	if !ok || value.kind == phpConfigNil {
		return nil
	}
	raw := strings.TrimSpace(valueToString(value))
	if raw == "" {
		return nil
	}
	if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
		return parsed
	}
	return raw
}

func valueToString(value phpConfigValue) string {
	switch value.kind {
	case phpConfigScalar, phpConfigRaw:
		return value.scalar
	default:
		return ""
	}
}

func escapePHPString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return value
}

func unescapePHPString(value string) string {
	value = strings.ReplaceAll(value, `\'`, `'`)
	value = strings.ReplaceAll(value, `\\`, `\`)
	return value
}

func listTemplateEntries(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func telegramAPICall(client *http.Client, token, method string, params map[string]string) error {
	endpoint := "https://api.telegram.org/bot" + token + "/" + method
	if len(params) > 0 {
		query := url.Values{}
		for key, value := range params {
			query.Set(key, value)
		}
		endpoint += "?" + query.Encode()
	}
	resp, err := client.Get(endpoint)
	if err != nil {
		return errors.New("请求失败")
	}
	defer resp.Body.Close()

	var payload struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return errors.New("请求失败")
	}
	if !payload.OK {
		if strings.TrimSpace(payload.Description) == "" {
			return errors.New("请求失败")
		}
		return errors.New("来自TG的错误：" + payload.Description)
	}
	return nil
}

func sendMail(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
	address := host + ":" + strconv.Itoa(port)
	message := buildSMTPMessage(from, fromName, to, subject, body)

	switch strings.ToLower(strings.TrimSpace(encryption)) {
	case "ssl":
		conn, err := tls.Dial("tcp", address, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		})
		if err != nil {
			return err
		}
		defer conn.Close()

		client, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer client.Quit()

		if username != "" {
			if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
				return err
			}
		}
		if err := client.Mail(from); err != nil {
			return err
		}
		if err := client.Rcpt(to); err != nil {
			return err
		}
		writer, err := client.Data()
		if err != nil {
			return err
		}
		if _, err := writer.Write(message); err != nil {
			return err
		}
		return writer.Close()
	default:
		var auth smtp.Auth
		if username != "" {
			auth = smtpcompat.PlainAuth("", username, password, host, smtpcompat.AllowInsecureAuth(encryption))
		}
		return smtp.SendMail(address, auth, from, []string{to}, message)
	}
}

func buildSMTPMessage(from, fromName, to, subject, body string) []byte {
	return []byte(strings.Join([]string{
		"To: " + to,
		"From: " + buildSMTPHeaderFrom(from, fromName),
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: " + adminSMTPContentType(body),
		"",
		body,
	}, "\r\n"))
}
