package passport

import (
	"context"
	"crypto/md5"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/platform/mailtmpl"
	"forest/go-api/internal/platform/smtpcompat"
	"forest/go-api/internal/queue"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrUnavailable = errors.New("passport service unavailable")
var passportProjectRoot = detectPassportProjectRoot()

const (
	cacheEmailVerifyCode             = "EMAIL_VERIFY_CODE"
	cacheLastSendEmailVerify         = "LAST_SEND_EMAIL_VERIFY_TIMESTAMP"
	cacheTempToken                   = "TEMP_TOKEN"
	cacheLastSendMailLogin           = "LAST_SEND_LOGIN_WITH_MAIL_LINK_TIMESTAMP"
	cachePasswordErrorLimit          = "PASSWORD_ERROR_LIMIT"
	cacheForgetRequestLimit          = "FORGET_REQUEST_LIMIT"
	cacheRegisterIPRateLimit         = "REGISTER_IP_RATE_LIMIT"
	cacheSendVerifyRateLimitIP       = "SEND_EMAIL_VERIFY_RATE_LIMIT_IP"
	mailTemplateVerify               = "verify"
	mailTemplateLogin                = "login"
	defaultDashboardRedirect         = "dashboard"
	bytesPerGB                 int64 = 1073741824
)

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type rower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type beginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type DBService struct {
	cfg        config.Config
	db         execer
	jobs       queue.Enqueuer
	mailSender func(to, subject, body string) error
	ensureErr  error
	ensureOne  sync.Once
}

type userRow struct {
	ID           int64
	Email        string
	Password     string
	PasswordAlgo sql.NullString
	PasswordSalt sql.NullString
	Token        string
	IsAdmin      int64
	IsStaff      int64
	Banned       int64
}

type inviteCodeRow struct {
	ID     int64
	UserID int64
	Code   string
}

type campaignRow struct {
	ID            int64
	UserID        int64
	InviteCode    string
	RewardAmount  int64
	TargetAmount  int64
	CurrentAmount int64
	InviteCount   int64
	Status        int64
	ExpiredAt     int64
}

type planRow struct {
	ID             int64
	TransferEnable float64
	DeviceLimit    sql.NullInt64
	GroupID        sql.NullInt64
	SpeedLimit     sql.NullInt64
}

type tryOutProfile struct {
	TransferEnable int64
	DeviceLimit    sql.NullInt64
	GroupID        sql.NullInt64
	PlanID         sql.NullInt64
	SpeedLimit     sql.NullInt64
	ExpiredAt      int64
}

func NewDBService(db execer) *DBService {
	return &DBService{
		cfg: config.Load(),
		db:  db,
	}
}

func NewDBServiceWithConfig(cfg config.Config, db execer) *DBService {
	return &DBService{
		cfg: cfg,
		db:  db,
	}
}

func (s *DBService) WithQueueRuntime(jobs queue.Enqueuer) *DBService {
	s.jobs = jobs
	return s
}

func (s *DBService) PV(ctx context.Context, inviteCode string) error {
	inviteCode = strings.TrimSpace(inviteCode)
	if inviteCode == "" {
		return nil
	}
	if s.db == nil {
		return ErrUnavailable
	}
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, `UPDATE v2_invite_code
SET pv = pv + 1,
    updated_at = EXTRACT(EPOCH FROM NOW())::bigint
WHERE code = $1`, inviteCode)
	if err != nil {
		return fmt.Errorf("increment invite code pv: %w", err)
	}
	return nil
}

func (s *DBService) SendEmailVerify(ctx context.Context, req SendEmailVerifyRequest) error {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return err
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if err := validateEmail(email); err != nil {
		return err
	}

	if req.IP != "" {
		attempt, err := s.kvIncr(ctx, cacheKey(cacheSendVerifyRateLimitIP, req.IP), 60)
		if err != nil {
			return err
		}
		if attempt > 3 {
			return NewHTTPError(http.StatusTooManyRequests, "Too many requests, please try again later.")
		}
	}

	if s.cfg.Recaptcha {
		if err := s.verifyRecaptcha(ctx, req.RecaptchaData); err != nil {
			return err
		}
	}

	exists, err := s.emailExists(ctx, email)
	if err != nil {
		return err
	}

	if s.cfg.EmailWhitelistEnabled && !emailSuffixAllowed(email, s.cfg.EmailWhitelist) {
		return NewHTTPError(http.StatusInternalServerError, "Email suffix is not in the Whitelist")
	}
	if s.cfg.EmailGmailLimitEnabled && gmailAlias(email) {
		return NewHTTPError(http.StatusInternalServerError, "Gmail alias is not supported")
	}

	if req.HasIsForget {
		if req.IsForget == 0 && exists {
			return NewHTTPError(http.StatusInternalServerError, "This email is registered")
		}
		if req.IsForget == 1 && !exists {
			return NewHTTPError(http.StatusInternalServerError, "This email is not registered in the system")
		}
	}

	lastSendKey := cacheKey(cacheLastSendEmailVerify, email)
	if _, ok, err := s.kvGet(ctx, lastSendKey); err != nil {
		return err
	} else if ok {
		return NewHTTPError(http.StatusInternalServerError, "Email verification code has been sent, please request again later")
	}

	code := randomDigits(6)
	settings := s.runtimeMailSettings()
	subject := fmt.Sprintf("%s邮箱验证码", settings.AppName)
	_ = s.sendEmailBestEffort(ctx, email, subject, mailTemplateVerify, fmt.Sprintf("您的验证码是：%s", code), map[string]string{
		"name":    settings.AppName,
		"url":     settings.AppURL,
		"code":    code,
		"content": fmt.Sprintf("您的验证码是：%s", code),
	})
	if err := s.kvSet(ctx, cacheKey(cacheEmailVerifyCode, email), code, 300); err != nil {
		return err
	}
	if err := s.kvSet(ctx, lastSendKey, strconv.FormatInt(time.Now().Unix(), 10), 60); err != nil {
		return err
	}

	return nil
}

