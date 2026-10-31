package aiops

import (
	"context"
	"database/sql"
	"time"
)

func ensureTelegramMiniSchema(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_mini_token(token_hash TEXT PRIMARY KEY,actor_id BIGINT NOT NULL,bot_id TEXT NOT NULL,bot_fingerprint TEXT NOT NULL,generation BIGINT NOT NULL,expires_at BIGINT NOT NULL,created_at BIGINT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS v2_ai_telegram_mini_token_actor_idx ON v2_ai_telegram_mini_token(actor_id,expires_at)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_telegram_mini_request(id BIGSERIAL PRIMARY KEY,actor_id BIGINT NOT NULL,bot_id TEXT NOT NULL,bot_fingerprint TEXT NOT NULL,generation BIGINT NOT NULL,operation_key TEXT NOT NULL,kind TEXT NOT NULL,request_hash TEXT NOT NULL,payload_cipher TEXT NOT NULL DEFAULT '',request_id TEXT NOT NULL DEFAULT '',session_id BIGINT REFERENCES v2_ai_session(id) ON DELETE SET NULL,turn_id BIGINT REFERENCES v2_ai_turn(id) ON DELETE SET NULL,action_id BIGINT REFERENCES v2_ai_action(id) ON DELETE SET NULL,status TEXT NOT NULL DEFAULT 'queued',created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,UNIQUE(actor_id,operation_key))`,
		`CREATE INDEX IF NOT EXISTS v2_ai_telegram_mini_request_session_idx ON v2_ai_telegram_mini_request(session_id)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func stopTelegramMiniBindingsTx(ctx context.Context, tx *sql.Tx, actor int64) error {
	for _, q := range []string{
		`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE status='running' AND EXISTS(SELECT 1 FROM v2_ai_telegram_mini_request m WHERE ($1=0 OR m.actor_id=$1) AND (m.turn_id=v2_ai_turn.id OR (m.actor_id=v2_ai_turn.actor_id AND m.request_id=v2_ai_turn.request_id)))`,
		`UPDATE v2_ai_action SET stop_requested=TRUE WHERE status='running' AND (id IN(SELECT action_id FROM v2_ai_telegram_mini_request WHERE $1=0 OR actor_id=$1) OR session_id IN(SELECT t.session_id FROM v2_ai_turn t JOIN v2_ai_telegram_mini_request m ON m.turn_id=t.id OR (m.actor_id=t.actor_id AND m.request_id=t.request_id) WHERE $1=0 OR m.actor_id=$1))`,
		`UPDATE v2_ai_telegram_mini_request SET status='cancelled',payload_cipher='',updated_at=EXTRACT(EPOCH FROM NOW())::BIGINT WHERE $1=0 OR actor_id=$1`,
		`DELETE FROM v2_ai_telegram_mini_token WHERE $1=0 OR actor_id=$1`,
	} {
		if _, err := tx.ExecContext(ctx, q, actor); err != nil {
			return err
		}
	}
	return nil
}

func purgeTelegramMiniSessionData(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_ai_telegram_mini_request SET status='cancelled',payload_cipher='',updated_at=EXTRACT(EPOCH FROM NOW())::BIGINT WHERE $1=0 OR session_id=$1 OR action_id IN(SELECT id FROM v2_ai_action WHERE session_id=$1) OR EXISTS(SELECT 1 FROM v2_ai_turn t WHERE t.session_id=$1 AND t.actor_id=v2_ai_telegram_mini_request.actor_id AND t.request_id=v2_ai_telegram_mini_request.request_id)`, id)
	return err
}

func (b *TelegramBridge) sweepTelegramMiniTx(ctx context.Context, tx *sql.Tx) error {
	bot, secret, _ := b.miniBot()
	fingerprint := hashOperation(secret)
	// Changing a token for the same bot is a revocation too. Cancel only old
	// credential tasks, not new tasks authorized after the rotation.
	for _, q := range []string{
		`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE status='running' AND EXISTS(SELECT 1 FROM v2_ai_telegram_mini_request m WHERE (m.bot_id<>$1 OR m.bot_fingerprint<>$2) AND (m.turn_id=v2_ai_turn.id OR (m.actor_id=v2_ai_turn.actor_id AND m.request_id=v2_ai_turn.request_id)))`,
		`UPDATE v2_ai_action SET stop_requested=TRUE WHERE status='running' AND (id IN(SELECT action_id FROM v2_ai_telegram_mini_request WHERE bot_id<>$1 OR bot_fingerprint<>$2) OR session_id IN(SELECT t.session_id FROM v2_ai_turn t JOIN v2_ai_telegram_mini_request m ON m.turn_id=t.id OR (m.actor_id=t.actor_id AND m.request_id=t.request_id) WHERE m.bot_id<>$1 OR m.bot_fingerprint<>$2))`,
		`UPDATE v2_ai_telegram_mini_request SET status='cancelled',payload_cipher='' WHERE bot_id<>$1 OR bot_fingerprint<>$2`,
		`DELETE FROM v2_ai_telegram_mini_token WHERE bot_id<>$1 OR bot_fingerprint<>$2`,
	} {
		if _, err := tx.ExecContext(ctx, q, bot, fingerprint); err != nil {
			return err
		}
	}
	// Abandoned or invalid prepared requests must not retain frozen SSH input
	// indefinitely. Expiration never dispatches work or removes correlation: an
	// already committed turn/action remains visible and revocable via its ids.
	if _, err := tx.ExecContext(ctx, `UPDATE v2_ai_telegram_mini_request SET status='cancelled',payload_cipher='' WHERE status='queued' AND updated_at<$1`, time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_ai_telegram_mini_request SET payload_cipher='' WHERE status IN('done','cancelled') AND updated_at<$1 AND payload_cipher<>''`, time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM v2_ai_telegram_mini_token WHERE expires_at<=$1`, time.Now().Unix())
	return err
}
