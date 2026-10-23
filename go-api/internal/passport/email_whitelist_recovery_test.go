package passport

import (
	"context"
	"database/sql/driver"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
)

type recoverySQLValue func(driver.Value) bool

func (match recoverySQLValue) Match(value driver.Value) bool { return match(value) }

func newRecoveryWhitelistService(t *testing.T) (*DBService, sqlmock.Sqlmock, *capturePassportQueue) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("SQL expectations: %v", err)
		}
		_ = db.Close()
	})
	jobs := &capturePassportQueue{}
	service := NewDBServiceWithConfig(config.Config{
		EmailWhitelistEnabled: true,
		EmailWhitelist:        []string{"allowed.example"},
	}, db).WithQueueRuntime(jobs)
	service.ensureOne.Do(func() {})
	return service, mock, jobs
}

func expectRecoveryEmailExists(mock sqlmock.Sqlmock, email string, exists bool) {
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_user WHERE LOWER\(email\) = \$1\)`).
		WithArgs(email).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
}

func expectRecoveryKV(mock sqlmock.Sqlmock, key, value string) {
	rows := sqlmock.NewRows([]string{"v", "expire_at"})
	if value != "" {
		rows.AddRow(value, time.Now().Add(time.Minute).Unix())
	}
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(key).WillReturnRows(rows)
}

func recoveryExpiry(ttlSeconds int64) sqlmock.Argument {
	before := time.Now().Unix()
	return recoverySQLValue(func(value driver.Value) bool {
		expiry, ok := value.(int64)
		return ok && expiry >= before+ttlSeconds && expiry <= time.Now().Unix()+ttlSeconds
	})
}

func TestEmailWhitelistRecoveryExistingUserCanSendCodeAndResetPassword(t *testing.T) {
	service, mock, jobs := newRecoveryWhitelistService(t)
	const email = "legacy@old.example"
	expectRecoveryEmailExists(mock, email, true)
	expectRecoveryKV(mock, cacheKey(cacheLastSendEmailVerify, email), "")
	var code string
	mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
		WithArgs(cacheKey(cacheEmailVerifyCode, email), recoverySQLValue(func(value driver.Value) bool {
			var ok bool
			code, ok = value.(string)
			return ok && isSixDigitCode(code)
		}), recoveryExpiry(300), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
		WithArgs(cacheKey(cacheLastSendEmailVerify, email), sqlmock.AnyArg(), recoveryExpiry(60), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := service.SendEmailVerify(context.Background(), SendEmailVerifyRequest{
		Email: " Legacy@OLD.Example ", HasIsForget: true, IsForget: 1,
	}); err != nil {
		t.Fatalf("existing user outside registration whitelist must receive recovery code: %v", err)
	}
	if len(jobs.queueNames) != 1 || jobs.queueNames[0] != "send_email" {
		t.Fatalf("expected one verification email, got %v", jobs.queueNames)
	}

	expectRecoveryKV(mock, cacheKey(cacheForgetRequestLimit, email), "")
	expectRecoveryKV(mock, cacheKey(cacheEmailVerifyCode, email), code)
	mock.ExpectQuery(`SELECT id, email, password, password_algo, password_salt, token, is_admin, is_staff, banned\s+FROM v2_user\s+WHERE LOWER\(email\) = \$1\s+LIMIT 1`).
		WithArgs(email).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password", "password_algo", "password_salt", "token", "is_admin", "is_staff", "banned"}).
			AddRow(int64(21), "Legacy@OLD.Example", "old-hash", nil, nil, "token-21", int64(0), int64(0), int64(0)))
	const newPassword = "new-password123"
	mock.ExpectExec(`UPDATE v2_user SET password = \$1, password_algo = NULL, password_salt = NULL, updated_at = \$2 WHERE id = \$3`).
		WithArgs(recoverySQLValue(func(value driver.Value) bool {
			hash, ok := value.(string)
			return ok && bcrypt.CompareHashAndPassword([]byte(hash), []byte(newPassword)) == nil
		}), sqlmock.AnyArg(), int64(21)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_runtime_kv WHERE k = \$1`).
		WithArgs(cacheKey(cacheEmailVerifyCode, email)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id = \$1`).
		WithArgs(int64(21)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := service.Forget(context.Background(), ForgetRequest{
		Email: " Legacy@OLD.Example ", Password: newPassword, EmailCode: code,
	}); err != nil {
		t.Fatalf("reset password using recovery code: %v", err)
	}
}

func TestEmailWhitelistRecoveryRejectsOtherVerificationFlows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		email     string
		exists    bool
		hasForget bool
		forget    int
		wantError string
	}{
		{"new registration", "new@old.example", false, true, 0, "Email suffix is not in the Whitelist"},
		{"existing registration", "legacy@old.example", true, true, 0, "Email suffix is not in the Whitelist"},
		{"default existing user", "legacy@old.example", true, false, 0, "Email suffix is not in the Whitelist"},
		{"default new user", "new@old.example", false, false, 0, "Email suffix is not in the Whitelist"},
		{"missing explicit flag", "legacy@old.example", true, false, 1, "Email suffix is not in the Whitelist"},
		{"invalid recovery flag", "legacy@old.example", true, true, 2, "Email suffix is not in the Whitelist"},
		{"unknown non-whitelisted recovery", "new@old.example", false, true, 1, "Email suffix is not in the Whitelist"},
		{"unknown whitelisted recovery", "new@allowed.example", false, true, 1, "This email is not registered in the system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, mock, jobs := newRecoveryWhitelistService(t)
			expectRecoveryEmailExists(mock, tc.email, tc.exists)
			err := service.SendEmailVerify(context.Background(), SendEmailVerifyRequest{
				Email: tc.email, HasIsForget: tc.hasForget, IsForget: tc.forget,
			})
			if err == nil || err.Error() != tc.wantError {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
			if len(jobs.queueNames) != 0 {
				t.Fatalf("rejected request sent email: %v", jobs.queueNames)
			}
		})
	}
}

func TestEmailWhitelistRecoveryDoesNotBypassDirectRegistration(t *testing.T) {
	service, _, jobs := newRecoveryWhitelistService(t)
	_, err := service.Register(context.Background(), RegisterRequest{
		Email: "legacy@old.example", Password: "password123", EmailCode: "123456",
	})
	if err == nil || err.Error() != "Email suffix is not in the Whitelist" {
		t.Fatalf("direct registration must still enforce whitelist: %v", err)
	}
	if len(jobs.queueNames) != 0 {
		t.Fatalf("rejected registration sent email: %v", jobs.queueNames)
	}
}

func TestEmailWhitelistRecoveryKeepsEmailCooldown(t *testing.T) {
	service, mock, jobs := newRecoveryWhitelistService(t)
	const email = "legacy@old.example"
	expectRecoveryEmailExists(mock, email, true)
	expectRecoveryKV(mock, cacheKey(cacheLastSendEmailVerify, email), strconv.FormatInt(time.Now().Unix(), 10))
	err := service.SendEmailVerify(context.Background(), SendEmailVerifyRequest{
		Email: email, HasIsForget: true, IsForget: 1,
	})
	if err == nil || err.Error() != "Email verification code has been sent, please request again later" {
		t.Fatalf("expected existing email cooldown, got %v", err)
	}
	if len(jobs.queueNames) != 0 {
		t.Fatalf("cooldown request sent email: %v", jobs.queueNames)
	}
}

func TestEmailWhitelistRecoveryKeepsIPRateLimit(t *testing.T) {
	service, mock, jobs := newRecoveryWhitelistService(t)
	const ip = "192.0.2.10"
	key := cacheKey(cacheSendVerifyRateLimitIP, ip)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 FOR UPDATE`).
		WithArgs(key).WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow("3", time.Now().Add(time.Minute).Unix()))
	mock.ExpectExec(`UPDATE v2_runtime_kv SET v = \$1, updated_at = \$2 WHERE k = \$3`).
		WithArgs("4", sqlmock.AnyArg(), key).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	err := service.SendEmailVerify(context.Background(), SendEmailVerifyRequest{
		Email: "legacy@old.example", HasIsForget: true, IsForget: 1, IP: ip,
	})
	if !IsHTTPStatus(err, http.StatusTooManyRequests) {
		t.Fatalf("fourth request in the IP window must be rate limited: %v", err)
	}
	if len(jobs.queueNames) != 0 {
		t.Fatalf("rate-limited request sent email: %v", jobs.queueNames)
	}
}

