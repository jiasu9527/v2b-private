package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	MailTemplate                 string
	TryOutPlanID                 int64
	TryOutHour                   float64
	InviteTryOutPlanID           int64
	InviteTryOutTransferGB       float64
	InviteTryOutHours            float64
}

const (
	defaultAdminJSONPath     = "../config/admin.json"
	defaultLegacyPHPFileName = "legacy.php"
)

func Load() Config {
	jsonConfigPath := ResolveProjectConfigPath("admin.json")
	legacyPHPConfigPath := ResolveLegacyPHPConfigPath()
	jsonConfig := loadJSONConfigMap(jsonConfigPath)
	defaultWithdrawMethods := []string{"支付宝", "USDT", "Paypal"}

	managedString := func(envKey, key, fallback string) string {
		return loadManagedString(jsonConfig, legacyPHPConfigPath, envKey, key, fallback)
	}
	managedBool := func(envKey, key string, fallback bool) bool {
		return loadManagedBool(jsonConfig, legacyPHPConfigPath, envKey, key, fallback)
	}
	managedInt64 := func(envKey, key string, fallback int64) int64 {
		return loadManagedInt64(jsonConfig, legacyPHPConfigPath, envKey, key, fallback)
	}
	managedFloat64 := func(envKey, key string, fallback float64) float64 {
		return loadManagedFloat64(jsonConfig, legacyPHPConfigPath, envKey, key, fallback)
	}
	managedList := func(envKey, key string, fallback []string) []string {
		return loadManagedStringList(jsonConfig, legacyPHPConfigPath, envKey, key, fallback)
	}

	cfg := Config{
		AppName:                      managedString("APP_NAME", "app_name", "Forest"),
		Addr:                         getEnv("APP_ADDR", ":8080"),
		PublicDir:                    getEnv("PUBLIC_DIR", "../public"),
		AdminPath:                    managedString("ADMIN_PATH", "secure_path", "localadmin"),
		PlanChangeEnable:             managedBool("PLAN_CHANGE_ENABLE", "plan_change_enable", true),
		SurplusEnable:                managedBool("SURPLUS_ENABLE", "surplus_enable", true),
		InviteCommission:             managedInt64("INVITE_COMMISSION", "invite_commission", 10),
		InviteGenLimit:               managedInt64("INVITE_GEN_LIMIT", "invite_gen_limit", 5),
		InviteCampaignEnable:         managedBool("INVITE_CAMPAIGN_ENABLE", "invite_campaign_enable", true),
		InviteCampaignRewardAmount:   managedInt64("INVITE_CAMPAIGN_REWARD_AMOUNT", "invite_campaign_reward_amount", 1000),
		InviteCampaignExpireHours:    managedInt64("INVITE_CAMPAIGN_EXPIRE_HOURS", "invite_campaign_expire_hours", 48),
		CommissionDistEnabled:        managedBool("COMMISSION_DISTRIBUTION_ENABLE", "commission_distribution_enable", false),
		CommissionDistL1:             managedInt64("COMMISSION_DISTRIBUTION_L1", "commission_distribution_l1", 30),
		CommissionDistL2:             managedInt64("COMMISSION_DISTRIBUTION_L2", "commission_distribution_l2", 10),
		CommissionDistL3:             managedInt64("COMMISSION_DISTRIBUTION_L3", "commission_distribution_l3", 5),
		CommissionWithdrawLimit:      managedInt64("COMMISSION_WITHDRAW_LIMIT", "commission_withdraw_limit", 100),
		CommissionWithdrawMethods:    managedList("COMMISSION_WITHDRAW_METHOD", "commission_withdraw_method", defaultWithdrawMethods),
		WithdrawCloseEnable:          managedBool("WITHDRAW_CLOSE_ENABLE", "withdraw_close_enable", false),
		TicketStatus:                 managedInt64("TICKET_STATUS", "ticket_status", 0),
		CommissionFirstTime:          managedBool("COMMISSION_FIRST_TIME_ENABLE", "commission_first_time_enable", true),
		OrderCancelRecoverTTL:        managedInt64("ORDER_CANCEL_RECOVER_TTL", "order_cancel_recover_ttl", 1800),
		SubscribeURL:                 managedString("SUBSCRIBE_URL", "subscribe_url", ""),
		SubscribePath:                managedString("SUBSCRIBE_PATH", "subscribe_path", "/api/v1/client/subscribe"),
		ShowInfoToServerEnable:       managedBool("SHOW_INFO_TO_SERVER_ENABLE", "show_info_to_server_enable", false),
		AllowNewPeriod:               managedBool("ALLOW_NEW_PERIOD", "allow_new_period", false),
		ShowSubscribeMethod:          managedInt64("SHOW_SUBSCRIBE_METHOD", "show_subscribe_method", 0),
		ShowSubscribeExpire:          managedInt64("SHOW_SUBSCRIBE_EXPIRE", "show_subscribe_expire", 5),
		ResetTrafficMethod:           managedInt64("RESET_TRAFFIC_METHOD", "reset_traffic_method", 0),
		ServerToken:                  managedString("SERVER_TOKEN", "server_token", ""),
		ServerPullInterval:           managedInt64("SERVER_PULL_INTERVAL", "server_pull_interval", 60),
		ServerPushInterval:           managedInt64("SERVER_PUSH_INTERVAL", "server_push_interval", 60),
		ServerNodeReportMinTraffic:   managedInt64("SERVER_NODE_REPORT_MIN_TRAFFIC", "server_node_report_min_traffic", 0),
		ServerDeviceOnlineMinTraffic: managedInt64("SERVER_DEVICE_ONLINE_MIN_TRAFFIC", "server_device_online_min_traffic", 0),
		DeviceLimitMode:              managedInt64("DEVICE_LIMIT_MODE", "device_limit_mode", 0),
		ServerLogEnable:              managedBool("SERVER_LOG_ENABLE", "server_log_enable", false),
		ServerV2RayDomain:            managedString("SERVER_V2RAY_DOMAIN", "server_v2ray_domain", ""),
		ServerV2RayProtocol:          managedString("SERVER_V2RAY_PROTOCOL", "server_v2ray_protocol", ""),
		WindowsVersion:               managedString("WINDOWS_VERSION", "windows_version", ""),
		WindowsDownloadURL:           managedString("WINDOWS_DOWNLOAD_URL", "windows_download_url", ""),
		MacOSVersion:                 managedString("MACOS_VERSION", "macos_version", ""),
		MacOSDownloadURL:             managedString("MACOS_DOWNLOAD_URL", "macos_download_url", ""),
		AndroidVersion:               managedString("ANDROID_VERSION", "android_version", ""),
		AndroidDownloadURL:           managedString("ANDROID_DOWNLOAD_URL", "android_download_url", ""),
		PostgresDSN:                  os.Getenv("POSTGRES_DSN"),
		QueueWorkers:                 int(getEnvInt64("QUEUE_WORKERS", 4)),
		AppKey:                       os.Getenv("APP_KEY"),
		AccessLogEnabled:             getEnvBool("ACCESS_LOG_ENABLE", false),
		SlowRequestLogThreshold:      getEnvDurationMS("SLOW_REQUEST_LOG_THRESHOLD_MS", 500*time.Millisecond),
		ReadTimeout:                  10 * time.Second,
		WriteTimeout:                 15 * time.Second,
		ShutdownTimeout:              10 * time.Second,
		TOSURL:                       managedString("TOS_URL", "tos_url", ""),
		EmailVerify:                  managedBool("EMAIL_VERIFY", "email_verify", false),
		InviteForce:                  managedBool("INVITE_FORCE", "invite_force", false),
		InviteNeverExpire:            managedBool("INVITE_NEVER_EXPIRE", "invite_never_expire", false),
		StopRegister:                 managedBool("STOP_REGISTER", "stop_register", false),
		LoginWithMailLink:            getEnvBool("LOGIN_WITH_MAIL_LINK_ENABLE", false),
		EmailWhitelist:               managedList("EMAIL_WHITELIST_SUFFIX", "email_whitelist_suffix", nil),
		EmailWhitelistEnabled:        managedBool("EMAIL_WHITELIST_ENABLE", "email_whitelist_enable", false),
		EmailGmailLimitEnabled:       managedBool("EMAIL_GMAIL_LIMIT_ENABLE", "email_gmail_limit_enable", false),
		Recaptcha:                    managedBool("RECAPTCHA_ENABLE", "recaptcha_enable", false),
		RecaptchaKey:                 managedString("RECAPTCHA_KEY", "recaptcha_key", ""),
		RecaptchaSiteKey:             managedString("RECAPTCHA_SITE_KEY", "recaptcha_site_key", ""),
		AppDescription:               managedString("APP_DESCRIPTION", "app_description", ""),
		AppURL:                       managedString("APP_URL", "app_url", ""),
		Logo:                         managedString("LOGO_URL", "logo", ""),
		RegisterLimitByIP:            managedBool("REGISTER_LIMIT_BY_IP_ENABLE", "register_limit_by_ip_enable", false),
		RegisterLimitCount:           managedInt64("REGISTER_LIMIT_COUNT", "register_limit_count", 3),
		RegisterLimitExpireMin:       managedInt64("REGISTER_LIMIT_EXPIRE", "register_limit_expire", 60),
		PasswordLimitEnabled:         managedBool("PASSWORD_LIMIT_ENABLE", "password_limit_enable", true),
		PasswordLimitCount:           managedInt64("PASSWORD_LIMIT_COUNT", "password_limit_count", 5),
		PasswordLimitExpireMin:       managedInt64("PASSWORD_LIMIT_EXPIRE", "password_limit_expire", 60),
		TelegramBotEnable:            managedBool("TELEGRAM_BOT_ENABLE", "telegram_bot_enable", false),
		TelegramBotToken:             managedString("TELEGRAM_BOT_TOKEN", "telegram_bot_token", ""),
		TelegramDiscussLink:          managedString("TELEGRAM_DISCUSS_LINK", "telegram_discuss_link", ""),
		StripePKLive:                 managedString("STRIPE_PK_LIVE", "stripe_pk_live", ""),
		Currency:                     managedString("CURRENCY", "currency", "CNY"),
		CurrencySymbol:               managedString("CURRENCY_SYMBOL", "currency_symbol", "¥"),
		MailHost:                     managedString("EMAIL_HOST", "email_host", getEnv("MAIL_HOST", "127.0.0.1")),
		MailPort:                     managedInt64("EMAIL_PORT", "email_port", getEnvInt64("MAIL_PORT", 25)),
		MailUsername:                 managedString("EMAIL_USERNAME", "email_username", os.Getenv("MAIL_USERNAME")),
		MailPassword:                 managedString("EMAIL_PASSWORD", "email_password", os.Getenv("MAIL_PASSWORD")),
		MailEncryption:               managedString("EMAIL_ENCRYPTION", "email_encryption", os.Getenv("MAIL_ENCRYPTION")),
		MailFromAddress:              managedString("EMAIL_FROM_ADDRESS", "email_from_address", getEnv("MAIL_FROM_ADDRESS", "")),
		MailFromName:                 managedString("EMAIL_FROM_NAME", "email_from_name", ""),
		MailTemplate:                 managedString("EMAIL_TEMPLATE", "email_template", "default"),
		TryOutPlanID:                 managedInt64("TRY_OUT_PLAN_ID", "try_out_plan_id", 0),
		TryOutHour:                   managedFloat64("TRY_OUT_HOUR", "try_out_hour", 0),
		InviteTryOutPlanID:           managedInt64("INVITE_CAMPAIGN_TRY_OUT_PLAN_ID", "invite_campaign_try_out_plan_id", 0),
		InviteTryOutTransferGB:       managedFloat64("INVITE_CAMPAIGN_TRY_OUT_TRANSFER_GB", "invite_campaign_try_out_transfer_gb", 0),
		InviteTryOutHours:            managedFloat64("INVITE_CAMPAIGN_TRY_OUT_HOURS", "invite_campaign_try_out_hours", 0),
	}

	if strings.TrimSpace(cfg.MailFromName) == "" {
		cfg.MailFromName = strings.TrimSpace(cfg.AppName)
	}
	if strings.TrimSpace(cfg.MailFromName) == "" {
		cfg.MailFromName = "Forest"
	}

	return cfg
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

func ResolveProjectConfigPath(fileName string) string {
	candidates := projectConfigCandidates(fileName)
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return filepath.Join("config", fileName)
}

func ResolveLegacyPHPConfigPath() string {
	return resolveLegacyPHPConfigPath("")
}

func ResolveLegacyPHPConfigPathFromRoot(root string) string {
	return resolveLegacyPHPConfigPath(root)
}

func projectConfigCandidates(fileName string) []string {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return nil
	}

	seen := map[string]struct{}{}
	candidates := make([]string, 0, 8)
	add := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}

	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		add(filepath.Join(cwd, "config", fileName))
		add(filepath.Join(cwd, "..", "config", fileName))
	}

	if exePath, err := os.Executable(); err == nil && strings.TrimSpace(exePath) != "" {
		exeDir := filepath.Dir(exePath)
		add(filepath.Join(exeDir, "config", fileName))
		add(filepath.Join(exeDir, "..", "config", fileName))
		add(filepath.Join(exeDir, "..", "..", "config", fileName))
	}

	add(filepath.Join("config", fileName))
	add(filepath.Join("..", "config", fileName))

	if fileName == "admin.json" {
		add(defaultAdminJSONPath)
	}

	return candidates
}

