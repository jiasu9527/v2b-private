package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AdminConversationQuery is deliberately separate from user chat access. Values
// are validated here as well as at the HTTP boundary; no filter is ignored.
type AdminConversationQuery struct {
	Current, PageSize           int
	UserID, StartAt, EndAt      int64
	Email, Keyword, Mode, Model string
}

type AdminConversationItem struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"user_id"`
	UserEmail     string `json:"user_email"`
	UserDeleted   bool   `json:"user_deleted"`
	Title         string `json:"title"`
	Mode          string `json:"mode"`
	Model         string `json:"model"`
	CharacterID   int64  `json:"character_id"`
	CharacterName string `json:"character_name"`
	MessageCount  int64  `json:"message_count"`
	LastMessageAt int64  `json:"last_message_at"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type AdminConversationPage struct {
	Items    []AdminConversationItem `json:"items"`
	Total    int64                   `json:"total"`
	Current  int                     `json:"current"`
	PageSize int                     `json:"page_size"`
}

type AdminConversationDetail struct {
	Conversation      Conversation   `json:"conversation"`
	UserID            int64          `json:"user_id"`
	UserEmail         string         `json:"user_email"`
	UserDeleted       bool           `json:"user_deleted"`
	CharacterSnapshot *CharacterData `json:"character_snapshot"`
	Messages          []ChatMessage  `json:"messages"`
	HasMore           bool           `json:"has_more"`
	NextBeforeID      int64          `json:"next_before_id"`
}

func normalizeAdminConversationQuery(q AdminConversationQuery) (AdminConversationQuery, error) {
	if q.Current == 0 {
		q.Current = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 20
	}
	if q.Current < 1 || q.Current > 100000 || q.PageSize < 1 || q.PageSize > 100 ||
		q.UserID < 0 || q.StartAt < 0 || q.EndAt < 0 || (q.StartAt > 0 && q.EndAt > 0 && q.StartAt >= q.EndAt) {
		return q, ErrInvalid
	}
	for _, value := range []string{q.Email, q.Keyword, q.Mode, q.Model} {
		if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return q, ErrInvalid
		}
	}
	if len(q.Email) > 254 || utf8.RuneCountInString(q.Keyword) > 100 ||
		(q.Mode != "" && q.Mode != "chat" && q.Mode != "character") ||
		(q.Model != "" && (!validChatModel(q.Model) || strings.TrimSpace(q.Model) != q.Model)) {
		return q, ErrInvalid
	}
	if q.Email != "" {
		email, err := mail.ParseAddress(q.Email)
		if err != nil || email.Address != q.Email || email.Name != "" {
			return q, ErrInvalid
		}
	}
	return q, nil
}

