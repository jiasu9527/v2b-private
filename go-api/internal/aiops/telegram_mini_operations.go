package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type miniRequest struct {
	id, actor, generation, session, turn, action int64
	bot, fingerprint, key, kind, hash, cipher    string
	requestID, status                            string
}

type telegramMiniSourceKey struct{}
type miniTGGuardKey struct{}

// RunMiniOperation serializes non-chat Mini mutations with unlink, disable and
// reset. Only this package can create the trusted guard marker; it is never
// accepted from JSON or headers. Business services still enforce their roles.
func (b *TelegramBridge) RunMiniOperation(ctx context.Context, token string, fn func(context.Context) error) error {
	if fn == nil {
		return ErrValidation
	}
	if err := b.ready(ctx); err != nil {
		return err
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return err
	}
	if _, err = b.authenticateMiniTokenTx(ctx, tx, token); err != nil {
		return err
	}
	return fn(context.WithValue(ctx, miniTGGuardKey{}, true))
}

const miniRequestColumns = `id,actor_id,bot_id,bot_fingerprint,generation,operation_key,kind,request_hash,payload_cipher,request_id,COALESCE(session_id,0),COALESCE(turn_id,0),COALESCE(action_id,0),status`

func scanMiniRequest(row interface{ Scan(...any) error }) (miniRequest, error) {
	var v miniRequest
	err := row.Scan(&v.id, &v.actor, &v.bot, &v.fingerprint, &v.generation, &v.key, &v.kind, &v.hash, &v.cipher, &v.requestID, &v.session, &v.turn, &v.action, &v.status)
	return v, err
}

func miniRequestAllowed(v miniRequest, identity miniAuthorization) bool {
	return v.actor == identity.ID && v.bot == identity.bot && v.fingerprint == identity.fingerprint && v.generation == identity.generation && v.status != "cancelled"
}

// Persist the original encrypted input and operation correlation BEFORE the
// business call. A transaction that includes both would lose this correlation
// if SendMessage commits on another connection and the process then crashes.
func (b *TelegramBridge) prepareMiniRequest(ctx context.Context, token string, input miniRequest) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return err
	}
	identity, err := b.authenticateMiniTokenTx(ctx, tx, token)
	if err != nil {
		return err
	}
	old, err := scanMiniRequest(tx.QueryRowContext(ctx, `SELECT `+miniRequestColumns+` FROM v2_ai_telegram_mini_request WHERE actor_id=$1 AND operation_key=$2`, identity.ID, input.key))
	if err == nil {
		if !miniRequestAllowed(old, identity) {
			return ErrForbidden
		}
		if old.hash != input.hash || old.kind != input.kind {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if input.session > 0 {
		if err = b.ai.requireSessionAccessTx(ctx, tx, identity.ID, input.session); err != nil {
			return err
		}
	}
	if input.action > 0 {
		var found int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM v2_ai_action WHERE id=$1 AND session_id=$2`, input.action, input.session).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	if input.requestID != "" {
		// Do not claim a pre-existing web/Bot request as a new Mini task.
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_turn WHERE actor_id=$1 AND request_id=$2)`, identity.ID, input.requestID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
	}
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_ai_telegram_mini_request(actor_id,bot_id,bot_fingerprint,generation,operation_key,kind,request_hash,payload_cipher,request_id,session_id,action_id,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,0),NULLIF($11,0),$12,$12)`, identity.ID, identity.bot, identity.fingerprint, identity.generation, input.key, input.kind, input.hash, input.cipher, input.requestID, input.session, input.action, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Reacquire the same revocation lock after durable prepare. Revocation between
// phases cancels the tombstone; revocation after a business commit can find its
// turn by actor/request_id even if this transaction's acknowledgement is lost.
func (b *TelegramBridge) beginMiniExecution(ctx context.Context, token, key string) (*sql.Tx, miniRequest, error) {
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, miniRequest{}, err
	}
	fail := func(err error) (*sql.Tx, miniRequest, error) {
		_ = tx.Rollback()
		return nil, miniRequest{}, err
	}
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return fail(err)
	}
	identity, err := b.authenticateMiniTokenTx(ctx, tx, token)
	if err != nil {
		return fail(err)
	}
	v, err := scanMiniRequest(tx.QueryRowContext(ctx, `SELECT `+miniRequestColumns+` FROM v2_ai_telegram_mini_request WHERE actor_id=$1 AND operation_key=$2`, identity.ID, key))
	if err != nil {
		return fail(err)
	}
	if !miniRequestAllowed(v, identity) {
		return fail(ErrForbidden)
	}
	return tx, v, nil
}

func (b *TelegramBridge) MiniSendMessage(ctx context.Context, token string, req SendMessageRequest) (SendMessageResult, error) {
	var out SendMessageResult
	if !requestIDPattern.MatchString(req.RequestID) || req.SessionID < 0 || len(req.Content) > maxContentBytes || strings.TrimSpace(req.Content) == "" || !utf8.ValidString(req.Content) {
		return out, ErrValidation
	}
	if err := b.ready(ctx); err != nil {
		return out, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return out, ErrValidation
	}
	cipher, err := b.ai.encryptSecret(string(raw))
	if err != nil {
		return out, err
	}
	input := miniRequest{key: "send:" + req.RequestID, kind: "send", hash: requestHash(req), cipher: cipher, requestID: req.RequestID, session: req.SessionID}
	if err = b.prepareMiniRequest(ctx, token, input); err != nil {
		return out, err
	}
	tx, frozen, err := b.beginMiniExecution(ctx, token, input.key)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	// Prefer the committed turn when an earlier bridge acknowledgement was
	// lost. This also keeps retries safe after encrypted payload retention ends.
	var committedHash string
	var committedID int64
	err = tx.QueryRowContext(ctx, `SELECT id,request_hash FROM v2_ai_turn WHERE actor_id=$1 AND request_id=$2`, frozen.actor, frozen.requestID).Scan(&committedID, &committedHash)
	if err == nil {
		if committedHash != frozen.hash {
			return out, ErrConflict
		}
		out.Turn, err = scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM v2_ai_turn WHERE id=$1`, committedID))
		if err == nil {
			err = b.ai.requireSessionAccessTx(ctx, tx, frozen.actor, out.Turn.SessionID)
		}
		if err == nil {
			out.Session, err = scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM v2_ai_session WHERE id=$1`, out.Turn.SessionID))
		}
		if err != nil {
			return SendMessageResult{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_mini_request SET session_id=$2,turn_id=$3,status='done',updated_at=$4 WHERE id=$1`, frozen.id, out.Session.ID, out.Turn.ID, time.Now().Unix()); err != nil {
			return SendMessageResult{}, err
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if frozen.status == "done" || frozen.cipher == "" {
		return out, ErrConflict
	}
	plain, err := b.ai.decryptSecret(frozen.cipher)
	if err != nil {
		return out, ErrUnavailable
	}
	var original SendMessageRequest
	if json.Unmarshal([]byte(plain), &original) != nil || requestHash(original) != frozen.hash {
		return out, ErrUnavailable
	}
	out, err = b.ai.SendMessage(context.WithValue(ctx, telegramMiniSourceKey{}, true), frozen.actor, original)
	if err != nil {
		return out, err
	}
	if original.SessionID == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_session SET source='telegram' WHERE id=$1`, out.Session.ID); err != nil {
			return SendMessageResult{}, err
		}
		out.Session.Source = "telegram"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_mini_request SET session_id=$2,turn_id=$3,status='done',updated_at=$4 WHERE id=$1`, frozen.id, out.Session.ID, out.Turn.ID, time.Now().Unix()); err != nil {
		return SendMessageResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SendMessageResult{}, err
	}
	return out, nil
}