func TestEmailWhitelistRecoveryKeepsRecaptcha(t *testing.T) {
	service, _, jobs := newRecoveryWhitelistService(t)
	service.cfg.Recaptcha = true
	service.cfg.RecaptchaKey = "test-key"
	err := service.SendEmailVerify(context.Background(), SendEmailVerifyRequest{
		Email: "legacy@old.example", HasIsForget: true, IsForget: 1,
	})
	if err == nil || err.Error() != "Invalid code is incorrect" {
		t.Fatalf("missing recaptcha must still be rejected: %v", err)
	}
	if len(jobs.queueNames) != 0 {
		t.Fatalf("missing-recaptcha request sent email: %v", jobs.queueNames)
	}
}

func TestEmailWhitelistRecoveryKeepsGmailAliasRule(t *testing.T) {
	service, mock, jobs := newRecoveryWhitelistService(t)
	service.cfg.EmailGmailLimitEnabled = true
	const email = "legacy+tag@gmail.com"
	expectRecoveryEmailExists(mock, email, true)
	err := service.SendEmailVerify(context.Background(), SendEmailVerifyRequest{
		Email: email, HasIsForget: true, IsForget: 1,
	})
	if err == nil || err.Error() != "Gmail alias is not supported" {
		t.Fatalf("whitelist exception must not bypass Gmail alias restriction: %v", err)
	}
	if len(jobs.queueNames) != 0 {
		t.Fatalf("Gmail alias request sent email: %v", jobs.queueNames)
	}
}

func TestEmailWhitelistRecoveryKeepsIncorrectCodeValidation(t *testing.T) {
	service, mock, _ := newRecoveryWhitelistService(t)
	const email = "legacy@old.example"
	expectRecoveryKV(mock, cacheKey(cacheForgetRequestLimit, email), "")
	expectRecoveryKV(mock, cacheKey(cacheEmailVerifyCode, email), "123456")
	mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
		WithArgs(cacheKey(cacheForgetRequestLimit, email), "1", recoveryExpiry(300), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	err := service.Forget(context.Background(), ForgetRequest{
		Email: email, Password: "password123", EmailCode: "654321",
	})
	if err == nil || !strings.Contains(err.Error(), "Incorrect email verification code") {
		t.Fatalf("incorrect recovery code must not change password: %v", err)
	}
}