func (s *DBService) Register(ctx context.Context, req RegisterRequest) (AuthData, error) {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return AuthData{}, err
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	password := strings.TrimSpace(req.Password)

	if err := validateEmail(email); err != nil {
		return AuthData{}, err
	}
	if len(password) < 8 {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Password must be greater than 8 digits")
	}

	if s.cfg.RegisterLimitByIP && req.IP != "" {
		key := cacheKey(cacheRegisterIPRateLimit, req.IP)
		count, err := s.kvGetInt(ctx, key)
		if err != nil {
			return AuthData{}, err
		}
		if count >= s.cfg.RegisterLimitCount {
			return AuthData{}, NewHTTPError(
				http.StatusInternalServerError,
				fmt.Sprintf("Register frequently, please try again after %d minute", s.cfg.RegisterLimitExpireMin),
			)
		}
	}

	if s.cfg.Recaptcha {
		if err := s.verifyRecaptcha(ctx, req.RecaptchaData); err != nil {
			return AuthData{}, err
		}
	}

	if s.cfg.EmailWhitelistEnabled && !emailSuffixAllowed(email, s.cfg.EmailWhitelist) {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Email suffix is not in the Whitelist")
	}
	if s.cfg.EmailGmailLimitEnabled && gmailAlias(email) {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Gmail alias is not supported")
	}
	if s.cfg.StopRegister {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Registration has closed")
	}

	inviteCodeInput := strings.TrimSpace(req.InviteCode)
	if s.cfg.InviteForce && inviteCodeInput == "" {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "You must use the invitation code to register")
	}

	if s.cfg.EmailVerify {
		if strings.TrimSpace(req.EmailCode) == "" {
			return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Email verification code cannot be empty")
		}
		code, ok, err := s.kvGet(ctx, cacheKey(cacheEmailVerifyCode, email))
		if err != nil {
			return AuthData{}, err
		}
		if !ok || code != strings.TrimSpace(req.EmailCode) {
			return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Incorrect email verification code")
		}
	}

	exists, err := s.emailExists(ctx, email)
	if err != nil {
		return AuthData{}, err
	}
	if exists {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Email already exists")
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return AuthData{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	now := time.Now().Unix()
	var inviteCode *inviteCodeRow
	var activeCampaign *campaignRow
	var inviteUserID sql.NullInt64
	if inviteCodeInput != "" {
		inviteCode, err = findInviteCodeTx(ctx, tx, inviteCodeInput)
		if err != nil {
			return AuthData{}, err
		}
		if inviteCode == nil {
			if s.cfg.InviteForce {
				return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Invalid invitation code")
			}
		} else {
			inviteUserID = sql.NullInt64{Int64: inviteCode.UserID, Valid: inviteCode.UserID > 0}
			activeCampaign, err = findActiveCampaignTx(ctx, tx, inviteCode.UserID, inviteCode.Code, now)
			if err != nil {
				return AuthData{}, err
			}
			if !s.cfg.InviteNeverExpire {
				if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_code SET status = 1, updated_at = $1 WHERE id = $2`, now, inviteCode.ID); err != nil {
					return AuthData{}, fmt.Errorf("expire invite code: %w", err)
				}
			}
		}
	}

	profile, err := s.resolveTryOutProfileTx(ctx, tx, activeCampaign, now)
	if err != nil {
		return AuthData{}, err
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return AuthData{}, fmt.Errorf("hash password: %w", err)
	}

	uuid, err := randomUUID()
	if err != nil {
		return AuthData{}, err
	}
	token, err := randomTokenHex(16)
	if err != nil {
		return AuthData{}, err
	}

	var created userRow
	row := tx.QueryRowContext(ctx, `INSERT INTO v2_user (
invite_user_id, email, password, uuid, token,
transfer_enable, device_limit, plan_id, group_id, speed_limit, expired_at, last_login_at,
created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5,
$6, $7, $8, $9, $10, $11, $12,
$13, $14
)
RETURNING id, email, password, password_algo, password_salt, token, is_admin, is_staff, banned`,
		nullInt64ToAny(inviteUserID),
		email,
		string(passwordHash),
		uuid,
		token,
		profile.TransferEnable,
		nullInt64ToAny(profile.DeviceLimit),
		nullInt64ToAny(profile.PlanID),
		nullInt64ToAny(profile.GroupID),
		nullInt64ToAny(profile.SpeedLimit),
		profile.ExpiredAt,
		now,
		now,
		now,
	)
	if err := scanUserRow(row, &created); err != nil {
		return AuthData{}, fmt.Errorf("create user: %w", err)
	}

	if inviteCode != nil && activeCampaign != nil {
		if err := accrueInviteRegistrationTx(ctx, tx, activeCampaign, inviteCode.Code, created.ID, now); err != nil {
			return AuthData{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return AuthData{}, fmt.Errorf("commit register transaction: %w", err)
	}

	if s.cfg.EmailVerify {
		_ = s.kvDelete(ctx, cacheKey(cacheEmailVerifyCode, email))
	}
	if s.cfg.RegisterLimitByIP && req.IP != "" {
		key := cacheKey(cacheRegisterIPRateLimit, req.IP)
		count, err := s.kvGetInt(ctx, key)
		if err == nil {
			_ = s.kvSet(ctx, key, strconv.FormatInt(count+1, 10), s.cfg.RegisterLimitExpireMin*3600)
		}
	}

	authData, err := s.createAuthData(ctx, created, req.IP, req.UserAgent)
	if err != nil {
		return AuthData{}, err
	}
	return authData, nil
}

func (s *DBService) Login(ctx context.Context, req LoginRequest) (AuthData, error) {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return AuthData{}, err
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	password := strings.TrimSpace(req.Password)
	if err := validateEmail(email); err != nil {
		return AuthData{}, err
	}
	if len(password) < 8 {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Password must be greater than 8 digits")
	}

	passwordErrKey := cacheKey(cachePasswordErrorLimit, email)
	if s.cfg.PasswordLimitEnabled {
		passwordErrCount, err := s.kvGetInt(ctx, passwordErrKey)
		if err != nil {
			return AuthData{}, err
		}
		if passwordErrCount >= s.cfg.PasswordLimitCount {
			return AuthData{}, NewHTTPError(
				http.StatusInternalServerError,
				fmt.Sprintf("There are too many password errors, please try again after %d minutes.", s.cfg.PasswordLimitExpireMin),
			)
		}
	}

	user, err := s.findUserByEmail(ctx, email)
	if err != nil {
		return AuthData{}, err
	}
	if user == nil {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Incorrect email or password")
	}

	if !verifyPassword(user.PasswordAlgo, user.PasswordSalt, password, user.Password) {
		if s.cfg.PasswordLimitEnabled {
			_, _ = s.kvIncrWithBase(ctx, passwordErrKey, s.cfg.PasswordLimitExpireMin*60, 0)
		}
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Incorrect email or password")
	}
	if user.Banned != 0 {
		return AuthData{}, NewHTTPError(http.StatusInternalServerError, "Your account has been suspended")
	}

	authData, err := s.createAuthData(ctx, *user, req.IP, req.UserAgent)
	if err != nil {
		return AuthData{}, err
	}
	return authData, nil
}

func (s *DBService) Forget(ctx context.Context, req ForgetRequest) error {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return err
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	password := strings.TrimSpace(req.Password)
	emailCode := strings.TrimSpace(req.EmailCode)

	if err := validateEmail(email); err != nil {
		return err
	}
	if len(password) < 8 {
		return NewHTTPError(http.StatusInternalServerError, "Password must be greater than 8 digits")
	}
	if emailCode == "" {
		return NewHTTPError(http.StatusInternalServerError, "Email verification code cannot be empty")
	}

	forgetLimitKey := cacheKey(cacheForgetRequestLimit, email)
	forgetLimit, err := s.kvGetInt(ctx, forgetLimitKey)
	if err != nil {
		return err
	}
	if forgetLimit >= 3 {
		return NewHTTPError(http.StatusInternalServerError, "Reset failed, Please try again later")
	}

	code, ok, err := s.kvGet(ctx, cacheKey(cacheEmailVerifyCode, email))
	if err != nil {
		return err
	}
	if !ok || code != emailCode {
		_ = s.kvSet(ctx, forgetLimitKey, strconv.FormatInt(forgetLimit+1, 10), 300)
		return NewHTTPError(http.StatusInternalServerError, "Incorrect email verification code")
	}

	user, err := s.findUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if user == nil {
		return NewHTTPError(http.StatusInternalServerError, "This email is not registered in the system")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash reset password: %w", err)
	}
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `UPDATE v2_user SET password = $1, password_algo = NULL, password_salt = NULL, updated_at = $2 WHERE id = $3`, string(hashedPassword), now, user.ID); err != nil {
		return NewHTTPError(http.StatusInternalServerError, "Reset failed")
	}

	_ = s.kvDelete(ctx, cacheKey(cacheEmailVerifyCode, email))
	_ = s.removeAllSessions(ctx, user.ID)
	return nil
}

func (s *DBService) TokenLogin(ctx context.Context, req TokenLoginRequest) (TokenLoginResult, error) {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return TokenLoginResult{}, err
	}

	token := strings.TrimSpace(req.Token)
	verify := strings.TrimSpace(req.Verify)
	redirect := strings.TrimSpace(req.Redirect)
	if redirect == "" {
		redirect = defaultDashboardRedirect
	}

	if token != "" {
		return TokenLoginResult{
			RedirectURL: s.buildFrontendURL(fmt.Sprintf("/#/login?verify=%s&redirect=%s", token, redirect)),
		}, nil
	}

	if verify == "" {
		return TokenLoginResult{}, nil
	}

	cacheKeyToken := cacheKey(cacheTempToken, verify)
	userIDValue, ok, err := s.kvGet(ctx, cacheKeyToken)
	if err != nil {
		return TokenLoginResult{}, err
	}
	if !ok {
		return TokenLoginResult{}, NewHTTPError(http.StatusInternalServerError, "Token error")
	}
	userID, err := strconv.ParseInt(userIDValue, 10, 64)
	if err != nil {
		return TokenLoginResult{}, NewHTTPError(http.StatusInternalServerError, "Token error")
	}
	user, err := s.findUserByID(ctx, userID)
	if err != nil {
		return TokenLoginResult{}, err
	}
	if user == nil {
		return TokenLoginResult{}, NewHTTPError(http.StatusInternalServerError, "The user does not ")
	}
	if user.Banned != 0 {
		return TokenLoginResult{}, NewHTTPError(http.StatusInternalServerError, "Your account has been suspended")
	}

	_ = s.kvDelete(ctx, cacheKeyToken)
	authData, err := s.createAuthData(ctx, *user, req.IP, req.UserAgent)
	if err != nil {
		return TokenLoginResult{}, err
	}
	return TokenLoginResult{AuthData: &authData}, nil
}

func (s *DBService) GetQuickLoginURL(ctx context.Context, req QuickLoginRequest) (string, error) {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return "", err
	}

	authToken := strings.TrimSpace(req.AuthData)
	if authToken == "" {
		return "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	user, sessionID, err := s.userFromAuthToken(ctx, authToken)
	if err != nil || user == nil || sessionID == "" {
		return "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	code, err := randomMD5Token()
	if err != nil {
		return "", err
	}
	if err := s.kvSet(ctx, cacheKey(cacheTempToken, code), strconv.FormatInt(user.ID, 10), 60); err != nil {
		return "", err
	}

	redirect := strings.TrimSpace(req.Redirect)
	if redirect == "" {
		redirect = defaultDashboardRedirect
	}

	return s.buildFrontendURL(fmt.Sprintf("/#/login?verify=%s&redirect=%s", code, redirect)), nil
}

func (s *DBService) LoginWithMailLink(ctx context.Context, req LoginWithMailLinkRequest) (any, error) {
	if err := s.ensureRuntimeTables(ctx); err != nil {
		return nil, err
	}

	if !s.cfg.LoginWithMailLink {
		return nil, NewHTTPError(http.StatusNotFound, "Not Found")
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if err := validateEmail(email); err != nil {
		return nil, err
	}

	lastSendKey := cacheKey(cacheLastSendMailLogin, email)
	if _, ok, err := s.kvGet(ctx, lastSendKey); err != nil {
		return nil, err
	} else if ok {
		return nil, NewHTTPError(http.StatusInternalServerError, "Sending frequently, please try again later")
	}

	user, err := s.findUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return true, nil
	}

	code, err := randomMD5Token()
	if err != nil {
		return nil, err
	}
	if err := s.kvSet(ctx, cacheKey(cacheTempToken, code), strconv.FormatInt(user.ID, 10), 300); err != nil {
		return nil, err
	}
	if err := s.kvSet(ctx, lastSendKey, strconv.FormatInt(time.Now().Unix(), 10), 60); err != nil {
		return nil, err
	}

	redirect := strings.TrimSpace(req.Redirect)
	if redirect == "" {
		redirect = defaultDashboardRedirect
	}
	settings := s.runtimeMailSettings()
	link := joinMailURL(settings.AppURL, fmt.Sprintf("/#/login?verify=%s&redirect=%s", code, redirect))
	subject := fmt.Sprintf("%s登录确认", settings.AppName)
	_ = s.sendEmailBestEffort(ctx, email, subject, mailTemplateLogin, fmt.Sprintf("请使用以下链接登录：\n%s", link), map[string]string{
		"name":    settings.AppName,
		"url":     settings.AppURL,
		"link":    link,
		"content": fmt.Sprintf("请使用以下链接登录：\n%s", link),
	})

	return link, nil
}

func (s *DBService) ensureRuntimeTables(ctx context.Context) error {
	s.ensureOne.Do(func() {
		if s.db == nil {
			s.ensureErr = ErrUnavailable
			return
		}
		if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS v2_runtime_kv (
		k VARCHAR(191) PRIMARY KEY,
		v TEXT NOT NULL,
		expire_at BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
)`); err != nil {
			s.ensureErr = fmt.Errorf("ensure v2_runtime_kv: %w", err)
			return
		}
		if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv(expire_at)`); err != nil {
			s.ensureErr = fmt.Errorf("ensure v2_runtime_kv index: %w", err)
			return
		}
		if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS v2_auth_session (
		id BIGSERIAL PRIMARY KEY,
		user_id INTEGER NOT NULL,
		session_id VARCHAR(64) NOT NULL,
	auth_data TEXT,
	ip VARCHAR(128),
	ua TEXT,
	login_at BIGINT NOT NULL,
	expire_at BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL,
		UNIQUE (user_id, session_id)
)`); err != nil {
			s.ensureErr = fmt.Errorf("ensure v2_auth_session: %w", err)
			return
		}
		if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session(user_id)`); err != nil {
			s.ensureErr = fmt.Errorf("ensure v2_auth_session index: %w", err)
			return
		}
	})
	return s.ensureErr
}

func (s *DBService) beginTx(ctx context.Context) (*sql.Tx, error) {
	beginnerDB, ok := s.db.(beginner)
	if !ok {
		return nil, ErrUnavailable
	}
	tx, err := beginnerDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	return tx, nil
}

func (s *DBService) findUserByEmail(ctx context.Context, email string) (*userRow, error) {
	rowerDB, ok := s.db.(rower)
	if !ok {
		return nil, ErrUnavailable
	}
	row := rowerDB.QueryRowContext(
		ctx,
		`SELECT id, email, password, password_algo, password_salt, token, is_admin, is_staff, banned
FROM v2_user
WHERE email = $1
LIMIT 1`,
		email,
	)
	var user userRow
	if err := scanUserRow(row, &user); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query user by email: %w", err)
	}
	return &user, nil
}

func (s *DBService) findUserByID(ctx context.Context, id int64) (*userRow, error) {
	rowerDB, ok := s.db.(rower)
	if !ok {
		return nil, ErrUnavailable
	}
	row := rowerDB.QueryRowContext(
		ctx,
		`SELECT id, email, password, password_algo, password_salt, token, is_admin, is_staff, banned
FROM v2_user
WHERE id = $1
LIMIT 1`,
		id,
	)
	var user userRow
	if err := scanUserRow(row, &user); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query user by id: %w", err)
	}
	return &user, nil
}

func (s *DBService) emailExists(ctx context.Context, email string) (bool, error) {
	rowerDB, ok := s.db.(rower)
	if !ok {
		return false, ErrUnavailable
	}
	var exists bool
	if err := rowerDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_user WHERE email = $1)`, email).Scan(&exists); err != nil {
		return false, fmt.Errorf("check email exists: %w", err)
	}
	return exists, nil
}

func (s *DBService) resolveTryOutProfileTx(ctx context.Context, tx *sql.Tx, campaign *campaignRow, now int64) (tryOutProfile, error) {
	if campaign != nil {
		return s.campaignTryOutProfileTx(ctx, tx, now)
	}
	return s.defaultTryOutProfileTx(ctx, tx, now)
}

func (s *DBService) defaultTryOutProfileTx(ctx context.Context, tx *sql.Tx, now int64) (tryOutProfile, error) {
	if s.cfg.TryOutPlanID <= 0 {
		return tryOutProfile{}, nil
	}
	plan, err := findPlanTx(ctx, tx, s.cfg.TryOutPlanID)
	if err != nil {
		return tryOutProfile{}, err
	}
	if plan == nil {
		return tryOutProfile{}, nil
	}
	transferBytes := convertGBToBytes(plan.TransferEnable)
	expiredAt := now + int64(math.Round(s.cfg.TryOutHour*3600))
	return tryOutProfile{
		TransferEnable: transferBytes,
		DeviceLimit:    plan.DeviceLimit,
		GroupID:        plan.GroupID,
		PlanID:         sql.NullInt64{Int64: plan.ID, Valid: true},
		SpeedLimit:     plan.SpeedLimit,
		ExpiredAt:      expiredAt,
	}, nil
}

func (s *DBService) campaignTryOutProfileTx(ctx context.Context, tx *sql.Tx, now int64) (tryOutProfile, error) {
	if s.cfg.InviteTryOutPlanID <= 0 {
		return tryOutProfile{}, nil
	}
	plan, err := findPlanTx(ctx, tx, s.cfg.InviteTryOutPlanID)
	if err != nil {
		return tryOutProfile{}, err
	}
	if plan == nil {
		return tryOutProfile{}, nil
	}

	transferGB := s.cfg.InviteTryOutTransferGB
	if transferGB <= 0 {
		transferGB = plan.TransferEnable
	}
	hours := s.cfg.InviteTryOutHours
	if hours <= 0 {
		hours = 24
	}
	expiredAt := now + int64(math.Round(hours*3600))

	return tryOutProfile{
		TransferEnable: convertGBToBytes(transferGB),
		DeviceLimit:    plan.DeviceLimit,
		GroupID:        plan.GroupID,
		PlanID:         sql.NullInt64{Int64: plan.ID, Valid: true},
		SpeedLimit:     plan.SpeedLimit,
		ExpiredAt:      expiredAt,
	}, nil
}

func (s *DBService) createAuthData(ctx context.Context, user userRow, ip, userAgent string) (AuthData, error) {
	if strings.TrimSpace(s.cfg.AppKey) == "" {
		return AuthData{}, fmt.Errorf("APP_KEY is empty")
	}

	sessionID, err := randomTokenHex(16)
	if err != nil {
		return AuthData{}, err
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":      user.ID,
		"session": sessionID,
	})
	authData, err := token.SignedString([]byte(s.cfg.AppKey))
	if err != nil {
		return AuthData{}, fmt.Errorf("sign auth token: %w", err)
	}
	if err := s.saveSession(ctx, user.ID, sessionID, authData, ip, userAgent); err != nil {
		return AuthData{}, err
	}

	return AuthData{
		Token:    user.Token,
		IsAdmin:  user.IsAdmin,
		AuthData: authData,
	}, nil
}

