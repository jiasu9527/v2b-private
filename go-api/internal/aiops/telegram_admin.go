package aiops

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

func (b *TelegramBridge) ready(ctx context.Context) error {
	if b == nil || b.ai == nil {
		return ErrUnavailable
	}
	return b.ai.ensureSchema(ctx)
}

func (b *TelegramBridge) identity() (string, bool) {
	b.mu.Lock()
	transport := b.transport
	b.mu.Unlock()
	if transport == nil {
		return "", false
	}
	return transport.Identity()
}

func telegramConfigRow(row interface{ Scan(...any) error }) (TelegramConfig, error) {
	var c TelegramConfig
	err := row.Scan(&c.Enabled, &c.DefaultModel, &c.DefaultAccessMode, &c.StreamUpdates, &c.NotifyCompletion)
	return c, err
}

const telegramConfigColumns = `enabled,default_model,default_access_mode,stream_updates,notify_completion`

func (b *TelegramBridge) Status(ctx context.Context, actor int64) (TelegramStatus, error) {
	out := TelegramStatus{Bindings: []TelegramBinding{}, Models: []string{}}
	if err := b.ready(ctx); err != nil {
		return out, err
	}
	if err := b.ai.requireOperator(ctx, actor); err != nil {
		return out, err
	}
	var err error
	out.Config, err = telegramConfigRow(b.ai.db.QueryRowContext(ctx, `SELECT `+telegramConfigColumns+` FROM v2_ai_telegram_config WHERE id=1`))
	if err != nil {
		return out, err
	}
	cfg, _, err := b.ai.loadConfig(ctx)
	if err != nil {
		return out, err
	}
	out.Models = cfg.Models
	if out.Config.DefaultModel == "" && len(out.Models) > 0 {
		out.Config.DefaultModel = out.Models[0]
	}
	_, out.BotReady = b.identity()
	rows, err := b.ai.db.QueryContext(ctx, `SELECT t.actor_id,u.email,t.telegram_id,t.active,COALESCE(t.session_id,0),t.bound_at FROM v2_ai_telegram_binding t JOIN v2_user u ON u.id=t.actor_id WHERE t.actor_id=$1 OR EXISTS(SELECT 1 FROM v2_user viewer WHERE viewer.id=$1 AND viewer.is_admin=1 AND viewer.banned=0) ORDER BY t.actor_id`, actor)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v TelegramBinding
		if err = rows.Scan(&v.ActorID, &v.Email, &v.TelegramID, &v.Active, &v.SessionID, &v.BoundAt); err != nil {
			return out, err
		}
		out.Bindings = append(out.Bindings, v)
		if v.ActorID == actor {
			copy := v
			out.MyBinding = &copy
		}
	}
	return out, rows.Err()
}

func (b *TelegramBridge) SaveConfig(ctx context.Context, actor int64, c TelegramConfig) (TelegramConfig, error) {
	if err := b.ready(ctx); err != nil {
		return c, err
	}
	if err := b.ai.requireAdmin(ctx, actor); err != nil {
		return c, err
	}
	if !validAccessMode(c.DefaultAccessMode) {
		return c, ErrValidation
	}
	if err := b.ai.validateModel(ctx, c.DefaultModel); c.Enabled && err != nil {
		return c, err
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return c, err
	}
	if err = b.ai.requireAdminTx(ctx, tx, actor); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_config SET enabled=$1,default_model=$2,default_access_mode=$3,stream_updates=$4,notify_completion=$5,updated_by=$6,updated_at=$7 WHERE id=1`, c.Enabled, c.DefaultModel, c.DefaultAccessMode, c.StreamUpdates, c.NotifyCompletion, actor, time.Now().Unix()); err != nil {
		return c, err
	}
	if !c.Enabled {
		if err = stopTelegramBindingsTx(ctx, tx, 0); err != nil {
			return c, err
		}
	}
	return c, tx.Commit()
}

func (b *TelegramBridge) CreateBindingCode(ctx context.Context, actor, target int64) (TelegramBindingCode, error) {
	var out TelegramBindingCode
	if err := b.ready(ctx); err != nil {
		return out, err
	}
	if target == 0 {
		target = actor
	}
	if err := b.ai.requireAdmin(ctx, actor); err != nil {
		return out, err
	}
	if err := b.ai.requireOperator(ctx, target); err != nil {
		return out, err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return out, err
	}
	out.Code = base64.RawURLEncoding.EncodeToString(raw)
	out.ExpiresAt = time.Now().Add(10 * time.Minute).Unix()
	out.Command = "/aibind " + out.Code
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return TelegramBindingCode{}, err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return TelegramBindingCode{}, err
	}
	if err = b.ai.requireAdminTx(ctx, tx, actor); err != nil {
		return TelegramBindingCode{}, err
	}
	if err = b.ai.requireOperatorTx(ctx, tx, target); err != nil {
		return TelegramBindingCode{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_telegram_code WHERE actor_id=$1 OR expires_at<$2`, target, time.Now().Unix()); err != nil {
		return TelegramBindingCode{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_ai_telegram_code(code_hash,actor_id,issued_by,expires_at) VALUES($1,$2,$3,$4)`, hashOperation(out.Code), target, actor, out.ExpiresAt); err != nil {
		return TelegramBindingCode{}, err
	}
	if err = tx.Commit(); err != nil {
		return TelegramBindingCode{}, err
	}
	return out, nil
}

func (b *TelegramBridge) RevokeBinding(ctx context.Context, actor, target int64) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	if target == 0 {
		target = actor
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return err
	}
	if target == actor {
		err = b.ai.requireOperatorTx(ctx, tx, actor)
	} else {
		err = b.ai.requireAdminTx(ctx, tx, actor)
	}
	if err != nil {
		return err
	}
	if err = stopTelegramBindingsTx(ctx, tx, target); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET active=FALSE,ai_mode=FALSE,generation=generation+1,session_id=NULL,updated_at=$2 WHERE actor_id=$1`, target, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_telegram_code WHERE actor_id=$1`, target); err != nil {
		return err
	}
	return tx.Commit()
}

// Stop is durable: existing AI workers on every process already poll these
// stop flags. Request IDs recover a SendMessage commit whose inbox ack was lost.
func stopTelegramBindingsTx(ctx context.Context, tx *sql.Tx, actor int64) error {
	if err := stopTelegramMiniBindingsTx(ctx, tx, actor); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE status='running' AND EXISTS(SELECT 1 FROM v2_ai_telegram_inbox i WHERE ($1=0 OR i.actor_id=$1) AND (i.turn_id=v2_ai_turn.id OR (i.actor_id=v2_ai_turn.actor_id AND i.request_id=v2_ai_turn.request_id)))`,
		`UPDATE v2_ai_action SET stop_requested=TRUE WHERE status='running' AND session_id IN(SELECT t.session_id FROM v2_ai_turn t JOIN v2_ai_telegram_inbox i ON i.turn_id=t.id OR (i.actor_id=t.actor_id AND i.request_id=t.request_id) WHERE $1=0 OR i.actor_id=$1)`,
		`UPDATE v2_ai_action SET stop_requested=TRUE WHERE status='running' AND id IN(SELECT action_id FROM v2_ai_telegram_inbox WHERE $1=0 OR actor_id=$1)`,
		`UPDATE v2_ai_telegram_inbox SET status='cancelled',payload_cipher='',updated_at=EXTRACT(EPOCH FROM NOW())::BIGINT WHERE actor_id>0 AND ($1=0 OR actor_id=$1) AND status IN('queued','running')`,
		`UPDATE v2_ai_telegram_outbox SET status='cancelled',content_cipher='',buttons='[]' WHERE actor_id>0 AND ($1=0 OR actor_id=$1) AND status<>'cancelled'`,
	} {
		if _, err := tx.ExecContext(ctx, q, actor); err != nil {
			return err
		}
	}
	return nil
}

