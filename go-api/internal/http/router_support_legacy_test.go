package httpapi

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestLegacyStaffHandlersAlwaysApplyProtectedAccountScope(t *testing.T) {
	for _, action := range []string{"getUserInfoById", "update", "ban", "unban", "sendMail"} {
		t.Run(action, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			for _, statement := range []string{`SELECT pg_advisory_xact_lock`, `CREATE TABLE IF NOT EXISTS v2_support_staff`, `CREATE TABLE IF NOT EXISTS v2_support_request_limit`, `CREATE TABLE IF NOT EXISTS v2_support_entry_override`, `CREATE INDEX IF NOT EXISTS v2_support_entry_override_target_idx`, `CREATE TABLE IF NOT EXISTS v2_support_operation`, `CREATE INDEX IF NOT EXISTS v2_support_operation_actor_idx`, `CREATE INDEX IF NOT EXISTS v2_support_operation_created_idx`} {
				mock.ExpectExec(statement).WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectCommit()
			want := http.StatusInternalServerError
			switch action {
			case "getUserInfoById", "update":
				mock.ExpectQuery(`(?s)SELECT row_to_json.*WHERE u.id = \$1 AND u.is_admin = 0 AND u.is_staff = 0.*NOT EXISTS \(SELECT 1 FROM v2_support_staff support WHERE support.user_id = u.id\)`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"user"}))
			case "ban", "unban":
				mock.ExpectQuery(`(?s)SELECT id FROM v2_user u WHERE.*NOT EXISTS \(SELECT 1 FROM v2_support_staff support WHERE support.user_id = u.id\)`).WithArgs(int64(0), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
				want = http.StatusOK
			case "sendMail":
				mock.ExpectQuery(`(?s)SELECT email FROM v2_user u WHERE.*NOT EXISTS \(SELECT 1 FROM v2_support_staff support WHERE support.user_id = u.id\)`).WithArgs(int64(0), int64(0)).WillReturnError(sql.ErrConnDone)
			}
			router := NewRouter(config.Config{AdminPath: "test-admin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 2, IsStaff: 1}}), WithAdminService(admin.NewDBService(config.Config{}, db)))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/user/"+action, strings.NewReader("auth_data=staff&id=7&subject=notice&content=message"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Fatalf("got %d want %d: %s", rec.Code, want, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
