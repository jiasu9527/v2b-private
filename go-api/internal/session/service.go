package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/config"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

var (
	ErrUnavailable  = errors.New("session service unavailable")
	ErrUnauthorized = errors.New("unauthorized")
)

type Identity struct {
	ID      int64  `json:"id"`
	Email   string `json:"email"`
	IsAdmin int64  `json:"is_admin"`
	IsStaff int64  `json:"is_staff"`
}

type SessionMeta struct {
	IP       string `json:"ip,omitempty"`
	LoginAt  int64  `json:"login_at"`
	UA       string `json:"ua,omitempty"`
	AuthData string `json:"auth_data,omitempty"`
}

type Service interface {
	Authenticate(ctx context.Context, authToken string, requireAdmin bool) (*Identity, error)
	ListSessions(ctx context.Context, userID int64) (map[string]SessionMeta, error)
	RemoveSession(ctx context.Context, userID int64, sessionID string) (bool, error)
}

type DBService struct {
	cfg       config.Config
	db        *sql.DB
	authCache *AuthCache
	authGroup singleflight.Group
}

const defaultAuthLookupTimeout = 10 * time.Second

func NewDBService(cfg config.Config, db *sql.DB) *DBService {
	return &DBService{cfg: cfg, db: db}
}

func (s *DBService) WithAuthCache(cache *AuthCache) *DBService {
	s.authCache = cache
	return s
}

func (s *DBService) Authenticate(ctx context.Context, authToken string, requireAdmin bool) (*Identity, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	authToken = cleanAuthToken(authToken)
	if authToken == "" {
		return nil, ErrUnauthorized
	}

	if s.authCache != nil {
		if identity, ok := s.authCache.Get(authToken); ok {
			if requireAdmin && identity.IsAdmin == 0 {
				return nil, ErrUnauthorized
			}
			return identity, nil
		}
	}

	if strings.TrimSpace(s.cfg.AppKey) == "" {
		return nil, ErrUnauthorized
	}

	identity, err := s.authenticateSlowPath(ctx, authToken)
	if err != nil {
		return nil, err
	}
	if requireAdmin && identity.IsAdmin == 0 {
		return nil, ErrUnauthorized
	}

	return identity, nil
}

func (s *DBService) authenticateSlowPath(ctx context.Context, authToken string) (*Identity, error) {
	resultCh := s.authGroup.DoChan(authToken, func() (any, error) {
		if s.authCache != nil {
			if identity, ok := s.authCache.Get(authToken); ok {
				return cloneIdentity(identity), nil
			}
		}

		lookupCtx, cancel := s.newAuthLookupContext()
		defer cancel()

		identity, sessionID, err := s.findIdentityForAuthToken(lookupCtx, authToken)
		if err != nil {
			return nil, err
		}
		if s.authCache != nil {
			s.authCache.Store(authToken, sessionID, identity)
		}
		return cloneIdentity(identity), nil
	})

	select {
	case <-ctx.Done():
		return nil, ErrUnauthorized
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}

		identity, ok := result.Val.(*Identity)
		if !ok || identity == nil {
			return nil, ErrUnauthorized
		}
		return cloneIdentity(identity), nil
	}
}

func (s *DBService) ListSessions(ctx context.Context, userID int64) (map[string]SessionMeta, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT session_id, ip, ua, login_at, auth_data
FROM v2_auth_session
WHERE user_id = $1 AND (expire_at = 0 OR expire_at > $2)
ORDER BY login_at DESC`, userID, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("list auth sessions: %w", err)
	}
	defer rows.Close()

	result := make(map[string]SessionMeta)
	for rows.Next() {
		var sessionID string
		var meta SessionMeta
		if err := rows.Scan(&sessionID, &meta.IP, &meta.UA, &meta.LoginAt, &meta.AuthData); err != nil {
			return nil, fmt.Errorf("scan auth session: %w", err)
		}
		result[sessionID] = meta
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate auth sessions: %w", err)
	}

	return result, nil
}

func (s *DBService) RemoveSession(ctx context.Context, userID int64, sessionID string) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if strings.TrimSpace(sessionID) == "" {
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id = $1 AND session_id = $2`, userID, sessionID); err != nil {
		return false, fmt.Errorf("remove auth session: %w", err)
	}
	if s.authCache != nil {
		s.authCache.InvalidateSession(userID, sessionID)
	}
	return true, nil
}

func (s *DBService) findIdentityBySession(ctx context.Context, userID int64, sessionID string) (*Identity, error) {
	var identity Identity
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.email, u.is_admin, u.is_staff
FROM v2_auth_session s
JOIN v2_user u ON u.id = s.user_id
WHERE s.user_id = $1 AND s.session_id = $2 AND (s.expire_at = 0 OR s.expire_at > $3) AND u.banned = 0
LIMIT 1`, userID, sessionID, time.Now().Unix()).Scan(&identity.ID, &identity.Email, &identity.IsAdmin, &identity.IsStaff)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find auth identity by session: %w", err)
	}
	return &identity, nil
}

func (s *DBService) findIdentityForAuthToken(ctx context.Context, authToken string) (*Identity, string, error) {
	parsed, err := jwt.Parse(authToken, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method %s", token.Method.Alg())
		}
		return []byte(s.cfg.AppKey), nil
	})
	if err != nil || !parsed.Valid {
		return nil, "", ErrUnauthorized
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, "", ErrUnauthorized
	}

	userID, err := mapClaimInt64(claims["id"])
	if err != nil {
		return nil, "", ErrUnauthorized
	}
	sessionID, ok := claims["session"].(string)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil, "", ErrUnauthorized
	}

	identity, err := s.findIdentityBySession(ctx, userID, sessionID)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if identity == nil {
		return nil, "", ErrUnauthorized
	}
	return identity, sessionID, nil
}

func cleanAuthToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) > 7 && strings.EqualFold(token[:7], "bearer ") {
		return strings.TrimSpace(token[7:])
	}
	return token
}

func mapClaimInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unsupported claim type %T", value)
	}
}

func cloneIdentity(identity *Identity) *Identity {
	if identity == nil {
		return nil
	}
	cloned := *identity
	return &cloned
}

func (s *DBService) newAuthLookupContext() (context.Context, context.CancelFunc) {
	timeout := s.cfg.ReadTimeout
	if timeout <= 0 {
		timeout = defaultAuthLookupTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}
