package aiops

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Service) ensureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaOK {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102901)`); err != nil {
		return err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS v2_ai_config(id SMALLINT PRIMARY KEY CHECK(id=1),enabled BOOLEAN NOT NULL DEFAULT FALSE,base_url TEXT NOT NULL DEFAULT '',models JSONB NOT NULL,api_key_cipher TEXT NOT NULL DEFAULT '',updated_by BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
		`ALTER TABLE v2_ai_config ADD COLUMN IF NOT EXISTS reasoning_efforts JSONB NOT NULL DEFAULT '{}'::JSONB`,
		`ALTER TABLE v2_ai_config ADD COLUMN IF NOT EXISTS fallback_relays JSONB NOT NULL DEFAULT '[]'::JSONB`,
		`ALTER TABLE v2_ai_config ADD COLUMN IF NOT EXISTS failover_enabled BOOLEAN NOT NULL DEFAULT FALSE`,
		`ALTER TABLE v2_ai_config ADD COLUMN IF NOT EXISTS failover_timeout_seconds INTEGER NOT NULL DEFAULT 30`,
		`CREATE TABLE IF NOT EXISTS v2_ai_session(id BIGSERIAL PRIMARY KEY,title VARCHAR(200) NOT NULL,model VARCHAR(120) NOT NULL,created_by BIGINT NOT NULL,status VARCHAR(16) NOT NULL DEFAULT 'idle',created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_turn(id BIGSERIAL PRIMARY KEY,session_id BIGINT NOT NULL REFERENCES v2_ai_session(id) ON DELETE CASCADE,actor_id BIGINT NOT NULL,request_id VARCHAR(80) NOT NULL,request_hash VARCHAR(64) NOT NULL,model VARCHAR(120) NOT NULL,status VARCHAR(16) NOT NULL,error TEXT NOT NULL DEFAULT '',created_at BIGINT NOT NULL,expires_at BIGINT NOT NULL,completed_at BIGINT NOT NULL DEFAULT 0,UNIQUE(actor_id,request_id))`,
		`CREATE TABLE IF NOT EXISTS v2_ai_message(id BIGSERIAL PRIMARY KEY,session_id BIGINT NOT NULL REFERENCES v2_ai_session(id) ON DELETE CASCADE,turn_id BIGINT NOT NULL REFERENCES v2_ai_turn(id) ON DELETE CASCADE,role VARCHAR(16) NOT NULL,content TEXT NOT NULL DEFAULT '',tool_calls JSONB NOT NULL DEFAULT '[]',tool_call_id VARCHAR(200) NOT NULL DEFAULT '',created_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_server_intake(id BIGSERIAL PRIMARY KEY,session_id BIGINT NOT NULL REFERENCES v2_ai_session(id) ON DELETE CASCADE,turn_id BIGINT NOT NULL REFERENCES v2_ai_turn(id) ON DELETE CASCADE,actor_id BIGINT NOT NULL,name TEXT NOT NULL,host TEXT NOT NULL,port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),username TEXT NOT NULL,host_key_sha256 TEXT NOT NULL DEFAULT '',password_cipher TEXT NOT NULL DEFAULT '',private_key_cipher TEXT NOT NULL DEFAULT '',key_passphrase_cipher TEXT NOT NULL DEFAULT '',created_at BIGINT NOT NULL,expires_at BIGINT NOT NULL,registered_server_id BIGINT NOT NULL DEFAULT 0,UNIQUE(turn_id))`,
		`CREATE INDEX IF NOT EXISTS v2_ai_server_intake_session_idx ON v2_ai_server_intake(session_id,id)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_result(id BIGSERIAL PRIMARY KEY,session_id BIGINT NOT NULL REFERENCES v2_ai_session(id) ON DELETE CASCADE,actor_id BIGINT NOT NULL,operation TEXT NOT NULL,data TEXT NOT NULL,expires_at BIGINT NOT NULL,admin_only BOOLEAN NOT NULL DEFAULT FALSE)`,
		`ALTER TABLE v2_ai_result ADD COLUMN IF NOT EXISTS admin_only BOOLEAN NOT NULL DEFAULT FALSE`,
		`CREATE INDEX IF NOT EXISTS v2_ai_result_expiry_idx ON v2_ai_result(expires_at)`,
		`CREATE INDEX IF NOT EXISTS v2_ai_result_session_idx ON v2_ai_result(session_id,id)`,
		`ALTER TABLE v2_ai_session ADD COLUMN IF NOT EXISTS access_mode TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE v2_ai_session ADD COLUMN IF NOT EXISTS access_version BIGINT NOT NULL DEFAULT 1`,
		`ALTER TABLE v2_ai_session ADD COLUMN IF NOT EXISTS full_access_by BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE v2_ai_session ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT 'default'`,
		`ALTER TABLE v2_ai_session ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'web'`,
		`ALTER TABLE v2_ai_turn ADD COLUMN IF NOT EXISTS access_mode TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE v2_ai_turn ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT 'auto'`,
		`ALTER TABLE v2_ai_turn ADD COLUMN IF NOT EXISTS stop_requested BOOLEAN NOT NULL DEFAULT FALSE`,
		`ALTER TABLE v2_ai_turn ADD COLUMN IF NOT EXISTS draft_content TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE v2_ai_turn ADD COLUMN IF NOT EXISTS draft_round INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE v2_ai_turn ADD COLUMN IF NOT EXISTS draft_version BIGINT NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS v2_ai_message_session_idx ON v2_ai_message(session_id,id)`,
		`CREATE INDEX IF NOT EXISTS v2_ai_turn_session_idx ON v2_ai_turn(session_id,id)`,
		`CREATE INDEX IF NOT EXISTS v2_ai_turn_running_idx ON v2_ai_turn(status,expires_at)`,
	} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("ensure AI schema: %w", err)
		}
	}
	if err = ensureActionSchema(ctx, tx); err != nil {
		return err
	}
	if err = ensureKnowledgeSchema(ctx, tx); err != nil {
		return err
	}
	if err = ensureTelegramSchema(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.schemaOK = true
	return nil
}

// All resets, sends and approvals take this shared transaction lock before
// per-session locks. Reset takes it exclusively, closing deletion/create races.
func lockAIState(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(2026102902)`)
	return err
}
func lockAIStateExclusive(ctx context.Context, tx *sql.Tx) error {
	// Remote operations hold the shared lock while they execute. Cleanup must
	// fail fast even when that operation belongs to a different session.
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(2026102902)`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrBusy
	}
	return nil
}
