package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (b *TelegramBridge) telegramCommandReply(ctx context.Context, tx *sql.Tx, i telegramInbox, plain string) (string, [][]TelegramButton, error) {
	if i.Kind == "notice" {
		return plain, telegramMenu(i.SessionID), nil
	}
	command, arg := telegramCommand(plain)
	parts := []string{}
	if strings.HasPrefix(plain, "ai:") {
		parts = strings.Split(plain, ":")
		if len(parts) < 2 {
			return "", nil, ErrValidation
		}
		command = "/" + parts[1]
		if len(parts) > 2 {
			arg = parts[2]
		}
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(session_id,0) FROM v2_ai_telegram_binding WHERE actor_id=$1 AND generation=$2 AND active`, i.ActorID, i.Generation).Scan(&current); err != nil {
		return "", nil, err
	}
	chooseSession := func() (int64, error) {
		if arg == "" {
			if current <= 0 {
				return 0, ErrNotFound
			}
			return current, nil
		}
		sid, err := strconv.ParseInt(arg, 10, 64)
		if err != nil || sid <= 0 {
			return 0, ErrValidation
		}
		if err = b.ai.requireSessionAccess(ctx, i.ActorID, sid); err != nil {
			return 0, err
		}
		return sid, nil
	}
	switch command {
	case "/ai":
		_, err := tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET ai_mode=TRUE WHERE actor_id=$1`, i.ActorID)
		return "已进入 AI 运维模式。直接发送任务；工具与执行记录保存在网页 AI 运维。", telegramMenu(current), err
	case "/new":
		cfg, err := telegramConfigRow(tx.QueryRowContext(ctx, `SELECT `+telegramConfigColumns+` FROM v2_ai_telegram_config WHERE id=1`))
		if err != nil {
			return "", nil, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET session_id=NULL,model=$2,access_mode=$3,ai_mode=TRUE,updated_at=$4 WHERE actor_id=$1`, i.ActorID, cfg.DefaultModel, cfg.DefaultAccessMode, time.Now().Unix())
		return "下一条消息将创建新会话，使用当前后台默认模型与权限设置。既有任务不会因切换而重跑。", telegramMenu(0), err
	case "/exit":
		_, err := tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET ai_mode=FALSE,updated_at=$2 WHERE actor_id=$1`, i.ActorID, time.Now().Unix())
		return "已退出 AI 输入模式，原机器人命令继续可用。正在运行的任务不会自动取消；需要停止请使用 /aistop，/ai 可重新进入。", [][]TelegramButton{{{Text: "进入 AI 运维", Data: "ai:ai"}}}, err
	case "/sessions":
		sessions, err := b.ai.ListSessions(ctx, i.ActorID)
		if err != nil {
			return "", nil, err
		}
		text := "可访问的最近会话："
		buttons := [][]TelegramButton{}
		for index, s := range sessions {
			if index >= 10 {
				break
			}
			text += fmt.Sprintf("\n#%d %s · %s", s.ID, contextText(s.Title, 80), s.Status)
			buttons = append(buttons, []TelegramButton{{Text: fmt.Sprintf("选择 #%d", s.ID), Data: fmt.Sprintf("ai:session:%d", s.ID)}})
		}
		if len(sessions) == 0 {
			text = "暂无会话，直接发送消息即可开始。"
		}
		return text, append(buttons, telegramMenu(current)...), nil
	case "/session":
		sid, err := chooseSession()
		if err != nil {
			return "", nil, err
		}
		detail, err := b.ai.GetSession(ctx, i.ActorID, sid)
		if err != nil {
			return "", nil, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET session_id=$2,model=$3,ai_mode=TRUE,updated_at=$4 WHERE actor_id=$1`, i.ActorID, sid, detail.Session.Model, time.Now().Unix())
		return fmt.Sprintf("已选择会话 #%d：%s。沿用该会话真实授权，不会自动提升权限。", sid, contextText(detail.Session.Title, 100)), telegramMenu(sid), err
	case "/model":
		cfg, _, err := b.ai.loadConfig(ctx)
		if err != nil {
			return "", nil, err
		}
		if arg == "" {
			buttons := [][]TelegramButton{}
			for _, model := range cfg.Models {
				buttons = append(buttons, []TelegramButton{{Text: model, Data: "ai:model:" + hashOperation(model)[:12]}})
			}
			return "选择接下来使用的模型；不改变会话权限。", buttons, nil
		}
		model := ""
		for _, candidate := range cfg.Models {
			if candidate == arg || (len(parts) > 0 && hashOperation(candidate)[:12] == arg) {
				model = candidate
				break
			}
		}
		if model == "" {
			return "", nil, ErrValidation
		}
		_, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_binding SET model=$2,updated_at=$3 WHERE actor_id=$1`, i.ActorID, model, time.Now().Unix())
		return "已选择模型：" + model, telegramMenu(current), err
	case "/stop":
		sid, err := chooseSession()
		if err != nil {
			return "", nil, err
		}
		if err = b.ai.StopSession(ctx, i.ActorID, sid); err != nil {
			return "", nil, err
		}
		// Cancel queued messages for this session as well, so stop cannot race
		// a not-yet-started inbox item into launching a fresh turn.
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_telegram_inbox SET status='cancelled',payload_cipher='' WHERE actor_id=$1 AND session_id=$2 AND kind='message' AND status='queued'`, i.ActorID, sid); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("已请求停止会话 #%d。已发生的远程变更不会回滚，请核对结果。", sid), telegramMenu(sid), nil
	case "/details":
		sid, err := chooseSession()
		if err != nil {
			return "", nil, err
		}
		detail, err := b.ai.GetSession(ctx, i.ActorID, sid)
		if err != nil {
			return "", nil, err
		}
		text := fmt.Sprintf("会话 #%d：%s\n模型：%s\n状态：%s\n权限：%s\n完整工具结果与命令输出请在网页 AI 运维查看。", sid, contextText(detail.Session.Title, 100), detail.Session.Model, detail.Session.Status, detail.Session.AccessMode)
		actions, err := b.ai.ListActions(ctx, i.ActorID, sid)
		if err != nil {
			return "", nil, err
		}
		for index, a := range actions {
			if index >= 8 {
				break
			}
			text += fmt.Sprintf("\n操作 #%d %s · %s", a.ID, a.Kind, a.Status)
		}
		return text, telegramMenu(sid), nil
	case "/approve", "/reject":
		if len(parts) != 5 {
			return "", nil, ErrValidation
		}
		sid, err := chooseSession()
		if err != nil {
			return "", nil, err
		}
		actionID, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || actionID <= 0 {
			return "", nil, ErrValidation
		}
		actions, err := b.ai.ListActions(ctx, i.ActorID, sid)
		if err != nil {
			return "", nil, err
		}
		var selected *Action
		for idx := range actions {
			if actions[idx].ID == actionID {
				selected = &actions[idx]
				break
			}
		}
		if selected == nil || selected.Status != "pending" || selected.ExpiresAt <= time.Now().Unix() {
			return "", nil, ErrConflict
		}
		card, key, err := telegramActionCard(*selected)
		if err != nil || key != parts[4] {
			return "", nil, ErrConflict
		}
		var total, sent int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE status='sent') FROM v2_ai_telegram_outbox WHERE actor_id=$1 AND generation=$2 AND chat_id=$3 AND output_key LIKE $4`, i.ActorID, i.Generation, i.ChatID, fmt.Sprintf("action:%d:%s:%%", actionID, key)).Scan(&total, &sent); err != nil {
			return "", nil, err
		}
		if total != len(telegramTextChunks(card)) || sent != total {
			return "完整操作卡尚未发送成功，不能批准未展示的命令。请稍后再点确认。", telegramMenu(sid), nil
		}
		var action Action
		if command == "/approve" {
			action, err = b.ai.ApproveAction(ctx, i.ActorID, sid, actionID)
		} else {
			action, err = b.ai.RejectAction(ctx, i.ActorID, sid, actionID)
		}
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("操作 #%d 当前状态：%s。完成情况请在会话详情查看；不会自动重放该操作。", action.ID, action.Status), telegramMenu(sid), nil
	}
	return "不支持此 AI 命令，请使用菜单。", telegramMenu(current), nil
}

