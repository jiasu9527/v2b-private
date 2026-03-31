package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("APP_ADDR", "")
	t.Setenv("PUBLIC_DIR", "")
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("ACCESS_LOG_ENABLE", "")
	t.Setenv("SLOW_REQUEST_LOG_THRESHOLD_MS", "")

	cfg := Load()

	if cfg.Addr != ":8080" {
		t.Fatalf("expected default addr :8080, got %q", cfg.Addr)
	}
	if cfg.PublicDir != "../public" {
		t.Fatalf("expected default public dir ../public, got %q", cfg.PublicDir)
	}
	if cfg.PostgresDSN != "" {
		t.Fatalf("expected empty postgres dsn, got %q", cfg.PostgresDSN)
	}
	if cfg.AccessLogEnabled {
		t.Fatal("expected access log disabled by default")
	}
	if cfg.SlowRequestLogThreshold != 500*time.Millisecond {
		t.Fatalf("expected default slow request threshold 500ms, got %s", cfg.SlowRequestLogThreshold)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("APP_ADDR", ":9090")
	t.Setenv("PUBLIC_DIR", "/srv/app/public")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1:5432/app")
	t.Setenv("ACCESS_LOG_ENABLE", "1")
	t.Setenv("SLOW_REQUEST_LOG_THRESHOLD_MS", "250")

	cfg := Load()

	if cfg.Addr != ":9090" {
		t.Fatalf("expected env addr :9090, got %q", cfg.Addr)
	}
	if cfg.PublicDir != "/srv/app/public" {
		t.Fatalf("expected env public dir override, got %q", cfg.PublicDir)
	}
	if cfg.PostgresDSN != "postgres://user:pass@127.0.0.1:5432/app" {
		t.Fatalf("expected postgres dsn override, got %q", cfg.PostgresDSN)
	}
	if !cfg.AccessLogEnabled {
		t.Fatal("expected access log enabled from env override")
	}
	if cfg.SlowRequestLogThreshold != 250*time.Millisecond {
		t.Fatalf("expected slow request threshold 250ms, got %s", cfg.SlowRequestLogThreshold)
	}
}

func TestLoadAdminJSONFallbacks(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"secure_path":                "newadmin",
		"commission_withdraw_method": []string{"USDT", "支付宝"},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	cfg := Load()
	if cfg.AdminPath != "newadmin" {
		t.Fatalf("expected admin path from admin.json, got %q", cfg.AdminPath)
	}
	if len(cfg.CommissionWithdrawMethods) != 2 || cfg.CommissionWithdrawMethods[0] != "USDT" || cfg.CommissionWithdrawMethods[1] != "支付宝" {
		t.Fatalf("unexpected json withdraw methods: %#v", cfg.CommissionWithdrawMethods)
	}
}

func TestLoadAdminJSONFallbacksFromProjectRootWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"secure_path": "rootadmin",
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir project root: %v", err)
	}

	cfg := Load()
	if cfg.AdminPath != "rootadmin" {
		t.Fatalf("expected admin path from project-root config/admin.json, got %q", cfg.AdminPath)
	}
}