func (s *DBService) userFromAuthToken(ctx context.Context, authToken string) (*userRow, string, error) {
	parsed, err := jwt.Parse(authToken, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method %s", token.Method.Alg())
		}
		return []byte(s.cfg.AppKey), nil
	})
	if err != nil || !parsed.Valid {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	userID, err := mapClaimInt64(claims["id"])
	if err != nil {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}
	sessionID, ok := claims["session"].(string)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	valid, err := s.sessionExists(ctx, userID, sessionID)
	if err != nil || !valid {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	user, err := s.findUserByID(ctx, userID)
	if err != nil || user == nil {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}
	if user.Banned != 0 {
		return nil, "", NewHTTPError(http.StatusForbidden, "未登录或登陆已过期")
	}

	return user, sessionID, nil
}

func (s *DBService) saveSession(ctx context.Context, userID int64, sessionID, authData, ip, ua string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_auth_session (
user_id, session_id, auth_data, ip, ua, login_at, expire_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, 0, $7, $8
)
ON CONFLICT (user_id, session_id) DO UPDATE SET
auth_data = EXCLUDED.auth_data,
ip = EXCLUDED.ip,
ua = EXCLUDED.ua,
login_at = EXCLUDED.login_at,
updated_at = EXCLUDED.updated_at`, userID, sessionID, authData, ip, ua, now, now, now)
	if err != nil {
		return fmt.Errorf("save auth session: %w", err)
	}
	return nil
}

func (s *DBService) sessionExists(ctx context.Context, userID int64, sessionID string) (bool, error) {
	rowerDB, ok := s.db.(rower)
	if !ok {
		return false, ErrUnavailable
	}
	now := time.Now().Unix()
	var exists bool
	if err := rowerDB.QueryRowContext(
		ctx,
		`SELECT EXISTS(
SELECT 1 FROM v2_auth_session
WHERE user_id = $1 AND session_id = $2 AND (expire_at = 0 OR expire_at > $3)
)`,
		userID,
		sessionID,
		now,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check auth session: %w", err)
	}
	return exists, nil
}

func (s *DBService) removeAllSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("remove auth sessions: %w", err)
	}
	return nil
}

func (s *DBService) kvGet(ctx context.Context, key string) (string, bool, error) {
	rowerDB, ok := s.db.(rower)
	if !ok {
		return "", false, ErrUnavailable
	}
	var value string
	var expireAt int64
	err := rowerDB.QueryRowContext(ctx, `SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`, key).Scan(&value, &expireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get runtime kv: %w", err)
	}
	if expireAt > 0 && expireAt <= time.Now().Unix() {
		_ = s.kvDelete(ctx, key)
		return "", false, nil
	}
	return value, true, nil
}

func (s *DBService) kvGetInt(ctx context.Context, key string) (int64, error) {
	raw, ok, err := s.kvGet(ctx, key)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return 0, err
	}
	value, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil {
		return 0, nil
	}
	return value, nil
}

func (s *DBService) kvSet(ctx context.Context, key, value string, ttlSeconds int64) error {
	expireAt := int64(0)
	now := time.Now().Unix()
	if ttlSeconds > 0 {
		expireAt = now + ttlSeconds
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_runtime_kv (
k, v, expire_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5
)
ON CONFLICT (k) DO UPDATE SET
v = EXCLUDED.v,
expire_at = EXCLUDED.expire_at,
updated_at = EXCLUDED.updated_at`, key, value, expireAt, now, now)
	if err != nil {
		return fmt.Errorf("set runtime kv: %w", err)
	}
	return nil
}

