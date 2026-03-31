package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppName                      string
	Addr                         string
	PublicDir                    string
	AdminPath                    string
	PlanChangeEnable             bool
	SurplusEnable                bool
	InviteCommission             int64
	InviteGenLimit               int64
	InviteCampaignEnable         bool
	InviteCampaignRewardAmount   int64
	InviteCampaignExpireHours    int64
	CommissionDistEnabled        bool
	CommissionDistL1             int64
	CommissionDistL2             int64
	CommissionDistL3             int64
	CommissionWithdrawLimit      int64
	CommissionWithdrawMethods    []string
	WithdrawCloseEnable          bool
	TicketStatus                 int64
	CommissionFirstTime          bool
	OrderCancelRecoverTTL        int64
	SubscribeURL                 string
	SubscribePath                string
	ShowInfoToServerEnable       bool
	AllowNewPeriod               bool
	ShowSubscribeMethod          int64
	ShowSubscribeExpire          int64
	ResetTrafficMethod           int64
	ServerToken                  string
	ServerPullInterval           int64
	ServerPushInterval           int64
	ServerNodeReportMinTraffic   int64
	ServerDeviceOnlineMinTraffic int64
	DeviceLimitMode              int64
	ServerLogEnable              bool
	ServerV2RayDomain            string
	ServerV2RayProtocol          string
	WindowsVersion               string
	WindowsDownloadURL           string
	MacOSVersion                 string
	MacOSDownloadURL             string
	AndroidVersion               string
	AndroidDownloadURL           string
	PostgresDSN                  string
	QueueWorkers                 int
	AppKey                       string
	AccessLogEnabled             bool
	SlowRequestLogThreshold      time.Duration
	ReadTimeout                  time.Duration
	WriteTimeout                 time.Duration
	ShutdownTimeout              time.Duration
	TOSURL                       string
	EmailVerify                  bool
	InviteForce                  bool
	InviteNeverExpire            bool
	StopRegister                 bool
	LoginWithMailLink            bool
	EmailWhitelist               []string
	EmailWhitelistEnabled        bool
	EmailGmailLimitEnabled       bool
	Recaptcha                    bool
	RecaptchaKey                 string
	RecaptchaSiteKey             string
	AppDescription               string
	AppURL                       string
	Logo                         string
	RegisterLimitByIP            bool
	RegisterLimitCount           int64
	RegisterLimitExpireMin       int64
	PasswordLimitEnabled         bool
	PasswordLimitCount           int64
	PasswordLimitExpireMin       int64
	TelegramBotEnable            bool
	TelegramBotToken             string
	TelegramDiscussLink          string
	StripePKLive                 string
	Currency                     string
	CurrencySymbol               string
	MailHost                     string
	MailPort                     int64
	MailUsername                 string
	MailPassword                 string
	MailEncryption               string
	MailFromAddress              string
	MailFromName                 string
	TryOutPlanID                 int64
	TryOutHour                   float64
	InviteTryOutPlanID           int64
	InviteTryOutTransferGB       float64
	InviteTryOutHours            float64
}

const (
	defaultAdminJSONPath       = "../config/admin.json"
	defaultLegacyPHPConfigPath = "../config/v2board.php"
)

