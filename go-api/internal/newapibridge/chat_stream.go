package newapibridge

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type chatPromptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatPlan struct {
	Conversation Conversation
	Message      ChatMessage
	RequestID    string
	Payload      map[string]any
	Meta         map[string]any
	Replay       bool
}
type workspaceChatOpener func(context.Context, int64, string, map[string]any) (io.ReadCloser, error)

func (s *Service) SendChat(ctx context.Context, userID int64, auth string, req ChatSendRequest, emit func(string, any) error) error {
	return s.sendChatWithOpener(ctx, userID, auth, req, emit, s.OpenWorkspaceChat)
}

func (s *Service) sendChatWithOpener(ctx context.Context, userID int64, auth string, req ChatSendRequest, emit func(string, any) error, open workspaceChatOpener) error {
	if emit == nil || open == nil {
		return ErrInvalid
	}
	plan, err := s.prepareChat(ctx, userID, req)
	if err != nil {
		return err
	}
	if err = emit("meta", plan.Meta); err != nil {
		if !plan.Replay {
			s.finishChat(userID, plan, "", "failed")
		}
		return err
	}
	if plan.Replay {
		if plan.Message.Content != "" {
			if err = emit("delta", map[string]any{"text": plan.Message.Content}); err != nil {
				return err
			}
		}
		return emit("done", map[string]any{"message": plan.Message, "status": plan.Message.Status, "replayed": true})
	}
	runCtx, cancel := context.WithTimeout(ctx, chatStreamTimeout)
	defer cancel()
	body, err := open(runCtx, userID, auth, plan.Payload)
	if err != nil {
		// Even a timeout while opening the stream can mean the remote accepted a
		// charge. The request ID remains consumed and is never retried implicitly.
		status := "unknown"
		if chatOpenFailedBeforeGeneration(err) {
			status = "failed"
		}
		saved, saveErr := s.finishChat(userID, plan, "", status)
		if saveErr != nil {
			return saveErr
		}
		_ = emit("error", chatError(err))
		_ = emit("done", map[string]any{"message": saved, "status": saved.Status})
		return err
	}
	defer body.Close()
	// Closing the reader also interrupts a body implementation whose Read does
	// not itself observe the request context. No retry follows cancellation.
	go func() { <-runCtx.Done(); _ = body.Close() }()
	var content strings.Builder
	lastSave := time.Now()
	finishReason, streamErr := readWorkspaceStream(body, func(text string) error {
		if content.Len()+len(text) > 256<<10 {
			return ErrChatInterrupted
		}
		content.WriteString(text)
		if time.Since(lastSave) >= 500*time.Millisecond {
			if e := s.saveChatPartial(userID, plan, content.String()); e != nil {
				return e
			}
			lastSave = time.Now()
		}
		return emit("delta", map[string]any{"text": text})
	})
	status := "complete"
	if streamErr != nil || runCtx.Err() != nil {
		status = "unknown"
		if content.Len() > 0 {
			status = "partial"
		}
		if runCtx.Err() != nil {
			streamErr = runCtx.Err()
		}
	}
	saved, saveErr := s.finishChat(userID, plan, content.String(), status)
	if saveErr != nil {
		return saveErr
	}
	if streamErr != nil {
		if errors.Is(streamErr, context.Canceled) {
			_ = emit("error", chatError(context.Canceled))
		} else {
			_ = emit("error", chatError(ErrChatInterrupted))
		}
	}
	if err = emit("done", map[string]any{"message": saved, "status": status, "finish_reason": finishReason}); err != nil {
		return err
	}
	if streamErr != nil {
		return ErrChatInterrupted
	}
	return nil
}

