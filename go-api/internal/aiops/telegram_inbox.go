package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (b *TelegramBridge) HandleUpdate(ctx context.Context, in TelegramInput) (bool, error) {
	handled, err := b.handleTelegramUpdate(ctx, in)
	if !handled || err == nil {
		return handled, err
	}
	if !errors.Is(err, ErrValidation) && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBusy) {
		return handled, err
	}
	// The inner transaction has rolled back before recording a fixed public
	// response. User mistakes are acknowledged once, not retried as HTTP 500s.
	bot, ready := b.identity()
	if !ready || bot != in.BotID {
		return true, nil
	}
	tx, dbErr := b.ai.db.BeginTx(ctx, nil)
	if dbErr != nil {
		return true, dbErr
	}
	defer tx.Rollback()
	if dbErr = lockTelegramBridge(ctx, tx); dbErr != nil {
		return true, dbErr
	}
	return true, b.publicTelegramNotice(ctx, tx, in, telegramSafeError(err))
}

func (b *TelegramBridge) handleTelegramUpdate(ctx context.Context, in TelegramInput) (bool, error) {
	if !in.IsPrivate || in.FromID <= 0 || in.ChatID != in.FromID || in.UpdateID < 0 || in.BotID == "" {
		return false, nil
	}
	command, argument := telegramCommand(in.Text)
	callback := strings.HasPrefix(in.Callback, "ai:")
	known := callback || command == "/aibind" || command == "/ai" || command == "/new" || command == "/sessions" || command == "/model" || command == "/stop" || command == "/details" || command == "/exit"
	if in.Callback != "" && !callback {
		return false, nil
	}
	if command != "" && !known {
		return false, nil
	}
	if err := b.ready(ctx); err != nil {
		return known, err
	}
	bot, ready := b.identity()
	if !ready || bot != in.BotID {
		return known, nil
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return known, err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return known, err
	}
	var duplicate bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2)`, in.BotID, in.UpdateID).Scan(&duplicate); err != nil {
		return known, err
	}
	if duplicate {
		return true, tx.Commit()
	}
	cfg, err := telegramConfigRow(tx.QueryRowContext(ctx, `SELECT `+telegramConfigColumns+` FROM v2_ai_telegram_config WHERE id=1`))
	if err != nil {
		return known, err
	}
	if !cfg.Enabled {
		if known {
			return true, b.publicTelegramNotice(ctx, tx, in, "Telegram AI 运维未启用，请联系管理员在后台开启。")
		}
		return false, nil
	}
	if command == "/aibind" {
		err = b.bindInput(ctx, tx, in, argument, cfg)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrValidation) || errors.Is(err, ErrConflict) {
			return true, b.publicTelegramNotice(ctx, tx, in, "绑定码无效、已过期或已使用，或 Telegram 已被其他账号授权。请联系管理员重新授权。")
		}
		return true, err
	}
	bound, err := telegramBindingForInput(ctx, tx, in)
	if errors.Is(err, ErrForbidden) {
		if known {
			return true, b.publicTelegramNotice(ctx, tx, in, "尚未获得 AI 运维授权，请让管理员在后台生成一次性绑定码，使用 /aibind 完成绑定。普通订阅绑定不提供运维权限。")
		}
		return false, nil
	}
	if err != nil {
		return known, err
	}
	if !known && !bound.enabled {
		return false, nil
	}
	if command == "" && !callback && (strings.TrimSpace(in.Text) == "" || len(in.Text) > maxContentBytes || !utf8.ValidString(in.Text)) {
		return true, ErrValidation
	}
	kind := "command"
	payload := in.Text
	if callback {
		payload = in.Callback
	}
	var actionID int64
	if strings.HasPrefix(in.Callback, "ai:approve:") || strings.HasPrefix(in.Callback, "ai:reject:") {
		parts := strings.Split(in.Callback, ":")
		if len(parts) == 5 {
			sid, sidErr := strconv.ParseInt(parts[2], 10, 64)
			target, targetErr := strconv.ParseInt(parts[3], 10, 64)
			if sidErr == nil && targetErr == nil && sid > 0 && target > 0 && b.ai.requireSessionAccess(ctx, bound.actor, sid) == nil {
				// Persist the target before dispatch so revocation can stop an
				// approval committed just before a process loses its inbox ack.
				var found int64
				e := tx.QueryRowContext(ctx, `SELECT id FROM v2_ai_action WHERE id=$1 AND session_id=$2 AND status='pending'`, target, sid).Scan(&found)
				if e == nil {
					actionID = found
					bound.session = sid
				} else if !errors.Is(e, sql.ErrNoRows) {
					return true, e
				}
			}
		}
	}
	// Stop is not a queued model task. Cancel unstarted input before a worker
	// can claim it, and signal existing turns under the same revocation lock.
	if command == "/stop" || strings.HasPrefix(in.Callback, "ai:stop:") {
		sid := bound.session
		if callback {
			pieces := strings.Split(in.Callback, ":")
			if len(pieces) != 3 {
				return true, ErrValidation
			}
			argument = pieces[2]
		}
		if argument != "" {
			sid, err = strconv.ParseInt(argument, 10, 64)
			if err != nil || sid <= 0 {
				return true, ErrValidation
			}
			if err = b.ai.requireSessionAccess(ctx, bound.actor, sid); err != nil {
				return true, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET status='cancelled',payload_cipher='' WHERE actor_id=$1 AND generation=$2 AND kind='message' AND status='queued' AND ($3::BIGINT=0 OR session_id=$3 OR session_id IS NULL)`, bound.actor, bound.generation, sid); err != nil {
			return true, err
		}
		if sid > 0 {
			if err = b.ai.StopSession(ctx, bound.actor, sid); err != nil {
				return true, err
			}
		}
		kind = "notice"
		payload = "已取消尚未启动的消息，并请求停止当前任务。已经发生的远程变更不会回滚，请核对结果。"
	}
	requestID := "tg:" + hashOperation(fmt.Sprintf("%s:%d", in.BotID, in.UpdateID))
	if command == "" && !callback {
		var busy bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_telegram_inbox WHERE actor_id=$1 AND generation=$2 AND ((kind='message' AND status IN('queued','running')) OR (kind='command' AND status='queued')))`, bound.actor, bound.generation).Scan(&busy); err != nil {
			return true, err
		}
		if busy {
			kind = "notice"
			payload = "当前任务或会话切换命令仍在处理，请等待确认后再发送；没有排队或执行这条新任务。"
		} else {
			req := SendMessageRequest{SessionID: bound.session, Model: bound.model, Content: in.Text, RequestID: requestID, AccessMode: bound.mode}
			if req.Model == "" {
				req.Model = cfg.DefaultModel
			}
			if req.Model == "" {
				models, _, e := b.ai.loadConfig(ctx)
				if e != nil {
					return true, e
				}
				if len(models.Models) > 0 {
					req.Model = models.Models[0]
				}
			}
			if req.SessionID > 0 {
				var session Session
				session, err = scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM v2_ai_session WHERE id=$1`, req.SessionID))
				if err != nil {
					return true, err
				}
				// Never upgrade a selected browser session. Full access is valid
				// only for its recorded granting actor and exact authorization version.
				req.AccessMode = ""
				if session.AccessMode == "full" && session.FullAccessBy == bound.actor {
					req.AccessMode = "full"
					version := session.AccessVersion
					req.AccessVersion = &version
				}
			}
			raw, e := json.Marshal(req)
			if e != nil {
				return true, e
			}
			payload = string(raw)
			kind = "message"
		}
	}
	cipher, err := b.ai.encryptSecret(payload)
	if err != nil {
		return true, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_telegram_inbox(bot_id,update_id,chat_id,actor_id,generation,kind,payload_cipher,request_id,session_id,created_at,updated_at,action_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,0),$10,$10,NULLIF($11,0)) RETURNING id`, in.BotID, in.UpdateID, in.ChatID, bound.actor, bound.generation, kind, cipher, requestID, bound.session, time.Now().Unix(), actionID).Scan(&id)
	if err != nil {
		return true, err
	}
	return true, tx.Commit()
}

func (b *TelegramBridge) publicTelegramNotice(ctx context.Context, tx *sql.Tx, in TelegramInput, text string) error {
	var recent bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND chat_id=$2 AND kind='rejected' AND created_at>$3)`, in.BotID, in.ChatID, time.Now().Add(-30*time.Second).Unix()).Scan(&recent); err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_ai_telegram_inbox(bot_id,update_id,chat_id,actor_id,generation,kind,status,created_at,updated_at) VALUES($1,$2,$3,0,0,'rejected','done',$4,$4) ON CONFLICT(bot_id,update_id) DO UPDATE SET update_id=EXCLUDED.update_id RETURNING id`, in.BotID, in.UpdateID, in.ChatID, time.Now().Unix()).Scan(&id); err != nil {
		return err
	}
	if !recent {
		if err := b.queueTelegramOutput(ctx, tx, telegramInbox{ID: id, BotID: in.BotID, ChatID: in.ChatID}, "reply", text, nil); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (b *TelegramBridge) bindInput(ctx context.Context, tx *sql.Tx, in TelegramInput, code string, cfg TelegramConfig) error {
	if len(code) != 32 {
		return ErrValidation
	}
	var actor int64
	err := tx.QueryRowContext(ctx, `SELECT actor_id FROM v2_ai_telegram_code WHERE code_hash=$1 AND used_at=0 AND expires_at>$2 FOR UPDATE`, hashOperation(code), time.Now().Unix()).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if err = b.ai.requireOperatorTx(ctx, tx, actor); err != nil {
		return err
	}
	var claimed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_telegram_binding WHERE active AND bot_id=$1 AND telegram_id=$2 AND actor_id<>$3)`, in.BotID, in.FromID, actor).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return ErrConflict
	}
	if err = stopTelegramBindingsTx(ctx, tx, actor); err != nil {
		return err
	}
	now := time.Now().Unix()
	var generation int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_telegram_binding(actor_id,bot_id,telegram_id,active,generation,session_id,model,access_mode,ai_mode,bound_at,updated_at) VALUES($1,$2,$3,TRUE,1,NULL,$4,$5,TRUE,$6,$6) ON CONFLICT(actor_id) DO UPDATE SET bot_id=EXCLUDED.bot_id,telegram_id=EXCLUDED.telegram_id,active=TRUE,generation=v2_ai_telegram_binding.generation+1,session_id=NULL,model=EXCLUDED.model,access_mode=EXCLUDED.access_mode,ai_mode=TRUE,bound_at=EXCLUDED.bound_at,updated_at=EXCLUDED.updated_at RETURNING generation`, actor, in.BotID, in.FromID, cfg.DefaultModel, cfg.DefaultAccessMode, now).Scan(&generation)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_code SET used_at=$2 WHERE code_hash=$1`, hashOperation(code), now); err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_telegram_inbox(bot_id,update_id,chat_id,actor_id,generation,kind,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'bind','done',$6,$6) RETURNING id`, in.BotID, in.UpdateID, in.ChatID, actor, generation, now).Scan(&id)
	if err != nil {
		return err
	}
	text := "AI 运维已独立绑定。发送文字开始会话；/ainew 新建，/aisessions 切换会话，/aimodel 选择模型，/aistop 停止，/aiexit 返回原机器人功能。当前新会话权限：" + cfg.DefaultAccessMode
	if err = b.queueTelegramOutput(ctx, tx, telegramInbox{ID: id, BotID: in.BotID, ChatID: in.ChatID, ActorID: actor, Generation: generation}, "reply", text, telegramMenu(0)); err != nil {
		return err
	}
	return tx.Commit()
}

type telegramInbox struct {
	ID, ChatID, ActorID, Generation, SessionID, TurnID, DraftVersion int64
	BotID, Kind, Cipher, RequestID, Status                           string
}

const telegramInboxColumns = `id,bot_id,chat_id,actor_id,generation,kind,payload_cipher,request_id,COALESCE(session_id,0),COALESCE(turn_id,0),status,draft_version`

func scanTelegramInbox(row interface{ Scan(...any) error }) (telegramInbox, error) {
	var i telegramInbox
	err := row.Scan(&i.ID, &i.BotID, &i.ChatID, &i.ActorID, &i.Generation, &i.Kind, &i.Cipher, &i.RequestID, &i.SessionID, &i.TurnID, &i.Status, &i.DraftVersion)
	return i, err
}

func telegramMenu(session int64) [][]TelegramButton {
	rows := [][]TelegramButton{{{Text: "新建会话", Data: "ai:new"}, {Text: "会话列表", Data: "ai:sessions"}, {Text: "选择模型", Data: "ai:model"}}}
	if session > 0 {
		rows = append(rows, []TelegramButton{{Text: "停止任务", Data: fmt.Sprintf("ai:stop:%d", session)}, {Text: "查看详情", Data: fmt.Sprintf("ai:details:%d", session)}})
	}
	return append(rows, []TelegramButton{{Text: "退出 AI 模式", Data: "ai:exit"}})
}

func (b *TelegramBridge) queueTelegramOutput(ctx context.Context, tx *sql.Tx, i telegramInbox, key, text string, buttons [][]TelegramButton) error {
	cipher, err := b.ai.encryptSecret(text)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(buttons)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_ai_telegram_outbox(inbox_id,bot_id,chat_id,actor_id,generation,session_id,output_key,content_cipher,buttons,created_at,updated_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,0),$7,$8,$9,$10,$10) ON CONFLICT(inbox_id,output_key) DO UPDATE SET content_cipher=EXCLUDED.content_cipher,buttons=EXCLUDED.buttons,status='queued',revision=v2_ai_telegram_outbox.revision+1,updated_at=EXCLUDED.updated_at WHERE v2_ai_telegram_outbox.status<>'cancelled'`, i.ID, i.BotID, i.ChatID, i.ActorID, i.Generation, i.SessionID, key, cipher, string(raw), time.Now().Unix())
	return err
}
