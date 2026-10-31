package aiops

import (
	"context"
	"database/sql"
	"fmt"
)

// Called by Service.ensureSchema in the same schema transaction. Telegram
// authorizations are intentionally independent of normal v2_user.telegram_id.
func ensureTelegramSchema(ctx context.Context, tx *sql.Tx) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_config(id SMALLINT PRIMARY KEY CHECK(id=1),enabled BOOLEAN NOT NULL DEFAULT FALSE,default_model TEXT NOT NULL DEFAULT '',default_access_mode TEXT NOT NULL DEFAULT 'full',stream_updates BOOLEAN NOT NULL DEFAULT TRUE,notify_completion BOOLEAN NOT NULL DEFAULT TRUE,updated_by BIGINT NOT NULL DEFAULT 0,updated_at BIGINT NOT NULL DEFAULT 0)`,
		`INSERT INTO v2_ai_telegram_config(id) VALUES(1) ON CONFLICT DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_binding(actor_id BIGINT PRIMARY KEY,bot_id TEXT NOT NULL,telegram_id BIGINT NOT NULL,active BOOLEAN NOT NULL DEFAULT TRUE,generation BIGINT NOT NULL DEFAULT 1,session_id BIGINT REFERENCES v2_ai_session(id) ON DELETE SET NULL,model TEXT NOT NULL DEFAULT '',access_mode TEXT NOT NULL DEFAULT 'full',ai_mode BOOLEAN NOT NULL DEFAULT TRUE,bound_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS v2_ai_telegram_binding_identity_idx ON v2_ai_telegram_binding(bot_id,telegram_id) WHERE active`,
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_code(code_hash TEXT PRIMARY KEY,actor_id BIGINT NOT NULL,issued_by BIGINT NOT NULL,expires_at BIGINT NOT NULL,used_at BIGINT NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS v2_ai_telegram_code_actor_idx ON v2_ai_telegram_code(actor_id)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_inbox(id BIGSERIAL PRIMARY KEY,bot_id TEXT NOT NULL,update_id BIGINT NOT NULL,chat_id BIGINT NOT NULL,actor_id BIGINT NOT NULL,generation BIGINT NOT NULL,kind TEXT NOT NULL,payload_cipher TEXT NOT NULL DEFAULT '',request_id TEXT NOT NULL DEFAULT '',session_id BIGINT REFERENCES v2_ai_session(id) ON DELETE SET NULL,turn_id BIGINT REFERENCES v2_ai_turn(id) ON DELETE SET NULL,status TEXT NOT NULL DEFAULT 'queued',draft_version BIGINT NOT NULL DEFAULT -1,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,UNIQUE(bot_id,update_id))`,
		`ALTER TABLE v2_ai_telegram_inbox ADD COLUMN IF NOT EXISTS action_id BIGINT REFERENCES v2_ai_action(id) ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS v2_ai_telegram_inbox_pending_idx ON v2_ai_telegram_inbox(status,id)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_outbox(id BIGSERIAL PRIMARY KEY,inbox_id BIGINT NOT NULL REFERENCES v2_ai_telegram_inbox(id) ON DELETE CASCADE,bot_id TEXT NOT NULL,chat_id BIGINT NOT NULL,actor_id BIGINT NOT NULL,generation BIGINT NOT NULL,session_id BIGINT REFERENCES v2_ai_session(id) ON DELETE SET NULL,output_key TEXT NOT NULL,content_cipher TEXT NOT NULL,buttons JSONB NOT NULL DEFAULT '[]',message_id BIGINT NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT 'queued',revision BIGINT NOT NULL DEFAULT 1,attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at BIGINT NOT NULL DEFAULT 0,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,UNIQUE(inbox_id,output_key))`,
		`ALTER TABLE v2_ai_telegram_outbox ADD COLUMN IF NOT EXISTS silent BOOLEAN NOT NULL DEFAULT FALSE`,
		`CREATE INDEX IF NOT EXISTS v2_ai_telegram_outbox_pending_idx ON v2_ai_telegram_outbox(status,next_attempt_at,id)`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("ensure AI Telegram schema: %w", err)
		}
	}
	return ensureTelegramMiniSchema(ctx, tx)
}

// All bridge dispatch, revocation and delivery share this lock across backend
// processes. Revocation cannot commit between the final guard and side effect.
func lockTelegramBridge(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026103101)`)
	return err
}
