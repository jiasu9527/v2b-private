package newapibridge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Settings is encrypted as one envelope. Never serialize it to an HTTP response.
type Settings struct {
	Enabled       bool   `json:"enabled"`
	APIURL        string `json:"api_url"`
	SiteURL       string `json:"site_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	MerchantID    string `json:"merchant_id"`
	MerchantKey   string `json:"merchant_key"`
	AdminToken    string `json:"admin_token"`
	AdminUserID   int64  `json:"admin_user_id"`
	MinAmountFen  int64  `json:"min_amount_fen"`
	MaxAmountFen  int64  `json:"max_amount_fen"`
	DailyLimitFen int64  `json:"daily_limit_fen"`
	Revision      int64  `json:"revision"`
	SigningKey    string `json:"signing_key"`
}

type SettingsSaveRequest struct {
	Enabled          bool   `json:"enabled"`
	APIURL           string `json:"api_url"`
	SiteURL          string `json:"site_url"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret"`
	MerchantID       string `json:"merchant_id"`
	MerchantKey      string `json:"merchant_key"`
	AdminToken       string `json:"admin_token"`
	AdminUserID      int64  `json:"admin_user_id"`
	MinAmountFen     int64  `json:"min_amount_fen"`
	MaxAmountFen     int64  `json:"max_amount_fen"`
	DailyLimitFen    int64  `json:"daily_limit_fen"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *Service) defaultSettings() Settings {
	base, _ := normalizeOrigin(s.siteURL)
	return Settings{SiteURL: base, ClientID: "forest", MerchantID: "forest-wallet", MinAmountFen: 100, MaxAmountFen: 100000, DailyLimitFen: 500000}
}

func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(u.Host, "\\\r\n\t ") {
		return "", ErrInvalid
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	return u.String(), nil
}

func (s *Service) loadSettings(ctx context.Context) (Settings, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return Settings{}, err
	}
	var encrypted string
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT config_cipher, revision FROM v2_newapi_settings WHERE id=1`).Scan(&encrypted, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return s.defaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	data, err := s.open(encrypted)
	if err != nil {
		return Settings{}, err
	}
	var value Settings
	if err = json.Unmarshal(data, &value); err != nil {
		return Settings{}, ErrUnavailable
	}
	value.Revision = revision
	return value, nil
}

func settingsView(c Settings) map[string]any {
	link := func(origin, path string) string {
		if origin == "" {
			return ""
		}
		return origin + path
	}
	return map[string]any{
		"enabled": c.Enabled, "api_url": c.APIURL, "site_url": c.SiteURL,
		"client_id": c.ClientID, "merchant_id": c.MerchantID, "admin_user_id": c.AdminUserID,
		"min_amount_fen": c.MinAmountFen, "max_amount_fen": c.MaxAmountFen,
		"daily_limit_fen": c.DailyLimitFen, "revision": c.Revision,
		"has_client_secret": c.ClientSecret != "", "has_merchant_key": c.MerchantKey != "", "has_admin_token": c.AdminToken != "",
		"authorize_url": link(c.SiteURL, "/new-api/authorize"),
		"token_url":     link(c.SiteURL, "/api/v1/new-api/oauth/token"),
		"userinfo_url":  link(c.SiteURL, "/api/v1/new-api/oauth/userinfo"),
		"epay_url":      link(c.SiteURL, "/new-api"), "callback_url": link(c.APIURL, "/oauth/oidc"),
	}
}

func (s *Service) GetConfig(ctx context.Context) (map[string]any, error) {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return nil, e
	}
	return settingsView(c), nil
}