func (s *DBService) kvDelete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE k = $1`, key)
	if err != nil {
		return fmt.Errorf("delete runtime kv: %w", err)
	}
	return nil
}

func (s *DBService) kvIncr(ctx context.Context, key string, ttlSeconds int64) (int64, error) {
	return s.kvIncrWithBase(ctx, key, ttlSeconds, 1)
}

func (s *DBService) kvIncrWithBase(ctx context.Context, key string, ttlSeconds, base int64) (int64, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	now := time.Now().Unix()
	expireAt := int64(0)
	if ttlSeconds > 0 {
		expireAt = now + ttlSeconds
	}

	var raw string
	var currentExpire int64
	err = tx.QueryRowContext(ctx, `SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 FOR UPDATE`, key).Scan(&raw, &currentExpire)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("lock runtime kv: %w", err)
	}

	var next int64
	if errors.Is(err, sql.ErrNoRows) || (currentExpire > 0 && currentExpire <= now) {
		next = base
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
			key, strconv.FormatInt(next, 10), expireAt, now, now); err != nil {
			return 0, fmt.Errorf("upsert runtime kv increment: %w", err)
		}
	} else {
		current, convErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if convErr != nil {
			current = 0
		}
		next = current + 1
		if _, err := tx.ExecContext(ctx, `UPDATE v2_runtime_kv SET v = $1, updated_at = $2 WHERE k = $3`, strconv.FormatInt(next, 10), now, key); err != nil {
			return 0, fmt.Errorf("update runtime kv increment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit runtime kv increment: %w", err)
	}
	return next, nil
}

func (s *DBService) verifyRecaptcha(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.TrimSpace(s.cfg.RecaptchaKey) == "" {
		return NewHTTPError(http.StatusInternalServerError, "Invalid code is incorrect")
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.PostForm("https://www.google.com/recaptcha/api/siteverify", url.Values{
		"secret":   {s.cfg.RecaptchaKey},
		"response": {token},
	})
	if err != nil {
		return NewHTTPError(http.StatusInternalServerError, "Invalid code is incorrect")
	}
	defer resp.Body.Close()

	var payload struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return NewHTTPError(http.StatusInternalServerError, "Invalid code is incorrect")
	}
	if !payload.Success {
		return NewHTTPError(http.StatusInternalServerError, "Invalid code is incorrect")
	}
	return nil
}

func (s *DBService) sendEmailBestEffort(ctx context.Context, email, subject, templateName, body string, templateValues map[string]string) error {
	runJob := func(jobCtx context.Context) error {
		sendBody := body
		settings := s.runtimeMailSettings()
		if rendered, htmlBody, err := mailtmpl.Render(passportProjectRoot, settings.Template, templateName, templateValues); err == nil {
			sendBody = rendered
			if htmlBody {
				sendBody = strings.TrimSpace(sendBody)
			}
		}
		sendErr := s.smtpSender()(email, subject, sendBody)
		logErr := ""
		if sendErr != nil {
			logErr = sendErr.Error()
		}
		_, _ = s.db.ExecContext(jobCtx, `INSERT INTO v2_mail_log (
email, subject, template_name, error, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6
)`, email, subject, templateName, nullableString(logErr), time.Now().Unix(), time.Now().Unix())
		return sendErr
	}

	if s.jobs != nil {
		if err := s.jobs.Enqueue("send_email", templateName+":"+email, runJob); err == nil {
			return nil
		}
	}

	return runJob(ctx)
}

func (s *DBService) sendSMTP(to, subject, body string) error {
	settings := s.runtimeMailSettings()
	host := settings.Host
	port := settings.Port
	from := settings.From
	if host == "" || port <= 0 || from == "" {
		return fmt.Errorf("mail config incomplete")
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	headerFrom := from
	if settings.FromName != "" {
		headerFrom = fmt.Sprintf("%s <%s>", settings.FromName, from)
	}

	msg := strings.Builder{}
	msg.WriteString(fmt.Sprintf("From: %s\r\n", headerFrom))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: " + smtpContentType(body) + "\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)
	msg.WriteString("\r\n")

	if strings.EqualFold(settings.Encryption, "ssl") {
		conn, err := tls.Dial("tcp", addr, &tls.Config{
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

		if settings.Username != "" {
			if err := client.Auth(smtp.PlainAuth("", settings.Username, settings.Password, host)); err != nil {
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
		if _, err := writer.Write([]byte(msg.String())); err != nil {
			return err
		}
		return writer.Close()
	}

	var auth smtp.Auth
	if settings.Username != "" {
		auth = smtpcompat.PlainAuth("", settings.Username, settings.Password, host, smtpcompat.AllowInsecureAuth(settings.Encryption))
	}
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg.String()))
}

func (s *DBService) smtpSender() func(to, subject, body string) error {
	if s.mailSender != nil {
		return s.mailSender
	}
	return s.sendSMTP
}

func (s *DBService) buildFrontendURL(path string) string {
	appURL := strings.TrimSpace(s.cfg.AppURL)
	if appURL == "" {
		return path
	}
	return strings.TrimRight(appURL, "/") + path
}

func cacheKey(name, unique string) string {
	return name + "_" + unique
}

type runtimeMailSettings struct {
	Host       string
	Port       int64
	Username   string
	Password   string
	Encryption string
	From       string
	FromName   string
	Template   string
	AppName    string
	AppURL     string
}

func (s *DBService) runtimeMailSettings() runtimeMailSettings {
	settings := runtimeMailSettings{
		Host:       strings.TrimSpace(s.cfg.MailHost),
		Port:       s.cfg.MailPort,
		Username:   strings.TrimSpace(s.cfg.MailUsername),
		Password:   s.cfg.MailPassword,
		Encryption: strings.TrimSpace(s.cfg.MailEncryption),
		From:       strings.TrimSpace(s.cfg.MailFromAddress),
		FromName:   strings.TrimSpace(s.cfg.MailFromName),
		Template:   "default",
		AppName:    strings.TrimSpace(s.cfg.AppName),
		AppURL:     strings.TrimSpace(s.cfg.AppURL),
	}

	if values, err := loadPassportAdminJSONValues(); err == nil {
		if host := strings.TrimSpace(passportStringValue(values["email_host"])); host != "" {
			settings.Host = host
		}
		if port := passportInt64Value(values["email_port"]); port > 0 {
			settings.Port = port
		}
		if username := strings.TrimSpace(passportStringValue(values["email_username"])); username != "" {
			settings.Username = username
		}
		if password := passportStringValue(values["email_password"]); password != "" {
			settings.Password = password
		}
		if encryption := strings.TrimSpace(passportStringValue(values["email_encryption"])); encryption != "" {
			settings.Encryption = encryption
		}
		if from := strings.TrimSpace(passportStringValue(values["email_from_address"])); from != "" {
			settings.From = from
		}
		if fromName := strings.TrimSpace(passportStringValue(values["email_from_name"])); fromName != "" {
			settings.FromName = fromName
		}
		if template := strings.TrimSpace(passportStringValue(values["email_template"])); template != "" {
			settings.Template = template
		}
		if appName := strings.TrimSpace(passportStringValue(values["app_name"])); appName != "" {
			settings.AppName = appName
		}
		if appURL := strings.TrimSpace(passportStringValue(values["app_url"])); appURL != "" {
			settings.AppURL = appURL
		}
	}

	if settings.Port <= 0 {
		settings.Port = 25
	}
	if settings.Template == "" {
		settings.Template = "default"
	}
	if settings.AppName == "" {
		settings.AppName = "V2Board"
	}
	if settings.FromName == "" || settings.FromName == "forest-go-api" || settings.FromName == "V2Board" {
		settings.FromName = settings.AppName
	}
	return settings
}

func joinMailURL(baseURL, path string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return path
	}
	return strings.TrimRight(baseURL, "/") + path
}

func smtpContentType(body string) string {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<body") || strings.Contains(lower, "<div") || strings.Contains(lower, "<table") {
		return "text/html; charset=UTF-8"
	}
	return "text/plain; charset=UTF-8"
}

func detectPassportProjectRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func loadPassportAdminJSONValues() (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(passportProjectRoot, "config", "admin.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func passportStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		if value == nil {
			return ""
		}
		return fmt.Sprint(value)
	}
}

func passportInt64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func validateEmail(email string) error {
	if strings.TrimSpace(email) == "" {
		return NewHTTPError(http.StatusInternalServerError, "Email can not be empty")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed == nil || parsed.Address != email {
		return NewHTTPError(http.StatusInternalServerError, "Email format is incorrect")
	}
	return nil
}

func emailSuffixAllowed(email string, whitelist []string) bool {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	suffix := strings.ToLower(strings.TrimSpace(parts[1]))
	for _, item := range whitelist {
		if suffix == strings.ToLower(strings.TrimSpace(item)) {
			return true
		}
	}
	return false
}

func gmailAlias(email string) bool {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	if strings.ToLower(parts[1]) != "gmail.com" {
		return false
	}
	prefix := parts[0]
	return strings.Contains(prefix, ".") || strings.Contains(prefix, "+")
}

func verifyPassword(algo sql.NullString, salt sql.NullString, password, hash string) bool {
	switch strings.ToLower(strings.TrimSpace(algo.String)) {
	case "md5":
		sum := md5.Sum([]byte(password))
		return hex.EncodeToString(sum[:]) == hash
	case "sha256":
		return sha256Hex(password) == hash
	case "md5salt":
		sum := md5.Sum([]byte(password + salt.String))
		return hex.EncodeToString(sum[:]) == hash
	default:
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
}

func sha256Hex(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

func randomDigits(length int) string {
	if length <= 0 {
		return ""
	}
	max := int64(1)
	for i := 0; i < length; i++ {
		max *= 10
	}
	n := cryptoRandInt(max)
	format := fmt.Sprintf("%%0%dd", length)
	return fmt.Sprintf(format, n)
}

func randomTokenHex(byteLength int) (string, error) {
	if byteLength <= 0 {
		return "", fmt.Errorf("invalid random byte length")
	}
	buf := make([]byte, byteLength)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", fmt.Errorf("generate uuid bytes: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32]), nil
}

func randomMD5Token() (string, error) {
	seed, err := randomTokenHex(32)
	if err != nil {
		return "", err
	}
	sum := md5.Sum([]byte(fmt.Sprintf("%s-%d", seed, time.Now().UnixNano())))
	return hex.EncodeToString(sum[:]), nil
}

func convertGBToBytes(gb float64) int64 {
	if gb <= 0 {
		return 0
	}
	return int64(math.Round(gb * float64(bytesPerGB)))
}

func nullInt64ToAny(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func mapClaimInt64(value any) (int64, error) {
	switch v := value.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case json.Number:
		return v.Int64()
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported claim type %T", value)
	}
}

func cryptoRandInt(max int64) int64 {
	if max <= 0 {
		return 0
	}
	limit := make([]byte, 8)
	if _, err := crand.Read(limit); err != nil {
		return time.Now().UnixNano() % max
	}
	value := int64(limit[0])<<56 |
		int64(limit[1])<<48 |
		int64(limit[2])<<40 |
		int64(limit[3])<<32 |
		int64(limit[4])<<24 |
		int64(limit[5])<<16 |
		int64(limit[6])<<8 |
		int64(limit[7])
	if value < 0 {
		value = -value
	}
	return value % max
}

func scanUserRow(scanner interface{ Scan(...any) error }, out *userRow) error {
	return scanner.Scan(
		&out.ID,
		&out.Email,
		&out.Password,
		&out.PasswordAlgo,
		&out.PasswordSalt,
		&out.Token,
		&out.IsAdmin,
		&out.IsStaff,
		&out.Banned,
	)
}

func findInviteCodeTx(ctx context.Context, tx *sql.Tx, code string) (*inviteCodeRow, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, user_id, code FROM v2_invite_code WHERE code = $1 AND status = 0 LIMIT 1`, code)
	var invite inviteCodeRow
	if err := row.Scan(&invite.ID, &invite.UserID, &invite.Code); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query invite code: %w", err)
	}
	invite.Code = strings.TrimSpace(invite.Code)
	return &invite, nil
}