func TestResolveLegacyPHPConfigPathAutoDetectsGenericPHPFile(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	expected := filepath.Join(configRoot, "site.php")
	if err := os.WriteFile(expected, []byte("<?php return [];"), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	got := ResolveLegacyPHPConfigPath()
	expectedResolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		t.Fatalf("eval expected symlinks: %v", err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("eval actual symlinks: %v", err)
	}
	if gotResolved != expectedResolved {
		t.Fatalf("expected auto-detected legacy config path %q, got %q", expectedResolved, gotResolved)
	}
}

func TestLoadAdminJSONInviteCampaignTryOutFallbacks(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"invite_campaign_try_out_plan_id":     9,
		"invite_campaign_try_out_transfer_gb": 12.5,
		"invite_campaign_try_out_hours":       36,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	t.Setenv("INVITE_CAMPAIGN_TRY_OUT_PLAN_ID", "")
	t.Setenv("INVITE_CAMPAIGN_TRY_OUT_TRANSFER_GB", "")
	t.Setenv("INVITE_CAMPAIGN_TRY_OUT_HOURS", "")

	cfg := Load()
	if cfg.InviteTryOutPlanID != 9 {
		t.Fatalf("expected invite try out plan id 9, got %d", cfg.InviteTryOutPlanID)
	}
	if cfg.InviteTryOutTransferGB != 12.5 {
		t.Fatalf("expected invite try out transfer 12.5, got %v", cfg.InviteTryOutTransferGB)
	}
	if cfg.InviteTryOutHours != 36 {
		t.Fatalf("expected invite try out hours 36, got %v", cfg.InviteTryOutHours)
	}
}

func TestLoadAdminJSONSiteFallbacks(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"app_name":                   "Forest Site",
		"app_description":            "Fast and stable",
		"app_url":                    "https://forest.test",
		"logo":                       "https://cdn.example.com/logo.png",
		"show_info_to_server_enable": 1,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	t.Setenv("APP_NAME", "")
	t.Setenv("APP_DESCRIPTION", "")
	t.Setenv("APP_URL", "")
	t.Setenv("LOGO_URL", "")

	cfg := Load()
	if cfg.AppName != "Forest Site" {
		t.Fatalf("expected app name from admin.json, got %q", cfg.AppName)
	}
	if cfg.AppDescription != "Fast and stable" {
		t.Fatalf("expected app description from admin.json, got %q", cfg.AppDescription)
	}
	if cfg.AppURL != "https://forest.test" {
		t.Fatalf("expected app url from admin.json, got %q", cfg.AppURL)
	}
	if cfg.Logo != "https://cdn.example.com/logo.png" {
		t.Fatalf("expected logo from admin.json, got %q", cfg.Logo)
	}
	if !cfg.ShowInfoToServerEnable {
		t.Fatal("expected show_info_to_server_enable from admin.json")
	}
}

func TestLoadAdminJSONSafeFallbacks(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"tos_url":                     "https://forest.test/tos",
		"email_verify":                1,
		"invite_force":                1,
		"invite_never_expire":         1,
		"stop_register":               1,
		"email_whitelist_enable":      1,
		"email_whitelist_suffix":      []string{"forest.test", "example.com"},
		"email_gmail_limit_enable":    1,
		"recaptcha_enable":            1,
		"recaptcha_key":               "secret-key",
		"recaptcha_site_key":          "site-key",
		"register_limit_by_ip_enable": 1,
		"register_limit_count":        8,
		"register_limit_expire":       120,
		"password_limit_enable":       0,
		"password_limit_count":        9,
		"password_limit_expire":       240,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	t.Setenv("TOS_URL", "")
	t.Setenv("EMAIL_VERIFY", "")
	t.Setenv("INVITE_FORCE", "")
	t.Setenv("INVITE_NEVER_EXPIRE", "")
	t.Setenv("STOP_REGISTER", "")
	t.Setenv("EMAIL_WHITELIST_ENABLE", "")
	t.Setenv("EMAIL_WHITELIST_SUFFIX", "")
	t.Setenv("EMAIL_GMAIL_LIMIT_ENABLE", "")
	t.Setenv("RECAPTCHA_ENABLE", "")
	t.Setenv("RECAPTCHA_KEY", "")
	t.Setenv("RECAPTCHA_SITE_KEY", "")
	t.Setenv("REGISTER_LIMIT_BY_IP_ENABLE", "")
	t.Setenv("REGISTER_LIMIT_COUNT", "")
	t.Setenv("REGISTER_LIMIT_EXPIRE", "")
	t.Setenv("PASSWORD_LIMIT_ENABLE", "")
	t.Setenv("PASSWORD_LIMIT_COUNT", "")
	t.Setenv("PASSWORD_LIMIT_EXPIRE", "")

	cfg := Load()
	if cfg.TOSURL != "https://forest.test/tos" {
		t.Fatalf("expected tos_url from admin.json, got %q", cfg.TOSURL)
	}
	if !cfg.EmailVerify {
		t.Fatal("expected email_verify from admin.json")
	}
	if !cfg.InviteForce {
		t.Fatal("expected invite_force from admin.json")
	}
	if !cfg.InviteNeverExpire {
		t.Fatal("expected invite_never_expire from admin.json")
	}
	if !cfg.StopRegister {
		t.Fatal("expected stop_register from admin.json")
	}
	if !cfg.EmailWhitelistEnabled {
		t.Fatal("expected email_whitelist_enable from admin.json")
	}
	if len(cfg.EmailWhitelist) != 2 || cfg.EmailWhitelist[0] != "forest.test" || cfg.EmailWhitelist[1] != "example.com" {
		t.Fatalf("unexpected email whitelist: %#v", cfg.EmailWhitelist)
	}
	if !cfg.EmailGmailLimitEnabled {
		t.Fatal("expected email_gmail_limit_enable from admin.json")
	}
	if !cfg.Recaptcha {
		t.Fatal("expected recaptcha_enable from admin.json")
	}
	if cfg.RecaptchaKey != "secret-key" {
		t.Fatalf("expected recaptcha_key from admin.json, got %q", cfg.RecaptchaKey)
	}
	if cfg.RecaptchaSiteKey != "site-key" {
		t.Fatalf("expected recaptcha_site_key from admin.json, got %q", cfg.RecaptchaSiteKey)
	}
	if !cfg.RegisterLimitByIP {
		t.Fatal("expected register_limit_by_ip_enable from admin.json")
	}
	if cfg.RegisterLimitCount != 8 {
		t.Fatalf("expected register_limit_count 8, got %d", cfg.RegisterLimitCount)
	}
	if cfg.RegisterLimitExpireMin != 120 {
		t.Fatalf("expected register_limit_expire 120, got %d", cfg.RegisterLimitExpireMin)
	}
	if cfg.PasswordLimitEnabled {
		t.Fatal("expected password_limit_enable disabled from admin.json")
	}
	if cfg.PasswordLimitCount != 9 {
		t.Fatalf("expected password_limit_count 9, got %d", cfg.PasswordLimitCount)
	}
	if cfg.PasswordLimitExpireMin != 240 {
		t.Fatalf("expected password_limit_expire 240, got %d", cfg.PasswordLimitExpireMin)
	}
}

func TestLoadAdminJSONSafeFallbacksIgnoreLegacyEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	configRoot := filepath.Join(dir, "config")
	workDir := filepath.Join(dir, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	raw, err := json.MarshalIndent(map[string]any{
		"email_verify": 1,
		"invite_force": 1,
		"app_url":      "https://forest-hot.example.com",
		"app_name":     "Forest Hot",
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	t.Setenv("EMAIL_VERIFY", "false")
	t.Setenv("INVITE_FORCE", "false")
	t.Setenv("APP_URL", "http://old-env.example.com")
	t.Setenv("APP_NAME", "Old Env")

	cfg := Load()
	if !cfg.EmailVerify {
		t.Fatal("expected admin.json email_verify to override legacy env")
	}
	if !cfg.InviteForce {
		t.Fatal("expected admin.json invite_force to override legacy env")
	}
	if cfg.AppURL != "https://forest-hot.example.com" {
		t.Fatalf("expected admin.json app_url to override legacy env, got %q", cfg.AppURL)
	}
	if cfg.AppName != "Forest Hot" {
		t.Fatalf("expected admin.json app_name to override legacy env, got %q", cfg.AppName)
	}
}
