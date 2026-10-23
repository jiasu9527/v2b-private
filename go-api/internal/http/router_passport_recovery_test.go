package httpapi

import (
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/queue"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRouterPassportSendEmailVerifyRecoveryIntent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		wantPresent bool
		wantIntent  int
	}{
		{"json number", "application/json", `{"email":"legacy@example.com","isforget":1}`, true, 1},
		{"json string", "application/json", `{"email":"legacy@example.com","isforget":"1"}`, true, 1},
		{"form recovery", "application/x-www-form-urlencoded", "email=legacy%40example.com&isforget=1", true, 1},
		{"registration", "application/json", `{"email":"legacy@example.com","isforget":0}`, true, 0},
		{"omitted", "application/json", `{"email":"legacy@example.com"}`, false, 0},
		{"null", "application/json", `{"email":"legacy@example.com","isforget":null}`, false, 0},
		{"boolean", "application/json", `{"email":"legacy@example.com","isforget":true}`, true, -1},
		{"invalid", "application/json", `{"email":"legacy@example.com","isforget":"forgot"}`, true, -1},
		{"boolean false", "application/json", `{"email":"legacy@example.com","isforget":false}`, true, -1},
		{"empty", "application/json", `{"email":"legacy@example.com","isforget":""}`, true, -1},
		{"decimal", "application/json", `{"email":"legacy@example.com","isforget":1.0}`, true, -1},
		{"array", "application/json", `{"email":"legacy@example.com","isforget":[]}`, true, -1},
		{"object", "application/json", `{"email":"legacy@example.com","isforget":{}}`, true, -1},
		{"overflow", "application/json", `{"email":"legacy@example.com","isforget":999999999999999999999}`, true, -1},
		{"empty form", "application/x-www-form-urlencoded", "email=legacy%40example.com&isforget=", true, -1},
		{"other intent", "application/json", `{"email":"legacy@example.com","isforget":2}`, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakePassportService{}
			router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/passport/comm/sendEmailVerify", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			got := service.lastSendVerify
			if got.Email != "legacy@example.com" || got.HasIsForget != tc.wantPresent || got.IsForget != tc.wantIntent {
				t.Fatalf("unexpected recovery intent: %#v", got)
			}
		})
	}
}

type recoveryHandlerQueue struct {
	jobs []string
}

func (q *recoveryHandlerQueue) Enqueue(queueName, jobName string, _ queue.JobFunc) error {
	// Do not execute jobs: these tests must never connect to a real SMTP server.
	q.jobs = append(q.jobs, queueName+":"+jobName)
	return nil
}

func (*recoveryHandlerQueue) Snapshot() queue.Snapshot { return queue.Snapshot{} }

type recoveryHandlerSQLValue func(driver.Value) bool

func (match recoveryHandlerSQLValue) Match(value driver.Value) bool { return match(value) }