func telegramActionCard(a Action) (string, string, error) {
	// MarshalJSON removes encrypted panel parameters and exposes only the
	// same credential-safe preview used by the browser approval card.
	raw, err := json.Marshal(a)
	if err != nil {
		return "", "", err
	}
	var public struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &public) != nil {
		return "", "", ErrValidation
	}
	var formatted strings.Builder
	var decoded any
	if json.Unmarshal(public.Payload, &decoded) != nil {
		return "", "", ErrValidation
	}
	pretty, _ := json.MarshalIndent(decoded, "", "  ")
	formatted.Write(pretty)
	text := fmt.Sprintf("待确认操作 #%d\n会话 #%d\n类型：%s\n目标：%s #%d %s\n原因：%s\n完整操作参数（敏感凭据隐藏）：\n%s", a.ID, a.SessionID, a.Kind, a.TargetType, a.TargetID, a.ServerLabel, a.Reason, formatted.String())
	return text, hashOperation(text)[:12], nil
}

func (b *TelegramBridge) queueTelegramActionCards(ctx context.Context, tx *sql.Tx, i telegramInbox, actions []Action) error {
	for _, a := range actions {
		if a.Status != "pending" {
			continue
		}
		text, key, err := telegramActionCard(a)
		if err != nil {
			return err
		}
		chunks := telegramTextChunks(text)
		for index, chunk := range chunks {
			buttons := [][]TelegramButton{}
			if index == len(chunks)-1 {
				buttons = [][]TelegramButton{{{Text: fmt.Sprintf("批准 #%d", a.ID), Data: fmt.Sprintf("ai:approve:%d:%d:%s", a.SessionID, a.ID, key)}, {Text: "拒绝", Data: fmt.Sprintf("ai:reject:%d:%d:%s", a.SessionID, a.ID, key)}}}
			}
			if err = b.queueTelegramOutput(ctx, tx, i, fmt.Sprintf("action:%d:%s:%d", a.ID, key, index), chunk, buttons); err != nil {
				return err
			}
		}
	}
	return nil
}