func (b *TelegramBridge) MiniApproveAction(ctx context.Context, token string, sessionID, actionID int64) (Action, error) {
	return b.miniChangeAction(ctx, token, sessionID, actionID, true)
}

func (b *TelegramBridge) MiniRejectAction(ctx context.Context, token string, sessionID, actionID int64) (Action, error) {
	return b.miniChangeAction(ctx, token, sessionID, actionID, false)
}

func (b *TelegramBridge) miniChangeAction(ctx context.Context, token string, sessionID, actionID int64, approve bool) (Action, error) {
	var out Action
	if sessionID <= 0 || actionID <= 0 {
		return out, ErrValidation
	}
	kind := "reject"
	if approve {
		kind = "approve"
	}
	key := fmt.Sprintf("%s:%d", kind, actionID)
	input := miniRequest{key: key, kind: kind, hash: hashOperation(fmt.Sprintf("%s:%d:%d", kind, sessionID, actionID)), session: sessionID, action: actionID}
	if err := b.prepareMiniRequest(ctx, token, input); err != nil {
		return out, err
	}
	tx, frozen, err := b.beginMiniExecution(ctx, token, key)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = b.ai.requireSessionAccessTx(ctx, tx, frozen.actor, sessionID); err != nil {
		return out, err
	}
	out, err = scanAction(tx.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1 AND session_id=$2`, actionID, sessionID))
	if err != nil {
		return out, err
	}
	if out.Status == "pending" {
		if approve {
			out, err = b.ai.ApproveAction(ctx, frozen.actor, sessionID, actionID)
		} else {
			out, err = b.ai.RejectAction(ctx, frozen.actor, sessionID, actionID)
		}
	} else if (approve && out.ApprovedBy == frozen.actor && out.ApprovalMode == "manual") || (!approve && out.Status == "rejected" && out.RejectedBy == frozen.actor) {
		// A prior call committed but its HTTP/bridge acknowledgement was lost.
		// Return the recorded outcome; never execute an already-claimed action.
		err = nil
	} else {
		err = ErrConflict
	}
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_mini_request SET status='done',updated_at=$2 WHERE id=$1`, frozen.id, time.Now().Unix()); err != nil {
		return Action{}, err
	}
	if err = tx.Commit(); err != nil {
		return Action{}, err
	}
	return out, nil
}
