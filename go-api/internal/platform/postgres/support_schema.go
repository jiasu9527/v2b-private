package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureSupportSchema deliberately keeps support membership separate from the
// legacy is_staff flag, which grants much broader API and Telegram capabilities.
func EnsureSupportSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize rolling-start DDL across server instances.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026101501)`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS v2_support_staff (
user_id INTEGER PRIMARY KEY,
enabled SMALLINT NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
target_keys JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(target_keys) = 'array'),
version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
created_at BIGINT NOT NULL,
updated_at BIGINT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS v2_support_request_limit (
actor_id INTEGER NOT NULL,
action VARCHAR(16) NOT NULL,
window_start BIGINT NOT NULL,
request_count INTEGER NOT NULL,
PRIMARY KEY (actor_id, action)
)`,
		`CREATE TABLE IF NOT EXISTS v2_support_entry_override (
user_id INTEGER NOT NULL,
source_policy_id INTEGER NOT NULL,
target_policy_id INTEGER DEFAULT NULL,
target_group_id BIGINT DEFAULT NULL,
target_key VARCHAR(255) NOT NULL DEFAULT '',
active SMALLINT NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
expires_at BIGINT DEFAULT NULL,
version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
updated_by INTEGER NOT NULL,
updated_at BIGINT NOT NULL,
PRIMARY KEY (user_id, source_policy_id)
)`,
		`CREATE INDEX IF NOT EXISTS v2_support_entry_override_target_idx ON v2_support_entry_override (target_policy_id, target_group_id) WHERE active = 1`,
		`CREATE TABLE IF NOT EXISTS v2_support_operation (
id BIGSERIAL PRIMARY KEY,
actor_id INTEGER NOT NULL,
target_user_id INTEGER NOT NULL,
source_policy_id INTEGER NOT NULL,
request_id VARCHAR(80) NOT NULL,
reason VARCHAR(500) NOT NULL,
before_state JSONB NOT NULL,
after_state JSONB NOT NULL,
created_at BIGINT NOT NULL,
UNIQUE (actor_id, request_id)
)`,
		`CREATE INDEX IF NOT EXISTS v2_support_operation_actor_idx ON v2_support_operation (actor_id, id DESC)`,
		`CREATE INDEX IF NOT EXISTS v2_support_operation_created_idx ON v2_support_operation (created_at, id)`,
	} {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure support schema: %w", err)
		}
	}
	return tx.Commit()
}
