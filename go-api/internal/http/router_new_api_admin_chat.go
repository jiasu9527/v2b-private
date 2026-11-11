package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"forest/go-api/internal/newapibridge"
)

// Separate from the user workspace interface: possessing a chat or portal
// session never grants permission to inspect somebody else's conversations.
type newAPIAdminChatService interface {
	AdminListConversations(context.Context, int64, newapibridge.AdminConversationQuery) (newapibridge.AdminConversationPage, error)
	AdminGetConversation(context.Context, int64, int64) (newapibridge.AdminConversationDetail, error)
	AdminGetConversationMessages(context.Context, int64, int64, int64, int) (newapibridge.MessagePage, error)
}

func adminChatInteger(q url.Values, key string, fallback, min, max int64) (int64, error) {
	if !q.Has(key) {
		return fallback, nil
	}
	raw := q.Get(key)
	if raw == "" || strings.Trim(raw, "0123456789") != "" {
		return 0, newapibridge.ErrInvalid
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < min || v > max {
		return 0, newapibridge.ErrInvalid
	}
	return v, nil
}

func parseAdminConversationQuery(r *http.Request) (newapibridge.AdminConversationQuery, error) {
	q := newapibridge.AdminConversationQuery{Current: 1, PageSize: 20}
	v, err := workspaceQuery(r, "current", "page_size", "user_id", "email", "keyword", "mode", "model", "start_at", "end_at")
	if err != nil {
		return q, err
	}
	current, err := adminChatInteger(v, "current", 1, 1, 100000)
	if err != nil {
		return q, err
	}
	size, err := adminChatInteger(v, "page_size", 20, 1, 100)
	if err != nil {
		return q, err
	}
	q.Current, q.PageSize = int(current), int(size)
	for key, target := range map[string]*int64{"user_id": &q.UserID, "start_at": &q.StartAt, "end_at": &q.EndAt} {
		*target, err = adminChatInteger(v, key, 0, 1, 1<<63-1)
		if err != nil {
			return q, err
		}
	}
	q.Email, q.Keyword, q.Mode, q.Model = strings.TrimSpace(v.Get("email")), strings.TrimSpace(v.Get("keyword")), v.Get("mode"), strings.TrimSpace(v.Get("model"))
	for _, field := range []struct {
		value string
		max   int
	}{{q.Email, 254}, {q.Keyword, 100}, {q.Model, 200}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max || strings.ContainsAny(field.value, "\x00\r\n") {
			return q, newapibridge.ErrInvalid
		}
	}
	if (q.Mode != "" && q.Mode != "chat" && q.Mode != "character") || (q.StartAt > 0 && q.EndAt > 0 && q.StartAt >= q.EndAt) {
		return q, newapibridge.ErrInvalid
	}
	return q, nil
}

func newAPIAdminChatError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "用户聊天记录暂时无法读取，请稍后重试"
	switch {
	case errors.Is(err, newapibridge.ErrInvalid):
		status, message = http.StatusBadRequest, "聊天查询参数无效，请检查筛选条件及分页"
	case errors.Is(err, newapibridge.ErrForbidden):
		status, message = http.StatusForbidden, "无权查看，或会话已被删除，请刷新列表"
	}
	writeJSON(w, status, map[string]any{"message": message})
}

// Called only after fresh administrator authentication in handleNewAPIAdmin.
// No chat recovery, generation, editing, deletion or upstream API is invoked.
func handleNewAPIAdminConversations(w http.ResponseWriter, r *http.Request, bridge newAPIBridgeService, actor int64, action string) {
	if !newAPIRequireMethod(w, r, http.MethodGet) {
		return
	}
	svc, ok := bridge.(newAPIAdminChatService)
	if !ok {
		newAPIAdminChatError(w, newapibridge.ErrUnavailable)
		return
	}
	var result any
	var err error
	if action == "conversations" {
		var q newapibridge.AdminConversationQuery
		q, err = parseAdminConversationQuery(r)
		if err == nil {
			result, err = svc.AdminListConversations(r.Context(), actor, q)
		}
	} else {
		allowed := []string{"id"}
		if action == "conversations/messages" {
			allowed = append(allowed, "before_id", "page_size")
		}
		var q url.Values
		q, err = workspaceQuery(r, allowed...)
		if err == nil {
			var id int64
			id, err = adminChatInteger(q, "id", 0, 1, 1<<63-1)
			if err == nil && id == 0 {
				err = newapibridge.ErrInvalid
			}
			if err == nil && action == "conversations/detail" {
				result, err = svc.AdminGetConversation(r.Context(), actor, id)
			} else if err == nil {
				var before, size int64
				before, err = adminChatInteger(q, "before_id", 0, 0, 1<<63-1)
				if err == nil {
					size, err = adminChatInteger(q, "page_size", 100, 1, 200)
				}
				if err == nil {
					result, err = svc.AdminGetConversationMessages(r.Context(), actor, id, before, int(size))
				}
			}
		}
	}
	if err != nil {
		newAPIAdminChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}