func Load() Config {
	jsonConfigPath := defaultAdminJSONPath
	legacyPHPConfigPath := defaultLegacyPHPConfigPath
	jsonConfig := loadJSONConfigMap(jsonConfigPath)
	defaultWithdrawMethods := []string{"支付宝", "USDT", "Paypal"}

	return Config{
		AppName:                      getEnv("APP_NAME", loadConfigString(jsonConfig, legacyPHPConfigPath, "app_name", "forest")),
		Addr:                         getEnv("APP_ADDR", ":8080"),
		PublicDir:                    getEnv("PUBLIC_DIR", "../public"),
		AdminPath:                    getEnv("ADMIN_PATH", loadConfigString(jsonConfig, legacyPHPConfigPath, "secure_path", "localadmin")),
		PlanChangeEnable:             getEnvBool("PLAN_CHANGE_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "plan_change_enable", 1) != 0),
		SurplusEnable:                getEnvBool("SURPLUS_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "surplus_enable", 1) != 0),
		InviteCommission:             getEnvInt64("INVITE_COMMISSION", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "invite_commission", 10)),
		InviteGenLimit:               getEnvInt64("INVITE_GEN_LIMIT", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "invite_gen_limit", 5)),
		InviteCampaignEnable:         getEnvBool("INVITE_CAMPAIGN_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "invite_campaign_enable", 1) != 0),
		InviteCampaignRewardAmount:   getEnvInt64("INVITE_CAMPAIGN_REWARD_AMOUNT", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "invite_campaign_reward_amount", 1000)),
		InviteCampaignExpireHours:    getEnvInt64("INVITE_CAMPAIGN_EXPIRE_HOURS", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "invite_campaign_expire_hours", 48)),
		CommissionDistEnabled:        getEnvBool("COMMISSION_DISTRIBUTION_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "commission_distribution_enable", 0) != 0),
		CommissionDistL1:             getEnvInt64("COMMISSION_DISTRIBUTION_L1", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "commission_distribution_l1", 30)),
		CommissionDistL2:             getEnvInt64("COMMISSION_DISTRIBUTION_L2", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "commission_distribution_l2", 10)),
		CommissionDistL3:             getEnvInt64("COMMISSION_DISTRIBUTION_L3", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "commission_distribution_l3", 5)),
		CommissionWithdrawLimit:      getEnvInt64("COMMISSION_WITHDRAW_LIMIT", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "commission_withdraw_limit", 100)),
		CommissionWithdrawMethods:    getEnvList("COMMISSION_WITHDRAW_METHOD", loadConfigStringList(jsonConfig, legacyPHPConfigPath, "commission_withdraw_method", defaultWithdrawMethods)),
		WithdrawCloseEnable:          getEnvBool("WITHDRAW_CLOSE_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "withdraw_close_enable", 0) != 0),
		TicketStatus:                 getEnvInt64("TICKET_STATUS", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "ticket_status", 0)),
		CommissionFirstTime:          getEnvBool("COMMISSION_FIRST_TIME_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "commission_first_time_enable", 1) != 0),
		OrderCancelRecoverTTL:        getEnvInt64("ORDER_CANCEL_RECOVER_TTL", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "order_cancel_recover_ttl", 1800)),
		SubscribeURL:                 getEnv("SUBSCRIBE_URL", loadConfigString(jsonConfig, legacyPHPConfigPath, "subscribe_url", "")),
		SubscribePath:                getEnv("SUBSCRIBE_PATH", loadConfigString(jsonConfig, legacyPHPConfigPath, "subscribe_path", "/api/v1/client/subscribe")),
		ShowInfoToServerEnable:       getEnvBool("SHOW_INFO_TO_SERVER_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "show_info_to_server_enable", 0) != 0),
		AllowNewPeriod:               getEnvBool("ALLOW_NEW_PERIOD", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "allow_new_period", 0) != 0),
		ShowSubscribeMethod:          getEnvInt64("SHOW_SUBSCRIBE_METHOD", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "show_subscribe_method", 0)),
		ShowSubscribeExpire:          getEnvInt64("SHOW_SUBSCRIBE_EXPIRE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "show_subscribe_expire", 5)),
		ResetTrafficMethod:           getEnvInt64("RESET_TRAFFIC_METHOD", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "reset_traffic_method", 0)),
		ServerToken:                  getEnv("SERVER_TOKEN", loadConfigString(jsonConfig, legacyPHPConfigPath, "server_token", "")),
		ServerPullInterval:           getEnvInt64("SERVER_PULL_INTERVAL", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "server_pull_interval", 60)),
		ServerPushInterval:           getEnvInt64("SERVER_PUSH_INTERVAL", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "server_push_interval", 60)),
		ServerNodeReportMinTraffic:   getEnvInt64("SERVER_NODE_REPORT_MIN_TRAFFIC", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "server_node_report_min_traffic", 0)),
		ServerDeviceOnlineMinTraffic: getEnvInt64("SERVER_DEVICE_ONLINE_MIN_TRAFFIC", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "server_device_online_min_traffic", 0)),
		DeviceLimitMode:              getEnvInt64("DEVICE_LIMIT_MODE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "device_limit_mode", 0)),
		ServerLogEnable:              getEnvBool("SERVER_LOG_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "server_log_enable", 0) != 0),
		ServerV2RayDomain:            getEnv("SERVER_V2RAY_DOMAIN", loadConfigString(jsonConfig, legacyPHPConfigPath, "server_v2ray_domain", "")),
		ServerV2RayProtocol:          getEnv("SERVER_V2RAY_PROTOCOL", loadConfigString(jsonConfig, legacyPHPConfigPath, "server_v2ray_protocol", "")),
		WindowsVersion:               getEnv("WINDOWS_VERSION", loadConfigString(jsonConfig, legacyPHPConfigPath, "windows_version", "")),
		WindowsDownloadURL:           getEnv("WINDOWS_DOWNLOAD_URL", loadConfigString(jsonConfig, legacyPHPConfigPath, "windows_download_url", "")),
		MacOSVersion:                 getEnv("MACOS_VERSION", loadConfigString(jsonConfig, legacyPHPConfigPath, "macos_version", "")),
		MacOSDownloadURL:             getEnv("MACOS_DOWNLOAD_URL", loadConfigString(jsonConfig, legacyPHPConfigPath, "macos_download_url", "")),
		AndroidVersion:               getEnv("ANDROID_VERSION", loadConfigString(jsonConfig, legacyPHPConfigPath, "android_version", "")),
		AndroidDownloadURL:           getEnv("ANDROID_DOWNLOAD_URL", loadConfigString(jsonConfig, legacyPHPConfigPath, "android_download_url", "")),
		PostgresDSN:                  os.Getenv("POSTGRES_DSN"),
		QueueWorkers:                 int(getEnvInt64("QUEUE_WORKERS", 4)),
		AppKey:                       os.Getenv("APP_KEY"),
		AccessLogEnabled:             getEnvBool("ACCESS_LOG_ENABLE", false),
		SlowRequestLogThreshold:      getEnvDurationMS("SLOW_REQUEST_LOG_THRESHOLD_MS", 500*time.Millisecond),
		ReadTimeout:                  10 * time.Second,
		WriteTimeout:                 15 * time.Second,
		ShutdownTimeout:              10 * time.Second,
		TOSURL:                       os.Getenv("TOS_URL"),
		EmailVerify:                  getEnvBool("EMAIL_VERIFY", false),
		InviteForce:                  getEnvBool("INVITE_FORCE", false),
		InviteNeverExpire:            getEnvBool("INVITE_NEVER_EXPIRE", false),
		StopRegister:                 getEnvBool("STOP_REGISTER", false),
		LoginWithMailLink:            getEnvBool("LOGIN_WITH_MAIL_LINK_ENABLE", false),
		EmailWhitelist:               getEnvList("EMAIL_WHITELIST_SUFFIX", loadConfigStringList(jsonConfig, legacyPHPConfigPath, "email_whitelist_suffix", nil)),
		EmailWhitelistEnabled:        getEnvBool("EMAIL_WHITELIST_ENABLE", false),
		EmailGmailLimitEnabled:       getEnvBool("EMAIL_GMAIL_LIMIT_ENABLE", false),
		Recaptcha:                    getEnvBool("RECAPTCHA_ENABLE", false),
		RecaptchaKey:                 os.Getenv("RECAPTCHA_KEY"),
		RecaptchaSiteKey:             os.Getenv("RECAPTCHA_SITE_KEY"),
		AppDescription:               getEnv("APP_DESCRIPTION", loadConfigString(jsonConfig, legacyPHPConfigPath, "app_description", "")),
		AppURL:                       getEnv("APP_URL", loadConfigString(jsonConfig, legacyPHPConfigPath, "app_url", "")),
		Logo:                         getEnv("LOGO_URL", loadConfigString(jsonConfig, legacyPHPConfigPath, "logo", "")),
		RegisterLimitByIP:            getEnvBool("REGISTER_LIMIT_BY_IP_ENABLE", false),
		RegisterLimitCount:           getEnvInt64("REGISTER_LIMIT_COUNT", 3),
		RegisterLimitExpireMin:       getEnvInt64("REGISTER_LIMIT_EXPIRE", 60),
		PasswordLimitEnabled:         getEnvBool("PASSWORD_LIMIT_ENABLE", true),
		PasswordLimitCount:           getEnvInt64("PASSWORD_LIMIT_COUNT", 5),
		PasswordLimitExpireMin:       getEnvInt64("PASSWORD_LIMIT_EXPIRE", 60),
		TelegramBotEnable:            getEnvBool("TELEGRAM_BOT_ENABLE", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "telegram_bot_enable", 0) != 0),
		TelegramBotToken:             getEnv("TELEGRAM_BOT_TOKEN", loadConfigString(jsonConfig, legacyPHPConfigPath, "telegram_bot_token", "")),
		TelegramDiscussLink:          getEnv("TELEGRAM_DISCUSS_LINK", loadConfigString(jsonConfig, legacyPHPConfigPath, "telegram_discuss_link", "")),
		StripePKLive:                 getEnv("STRIPE_PK_LIVE", loadConfigString(jsonConfig, legacyPHPConfigPath, "stripe_pk_live", "")),
		Currency:                     getEnv("CURRENCY", loadConfigString(jsonConfig, legacyPHPConfigPath, "currency", "CNY")),
		CurrencySymbol:               getEnv("CURRENCY_SYMBOL", loadConfigString(jsonConfig, legacyPHPConfigPath, "currency_symbol", "¥")),
		MailHost:                     getEnv("EMAIL_HOST", getEnv("MAIL_HOST", "127.0.0.1")),
		MailPort:                     getEnvInt64("EMAIL_PORT", getEnvInt64("MAIL_PORT", 25)),
		MailUsername:                 getEnv("EMAIL_USERNAME", os.Getenv("MAIL_USERNAME")),
		MailPassword:                 getEnv("EMAIL_PASSWORD", os.Getenv("MAIL_PASSWORD")),
		MailEncryption:               getEnv("EMAIL_ENCRYPTION", os.Getenv("MAIL_ENCRYPTION")),
		MailFromAddress:              getEnv("EMAIL_FROM_ADDRESS", getEnv("MAIL_FROM_ADDRESS", "")),
		MailFromName:                 getEnv("EMAIL_FROM_NAME", getEnv("APP_NAME", "V2Board")),
		TryOutPlanID:                 getEnvInt64("TRY_OUT_PLAN_ID", 0),
		TryOutHour:                   getEnvFloat64("TRY_OUT_HOUR", 0),
		InviteTryOutPlanID:           getEnvInt64("INVITE_CAMPAIGN_TRY_OUT_PLAN_ID", loadConfigInt64(jsonConfig, legacyPHPConfigPath, "invite_campaign_try_out_plan_id", 0)),
		InviteTryOutTransferGB:       getEnvFloat64("INVITE_CAMPAIGN_TRY_OUT_TRANSFER_GB", loadConfigFloat64(jsonConfig, legacyPHPConfigPath, "invite_campaign_try_out_transfer_gb", 0)),
		InviteTryOutHours:            getEnvFloat64("INVITE_CAMPAIGN_TRY_OUT_HOURS", loadConfigFloat64(jsonConfig, legacyPHPConfigPath, "invite_campaign_try_out_hours", 0)),
	}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func getEnvList(key string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return cloneStrings(fallback)
	}

	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		result = append(result, item)
	}

	return result
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func getEnvInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}

	return parsed
}