// The transaction's first read checks the current actor, not token claims. Every
// method uses a read-only repeatable-read snapshot: count/items and detail/page
// see the same chat state, and opening an admin view never recovers a stale turn.
func (s *Service) adminChatReadTx(ctx context.Context, actorID int64) (*sql.Tx, error) {
	if actorID <= 0 {
		return nil, ErrForbidden
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_user WHERE id=$1 AND is_admin=1 AND banned=0)`, actorID).Scan(&exists)
	if err != nil || !exists {
		_ = tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	return tx, nil
}

func adminConversationWhere(q AdminConversationQuery) (string, []any) {
	clauses := []string{"TRUE"}
	args := []any{}
	add := func(expression string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expression, len(args)))
	}
	if q.UserID != 0 {
		add("c.user_id=$%d", q.UserID)
	}
	if q.Email != "" {
		add("u.email=$%d", q.Email)
	}
	if q.Keyword != "" {
		// strpos treats %, _, backslashes and quotes literally, not as SQL/LIKE
		// syntax. Case folding only affects matching, never returned titles.
		add("strpos(lower(c.title),lower($%d::text))>0", q.Keyword)
	}
	if q.Mode != "" {
		add("c.mode=$%d", q.Mode)
	}
	if q.Model != "" {
		add("c.model=$%d", q.Model)
	}
	if q.StartAt != 0 {
		add("c.created_at >= $%d", q.StartAt)
	}
	if q.EndAt != 0 {
		add("c.created_at < $%d", q.EndAt)
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Service) AdminListConversations(ctx context.Context, actorID int64, query AdminConversationQuery) (AdminConversationPage, error) {
	page := AdminConversationPage{Items: []AdminConversationItem{}}
	if actorID <= 0 {
		return page, ErrForbidden
	}
	q, err := normalizeAdminConversationQuery(query)
	if err != nil {
		return page, err
	}
	page.Current, page.PageSize = q.Current, q.PageSize
	tx, err := s.adminChatReadTx(ctx, actorID)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	where, args := adminConversationWhere(q)
	from := ` FROM v2_newapi_chat c LEFT JOIN v2_user u ON u.id=c.user_id WHERE ` + where
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	// Bound the conversation page before aggregating messages, so a global
	// list does not count messages for every historical conversation.
	querySQL := `WITH selected AS MATERIALIZED (
SELECT c.id,c.user_id,COALESCE(u.email,'') AS user_email,(u.id IS NULL) AS user_deleted,
c.title,c.mode,c.model,c.character_id,COALESCE(c.character_snapshot->>'name','') AS character_name,c.created_at,c.updated_at` + from +
		fmt.Sprintf(` ORDER BY c.updated_at DESC,c.id DESC LIMIT $%d OFFSET $%d)
SELECT c.id,c.user_id,c.user_email,c.user_deleted,c.title,c.mode,c.model,c.character_id,c.character_name,
m.message_count,m.last_message_at,c.created_at,c.updated_at FROM selected c
CROSS JOIN LATERAL (SELECT COUNT(*) AS message_count,COALESCE(MAX(created_at),0) AS last_message_at
FROM v2_newapi_chat_message WHERE conversation_id=c.id AND user_id=c.user_id) m
ORDER BY c.updated_at DESC,c.id DESC`, len(args)+1, len(args)+2)
	args = append(args, q.PageSize, (q.Current-1)*q.PageSize)
	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AdminConversationItem
		if err = rows.Scan(&item.ID, &item.UserID, &item.UserEmail, &item.UserDeleted, &item.Title, &item.Mode, &item.Model, &item.CharacterID, &item.CharacterName, &item.MessageCount, &item.LastMessageAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func (s *Service) AdminGetConversation(ctx context.Context, actorID, id int64) (AdminConversationDetail, error) {
	detail := AdminConversationDetail{Messages: []ChatMessage{}}
	if actorID <= 0 || id <= 0 {
		return detail, ErrForbidden
	}
	tx, err := s.adminChatReadTx(ctx, actorID)
	if err != nil {
		return detail, err
	}
	defer tx.Rollback()
	c := &detail.Conversation
	var snapshot []byte
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.title,c.mode,c.model,c.character_id,c.persona,c.parent_id,c.created_at,c.updated_at,
c.user_id,COALESCE(u.email,''),(u.id IS NULL),c.character_snapshot
FROM v2_newapi_chat c LEFT JOIN v2_user u ON u.id=c.user_id WHERE c.id=$1`, id).Scan(
		&c.ID, &c.Title, &c.Mode, &c.Model, &c.CharacterID, &c.Persona, &c.ParentID, &c.CreatedAt, &c.UpdatedAt,
		&detail.UserID, &detail.UserEmail, &detail.UserDeleted, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrForbidden
	}
	if err != nil {
		return detail, err
	}
	if c.Mode == "character" && len(snapshot) > 0 && string(snapshot) != "{}" && string(snapshot) != "null" {
		var card CharacterData
		if err = json.Unmarshal(snapshot, &card); err != nil {
			return detail, ErrUnavailable
		}
		// Only typed card context is returned. Imported arbitrary extensions and
		// unknown JSON fields are not part of the admin conversation contract.
		card.Extensions = nil
		if card.CharacterBook != nil {
			card.CharacterBook.Extensions = nil
			for i := range card.CharacterBook.Entries {
				card.CharacterBook.Entries[i].Extensions = nil
			}
		}
		detail.CharacterSnapshot = &card
	}
	messages, err := adminConversationMessages(ctx, tx, detail.UserID, id, 0, 100)
	if err != nil {
		return detail, err
	}
	detail.Messages, detail.HasMore, detail.NextBeforeID = messages.Items, messages.HasMore, messages.NextBeforeID
	return detail, tx.Commit()
}

func (s *Service) AdminGetConversationMessages(ctx context.Context, actorID, id, beforeID int64, size int) (MessagePage, error) {
	page := MessagePage{Items: []ChatMessage{}}
	if actorID <= 0 || id <= 0 {
		return page, ErrForbidden
	}
	if beforeID < 0 || size < 1 || size > 200 {
		return page, ErrInvalid
	}
	tx, err := s.adminChatReadTx(ctx, actorID)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	var ownerID int64
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM v2_newapi_chat WHERE id=$1`, id).Scan(&ownerID); errors.Is(err, sql.ErrNoRows) {
		return page, ErrForbidden
	} else if err != nil {
		return page, err
	}
	page, err = adminConversationMessages(ctx, tx, ownerID, id, beforeID, size)
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func adminConversationMessages(ctx context.Context, tx *sql.Tx, userID, id, beforeID int64, size int) (MessagePage, error) {
	page := MessagePage{Items: []ChatMessage{}}
	rows, err := tx.QueryContext(ctx, `SELECT id,conversation_id,role,content,status,client_request_id,created_at,updated_at
FROM v2_newapi_chat_message WHERE user_id=$1 AND conversation_id=$2 AND ($3::bigint=0 OR id<$3)
ORDER BY id DESC LIMIT $4`, userID, id, beforeID, size+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		message, err := scanChatMessage(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, message)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > size {
		page.HasMore = true
		page.Items = page.Items[:size]
	}
	if len(page.Items) > 0 {
		page.NextBeforeID = page.Items[len(page.Items)-1].ID
	}
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page, nil
}