func (s *Service) SaveConfig(ctx context.Context, actorID int64, req SettingsSaveRequest) (map[string]any, error) {
	if actorID <= 0 {
		return nil, ErrForbidden
	}
	apiURL := strings.TrimSpace(req.APIURL)
	if apiURL != "" {
		var err error
		apiURL, err = normalizeOrigin(apiURL)
		if err != nil {
			return nil, fmt.Errorf("%w：AI站地址必须是HTTPS根地址", ErrInvalid)
		}
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(763918202)`); err != nil {
		return nil, err
	}
	current := s.defaultSettings()
	var encrypted string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT config_cipher,revision FROM v2_newapi_settings WHERE id=1 FOR UPDATE`).Scan(&encrypted, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		raw, e := s.open(encrypted)
		if e != nil {
			return nil, e
		}
		if json.Unmarshal(raw, &current) != nil {
			return nil, ErrUnavailable
		}
	}
	if req.ExpectedRevision != revision {
		return nil, ErrConflict
	}
	// A paid order must not move to another instance/merchant while awaiting
	// settlement. Credential rotation for that same destination remains possible.
	if revision > 0 && (apiURL != current.APIURL || strings.TrimSpace(req.MerchantID) != current.MerchantID) {
		var pending bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_newapi_transfer WHERE status='pending')`).Scan(&pending); err != nil {
			return nil, err
		}
		if pending {
			return nil, fmt.Errorf("%w：有待到账划转时不能切换 AI 站或商户", ErrConflict)
		}
	}
	c := Settings{Enabled: req.Enabled, APIURL: apiURL, SiteURL: strings.TrimSpace(req.SiteURL), ClientID: strings.TrimSpace(req.ClientID), MerchantID: strings.TrimSpace(req.MerchantID), AdminUserID: req.AdminUserID, MinAmountFen: req.MinAmountFen, MaxAmountFen: req.MaxAmountFen, DailyLimitFen: req.DailyLimitFen, ClientSecret: current.ClientSecret, MerchantKey: current.MerchantKey, AdminToken: current.AdminToken, SigningKey: current.SigningKey, Revision: revision + 1}
	if req.ClientSecret != "" {
		c.ClientSecret = strings.TrimSpace(req.ClientSecret)
	}
	if req.MerchantKey != "" {
		c.MerchantKey = strings.TrimSpace(req.MerchantKey)
	}
	if req.AdminToken != "" {
		c.AdminToken = strings.TrimSpace(req.AdminToken)
	}
	if c.SiteURL != "" {
		c.SiteURL, err = normalizeOrigin(c.SiteURL)
		if err != nil {
			return nil, fmt.Errorf("%w：本站地址必须是HTTPS根地址", ErrInvalid)
		}
	}
	if c.ClientID == "" || len(c.ClientID) > 100 || c.MerchantID == "" || len(c.MerchantID) > 100 || c.AdminUserID < 0 || c.MinAmountFen < 1 || c.MaxAmountFen < c.MinAmountFen || c.DailyLimitFen < c.MaxAmountFen || c.DailyLimitFen > 100000000 {
		return nil, ErrInvalid
	}
	for _, secret := range []string{c.ClientSecret, c.MerchantKey, c.AdminToken} {
		if len(secret) > 4096 || strings.ContainsAny(secret, "\r\n") {
			return nil, ErrInvalid
		}
	}
	if c.Enabled && (c.APIURL == "" || c.SiteURL == "" || c.APIURL == c.SiteURL || len(c.ClientSecret) < 32 || len(c.MerchantKey) < 32 || c.AdminToken == "" || c.AdminUserID <= 0) {
		return nil, fmt.Errorf("%w：启用前需填写完整地址、独立强密钥与管理只读凭据", ErrInvalid)
	}
	if c.Enabled && c.SigningKey == "" {
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			return nil, e
		}
		c.SigningKey = base64.RawStdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key))
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	encrypted, err = s.seal(raw)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_newapi_settings(id,revision,config_cipher,updated_by,updated_at) VALUES(1,$1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET revision=EXCLUDED.revision,config_cipher=EXCLUDED.config_cipher,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`, c.Revision, encrypted, actorID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return settingsView(c), nil
}

func (s *Service) PublicInfo(ctx context.Context) (map[string]any, error) {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return nil, e
	}
	return map[string]any{"enabled": c.Enabled, "api_url": c.APIURL, "site_url": c.SiteURL, "min_amount_fen": c.MinAmountFen, "max_amount_fen": c.MaxAmountFen, "daily_limit_fen": c.DailyLimitFen}, nil
}