func getEnvFloat64(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}

	return parsed
}

func getEnvDurationMS(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	if parsed <= 0 {
		return 0
	}
	return time.Duration(parsed) * time.Millisecond
}

func loadJSONConfigMap(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	values := map[string]any{}
	if err := decoder.Decode(&values); err != nil {
		return nil
	}
	return values
}

func loadConfigString(values map[string]any, legacyPath, key, fallback string) string {
	raw := strings.TrimSpace(jsonValueString(values[key]))
	if raw != "" {
		return raw
	}
	return loadPHPConfigString(legacyPath, key, fallback)
}

func loadConfigInt64(values map[string]any, legacyPath, key string, fallback int64) int64 {
	raw := strings.TrimSpace(jsonValueString(values[key]))
	if raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return parsed
		}
	}
	return loadPHPConfigInt64(legacyPath, key, fallback)
}

func loadConfigStringList(values map[string]any, legacyPath, key string, fallback []string) []string {
	raw, ok := values[key]
	if ok {
		switch typed := raw.(type) {
		case []any:
			result := make([]string, 0, len(typed))
			for _, item := range typed {
				next := strings.TrimSpace(jsonValueString(item))
				if next != "" {
					result = append(result, next)
				}
			}
			if len(result) > 0 {
				return result
			}
		case []string:
			result := make([]string, 0, len(typed))
			for _, item := range typed {
				item = strings.TrimSpace(item)
				if item != "" {
					result = append(result, item)
				}
			}
			if len(result) > 0 {
				return result
			}
		default:
			next := strings.TrimSpace(jsonValueString(typed))
			if next != "" {
				return []string{next}
			}
		}
	}
	return loadPHPConfigStringList(legacyPath, key, fallback)
}

