package session

import (
	"context"
	"strings"
)

// AuthenticateFresh bypasses per-process caches for restricted staff endpoints.
// Session revocation and role changes therefore take effect across instances.
func (s *DBService) AuthenticateFresh(ctx context.Context, authToken string, requireAdmin bool) (*Identity, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	authToken = cleanAuthToken(authToken)
	if authToken == "" || strings.TrimSpace(s.cfg.AppKey) == "" {
		return nil, ErrUnauthorized
	}
	identity, _, err := s.findIdentityForAuthToken(ctx, authToken)
	if err != nil {
		return nil, err
	}
	if requireAdmin && identity.IsAdmin == 0 {
		return nil, ErrUnauthorized
	}
	return identity, nil
}
