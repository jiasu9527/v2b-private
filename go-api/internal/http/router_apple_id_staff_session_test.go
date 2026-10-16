package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/appleid"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
)

// Exercise the production session implementation, not a fake Fresh method: a
// previously valid cached employee must not retain Apple ID module privileges.
func TestAppleIDOperatorUsesDatabaseSessionDespiteCachedStaff(t *testing.T) {
	for _, tc := range []struct {
		name        string
		exists      bool
		currentID   int64
		isAdmin     int64
		isStaff     int64
		databaseErr error
		wantStatus  int
	}{
		{name: "current employee can cancel", exists: true, currentID: 77, isStaff: 1, wantStatus: http.StatusOK},
		{name: "promoted administrator can cancel", exists: true, currentID: 77, isAdmin: 1, wantStatus: http.StatusOK},
		{name: "removed employee flag rejects cached role", exists: true, currentID: 77, wantStatus: http.StatusForbidden},
		{name: "revoked banned or expired session rejects cached role", wantStatus: http.StatusForbidden},
		{name: "database unavailable fails closed", databaseErr: errors.New("fixture database unavailable"), wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := config.Config{AdminPath: "localadmin", AppKey: "apple-staff-real-session-fixture-key"}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"id": int64(77), "session": "apple-staff-session"}).SignedString([]byte(cfg.AppKey))
			if err != nil {
				t.Fatal(err)
			}
			cache := session.NewAuthCache(time.Minute)
			cache.Store(token, "apple-staff-session", &session.Identity{ID: 77, Email: "cached-staff@example.test", IsStaff: 1})
			sessions := session.NewDBService(cfg, db).WithAuthCache(cache)
			// Prove the old cached identity is usable without touching SQL. The
			// Apple route must still query the current session and role below.
			cached, err := sessions.Authenticate(context.Background(), token, false)
			if err != nil || cached == nil || cached.IsStaff != 1 {
				t.Fatalf("cached fixture missing: %+v %v", cached, err)
			}
			const identityQuery = `SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`
			// The exact predicate guards both bans and session expiry; no cached
			// user flags or client-provided actor ID can replace this lookup.
			expected := mock.ExpectQuery(regexp.QuoteMeta(identityQuery)).WithArgs(int64(77), "apple-staff-session", sqlmock.AnyArg())
			if tc.databaseErr != nil {
				expected.WillReturnError(tc.databaseErr)
			} else {
				rows := sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"})
				if tc.exists {
					rows.AddRow(tc.currentID, "current-operator@example.test", tc.isAdmin, tc.isStaff)
				}
				expected.WillReturnRows(rows)
			}
			service := &fakeAppleIDService{}
			router := NewRouter(cfg, WithSessionService(sessions), WithAppleIDServices(service, service))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/apple-id/order/cancel", strings.NewReader(`{"id":11,"reason":"customer request","admin_id":99,"actor_user_id":99}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				want := appleid.AdminCancelOrderRequest{OrderID: 11, AdminID: 77, Reason: "customer request"}
				if service.adminCalls != 1 || service.lastCancel != want {
					t.Fatalf("wrong actor or business call count: calls=%d request=%+v", service.adminCalls, service.lastCancel)
				}
			} else if service.adminCalls != 0 || service.lastCancel != (appleid.AdminCancelOrderRequest{}) {
				t.Fatalf("unauthorized request reached business service: calls=%d request=%+v", service.adminCalls, service.lastCancel)
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("operator response must not be cacheable")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