func loadConfigFloat64(values map[string]any, legacyPath, key string, fallback float64) float64 {
	raw := strings.TrimSpace(jsonValueString(values[key]))
	if raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			return parsed
		}
	}
	return loadPHPConfigFloat64(legacyPath, key, fallback)
}

func jsonValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case bool:
		if typed {
			return "1"
		}
		return "0"
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func loadPHPConfigString(path, key, fallback string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}

	pattern := regexp.MustCompile(`'` + regexp.QuoteMeta(key) + `'\\s*=>\\s*'([^']*)'`)
	matches := pattern.FindStringSubmatch(string(content))
	if len(matches) != 2 {
		return fallback
	}

	value := strings.TrimSpace(matches[1])
	if value == "" {
		return fallback
	}
	return value
}

func loadPHPConfigInt64(path, key string, fallback int64) int64 {
	value := loadPHPConfigString(path, key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func loadPHPConfigFloat64(path, key string, fallback float64) float64 {
	value := loadPHPConfigString(path, key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func loadPHPConfigStringList(path, key string, fallback []string) []string {
	content, err := os.ReadFile(path)
	if err != nil {
		return cloneStrings(fallback)
	}

	pattern := regexp.MustCompile(`(?s)'` + regexp.QuoteMeta(key) + `'\s*=>\s*array\s*\((.*?)\)\s*,`)
	matches := pattern.FindStringSubmatch(string(content))
	if len(matches) != 2 {
		return cloneStrings(fallback)
	}

	itemPattern := regexp.MustCompile(`'([^']*)'`)
	itemMatches := itemPattern.FindAllStringSubmatch(matches[1], -1)
	if len(itemMatches) == 0 {
		return cloneStrings(fallback)
	}

	result := make([]string, 0, len(itemMatches))
	for _, match := range itemMatches {
		value := strings.TrimSpace(match[1])
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	if len(result) == 0 {
		return cloneStrings(fallback)
	}
	return result
}
