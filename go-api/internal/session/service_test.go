package session

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
)

func TestDBServiceAuthenticateCachesAuthToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	cache := NewAuthCache(time.Minute)
	service := NewDBService(cfg, db).WithAuthCache(cache)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "demo@example.com", int64(0), int64(0)))

	first, err := service.Authenticate(context.Background(), authToken, false)
	if err != nil {
		t.Fatalf("first authenticate: %v", err)
	}
	second, err := service.Authenticate(context.Background(), authToken, false)
	if err != nil {
		t.Fatalf("second authenticate: %v", err)
	}

	if first.Email != "demo@example.com" || second.Email != "demo@example.com" {
		t.Fatalf("unexpected identities: %#v %#v", first, second)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestDBServiceAuthenticateReloadsAfterUserInvalidation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	cache := NewAuthCache(time.Minute)
	service := NewDBService(cfg, db).WithAuthCache(cache)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")
	query := regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)

	mock.ExpectQuery(query).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "demo@example.com", int64(0), int64(0)))

	if _, err := service.Authenticate(context.Background(), authToken, false); err != nil {
		t.Fatalf("first authenticate: %v", err)
	}

	cache.InvalidateUser(9)

	mock.ExpectQuery(query).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "reloaded@example.com", int64(0), int64(0)))

	identity, err := service.Authenticate(context.Background(), authToken, false)
	if err != nil {
		t.Fatalf("second authenticate: %v", err)
	}
	if identity.Email != "reloaded@example.com" {
		t.Fatalf("expected reloaded identity, got %#v", identity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestDBServiceAuthenticateUsesCacheBeforeJWTParse(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	cache := NewAuthCache(time.Minute)
	service := NewDBService(cfg, db).WithAuthCache(cache)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "demo@example.com", int64(0), int64(0)))

	if _, err := service.Authenticate(context.Background(), authToken, false); err != nil {
		t.Fatalf("first authenticate: %v", err)
	}

	service.cfg.AppKey = "rotated-secret"

	identity, err := service.Authenticate(context.Background(), authToken, false)
	if err != nil {
		t.Fatalf("second authenticate from cache: %v", err)
	}
	if identity.Email != "demo@example.com" {
		t.Fatalf("unexpected cached identity: %#v", identity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestDBServiceAuthenticateCoalescesConcurrentCacheMisses(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	cache := NewAuthCache(time.Minute)
	service := NewDBService(cfg, db).WithAuthCache(cache)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillDelayFor(50 * time.Millisecond).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "demo@example.com", int64(0), int64(0)))

	const workers = 8
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			identity, err := service.Authenticate(context.Background(), authToken, false)
			if err != nil {
				errCh <- err
				return
			}
			if identity.Email != "demo@example.com" {
				errCh <- ErrUnauthorized
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent authenticate: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestDBServiceAuthenticateSharedLookupSurvivesLeaderCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	cache := NewAuthCache(time.Minute)
	service := NewDBService(cfg, db).WithAuthCache(cache)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillDelayFor(40 * time.Millisecond).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "demo@example.com", int64(0), int64(0)))

	leaderCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	leaderErrCh := make(chan error, 1)

	go func() {
		_, err := service.Authenticate(leaderCtx, authToken, false)
		leaderErrCh <- err
	}()

	time.Sleep(5 * time.Millisecond)

	identity, err := service.Authenticate(context.Background(), authToken, false)
	if err != nil {
		t.Fatalf("follower authenticate: %v", err)
	}
	if identity.Email != "demo@example.com" {
		t.Fatalf("unexpected follower identity: %#v", identity)
	}

	leaderErr := <-leaderErrCh
	if leaderErr == nil {
		t.Fatal("expected leader request to exit with error after cancellation")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestDBServiceAuthenticateReturnsUnavailableOnLookupError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	service := NewDBService(cfg, db)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")
	lookupErr := errors.New("db down")

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnError(lookupErr)

	_, err = service.Authenticate(context.Background(), authToken, false)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestDBServiceRemoveSessionEvictsCachedAuth(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cfg := config.Config{AppKey: "unit-test-secret"}
	cache := NewAuthCache(time.Minute)
	service := NewDBService(cfg, db).WithAuthCache(cache)
	authToken := signedAuthToken(t, cfg.AppKey, 9, "sess-1")

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}).AddRow(int64(9), "demo@example.com", int64(0), int64(0)))

	if _, err := service.Authenticate(context.Background(), authToken, false); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_auth_session WHERE user_id = $1 AND session_id = $2`)).
		WithArgs(int64(9), "sess-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	removed, err := service.RemoveSession(context.Background(), 9, "sess-1")
	if err != nil {
		t.Fatalf("remove session: %v", err)
	}
	if !removed {
		t.Fatal("expected remove success")
	}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`)).
		WithArgs(int64(9), "sess-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "is_admin", "is_staff"}))

	if _, err := service.Authenticate(context.Background(), authToken, false); err != ErrUnauthorized {
		t.Fatalf("expected unauthorized after remove, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func signedAuthToken(t *testing.T, appKey string, userID int64, sessionID string) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":      userID,
		"session": sessionID,
	})
	signed, err := token.SignedString([]byte(appKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}