func findActiveCampaignTx(ctx context.Context, tx *sql.Tx, userID int64, inviteCode string, now int64) (*campaignRow, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, user_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, expired_at
FROM v2_invite_campaign
WHERE user_id = $1 AND invite_code = $2 AND status IN (0,1)
ORDER BY id DESC
LIMIT 1`, userID, inviteCode)
	var campaign campaignRow
	if err := row.Scan(
		&campaign.ID,
		&campaign.UserID,
		&campaign.InviteCode,
		&campaign.RewardAmount,
		&campaign.TargetAmount,
		&campaign.CurrentAmount,
		&campaign.InviteCount,
		&campaign.Status,
		&campaign.ExpiredAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query invite campaign: %w", err)
	}
	campaign.InviteCode = strings.TrimSpace(campaign.InviteCode)

	if campaign.Status != 0 {
		return nil, nil
	}
	if campaign.ExpiredAt <= now {
		return nil, nil
	}
	return &campaign, nil
}

func findPlanTx(ctx context.Context, tx *sql.Tx, planID int64) (*planRow, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, transfer_enable, device_limit, group_id, speed_limit FROM v2_plan WHERE id = $1 LIMIT 1`, planID)
	var plan planRow
	var transfer sql.NullFloat64
	if err := row.Scan(&plan.ID, &transfer, &plan.DeviceLimit, &plan.GroupID, &plan.SpeedLimit); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query plan: %w", err)
	}
	if transfer.Valid {
		plan.TransferEnable = transfer.Float64
	}
	return &plan, nil
}

