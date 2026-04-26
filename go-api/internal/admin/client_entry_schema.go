package admin

import (
	"context"
	"fmt"

	platformpostgres "forest/go-api/internal/platform/postgres"
)

func (s *DBService) ensureClientEntrySchema(ctx context.Context) error {
	if s.db == nil {
		return ErrUnavailable
	}
	s.clientEntryEnsureOnce.Do(func() {
		if err := platformpostgres.EnsureClientEntrySchema(ctx, s.db); err != nil {
			s.clientEntryEnsureErr = fmt.Errorf("ensure client entry schema: %w", err)
		}
	})
	return s.clientEntryEnsureErr
}