func normalizeChatSend(req *ChatSendRequest) error {
	req.Model = strings.TrimSpace(req.Model)
	if req.ConversationID <= 0 || req.BranchFromMessageID < 0 || !validChatRequestID(req.ClientRequestID) || len(req.Content) > chatMaxMessageBytes || !utf8.ValidString(req.Content) || strings.ContainsRune(req.Content, 0) {
		return ErrInvalid
	}
	if req.BranchFromMessageID == 0 && strings.TrimSpace(req.Content) == "" {
		return ErrInvalid
	}
	if req.Model != "" && !validChatModel(req.Model) {
		return ErrInvalid
	}
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = 2048
	}
	if req.MaxOutputTokens < 64 || req.MaxOutputTokens > chatMaxOutputTokens {
		return ErrInvalid
	}
	return nil
}

func (s *Service) prepareChat(ctx context.Context, userID int64, req ChatSendRequest) (chatPlan, error) {
	plan := chatPlan{RequestID: req.ClientRequestID}
	if userID <= 0 {
		return plan, ErrForbidden
	}
	if err := normalizeChatSend(&req); err != nil {
		return plan, err
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return plan, err
	}
	if err := s.recoverStaleChat(ctx, userID, req.ConversationID); err != nil {
		return plan, err
	}
	// A branch request is keyed by its original input conversation but executes
	// in a newly created conversation. Recover that lease as well on replay.
	var priorConversation int64
	lookupErr := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM v2_newapi_chat_request WHERE user_id=$1 AND client_request_id=$2`, userID, req.ClientRequestID).Scan(&priorConversation)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return plan, lookupErr
	}
	if lookupErr == nil && priorConversation != req.ConversationID {
		if err := s.recoverStaleChat(ctx, userID, priorConversation); err != nil {
			return plan, err
		}
	}
	raw, _ := json.Marshal(req)
	hash := tokenHash(string(raw))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	// Serialize duplicate client IDs, including duplicates targeting different
	// conversations. The paid request is only opened after this transaction commits.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,718301))`, fmt.Sprint(userID)); err != nil {
		return plan, err
	}
	var savedHash, status string
	var existingConversationID, messageID int64
	err = tx.QueryRowContext(ctx, `SELECT request_hash,status,conversation_id,assistant_message_id FROM v2_newapi_chat_request WHERE user_id=$1 AND client_request_id=$2`, userID, req.ClientRequestID).Scan(&savedHash, &status, &existingConversationID, &messageID)
	if err == nil {
		if savedHash != hash {
			return plan, ErrConflict
		}
		if status == "running" {
			return plan, ErrChatBusy
		}
		plan.Message, err = scanChatMessage(tx.QueryRowContext(ctx, `SELECT id,conversation_id,role,content,status,client_request_id,created_at,updated_at FROM v2_newapi_chat_message WHERE user_id=$1 AND id=$2 AND conversation_id=$3`, userID, messageID, existingConversationID))
		if errors.Is(err, sql.ErrNoRows) {
			return plan, ErrConflict
		}
		if err != nil {
			return plan, err
		}
		plan.Replay = true
		plan.Meta = map[string]any{"conversation_id": existingConversationID, "message_id": messageID, "client_request_id": req.ClientRequestID, "request_id": req.ClientRequestID, "replayed": true, "limits": WorkspaceChatLimits()}
		return plan, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return plan, err
	}
	var c Conversation
	var snapshot []byte
	var active string
	err = tx.QueryRowContext(ctx, `SELECT `+conversationColumns+`,character_snapshot,active_request FROM v2_newapi_chat WHERE user_id=$1 AND id=$2 FOR UPDATE`, userID, req.ConversationID).Scan(&c.ID, &c.Title, &c.Mode, &c.Model, &c.CharacterID, &c.Persona, &c.ParentID, &c.CreatedAt, &c.UpdatedAt, &snapshot, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, ErrForbidden
	}
	if err != nil {
		return plan, err
	}
	if active != "" {
		return plan, ErrChatBusy
	}
	if req.Model != "" {
		c.Model = req.Model
	}
	now := time.Now().Unix()
	if req.BranchFromMessageID > 0 {
		c, req.Content, err = branchChat(ctx, tx, userID, c, snapshot, req.BranchFromMessageID, req.Content)
		if err != nil {
			return plan, err
		}
	}
	var card CharacterData
	if c.Mode == "character" {
		if json.Unmarshal(snapshot, &card) != nil {
			return plan, ErrUnavailable
		}
	}
	history, historyTotal, err := chatHistory(ctx, tx, userID, c.ID)
	if err != nil {
		return plan, err
	}
	payload, info, err := buildChatPayload(c, card, history, historyTotal, req.Content, req.MaxOutputTokens)
	if err != nil {
		return plan, err
	}
	var userMessageID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,client_request_id,created_at,updated_at) VALUES($1,$2,'user',$3,'complete',$4,$5,$5) RETURNING id`, c.ID, userID, req.Content, req.ClientRequestID, now).Scan(&userMessageID)
	if err != nil {
		return plan, err
	}
	message := ChatMessage{ConversationID: c.ID, Role: "assistant", Status: "streaming", ClientRequestID: req.ClientRequestID, CreatedAt: now, UpdatedAt: now}
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,client_request_id,created_at,updated_at) VALUES($1,$2,'assistant','','streaming',$3,$4,$4) RETURNING id`, c.ID, userID, req.ClientRequestID, now).Scan(&message.ID)
	if err != nil {
		return plan, err
	}
	if c.Title == "新对话" {
		title := []rune(strings.TrimSpace(req.Content))
		if len(title) > 40 {
			title = title[:40]
		}
		c.Title = string(title)
	}
	_, err = tx.ExecContext(ctx, `UPDATE v2_newapi_chat SET model=$1,title=$2,active_request=$3,active_until=$4,updated_at=$5 WHERE id=$6 AND user_id=$7`, c.Model, c.Title, req.ClientRequestID, time.Now().Add(chatStreamTimeout+time.Minute).Unix(), now, c.ID, userID)
	if err != nil {
		return plan, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_newapi_chat_request(user_id,client_request_id,request_hash,conversation_id,user_message_id,assistant_message_id,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'running',$7,$7)`, userID, req.ClientRequestID, hash, c.ID, userMessageID, message.ID, now)
	if err != nil {
		return plan, err
	}
	plan.Conversation, plan.Message, plan.Payload = c, message, payload
	plan.Meta = map[string]any{"conversation_id": c.ID, "message_id": message.ID, "user_message_id": userMessageID, "client_request_id": req.ClientRequestID, "request_id": req.ClientRequestID, "context": info, "limits": WorkspaceChatLimits(), "replayed": false}
	return plan, tx.Commit()
}

func branchChat(ctx context.Context, tx *sql.Tx, userID int64, c Conversation, snapshot []byte, fromID int64, content string) (Conversation, string, error) {
	var role, original string
	err := tx.QueryRowContext(ctx, `SELECT role,content FROM v2_newapi_chat_message WHERE id=$1 AND conversation_id=$2 AND user_id=$3`, fromID, c.ID, userID).Scan(&role, &original)
	if errors.Is(err, sql.ErrNoRows) {
		return c, content, ErrForbidden
	}
	if err != nil {
		return c, content, err
	}
	cutoff := fromID
	if role == "assistant" {
		err = tx.QueryRowContext(ctx, `SELECT id,content FROM v2_newapi_chat_message WHERE conversation_id=$1 AND user_id=$2 AND role='user' AND id<$3 ORDER BY id DESC LIMIT 1`, c.ID, userID, fromID).Scan(&cutoff, &original)
		if errors.Is(err, sql.ErrNoRows) {
			return c, content, ErrInvalid
		}
		if err != nil {
			return c, content, err
		}
	}
	if strings.TrimSpace(content) == "" {
		content = original
	}
	if strings.TrimSpace(content) == "" {
		return c, content, ErrInvalid
	}
	oldID := c.ID
	c.ParentID = oldID
	c.Title = "分支 · " + c.Title
	if title := []rune(c.Title); len(title) > 100 {
		c.Title = string(title[:100])
	}
	c.CreatedAt = time.Now().Unix()
	c.UpdatedAt = c.CreatedAt
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_newapi_chat(user_id,title,mode,model,character_id,character_snapshot,persona,parent_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) RETURNING id`, userID, c.Title, c.Mode, c.Model, c.CharacterID, string(snapshot), c.Persona, oldID, c.CreatedAt).Scan(&c.ID)
	if err != nil {
		return c, content, err
	}
	// Copy only completed turns. A previous interrupted answer is available in
	// its original conversation but is not silently promoted into valid context.
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,created_at,updated_at) SELECT $1,user_id,role,content,status,created_at,updated_at FROM v2_newapi_chat_message m WHERE user_id=$2 AND conversation_id=$3 AND id<$4 AND status='complete' AND (client_request_id='' OR EXISTS(SELECT 1 FROM v2_newapi_chat_request r WHERE r.user_id=m.user_id AND r.client_request_id=m.client_request_id AND r.status='complete')) ORDER BY id`, c.ID, userID, oldID, cutoff)
	return c, content, err
}
func chatHistory(ctx context.Context, tx *sql.Tx, userID, conversationID int64) ([]chatPromptMessage, int, error) {
	var total int
	where := `FROM v2_newapi_chat_message m WHERE user_id=$1 AND conversation_id=$2 AND status='complete' AND (client_request_id='' OR EXISTS(SELECT 1 FROM v2_newapi_chat_request r WHERE r.user_id=m.user_id AND r.client_request_id=m.client_request_id AND r.status='complete'))`
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) `+where, userID, conversationID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT role,content `+where+` ORDER BY id DESC LIMIT 100`, userID, conversationID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	history := []chatPromptMessage{}
	for rows.Next() {
		var m chatPromptMessage
		if err = rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, 0, err
		}
		history = append(history, m)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	return history, total, nil
}

func buildChatPayload(c Conversation, card CharacterData, history []chatPromptMessage, total int, content string, maxOutput int) (map[string]any, map[string]any, error) {
	const defaultSystem = "你是用户的 AI 聊天助手。回答应清晰、自然。"
	system := defaultSystem
	current := chatPromptMessage{Role: "user", Content: content}
	before, after, used, dropped := "", "", 0, 0
	post := ""
	if c.Mode == "character" {
		if card.SystemPrompt != "" {
			system = strings.ReplaceAll(card.SystemPrompt, "{{original}}", defaultSystem)
		}
		before, after, used, dropped = selectLore(card.CharacterBook, append(append([]chatPromptMessage{}, history...), current))
		system += "\n" + before + "\n角色：" + card.Name + "\n" + card.Description + "\n" + card.Personality + "\n" + card.Scenario + "\n" + after
		if card.MessageExample != "" {
			system += "\n以下为对话风格示例（不是本次会话消息）：\n" + card.MessageExample
		}
		system = replaceCharacterVariables(system, card.Name, c.Persona)
		post = replaceCharacterVariables(strings.ReplaceAll(card.PostHistoryInstructions, "{{original}}", ""), card.Name, c.Persona)
	}
	if c.Persona != "" {
		system += "\n用户人设：\n" + c.Persona
	}
	cost := len(system) + len(content) + len(post) + 32
	if cost > chatContextBudget {
		return nil, nil, fmt.Errorf("%w：角色、人设和当前消息超过上下文预算", ErrInvalid)
	}
	available := chatContextBudget - cost
	start := len(history)
	for start > 0 {
		m := history[start-1]
		n := len(m.Content) + 16
		if n > available {
			break
		}
		available -= n
		start--
	}
	// Do not keep an assistant reply after trimming away the user message that
	// prompted it. The initial character greeting is the one intentional exception.
	for start > 0 && start < len(history) && history[start].Role == "assistant" {
		available += len(history[start].Content) + 16
		start++
	}
	messages := []chatPromptMessage{{Role: "system", Content: system}}
	messages = append(messages, history[start:]...)
	messages = append(messages, current)
	if post != "" {
		messages = append(messages, chatPromptMessage{Role: "system", Content: post})
	}
	info := map[string]any{"history_messages_included": len(history) - start, "history_messages_omitted": total - len(history) + start, "input_budget_used": chatContextBudget - available, "budget_unit": "utf8_bytes_conservative_token_estimate", "lore_entries_used": used, "lore_entries_omitted": dropped, "max_output_tokens": maxOutput}
	return map[string]any{"model": c.Model, "messages": messages, "stream": true, "max_tokens": maxOutput}, info, nil
}
func replaceCharacterVariables(value, name, persona string) string {
	// Persona is descriptive text, not a display name. It is injected separately.
	replacer := strings.NewReplacer("{{char}}", name, "{{CHAR}}", name, "<BOT>", name, "<bot>", name, "{{user}}", "用户", "{{USER}}", "用户", "<USER>", "用户", "<user>", "用户")
	return replacer.Replace(value)
}

func (s *Service) saveChatPartial(userID int64, plan chatPlan, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE v2_newapi_chat_message SET content=$1,updated_at=$2 WHERE id=$3 AND user_id=$4 AND status='streaming'`, content, time.Now().Unix(), plan.Message.ID, userID)
	return chatDBError(err)
}
func (s *Service) finishChat(userID int64, plan chatPlan, content, status string) (ChatMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChatMessage{}, chatDBError(err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	message, err := scanChatMessage(tx.QueryRowContext(ctx, `UPDATE v2_newapi_chat_message SET content=$1,status=$2,updated_at=$3 WHERE id=$4 AND user_id=$5 RETURNING id,conversation_id,role,content,status,client_request_id,created_at,updated_at`, content, status, now, plan.Message.ID, userID))
	if err != nil {
		return message, chatDBError(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_newapi_chat_request SET status=$1,updated_at=$2 WHERE user_id=$3 AND client_request_id=$4`, status, now, userID, plan.RequestID); err != nil {
		return message, chatDBError(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_newapi_chat SET active_request='',active_until=0,updated_at=$1 WHERE user_id=$2 AND id=$3 AND active_request=$4`, now, userID, plan.Conversation.ID, plan.RequestID); err != nil {
		return message, chatDBError(err)
	}
	return message, chatDBError(tx.Commit())
}

func readWorkspaceStream(body io.Reader, delta func(string) error) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var lines []string
	frameBytes := 0
	finishReason := ""
	done := false
	consume := func() error {
		if len(lines) == 0 {
			return nil
		}
		data := strings.Join(lines, "\n")
		lines = nil
		frameBytes = 0
		if data == "[DONE]" {
			done = true
			return nil
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Content   json.RawMessage `json:"content"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			return ErrChatInterrupted
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			return ErrChatInterrupted
		}
		for _, choice := range event.Choices {
			if len(choice.Delta.ToolCalls) > 0 && string(choice.Delta.ToolCalls) != "null" && string(choice.Delta.ToolCalls) != "[]" {
				return ErrChatInterrupted
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
				if finishReason == "tool_calls" || finishReason == "function_call" {
					return ErrChatInterrupted
				}
			}
			if len(choice.Delta.Content) > 0 && string(choice.Delta.Content) != "null" {
				var text string
				if json.Unmarshal(choice.Delta.Content, &text) != nil {
					return ErrChatInterrupted
				}
				if text != "" {
					if err := delta(text); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := consume(); err != nil {
				return finishReason, err
			}
			if done {
				return finishReason, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			frameBytes += len(value) + 1
			if frameBytes > 1<<20 {
				return finishReason, ErrChatInterrupted
			}
			lines = append(lines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return finishReason, ErrChatInterrupted
	}
	if err := consume(); err != nil {
		return finishReason, err
	}
	if !done && finishReason == "" {
		return finishReason, ErrChatInterrupted
	}
	return finishReason, nil
}

// These are fixed, classified rejections before receiving a generation stream.
// A transport failure remains unknown because the request may have been accepted.
func chatOpenFailedBeforeGeneration(err error) bool {
	for _, known := range []error{ErrForbidden, ErrInvalid, ErrDisabled, ErrWorkspaceNotConnected, ErrWorkspaceQuota, ErrWorkspaceRateLimit, ErrWorkspaceContext, ErrWorkspaceModel} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}
