package newapibridge

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

const chatContextBudget = 24000
const chatMaxOutputTokens = 8192
const chatMaxMessageBytes = 16000
const chatStreamTimeout = 5 * time.Minute

var ErrChatBusy = errors.New("此会话仍在生成，请等待完成或停止后再试")
var ErrChatInterrupted = errors.New("生成未完整结束，已保存现有内容；同一请求不会重复计费重发")
var ErrChatEmptyReply = errors.New("模型未返回有效文本，可能已产生用量；请核对用量记录，同一请求不会自动重发")

type Conversation struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Mode        string `json:"mode"`
	Model       string `json:"model"`
	CharacterID int64  `json:"character_id"`
	Persona     string `json:"persona"`
	ParentID    int64  `json:"parent_id"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}
type ConversationCreateRequest struct {
	Title       string `json:"title"`
	Mode        string `json:"mode"`
	Model       string `json:"model"`
	CharacterID int64  `json:"character_id"`
	Persona     string `json:"persona"`
}
type ConversationPage struct {
	Items    []Conversation `json:"items"`
	Total    int64          `json:"total"`
	Current  int            `json:"current"`
	PageSize int            `json:"page_size"`
}
type ChatMessage struct {
	ID              int64  `json:"id"`
	ConversationID  int64  `json:"conversation_id"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	Status          string `json:"status"`
	ClientRequestID string `json:"client_request_id"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}
type MessagePage struct {
	Items        []ChatMessage `json:"items"`
	HasMore      bool          `json:"has_more"`
	NextBeforeID int64         `json:"next_before_id"`
}
type ConversationDetail struct {
	Conversation Conversation   `json:"conversation"`
	Messages     []ChatMessage  `json:"messages"`
	HasMore      bool           `json:"has_more"`
	NextBeforeID int64          `json:"next_before_id"`
	Limits       map[string]any `json:"limits"`
}
type ChatSendRequest struct {
	ConversationID      int64  `json:"conversation_id"`
	ClientRequestID     string `json:"client_request_id"`
	Content             string `json:"content"`
	Model               string `json:"model"`
	MaxOutputTokens     int    `json:"max_output_tokens"`
	BranchFromMessageID int64  `json:"branch_from_message_id"`
}

// ChatRequestState is a receipt, not a generation endpoint. In particular a
// lost branch meta frame can be resolved without submitting another paid turn.
type ChatRequestState struct {
	Found              bool   `json:"found"`
	ClientRequestID    string `json:"client_request_id"`
	Deleted            bool   `json:"deleted"`
	Status             string `json:"status,omitempty"`
	ConversationID     int64  `json:"conversation_id,omitempty"`
	UserMessageID      int64  `json:"user_message_id,omitempty"`
	AssistantMessageID int64  `json:"assistant_message_id,omitempty"`
	CreatedAt          int64  `json:"created_at,omitempty"`
	UpdatedAt          int64  `json:"updated_at,omitempty"`
}

func validChatRequestID(value string) bool {
	if len(value) < 8 || len(value) > 100 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Service) GetChatRequest(ctx context.Context, userID int64, requestID string) (ChatRequestState, error) {
	state := ChatRequestState{ClientRequestID: requestID}
	if userID <= 0 {
		return state, ErrForbidden
	}
	if !validChatRequestID(requestID) {
		return state, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return state, err
	}
	// Missing is explicit and only sql.ErrNoRows means absent. Database failures
	// must not be mistaken for permission to generate a second paid request.
	// No request hash, message text, remote token or credential is exposed.
	err := s.db.QueryRowContext(ctx, `SELECT r.status,r.conversation_id,r.user_message_id,r.assistant_message_id,r.created_at,r.updated_at,(c.id IS NULL) FROM v2_newapi_chat_request r LEFT JOIN v2_newapi_chat c ON c.id=r.conversation_id AND c.user_id=r.user_id WHERE r.user_id=$1 AND r.client_request_id=$2`, userID, requestID).Scan(&state.Status, &state.ConversationID, &state.UserMessageID, &state.AssistantMessageID, &state.CreatedAt, &state.UpdatedAt, &state.Deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return ChatRequestState{ClientRequestID: requestID}, err
	}
	state.Found = true
	return state, nil
}

var workspaceChatSchema = []string{
	`CREATE TABLE IF NOT EXISTS v2_newapi_character(id BIGSERIAL PRIMARY KEY,user_id BIGINT NOT NULL,version BIGINT NOT NULL DEFAULT 1,data JSONB NOT NULL,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_character_user ON v2_newapi_character(user_id,updated_at DESC,id DESC)`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_chat(id BIGSERIAL PRIMARY KEY,user_id BIGINT NOT NULL,title TEXT NOT NULL,mode TEXT NOT NULL CHECK(mode IN ('chat','character')),model TEXT NOT NULL,character_id BIGINT NOT NULL DEFAULT 0,character_snapshot JSONB NOT NULL DEFAULT '{}',persona TEXT NOT NULL DEFAULT '',parent_id BIGINT NOT NULL DEFAULT 0,active_request TEXT NOT NULL DEFAULT '',active_until BIGINT NOT NULL DEFAULT 0,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_chat_user ON v2_newapi_chat(user_id,updated_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_chat_admin_order ON v2_newapi_chat(updated_at DESC,id DESC)`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_chat_message(id BIGSERIAL PRIMARY KEY,conversation_id BIGINT NOT NULL REFERENCES v2_newapi_chat(id) ON DELETE CASCADE,user_id BIGINT NOT NULL,role TEXT NOT NULL CHECK(role IN ('user','assistant')),content TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('complete','streaming','partial','failed','unknown')),client_request_id TEXT NOT NULL DEFAULT '',created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_chat_message_conversation ON v2_newapi_chat_message(user_id,conversation_id,id DESC)`,
	// Retain idempotency tombstones after deleting a conversation; otherwise a lost
	// HTTP response followed by delete/retry could trigger a second paid request.
	`CREATE TABLE IF NOT EXISTS v2_newapi_chat_request(user_id BIGINT NOT NULL,client_request_id TEXT NOT NULL,request_hash TEXT NOT NULL,conversation_id BIGINT NOT NULL,user_message_id BIGINT NOT NULL,assistant_message_id BIGINT NOT NULL,status TEXT NOT NULL,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,PRIMARY KEY(user_id,client_request_id))`,
}

func WorkspaceChatLimits() map[string]any {
	return map[string]any{
		"context_budget": chatContextBudget, "context_budget_unit": "utf8_bytes_conservative_token_estimate",
		"max_output_tokens": chatMaxOutputTokens, "default_output_tokens": 2048, "max_message_bytes": chatMaxMessageBytes,
		"max_history_messages": 100, "max_duration_seconds": int(chatStreamTimeout / time.Second),
		"character_format": "chara_card_v2", "max_character_import_bytes": MaxCharacterImportBytes,
		"lore_recursive_supported": false, "lore_regex_supported": false, "extensions_executed": false,
		"model_context_limit_known": false, "context_note": "本站按 UTF-8 字节数保守限制上下文；各模型实际窗口以上游为准，超出时请缩短对话",
	}
}
func validChatModel(value string) bool {
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, "\x00\r\n\t") && utf8.ValidString(value)
}
func normalizeConversation(req *ConversationCreateRequest) error {
	req.Title = strings.TrimSpace(req.Title)
	req.Model = strings.TrimSpace(req.Model)
	if req.Mode == "" {
		req.Mode = "chat"
	}
	if req.Title == "" {
		req.Title = "新对话"
	}
	if utf8.RuneCountInString(req.Title) > 100 || !validChatModel(req.Model) || len(req.Persona) > 4000 || !utf8.ValidString(req.Persona) {
		return ErrInvalid
	}
	if req.Mode != "chat" && req.Mode != "character" {
		return ErrInvalid
	}
	if (req.Mode == "character" && req.CharacterID <= 0) || (req.Mode == "chat" && req.CharacterID != 0) {
		return ErrInvalid
	}
	return nil
}
func (s *Service) CreateConversation(ctx context.Context, userID int64, req ConversationCreateRequest) (Conversation, error) {
	if userID <= 0 {
		return Conversation{}, ErrForbidden
	}
	if err := normalizeConversation(&req); err != nil {
		return Conversation{}, err
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return Conversation{}, err
	}
	var card CharacterData
	snapshot := []byte(`{}`)
	if req.Mode == "character" {
		c, err := s.GetCharacter(ctx, userID, req.CharacterID)
		if err != nil {
			return Conversation{}, err
		}
		card = c.Data
		snapshot, _ = json.Marshal(card)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	c := Conversation{Title: req.Title, Mode: req.Mode, Model: req.Model, CharacterID: req.CharacterID, Persona: req.Persona, CreatedAt: now, UpdatedAt: now}
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_newapi_chat(user_id,title,mode,model,character_id,character_snapshot,persona,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING id`, userID, c.Title, c.Mode, c.Model, c.CharacterID, string(snapshot), c.Persona, now).Scan(&c.ID)
	if err != nil {
		return c, err
	}
	if card.FirstMessage != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,created_at,updated_at) VALUES($1,$2,'assistant',$3,'complete',$4,$4)`, c.ID, userID, replaceCharacterVariables(card.FirstMessage, card.Name, c.Persona), now)
		if err != nil {
			return c, err
		}
	}
	return c, tx.Commit()
}

const conversationColumns = `id,title,mode,model,character_id,persona,parent_id,created_at,updated_at`

func scanConversation(row interface{ Scan(...any) error }) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.Title, &c.Mode, &c.Model, &c.CharacterID, &c.Persona, &c.ParentID, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}
func (s *Service) ListConversations(ctx context.Context, userID int64, current, size int) (ConversationPage, error) {
	p := ConversationPage{Items: []Conversation{}, Current: current, PageSize: size}
	if userID <= 0 {
		return p, ErrForbidden
	}
	if current < 1 || current > 100000 || size < 1 || size > 100 {
		return p, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return p, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_newapi_chat WHERE user_id=$1`, userID).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+conversationColumns+` FROM v2_newapi_chat WHERE user_id=$1 ORDER BY updated_at DESC,id DESC LIMIT $2 OFFSET $3`, userID, size, (current-1)*size)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		c, e := scanConversation(rows)
		if e != nil {
			return p, e
		}
		p.Items = append(p.Items, c)
	}
	return p, rows.Err()
}
func (s *Service) GetConversation(ctx context.Context, userID, id int64) (ConversationDetail, error) {
	d := ConversationDetail{Messages: []ChatMessage{}, Limits: WorkspaceChatLimits()}
	if userID <= 0 || id <= 0 {
		return d, ErrForbidden
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return d, err
	}
	var err error
	d.Conversation, err = scanConversation(s.db.QueryRowContext(ctx, `SELECT `+conversationColumns+` FROM v2_newapi_chat WHERE user_id=$1 AND id=$2`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	if err != nil {
		return d, err
	}
	if err = s.recoverStaleChat(ctx, userID, id); err != nil {
		return d, err
	}
	p, err := s.GetConversationMessages(ctx, userID, id, 0, 100)
	d.Messages, d.HasMore, d.NextBeforeID = p.Items, p.HasMore, p.NextBeforeID
	return d, err
}
func (s *Service) GetConversationMessages(ctx context.Context, userID, conversationID, beforeID int64, size int) (MessagePage, error) {
	p := MessagePage{Items: []ChatMessage{}}
	if userID <= 0 || conversationID <= 0 {
		return p, ErrForbidden
	}
	if beforeID < 0 || size < 1 || size > 200 {
		return p, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return p, err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_newapi_chat WHERE id=$1 AND user_id=$2)`, conversationID, userID).Scan(&exists); err != nil {
		return p, err
	}
	if !exists {
		return p, ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,role,content,status,client_request_id,created_at,updated_at FROM v2_newapi_chat_message WHERE user_id=$1 AND conversation_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, userID, conversationID, beforeID, size+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		m, e := scanChatMessage(rows)
		if e != nil {
			return p, e
		}
		p.Items = append(p.Items, m)
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	if len(p.Items) > size {
		p.HasMore = true
		p.Items = p.Items[:size]
	}
	if len(p.Items) > 0 {
		p.NextBeforeID = p.Items[len(p.Items)-1].ID
	}
	for i, j := 0, len(p.Items)-1; i < j; i, j = i+1, j-1 {
		p.Items[i], p.Items[j] = p.Items[j], p.Items[i]
	}
	return p, nil
}
func scanChatMessage(row interface{ Scan(...any) error }) (ChatMessage, error) {
	var m ChatMessage
	err := row.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.Status, &m.ClientRequestID, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}
func (s *Service) DeleteConversation(ctx context.Context, userID, id int64) error {
	if userID <= 0 || id <= 0 {
		return ErrForbidden
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if err := s.recoverStaleChat(ctx, userID, id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_newapi_chat WHERE user_id=$1 AND id=$2 AND active_request=''`, userID, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrChatBusy
	}
	return nil
}

func (s *Service) recoverStaleChat(ctx context.Context, userID, conversationID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var request string
	err = tx.QueryRowContext(ctx, `UPDATE v2_newapi_chat SET active_request='',active_until=0 WHERE id=$1 AND user_id=$2 AND active_request<>'' AND active_until<$3 RETURNING (SELECT client_request_id FROM v2_newapi_chat_request r WHERE r.conversation_id=$1 AND r.user_id=$2 AND r.status='running' ORDER BY r.created_at DESC LIMIT 1)`, conversationID, userID, time.Now().Unix()).Scan(&request)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE v2_newapi_chat_request SET status='unknown',updated_at=$3 WHERE user_id=$1 AND client_request_id=$2 AND status='running'`, userID, request, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_newapi_chat_message SET status=CASE WHEN content='' THEN 'unknown' ELSE 'partial' END,updated_at=$3 WHERE user_id=$1 AND client_request_id=$2 AND role='assistant' AND status='streaming'`, userID, request, now); err != nil {
		return err
	}
	return tx.Commit()
}

