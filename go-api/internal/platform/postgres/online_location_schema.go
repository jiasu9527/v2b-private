package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// One replaceable region per user, not an IP or connection history.
func EnsureOnlineLocationSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`SELECT pg_advisory_xact_lock(2026102401)`,
		`CREATE TABLE IF NOT EXISTS v2_user_online_location (
user_id INTEGER PRIMARY KEY REFERENCES v2_user(id) ON DELETE CASCADE,
country_code VARCHAR(2) NOT NULL DEFAULT '',
country_name VARCHAR(128) NOT NULL DEFAULT '',
province_code VARCHAR(6) NOT NULL DEFAULT '',
province_name VARCHAR(128) NOT NULL DEFAULT '',
unknown_reason VARCHAR(32) NOT NULL DEFAULT '',
last_seen_at BIGINT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_v2_user_online_location_last_seen ON v2_user_online_location(last_seen_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure online location schema: %w", err)
		}
	}
	return tx.Commit()
}