func resolveLegacyPHPConfigPath(root string) string {
	root = strings.TrimSpace(root)
	if override := strings.TrimSpace(os.Getenv("LEGACY_PHP_CONFIG_PATH")); override != "" {
		return filepath.Clean(override)
	}

	for _, dir := range legacyPHPConfigDirs(root) {
		if candidate := firstPHPConfigInDir(dir); candidate != "" {
			return candidate
		}
	}

	if root != "" {
		return filepath.Join(root, "config", defaultLegacyPHPFileName)
	}

	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Join(cwd, "config", defaultLegacyPHPFileName)
	}
	return filepath.Join("config", defaultLegacyPHPFileName)
}

func legacyPHPConfigDirs(root string) []string {
	seen := map[string]struct{}{}
	dirs := make([]string, 0, 8)
	add := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		dirs = append(dirs, path)
	}

	if root != "" {
		add(filepath.Join(root, "config"))
		return dirs
	}

	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		add(filepath.Join(cwd, "config"))
		add(filepath.Join(cwd, "..", "config"))
	}

	if exePath, err := os.Executable(); err == nil && strings.TrimSpace(exePath) != "" {
		exeDir := filepath.Dir(exePath)
		add(filepath.Join(exeDir, "config"))
		add(filepath.Join(exeDir, "..", "config"))
		add(filepath.Join(exeDir, "..", "..", "config"))
	}

	add(filepath.Join("config"))
	add(filepath.Join("..", "config"))
	return dirs
}

func firstPHPConfigInDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if !strings.EqualFold(filepath.Ext(name), ".php") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}

	sort.Strings(files)
	if len(files) == 0 {
		return ""
	}
	return files[0]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadManagedString(values map[string]any, legacyPath, envKey, key, fallback string) string {
	if raw, ok := values[key]; ok {
		return strings.TrimSpace(jsonValueString(raw))
	}
	if hasLegacyConfigKey(legacyPath, key) {
		return loadConfigString(values, legacyPath, key, fallback)
	}
	return getEnv(envKey, fallback)
}

func loadManagedBool(values map[string]any, legacyPath, envKey, key string, fallback bool) bool {
	if raw, ok := values[key]; ok {
		if parsed, ok := jsonValueBool(raw); ok {
			return parsed
		}
		return fallback
	}
	if hasLegacyConfigKey(legacyPath, key) {
		return loadConfigInt64(values, legacyPath, key, boolToInt64(fallback)) != 0
	}
	return getEnvBool(envKey, fallback)
}

func loadManagedInt64(values map[string]any, legacyPath, envKey, key string, fallback int64) int64 {
	if raw, ok := values[key]; ok {
		if parsed, ok := jsonValueInt64(raw); ok {
			return parsed
		}
		return fallback
	}
	if hasLegacyConfigKey(legacyPath, key) {
		return loadConfigInt64(values, legacyPath, key, fallback)
	}
	return getEnvInt64(envKey, fallback)
}

