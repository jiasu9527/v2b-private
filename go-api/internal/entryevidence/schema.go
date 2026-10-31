package entryevidence

import (
	"context"
	"database/sql"
)

const Schema = `
CREATE TABLE IF NOT EXISTS v2_entry_delivery_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), keep_days INTEGER NOT NULL DEFAULT 30 CHECK(keep_days BETWEEN 1 AND 365), updated_at BIGINT NOT NULL DEFAULT 0
);
INSERT INTO v2_entry_delivery_settings(id) VALUES(1) ON CONFLICT(id) DO NOTHING;
CREATE TABLE IF NOT EXISTS v2_entry_delivery_request (
 id TEXT PRIMARY KEY, user_id BIGINT NOT NULL, email_at_event TEXT NOT NULL DEFAULT '', ua TEXT NOT NULL DEFAULT '',
 requested_at BIGINT NOT NULL, completed_at BIGINT NOT NULL, transport TEXT NOT NULL, status INTEGER NOT NULL,
 response_complete BOOLEAN NOT NULL, coverage TEXT NOT NULL, coverage_reason TEXT NOT NULL DEFAULT '',
 node_count INTEGER NOT NULL, bytes_written BIGINT NOT NULL DEFAULT 0, write_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_entry_delivery_request_user_time ON v2_entry_delivery_request(user_id,completed_at DESC);
CREATE INDEX IF NOT EXISTS idx_entry_delivery_request_time ON v2_entry_delivery_request(completed_at DESC);
CREATE TABLE IF NOT EXISTS v2_entry_delivery_node (
 request_id TEXT NOT NULL REFERENCES v2_entry_delivery_request(id) ON DELETE CASCADE, ordinal INTEGER NOT NULL,
 server_type TEXT NOT NULL, server_id BIGINT NOT NULL, policy_id BIGINT NOT NULL DEFAULT 0, group_id BIGINT NOT NULL DEFAULT 0,
 rule_revision TEXT NOT NULL DEFAULT '', source_host TEXT NOT NULL DEFAULT '', delivered_host TEXT NOT NULL DEFAULT '', port TEXT NOT NULL DEFAULT '',
 resolved BOOLEAN NOT NULL DEFAULT FALSE, provider_id BIGINT NOT NULL DEFAULT 0, collection_id BIGINT NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(request_id,ordinal)
);
ALTER TABLE v2_entry_delivery_node ADD COLUMN IF NOT EXISTS provider_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_entry_delivery_node_rule ON v2_entry_delivery_node(policy_id,group_id,delivered_host,port);
`

func EnsureSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, Schema)
	return err
}