func TestPassportRecoveryHTTPWithDBService(t *testing.T) {
	for _, tc := range []struct {
		name        string
		intent      string
		form        bool
		exists      bool
		guard       string
		wantStatus  int
		wantMessage string
	}{
		{"legacy JSON omitted", "", false, true, "", 200, ""},
		{"legacy JSON null", `,"isforget":null`, false, true, "", 200, ""},
		{"explicit JSON recovery", `,"isforget":1`, false, true, "", 200, ""},
		{"explicit JSON string recovery", `,"isforget":"1"`, false, true, "", 200, ""},
		{"legacy form omitted", "", true, true, "", 200, ""},
		{"explicit form recovery", "&isforget=1", true, true, "", 200, ""},
		{"unknown legacy email", "", false, false, "", 500, "邮箱后缀不在白名单中"},
		{"unknown null intent", `,"isforget":null`, false, false, "", 500, "邮箱后缀不在白名单中"},
		{"unknown explicit recovery", `,"isforget":1`, false, false, "", 500, "邮箱后缀不在白名单中"},
		{"new registration", `,"isforget":0`, false, false, "", 500, "邮箱后缀不在白名单中"},
		{"existing registration", `,"isforget":0`, false, true, "", 500, "邮箱后缀不在白名单中"},
		{"other intent", `,"isforget":2`, false, true, "", 500, "邮箱后缀不在白名单中"},
		{"invalid string", `,"isforget":"forgot"`, false, true, "", 500, "邮箱后缀不在白名单中"},
		{"invalid boolean", `,"isforget":true`, false, true, "", 500, "邮箱后缀不在白名单中"},
		{"invalid empty form", "&isforget=", true, true, "", 500, "邮箱后缀不在白名单中"},
		{"legacy email cooldown", "", false, true, "cooldown", 500, "邮箱验证码已发送，请稍后再试"},
		{"legacy IP rate limit", "", false, true, "ip", 429, "请求过于频繁，请稍后再试"},
		{"legacy captcha required", "", false, true, "captcha", 500, "验证码错误"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			jobs := &recoveryHandlerQueue{}
			service := passport.NewDBServiceWithConfig(config.Config{
				EmailWhitelistEnabled: true,
				EmailWhitelist:        []string{"allowed.example"},
				Recaptcha:             tc.guard == "captcha",
				RecaptchaKey:          "test-key",
			}, db).WithQueueRuntime(jobs)
			mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_runtime_kv`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_auth_session`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id`).WillReturnResult(sqlmock.NewResult(0, 0))

			const email = "legacy@old.example"
			const ipKey = "SEND_EMAIL_VERIFY_RATE_LIMIT_IP_192.0.2.10"
			before := time.Now().Unix()
			expiry := func(ttl int64) sqlmock.Argument {
				return recoveryHandlerSQLValue(func(value driver.Value) bool {
					v, ok := value.(int64)
					return ok && v >= before+ttl && v <= time.Now().Unix()+ttl
				})
			}
			mock.ExpectBegin()
			ipRows := sqlmock.NewRows([]string{"v", "expire_at"})
			if tc.guard == "ip" {
				ipRows.AddRow("3", before+60)
			}
			mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 FOR UPDATE`).
				WithArgs(ipKey).WillReturnRows(ipRows)
			if tc.guard == "ip" {
				mock.ExpectExec(`UPDATE v2_runtime_kv SET v = \$1, updated_at = \$2 WHERE k = \$3`).
					WithArgs("4", sqlmock.AnyArg(), ipKey).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
					WithArgs(ipKey, "1", expiry(60), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			if tc.guard != "ip" && tc.guard != "captcha" {
				mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_user WHERE LOWER\(email\) = \$1\)`).
					WithArgs(email).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.exists))
			}
			if tc.wantStatus == http.StatusOK || tc.guard == "cooldown" {
				cooldownRows := sqlmock.NewRows([]string{"v", "expire_at"})
				if tc.guard == "cooldown" {
					cooldownRows.AddRow("last-send", before+60)
				}
				mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
					WithArgs("LAST_SEND_EMAIL_VERIFY_TIMESTAMP_" + email).WillReturnRows(cooldownRows)
			}
			if tc.wantStatus == http.StatusOK {
				mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
					WithArgs("EMAIL_VERIFY_CODE_"+email, recoveryHandlerSQLValue(func(value driver.Value) bool {
						code, ok := value.(string)
						return ok && regexp.MustCompile(`^[0-9]{6}$`).MatchString(code)
					}), expiry(300), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`INSERT INTO v2_runtime_kv`).
					WithArgs("LAST_SEND_EMAIL_VERIFY_TIMESTAMP_"+email, sqlmock.AnyArg(), expiry(60), sqlmock.AnyArg(), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}

			body := `{"email":" Legacy@OLD.Example "` + tc.intent + `}`
			contentType := "application/json"
			if tc.form {
				body = "email=Legacy%40OLD.Example" + tc.intent
				contentType = "application/x-www-form-urlencoded"
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/passport/comm/sendEmailVerify", strings.NewReader(body))
			req.RemoteAddr = "192.0.2.10:54321"
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			if handled := handlePassportSendEmailVerify(rec, req, service); !handled {
				t.Fatal("request was not handled")
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected HTTP %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if !strings.Contains(rec.Body.String(), `"data":true`) || len(jobs.jobs) != 1 || jobs.jobs[0] != "send_email:verify:"+email {
					t.Fatalf("expected one queued verification email, got response=%s jobs=%v", rec.Body.String(), jobs.jobs)
				}
			} else if !strings.Contains(rec.Body.String(), tc.wantMessage) || len(jobs.jobs) != 0 {
				t.Fatalf("expected rejection %q without email, got response=%s jobs=%v", tc.wantMessage, rec.Body.String(), jobs.jobs)
			}
		})
	}
}