func accrueInviteRegistrationTx(ctx context.Context, tx *sql.Tx, campaign *campaignRow, inviteCode string, inviteeUserID int64, now int64) error {
	row := tx.QueryRowContext(ctx, `SELECT status, target_amount, current_amount, invite_count, reward_amount, expired_at
FROM v2_invite_campaign
WHERE id = $1
FOR UPDATE`, campaign.ID)
	var status, targetAmount, currentAmount, inviteCount, rewardAmount, expiredAt int64
	if err := row.Scan(&status, &targetAmount, &currentAmount, &inviteCount, &rewardAmount, &expiredAt); err != nil {
		return fmt.Errorf("lock invite campaign: %w", err)
	}
	if status != 0 || expiredAt <= now {
		return nil
	}

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_invite_campaign_record WHERE campaign_id = $1 AND invitee_user_id = $2)`, campaign.ID, inviteeUserID).Scan(&exists); err != nil {
		return fmt.Errorf("check invite campaign record: %w", err)
	}
	if exists {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_invite_campaign_record (
campaign_id, invitee_user_id, invite_code, reward_amount, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6
)`, campaign.ID, inviteeUserID, inviteCode, rewardAmount, now, now); err != nil {
		return fmt.Errorf("insert invite campaign record: %w", err)
	}

	newCurrent := currentAmount + rewardAmount
	if newCurrent > targetAmount {
		newCurrent = targetAmount
	}
	newInviteCount := inviteCount + 1
	newStatus := status
	var completedAt any
	if newCurrent >= targetAmount {
		newStatus = 1
		completedAt = now
	}

	if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign
SET current_amount = $1,
invite_count = $2,
status = $3,
completed_at = COALESCE($4, completed_at),
updated_at = $5
WHERE id = $6`, newCurrent, newInviteCount, newStatus, completedAt, now, campaign.ID); err != nil {
		return fmt.Errorf("update invite campaign progress: %w", err)
	}
	return nil
}