func chatError(err error) map[string]any {
	code, message := "chat_failed", "AI 对话暂时不可用，请查看已保存消息后再处理"
	switch {
	case errors.Is(err, ErrWorkspaceQuota):
		code, message = "insufficient_ai_balance", ErrWorkspaceQuota.Error()
	case errors.Is(err, ErrWorkspaceRateLimit):
		code, message = "model_rate_limit", ErrWorkspaceRateLimit.Error()
	case errors.Is(err, ErrWorkspaceContext):
		code, message = "model_context_limit", ErrWorkspaceContext.Error()
	case errors.Is(err, ErrWorkspaceModel):
		code, message = "model_unavailable", ErrWorkspaceModel.Error()
	case errors.Is(err, ErrWorkspaceCapacity):
		code, message = "model_capacity_unavailable", ErrWorkspaceCapacity.Error()
	case errors.Is(err, ErrWorkspaceInvalidRequest):
		code, message = "upstream_invalid_request", ErrWorkspaceInvalidRequest.Error()
	case errors.Is(err, ErrWorkspaceResponse):
		code, message = "upstream_invalid_response", ErrWorkspaceResponse.Error()
	case errors.Is(err, ErrRemoteUnavailable), errors.Is(err, context.DeadlineExceeded):
		code, message = "upstream_unavailable", "AI 上游服务暂时不可用，请核对已保存消息与用量；同一请求不会自动重发"
	case errors.Is(err, ErrWorkspaceNotConnected):
		code, message = "not_connected", ErrWorkspaceNotConnected.Error()
	case errors.Is(err, ErrDisabled):
		code, message = "disabled", ErrDisabled.Error()
	case errors.Is(err, ErrInvalid):
		code, message = "invalid_arguments", "对话参数或上下文超出限制，请缩短内容或新建会话"
	case errors.Is(err, ErrChatBusy):
		code, message = "conversation_busy", ErrChatBusy.Error()
	case errors.Is(err, ErrForbidden):
		code, message = "forbidden", "无权读取或修改此会话"
	case errors.Is(err, ErrConflict):
		code, message = "conflict", "请求编号已使用或会话已变化，请刷新核对"
	case errors.Is(err, ErrChatEmptyReply):
		code, message = "empty_model_reply", ErrChatEmptyReply.Error()
	case errors.Is(err, ErrChatInterrupted):
		code, message = "interrupted", ErrChatInterrupted.Error()
	case errors.Is(err, context.Canceled):
		code, message = "cancelled", "生成已停止，现有内容已保存"
	}
	return map[string]any{"code": code, "message": message}
}

func chatDBError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("chat persistence: %w", err)
}