// Called while holding the Telegram advisory lock, before deleting sessions.
// Keep update tombstones so webhook retries cannot recreate deleted tasks.
func purgeTelegramSessionData(ctx context.Context, tx *sql.Tx, id int64, reset bool) error {
	if err := purgeTelegramMiniSessionData(ctx, tx, id); err != nil {
		return err
	}
	if reset {
		if err := stopTelegramBindingsTx(ctx, tx, 0); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`UPDATE v2_ai_telegram_inbox SET status='cancelled',payload_cipher='' WHERE $1=0 OR session_id=$1 OR action_id IN(SELECT id FROM v2_ai_action WHERE session_id=$1) OR EXISTS(SELECT 1 FROM v2_ai_turn t WHERE t.session_id=$1 AND t.actor_id=v2_ai_telegram_inbox.actor_id AND t.request_id=v2_ai_telegram_inbox.request_id)`,
		`UPDATE v2_ai_telegram_outbox SET status='cancelled',content_cipher='',buttons='[]' WHERE $1=0 OR session_id=$1 OR inbox_id IN(SELECT id FROM v2_ai_telegram_inbox WHERE status='cancelled')`,
		`UPDATE v2_ai_telegram_binding SET session_id=NULL WHERE $1=0 OR session_id=$1`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	if reset {
		for _, q := range []string{`UPDATE v2_ai_telegram_binding SET active=FALSE,ai_mode=FALSE,generation=generation+1,session_id=NULL`, `DELETE FROM v2_ai_telegram_code`, `UPDATE v2_ai_telegram_config SET enabled=FALSE`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	return nil
}

type telegramBound struct {
	actor, generation, session int64
	model, mode                string
	enabled                    bool
}

func telegramBindingForInput(ctx context.Context, tx *sql.Tx, in TelegramInput) (telegramBound, error) {
	var v telegramBound
	err := tx.QueryRowContext(ctx, `SELECT b.actor_id,b.generation,COALESCE(b.session_id,0),b.model,b.access_mode,b.ai_mode FROM v2_ai_telegram_binding b JOIN v2_user u ON u.id=b.actor_id WHERE b.bot_id=$1 AND b.telegram_id=$2 AND b.active AND u.banned=0 AND (u.is_admin=1 OR u.is_staff=1)`, in.BotID, in.FromID).Scan(&v.actor, &v.generation, &v.session, &v.model, &v.mode, &v.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrForbidden
	}
	return v, err
}

func telegramCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(strings.SplitN(parts[0], "@", 2)[0])
	argument := ""
	if len(parts) == 2 {
		argument = strings.TrimSpace(parts[1])
	}
	aliases := map[string]string{"/ainew": "/new", "/aisessions": "/sessions", "/aimodel": "/model", "/aistop": "/stop", "/aidetails": "/details", "/aiexit": "/exit"}
	if alias := aliases[command]; alias != "" {
		command = alias
	}
	return command, argument
}
