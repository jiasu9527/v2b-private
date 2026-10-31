package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (b *TelegramBridge) Start(ctx context.Context, transport TelegramTransport) error {
	if transport == nil {
		return ErrValidation
	}
	if err := b.ready(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		return ErrConflict
	}
	worker, cancel := context.WithCancel(ctx)
	b.transport = transport
	b.cancel = cancel
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
				b.telegramTick(worker)
			}
		}
	}()
	return nil
}

func (b *TelegramBridge) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	b.wg.Wait()
}

func (b *TelegramBridge) telegramTick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := b.sweepTelegram(ctx); err != nil {
		return
	}
	_ = b.dispatchTelegram(ctx)
	_ = b.pollTelegramTurns(ctx)
	for count := 0; count < 4; count++ {
		sent, err := b.deliverTelegram(ctx)
		if err != nil || !sent {
			return
		}
	}
}

func (b *TelegramBridge) sweepTelegram(ctx context.Context) error {
	// Reconcile only the existing action lease; never replay remote work after
	// a restart. Otherwise an abandoned SSH action can keep TG sessions busy.
	if err := b.ai.reconcileExpiredActions(ctx); err != nil {
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
	bot, ready := b.identity()
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM v2_ai_telegram_config WHERE id=1`).Scan(&enabled); err != nil {
		return err
	}
	if !ready || !enabled {
		if err = stopTelegramBindingsTx(ctx, tx, 0); err != nil {
			return err
		}
		return tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.actor_id FROM v2_ai_telegram_binding b LEFT JOIN v2_user u ON u.id=b.actor_id WHERE b.active AND (b.bot_id<>$1 OR u.id IS NULL OR u.banned<>0 OR (u.is_admin<>1 AND u.is_staff<>1))`, bot)
	if err != nil {
		return err
	}
	actors := []int64{}
	for rows.Next() {
		var actor int64
		if err = rows.Scan(&actor); err != nil {
			rows.Close()
			return err
		}
		actors = append(actors, actor)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, actor := range actors {
		if err = stopTelegramBindingsTx(ctx, tx, actor); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET active=FALSE,ai_mode=FALSE,generation=generation+1,session_id=NULL WHERE actor_id=$1`, actor); err != nil {
			return err
		}
	}
	// Payloads disappear immediately after dispatch; output bodies are retained
	// briefly for Telegram retries, while update IDs stay as compact tombstones.
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_outbox SET content_cipher='',buttons='[]' WHERE status IN('sent','cancelled') AND updated_at<$1 AND content_cipher<>''`, time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_telegram_code WHERE expires_at<$1`, time.Now().Unix()); err != nil {
		return err
	}
	if err = b.sweepTelegramMiniTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *TelegramBridge) validTelegramInbox(ctx context.Context, tx *sql.Tx, i telegramInbox) bool {
	bot, ready := b.identity()
	if !ready || bot != i.BotID {
		return false
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_telegram_binding b JOIN v2_user u ON u.id=b.actor_id JOIN v2_ai_telegram_config c ON c.id=1 WHERE c.enabled AND b.active AND b.actor_id=$1 AND b.bot_id=$2 AND b.telegram_id=$3 AND b.generation=$4 AND u.banned=0 AND (u.is_admin=1 OR u.is_staff=1))`, i.ActorID, i.BotID, i.ChatID, i.Generation).Scan(&valid)
	return err == nil && valid
}

func cancelTelegramInbox(ctx context.Context, tx *sql.Tx, i telegramInbox) error {
	for _, q := range []string{
		`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE $1::BIGINT>0 AND actor_id=$2 AND request_id=$3 AND status='running'`,
		`UPDATE v2_ai_action SET stop_requested=TRUE WHERE $1::BIGINT>0 AND session_id IN(SELECT session_id FROM v2_ai_turn WHERE actor_id=$2 AND request_id=$3) AND status='running'`,
		`UPDATE v2_ai_telegram_inbox SET status='cancelled',payload_cipher='' WHERE id=$1 AND actor_id=$2 AND request_id=$3`,
		`UPDATE v2_ai_telegram_outbox SET status='cancelled',content_cipher='',buttons='[]' WHERE inbox_id=$1 AND actor_id=$2 AND EXISTS(SELECT 1 FROM v2_ai_telegram_inbox i WHERE i.id=$1 AND i.request_id=$3)`,
	} {
		if _, err := tx.ExecContext(ctx, q, i.ID, i.ActorID, i.RequestID); err != nil {
			return err
		}
	}
	return nil
}

func (b *TelegramBridge) dispatchTelegram(ctx context.Context) error {
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return err
	}
	i, err := scanTelegramInbox(tx.QueryRowContext(ctx, `SELECT `+telegramInboxColumns+` FROM v2_ai_telegram_inbox WHERE status='queued' ORDER BY id LIMIT 1 FOR UPDATE`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !b.validTelegramInbox(ctx, tx, i) {
		if err = cancelTelegramInbox(ctx, tx, i); err != nil {
			return err
		}
		return tx.Commit()
	}
	plain, err := b.ai.decryptSecret(i.Cipher)
	if err != nil {
		return err
	}
	if i.Kind != "message" {
		text, buttons, commandErr := b.telegramCommandReply(ctx, tx, i, plain)
		if commandErr != nil {
			text = telegramSafeError(commandErr)
			buttons = telegramMenu(i.SessionID)
		}
		for index, chunk := range telegramTextChunks(text) {
			markup := [][]TelegramButton{}
			if index == len(telegramTextChunks(text))-1 {
				markup = buttons
			}
			if err = b.queueTelegramOutput(ctx, tx, i, fmt.Sprintf("reply:%d", index), chunk, markup); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET status='done',payload_cipher='',updated_at=$2 WHERE id=$1`, i.ID, time.Now().Unix()); err != nil {
			return err
		}
		return tx.Commit()
	}
	var req SendMessageRequest
	if json.Unmarshal([]byte(plain), &req) != nil || req.RequestID != i.RequestID {
		return ErrValidation
	}
	// SendMessage's actor/request-ID CAS is the execution boundary. A crash
	// before this inbox acknowledgement replays the exact encrypted request,
	// returning the original turn rather than running the task twice.
	result, sendErr := b.ai.SendMessage(ctx, i.ActorID, req)
	if sendErr != nil {
		if !errors.Is(sendErr, ErrBusy) && !errors.Is(sendErr, ErrForbidden) && !errors.Is(sendErr, ErrNotFound) && !errors.Is(sendErr, ErrConflict) && !errors.Is(sendErr, ErrValidation) && !errors.Is(sendErr, ErrUnavailable) {
			return sendErr
		}
		if err = b.queueTelegramOutput(ctx, tx, i, "reply", telegramSafeError(sendErr), telegramMenu(i.SessionID)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET status='failed',payload_cipher='',updated_at=$2 WHERE id=$1`, i.ID, time.Now().Unix()); err != nil {
			return err
		}
		return tx.Commit()
	}
	i.SessionID = result.Session.ID
	i.TurnID = result.Turn.ID
	if req.SessionID == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_session SET source='telegram' WHERE id=$1`, i.SessionID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET session_id=$2,model=$3,updated_at=$4 WHERE actor_id=$1 AND generation=$5 AND active`, i.ActorID, i.SessionID, req.Model, time.Now().Unix(), i.Generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET status='running',session_id=$2,turn_id=$3,payload_cipher='',updated_at=$4 WHERE id=$1`, i.ID, i.SessionID, i.TurnID, time.Now().Unix()); err != nil {
		return err
	}
	if err = b.queueTelegramOutput(ctx, tx, i, "progress", fmt.Sprintf("正在处理 · 会话 #%d\n可在网页 AI 运维查看工具及操作详情。", i.SessionID), telegramMenu(i.SessionID)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_outbox SET silent=TRUE WHERE inbox_id=$1 AND output_key='progress'`, i.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func telegramSafeError(err error) string {
	switch {
	case errors.Is(err, ErrBusy):
		return "当前会话或运维服务正在处理任务；本条没有排队执行，请等待或停止后重新发送。"
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		return "授权或会话已变化，操作未执行。请在网页核对账号权限与 Telegram 绑定。"
	case errors.Is(err, ErrConflict):
		return "会话或授权版本已变化，未自动重新授权或重试。请重新选择会话后发送新请求。"
	case errors.Is(err, ErrValidation):
		return "参数无效，未执行。请核对命令、模型或会话编号。"
	default:
		return "AI 运维暂不可用，请在网页核对配置与任务状态；没有自动重放业务操作。"
	}
}

func (b *TelegramBridge) pollTelegramTurns(ctx context.Context) error {
	rows, err := b.ai.db.QueryContext(ctx, `SELECT id FROM v2_ai_telegram_inbox WHERE status='running' ORDER BY id LIMIT 20`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = b.pollTelegramTurn(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (b *TelegramBridge) pollTelegramTurn(ctx context.Context, id int64) error {
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return err
	}
	i, err := scanTelegramInbox(tx.QueryRowContext(ctx, `SELECT `+telegramInboxColumns+` FROM v2_ai_telegram_inbox WHERE id=$1 AND status='running' FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if i.SessionID <= 0 || i.TurnID <= 0 || !b.validTelegramInbox(ctx, tx, i) {
		if err = cancelTelegramInbox(ctx, tx, i); err != nil {
			return err
		}
		return tx.Commit()
	}
	draft, err := b.ai.GetDraft(ctx, i.ActorID, i.SessionID)
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			if err = cancelTelegramInbox(ctx, tx, i); err != nil {
				return err
			}
			return tx.Commit()
		}
		return err
	}
	cfg, err := telegramConfigRow(tx.QueryRowContext(ctx, `SELECT `+telegramConfigColumns+` FROM v2_ai_telegram_config WHERE id=1`))
	if err != nil {
		return err
	}
	var status string
	var expiresAt int64
	if err = tx.QueryRowContext(ctx, `SELECT status,expires_at FROM v2_ai_turn WHERE id=$1`, i.TurnID).Scan(&status, &expiresAt); err != nil {
		return err
	}
	if status == "running" && expiresAt > time.Now().Unix() && draft.TurnID == i.TurnID && draft.Status == "running" {
		if cfg.StreamUpdates && draft.Version != i.DraftVersion {
			text := fmt.Sprintf("正在处理 · 会话 #%d", i.SessionID)
			if draft.Content != "" {
				parts := telegramTextChunks(draft.Content)
				text = parts[0]
			}
			if err = b.queueTelegramOutput(ctx, tx, i, "progress", text, telegramMenu(i.SessionID)); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET draft_version=$2,updated_at=$3 WHERE id=$1`, i.ID, draft.Version, time.Now().Unix()); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	detail, err := b.ai.GetSession(ctx, i.ActorID, i.SessionID)
	if err != nil {
		return err
	}
	var turn *Turn
	for idx := range detail.Turns {
		if detail.Turns[idx].ID == i.TurnID {
			turn = &detail.Turns[idx]
			break
		}
	}
	if turn == nil {
		return ErrNotFound
	}
	if turn.Status == "running" {
		return tx.Commit()
	}
	texts := []string{}
	for _, m := range detail.Messages {
		if m.TurnID == i.TurnID && m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			texts = append(texts, m.Content)
		}
	}
	text := strings.Join(texts, "\n\n")
	if text == "" {
		text = "本轮已结束，请在网页查看详情。"
	}
	if turn.Status == "failed" || turn.Status == "interrupted" {
		text += "\n\n本轮已中断或失败；请核对已执行操作，未自动重跑任务。"
	}
	buttons := telegramMenu(i.SessionID)
	if turn.Status == "pending" {
		actions, e := b.ai.ListActions(ctx, i.ActorID, i.SessionID)
		if e != nil {
			return e
		}
		if err = b.queueTelegramActionCards(ctx, tx, i, actions); err != nil {
			return err
		}
		text += "\n请完整阅读待确认操作卡后再批准；未批准的操作不会执行。"
	}
	chunks := telegramTextChunks(text)
	for index, chunk := range chunks {
		key := "progress"
		if index > 0 {
			key = fmt.Sprintf("final:%d", index)
		}
		markup := [][]TelegramButton{}
		if index == len(chunks)-1 {
			markup = buttons
		}
		if err = b.queueTelegramOutput(ctx, tx, i, key, chunk, markup); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_outbox SET silent=TRUE WHERE inbox_id=$1 AND output_key=$2`, i.ID, key); err != nil {
			return err
		}
	}
	// Edits do not trigger Telegram notifications. Send one bounded separate
	// notification when requested, never repeat or truncate the answer body.
	if cfg.NotifyCompletion {
		if err = b.queueTelegramOutput(ctx, tx, i, "completion_notice", fmt.Sprintf("会话 #%d 本轮已结束（%s），完整结果已更新。", i.SessionID, turn.Status), telegramMenu(i.SessionID)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET status='done',updated_at=$2 WHERE id=$1`, i.ID, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Limit chunks by UTF-16 units (Telegram's limit), not UTF-8 bytes or runes.
func telegramTextChunks(text string) []string {
	const limit = 3500
	out := []string{}
	start, units := 0, 0
	for index, r := range text {
		cost := 1
		if r > 0xffff {
			cost = 2
		}
		if units+cost > limit {
			out = append(out, text[start:index])
			start = index
			units = 0
		}
		units += cost
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	if len(out) == 0 {
		out = []string{"本轮无文本输出。"}
	}
	return out
}

func (b *TelegramBridge) deliverTelegram(ctx context.Context) (bool, error) {
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return false, err
	}
	var id, messageID, sessionID, inboxID, attempts int64
	var cipher, buttonsRaw string
	var silent bool
	err = tx.QueryRowContext(ctx, `SELECT id,inbox_id,COALESCE(session_id,0),message_id,content_cipher,buttons::TEXT,attempts,silent FROM v2_ai_telegram_outbox WHERE status='queued' AND next_attempt_at<=$1 ORDER BY id LIMIT 1 FOR UPDATE`, time.Now().Unix()).Scan(&id, &inboxID, &sessionID, &messageID, &cipher, &buttonsRaw, &attempts, &silent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	i, err := scanTelegramInbox(tx.QueryRowContext(ctx, `SELECT `+telegramInboxColumns+` FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID))
	if err != nil {
		return false, err
	}
	bot, ready := b.identity()
	public := i.Kind == "rejected" && i.ActorID == 0 && ready && bot == i.BotID
	if i.Status == "cancelled" || (!public && !b.validTelegramInbox(ctx, tx, i)) || (i.Kind == "message" && i.Status != "failed" && (i.SessionID == 0 || sessionID != i.SessionID)) {
		_, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_outbox SET status='cancelled',content_cipher='',buttons='[]' WHERE id=$1`, id)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if sessionID > 0 {
		if err = b.ai.requireSessionAccess(ctx, i.ActorID, sessionID); err != nil {
			if e := cancelTelegramInbox(ctx, tx, i); e != nil {
				return false, e
			}
			return true, tx.Commit()
		}
	}
	// Only external delivery holds a role-row lock through its side effect.
	// Dispatch delegates role locks to SendMessage/ApproveAction themselves;
	// holding this lock there would deadlock a synchronous self-user update.
	if !public {
		if err = b.ai.requireOperatorTx(ctx, tx, i.ActorID); err != nil {
			if e := cancelTelegramInbox(ctx, tx, i); e != nil {
				return false, e
			}
			return true, tx.Commit()
		}
	}
	text, err := b.ai.decryptSecret(cipher)
	if err != nil {
		return false, err
	}
	var buttons [][]TelegramButton
	if json.Unmarshal([]byte(buttonsRaw), &buttons) != nil {
		return false, ErrValidation
	}
	b.mu.Lock()
	transport := b.transport
	b.mu.Unlock()
	if transport == nil {
		return false, ErrUnavailable
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if messageID > 0 {
		err = transport.Edit(sendCtx, i.ChatID, messageID, text, buttons)
	} else {
		quiet, ok := transport.(interface {
			SendSilent(context.Context, int64, string, [][]TelegramButton) (int64, error)
		})
		if silent && ok {
			messageID, err = quiet.SendSilent(sendCtx, i.ChatID, text, buttons)
		} else {
			messageID, err = transport.Send(sendCtx, i.ChatID, text, buttons)
		}
	}
	cancel()
	if err != nil {
		delay := time.Duration(1<<min(attempts, 8)) * time.Second
		var retry interface{ RetryAfter() time.Duration }
		if errors.As(err, &retry) && retry.RetryAfter() > delay {
			delay = retry.RetryAfter()
		}
		if delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		_, dbErr := tx.ExecContext(ctx, `UPDATE v2_ai_telegram_outbox SET attempts=attempts+1,next_attempt_at=$2,updated_at=$3 WHERE id=$1`, id, time.Now().Add(delay).Unix(), time.Now().Unix())
		if dbErr != nil {
			return false, dbErr
		}
		return false, tx.Commit()
	}
	if messageID <= 0 {
		return false, ErrUnavailable
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_outbox SET status='sent',message_id=$2,attempts=0,next_attempt_at=0,updated_at=$3 WHERE id=$1`, id, messageID, time.Now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