func loadManagedFloat64(values map[string]any, legacyPath, envKey, key string, fallback float64) float64 {
	if raw, ok := values[key]; ok {
		if parsed, ok := jsonValueFloat64(raw); ok {
			return parsed
		}
		return fallback
	}
	if hasLegacyConfigKey(legacyPath, key) {
		return loadConfigFloat64(values, legacyPath, key, fallback)
	}
	return getEnvFloat64(envKey, fallback)
}

func loadManagedStringList(values map[string]any, legacyPath, envKey, key string, fallback []string) []string {
	if raw, ok := values[key]; ok {
		return jsonValueStringList(raw)
	}
	if hasLegacyConfigKey(legacyPath, key) {
		return loadConfigStringList(values, legacyPath, key, fallback)
	}
	return getEnvList(envKey, fallback)
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

func jsonValueBool(value any) (bool, bool) {
	switch strings.TrimSpace(strings.ToLower(jsonValueString(value))) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off", "":
		return false, true
	default:
		return false, false
	}
}

func jsonValueInt64(value any) (int64, bool) {
	raw := strings.TrimSpace(jsonValueString(value))
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func jsonValueFloat64(value any) (float64, bool) {
	raw := strings.TrimSpace(jsonValueString(value))
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func jsonValueStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			next := strings.TrimSpace(jsonValueString(item))
			if next != "" {
				result = append(result, next)
			}
		}
		return result
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				result = append(result, item)
			}
		}
		return result
	default:
		next := strings.TrimSpace(jsonValueString(typed))
		if next == "" {
			return nil
		}
		return []string{next}
	}
}

func hasLegacyConfigKey(path, key string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pattern := regexp.MustCompile(`'` + regexp.QuoteMeta(key) + `'\s*=>`)
	return pattern.Match(content)
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
