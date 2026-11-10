package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/session"
)

type newAPIWorkspaceAccountService interface {
	WorkspaceAccount(context.Context, int64, string) (map[string]any, error)
	ConnectAccount(context.Context, int64, string) (map[string]any, error)
	VerifyWorkspaceLogin(context.Context, int64, string, newapibridge.WorkspaceLoginVerification) (map[string]any, error)
	WorkspaceModels(context.Context, int64, string) (any, error)
	WorkspaceUsage(context.Context, int64, string, int, int) (any, error)
	WorkspaceKeys(context.Context, int64, string) (any, error)
	WorkspaceKeysPage(context.Context, int64, string, int, int) (any, error)
	CreateWorkspaceKey(context.Context, int64, string, newapibridge.WorkspaceKeyRequest) (any, error)
	RevealWorkspaceKey(context.Context, int64, string, int64) (any, error)
	DeleteWorkspaceKey(context.Context, int64, string, int64) error
	QuoteWorkspaceTransfer(context.Context, int64, string, newapibridge.WorkspaceQuoteRequest) (any, error)
	ConfirmWorkspaceTransfer(context.Context, int64, string, string) (newapibridge.Transfer, error)
	ReviewWorkspaceTransfer(context.Context, int64, string, string) (any, error)
	ResolveWorkspaceTransfer(context.Context, int64, string, newapibridge.WorkspaceResolveRequest) (newapibridge.Transfer, error)
}

type newAPIWorkspacePricingService interface {
	WorkspacePricing(context.Context, int64, string) (newapibridge.WorkspacePricing, error)
}

type newAPIWorkspaceChatService interface {
	ListConversations(context.Context, int64, int, int) (newapibridge.ConversationPage, error)
	CreateConversation(context.Context, int64, newapibridge.ConversationCreateRequest) (newapibridge.Conversation, error)
	GetConversation(context.Context, int64, int64) (newapibridge.ConversationDetail, error)
	GetConversationMessages(context.Context, int64, int64, int64, int) (newapibridge.MessagePage, error)
	DeleteConversation(context.Context, int64, int64) error
	SendChat(context.Context, int64, string, newapibridge.ChatSendRequest, func(string, any) error) error
	GetChatRequest(context.Context, int64, string) (newapibridge.ChatRequestState, error)
	ListCharacters(context.Context, int64, int, int) (newapibridge.CharacterPage, error)
	GetCharacter(context.Context, int64, int64) (newapibridge.Character, error)
	SaveCharacter(context.Context, int64, newapibridge.CharacterSaveRequest) (newapibridge.Character, error)
	DeleteCharacter(context.Context, int64, int64) error
	ImportCharacter(context.Context, int64, []byte) (newapibridge.Character, error)
	ExportCharacter(context.Context, int64, int64) (newapibridge.CharacterCard, error)
}

var workspaceMethods = map[string]string{
	"account": "GET", "account/connect": "POST", "account/verify": "POST", "models": "GET", "pricing": "GET", "usage": "GET", "keys": "GET|POST", "keys/reveal": "POST", "keys/delete": "POST",
	"transfer/quote": "POST", "transfer/confirm": "POST", "transfer/review": "GET", "transfer/resolve": "POST", "password/prepare": "POST", "password/confirm": "POST", "password/reveal": "POST",
	"conversations": "GET", "conversations/create": "POST", "conversations/detail": "GET", "conversations/messages": "GET", "conversations/delete": "POST", "chat/send": "POST", "chat/request": "GET",
	"characters": "GET", "characters/detail": "GET", "characters/save": "POST", "characters/delete": "POST", "characters/import": "POST", "characters/export": "GET",
}

func decodeWorkspaceJSON(w http.ResponseWriter, r *http.Request, size int64, target any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		writeJSON(w, 415, map[string]any{"message": "Content-Type must be application/json"})
		return false
	}
	var raw json.RawMessage
	if err = decodeDNSProbeJSON(w, r, size, &raw); err == nil {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			err = newapibridge.ErrInvalid
		} else {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(target)
		}
	}
	if err != nil {
		writeJSON(w, 400, map[string]any{"message": "参数格式无效，仅接受声明字段的 JSON 对象"})
		return false
	}
	return true
}
func workspaceQuery(r *http.Request, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, newapibridge.ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 {
			return nil, newapibridge.ErrInvalid
		}
		found := false
		for _, a := range allowed {
			if a == k {
				found = true
				break
			}
		}
		if !found {
			return nil, newapibridge.ErrInvalid
		}
	}
	return q, nil
}
func workspacePositive(q url.Values, key string, fallback, max int64) (int64, error) {
	raw := q.Get(key)
	if raw == "" {
		return fallback, nil
	}
	v, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || v < 1 || v > max {
		return 0, newapibridge.ErrInvalid
	}
	return v, nil
}
func workspacePagination(q url.Values) (int, int, error) {
	c, e := workspacePositive(q, "current", 1, 100000)
	if e != nil {
		return 0, 0, e
	}
	n, e := workspacePositive(q, "page_size", 20, 100)
	return int(c), int(n), e
}

func handleNewAPIWorkspace(w http.ResponseWriter, r *http.Request, sessions session.Service, passports passport.Service, svc newAPIBridgeService, action string) bool {
	methods, known := workspaceMethods[action]
	if !known {
		return false
	}
	newAPIHeaders(w)
	if !strings.Contains("|"+methods+"|", "|"+r.Method+"|") {
		w.Header().Set("Allow", strings.ReplaceAll(methods, "|", ", "))
		writeJSON(w, 405, map[string]any{"message": "Method not allowed"})
		return true
	}
	// Bridge portal credentials remain limited to the original checkout/SSO API.
	if strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer "), "nap_") {
		newAPIError(w, newapibridge.ErrForbidden)
		return true
	}
	actor, auth, ok := newAPIIdentity(w, r, sessions, svc, false)
	if !ok {
		return true
	}
	if svc == nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return true
	}
	if !workspaceAllow(actor.ID, action, r.Method) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, 429, map[string]any{"message": "操作过于频繁，请稍后重试"})
		return true
	}
	if r.Method == http.MethodPost {
		if _, err := workspaceQuery(r); err != nil {
			newAPIError(w, err)
			return true
		}
	}
	if action == "chat/send" {
		chat, ok := svc.(newAPIWorkspaceChatService)
		if !ok {
			newAPIError(w, newapibridge.ErrUnavailable)
			return true
		}
		var req newapibridge.ChatSendRequest
		if !decodeWorkspaceJSON(w, r, 256<<10, &req) {
			return true
		}
		workspaceChatStream(w, r, sessions, chat, actor.ID, auth, req)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(65 * time.Second))
	r = r.WithContext(ctx)
	var result any
	var err error
	if action == "pricing" {
		if _, err = workspaceQuery(r); err != nil {
			workspaceError(w, err)
			return true
		}
		if r.Body != nil {
			body, e := io.ReadAll(io.LimitReader(r.Body, 1))
			if len(body) != 0 || e != nil {
				workspaceError(w, newapibridge.ErrInvalid)
				return true
			}
		}
		pricing, ok := svc.(newAPIWorkspacePricingService)
		if !ok {
			workspaceError(w, newapibridge.ErrUnavailable)
			return true
		}
		result, err = pricing.WorkspacePricing(ctx, actor.ID, auth)
		if err != nil {
			workspaceError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"data": result})
		}
		return true
	}
	if strings.HasPrefix(action, "password/") {
		handleWorkspacePassword(w, r, passports, svc, actor.ID, auth, action)
		return true
	}
	if strings.HasPrefix(action, "conversations") || strings.HasPrefix(action, "characters") || action == "chat/request" {
		chat, ok := svc.(newAPIWorkspaceChatService)
		if !ok {
			newAPIError(w, newapibridge.ErrUnavailable)
			return true
		}
		result, err = workspaceChatJSON(w, r, chat, actor.ID, action)
		if errors.Is(err, errWorkspaceResponseWritten) {
			return true
		}
	} else {
		accounts, ok := svc.(newAPIWorkspaceAccountService)
		if !ok {
			newAPIError(w, newapibridge.ErrUnavailable)
			return true
		}
		result, err = workspaceAccountJSON(w, r, accounts, actor.ID, auth, action)
		if errors.Is(err, errWorkspaceResponseWritten) {
			return true
		}
	}
	if err != nil {
		workspaceError(w, err)
		return true
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return true
}

var errWorkspaceResponseWritten = errors.New("workspace response written")

func workspaceAccountJSON(w http.ResponseWriter, r *http.Request, s newAPIWorkspaceAccountService, id int64, auth, action string) (any, error) {
	ctx := r.Context()
	switch action {
	case "account", "models", "keys":
		if r.Method == http.MethodGet {
			if action == "keys" {
				q, e := workspaceQuery(r, "current", "page_size")
				if e != nil {
					return nil, e
				}
				c, n, e := workspacePagination(q)
				if e != nil {
					return nil, e
				}
				return s.WorkspaceKeysPage(ctx, id, auth, c, n)
			}
			if _, e := workspaceQuery(r); e != nil {
				return nil, e
			}
			if action == "account" {
				return s.WorkspaceAccount(ctx, id, auth)
			}
			if action == "models" {
				return s.WorkspaceModels(ctx, id, auth)
			}
			return s.WorkspaceKeys(ctx, id, auth)
		}
		var req newapibridge.WorkspaceKeyRequest
		if !decodeWorkspaceJSON(w, r, 32<<10, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.CreateWorkspaceKey(ctx, id, auth, req)
	case "account/verify":
		var req newapibridge.WorkspaceLoginVerification
		if !decodeWorkspaceJSON(w, r, 2048, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.VerifyWorkspaceLogin(ctx, id, auth, req)
	case "account/connect":
		var empty struct{}
		if !decodeWorkspaceJSON(w, r, 1024, &empty) {
			return nil, errWorkspaceResponseWritten
		}
		return s.ConnectAccount(ctx, id, auth)
	case "usage":
		q, e := workspaceQuery(r, "current", "page_size")
		if e != nil {
			return nil, e
		}
		c, n, e := workspacePagination(q)
		if e != nil {
			return nil, e
		}
		return s.WorkspaceUsage(ctx, id, auth, c, n)
	case "keys/reveal", "keys/delete":
		var req struct {
			ID int64 `json:"id"`
		}
		if !decodeWorkspaceJSON(w, r, 1024, &req) {
			return nil, errWorkspaceResponseWritten
		}
		if req.ID <= 0 {
			return nil, newapibridge.ErrInvalid
		}
		if action == "keys/reveal" {
			return s.RevealWorkspaceKey(ctx, id, auth, req.ID)
		}
		return true, s.DeleteWorkspaceKey(ctx, id, auth, req.ID)
	case "transfer/quote":
		var req newapibridge.WorkspaceQuoteRequest
		if !decodeWorkspaceJSON(w, r, 2048, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.QuoteWorkspaceTransfer(ctx, id, auth, req)
	case "transfer/review":
		q, e := workspaceQuery(r, "quote_id")
		if e != nil || q.Get("quote_id") == "" {
			return nil, newapibridge.ErrInvalid
		}
		return s.ReviewWorkspaceTransfer(ctx, id, auth, q.Get("quote_id"))
	case "transfer/resolve":
		var req newapibridge.WorkspaceResolveRequest
		if !decodeWorkspaceJSON(w, r, 2048, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.ResolveWorkspaceTransfer(ctx, id, auth, req)
	case "transfer/confirm":
		var req struct {
			QuoteID string `json:"quote_id"`
		}
		if !decodeWorkspaceJSON(w, r, 2048, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.ConfirmWorkspaceTransfer(ctx, id, auth, req.QuoteID)
	}
	return nil, newapibridge.ErrInvalid
}
func workspaceChatJSON(w http.ResponseWriter, r *http.Request, s newAPIWorkspaceChatService, user int64, action string) (any, error) {
	ctx := r.Context()
	switch action {
	case "chat/request":
		q, e := workspaceQuery(r, "client_request_id")
		if e != nil {
			return nil, e
		}
		return s.GetChatRequest(ctx, user, q.Get("client_request_id"))
	case "conversations", "characters":
		q, e := workspaceQuery(r, "current", "page_size")
		if e != nil {
			return nil, e
		}
		c, n, e := workspacePagination(q)
		if e != nil {
			return nil, e
		}
		if action == "conversations" {
			return s.ListConversations(ctx, user, c, n)
		}
		return s.ListCharacters(ctx, user, c, n)
	case "conversations/create":
		var req newapibridge.ConversationCreateRequest
		if !decodeWorkspaceJSON(w, r, 64<<10, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.CreateConversation(ctx, user, req)
	case "conversations/detail", "characters/detail", "characters/export":
		q, e := workspaceQuery(r, "id")
		if e != nil {
			return nil, e
		}
		id, e := workspacePositive(q, "id", 0, 1<<53-1)
		if e != nil || id == 0 {
			return nil, newapibridge.ErrInvalid
		}
		if action == "conversations/detail" {
			return s.GetConversation(ctx, user, id)
		}
		if action == "characters/detail" {
			return s.GetCharacter(ctx, user, id)
		}
		return s.ExportCharacter(ctx, user, id)
	case "conversations/messages":
		q, e := workspaceQuery(r, "id", "before_id", "page_size")
		if e != nil {
			return nil, e
		}
		id, e := workspacePositive(q, "id", 0, 1<<53-1)
		if e != nil || id == 0 {
			return nil, newapibridge.ErrInvalid
		}
		before, e := workspacePositive(q, "before_id", 0, 1<<53-1)
		if e != nil {
			return nil, e
		}
		n, e := workspacePositive(q, "page_size", 100, 100)
		if e != nil {
			return nil, e
		}
		return s.GetConversationMessages(ctx, user, id, before, int(n))
	case "conversations/delete", "characters/delete":
		var req struct {
			ID int64 `json:"id"`
		}
		if !decodeWorkspaceJSON(w, r, 1024, &req) {
			return nil, errWorkspaceResponseWritten
		}
		if req.ID <= 0 {
			return nil, newapibridge.ErrInvalid
		}
		if action == "conversations/delete" {
			return true, s.DeleteConversation(ctx, user, req.ID)
		}
		return true, s.DeleteCharacter(ctx, user, req.ID)
	case "characters/save":
		var req newapibridge.CharacterSaveRequest
		if !decodeWorkspaceJSON(w, r, 300<<10, &req) {
			return nil, errWorkspaceResponseWritten
		}
		return s.SaveCharacter(ctx, user, req)
	case "characters/import":
		var req struct {
			Payload  string `json:"payload"`
			Encoding string `json:"encoding"`
		}
		if !decodeWorkspaceJSON(w, r, 3<<20, &req) {
			return nil, errWorkspaceResponseWritten
		}
		var raw []byte
		var err error
		switch req.Encoding {
		case "json":
			raw = []byte(req.Payload)
		case "base64":
			raw, err = base64.StdEncoding.DecodeString(req.Payload)
		default:
			err = newapibridge.ErrInvalid
		}
		if err != nil || len(raw) > newapibridge.MaxCharacterImportBytes {
			return nil, newapibridge.ErrInvalid
		}
		return s.ImportCharacter(ctx, user, raw)
	}
	return nil, newapibridge.ErrInvalid
}

var workspaceLimits = struct {
	sync.Mutex
	items map[string]struct {
		count int
		until int64
	}
}{items: make(map[string]struct {
	count int
	until int64
})}

func workspaceAllow(id int64, action, method string) bool {
	group, limit := "read", 240
	if method != "GET" {
		group, limit = "write", 60
	}
	if strings.HasPrefix(action, "password/") {
		group, limit = "credential", 10
	}
	if action == "account/connect" {
		group, limit = "connect", 10
	}
	now := time.Now().Unix()
	key := strconv.FormatInt(id, 10) + ":" + group
	workspaceLimits.Lock()
	defer workspaceLimits.Unlock()
	if len(workspaceLimits.items) >= 10000 {
		for k, v := range workspaceLimits.items {
			if v.until <= now {
				delete(workspaceLimits.items, k)
			}
		}
		if _, ok := workspaceLimits.items[key]; !ok && len(workspaceLimits.items) >= 10000 {
			return false
		}
	}
	v := workspaceLimits.items[key]
	if v.until <= now {
		v.count = 0
		v.until = now + 60
	}
	v.count++
	workspaceLimits.items[key] = v
	return v.count <= limit
}

func workspaceError(w http.ResponseWriter, err error) {
	status, code, message := 0, "", ""
	switch {
	case errors.Is(err, newapibridge.ErrUnsupportedCharacter):
		status, code, message = 400, "unsupported_character", "仅支持酒馆 V2 角色卡，请检查文件格式"
	case errors.Is(err, newapibridge.ErrWorkspaceNotConnected):
		status, code, message = 409, "not_connected", "请先连接本人的 AI 账号"
	case errors.Is(err, newapibridge.ErrWorkspaceVerification):
		status, code, message = 409, "verification_required", "AI 账号需要完成本人安全验证，请按提示操作或进入 AI 控制台"
	case errors.Is(err, newapibridge.ErrWorkspaceUnknown):
		status, code, message = 409, "outcome_unknown", "远端操作结果尚未确认，请查看原记录核对，不要重复创建"
	case errors.Is(err, newapibridge.ErrWorkspaceAmount):
		status, code, message = 400, "amount_unavailable", "该金额不符合当前 AI 充值档位或费率，请调整金额"
	case errors.Is(err, newapibridge.ErrWorkspaceQuota):
		status, code, message = 402, "insufficient_ai_balance", "AI 余额不足，请先转入额度"
	case errors.Is(err, newapibridge.ErrWorkspaceRateLimit):
		status, code, message = 429, "model_rate_limit", "模型服务请求过于频繁，请稍后再试"
	case errors.Is(err, newapibridge.ErrWorkspaceContext):
		status, code, message = 400, "model_context_limit", "当前模型上下文不足，请减少历史或换用更大上下文的模型"
	case errors.Is(err, newapibridge.ErrWorkspaceModel):
		status, code, message = 400, "model_unavailable", "当前模型不可用，请检查 AI 站模型渠道或选择其他模型"
	case errors.Is(err, newapibridge.ErrChatBusy):
		status, code, message = 409, "conversation_busy", "此会话仍在生成，请停止或等待结束"
	case errors.Is(err, newapibridge.ErrChatInterrupted):
		status, code, message = 409, "generation_interrupted", "生成未完整结束，已保存现有内容，请刷新查看"
	}
	if status != 0 {
		writeJSON(w, status, map[string]any{"code": code, "message": message})
		return
	}
	newAPIError(w, err)
}

type workspaceStreamEvent struct {
	name string
	data any
}

func workspaceChatStream(w http.ResponseWriter, r *http.Request, sessions session.Service, s newAPIWorkspaceChatService, user int64, auth string, req newapibridge.ChatSendRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	events := make(chan workspaceStreamEvent, 1)
	finished := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				finished <- newapibridge.ErrUnavailable
			}
		}()
		finished <- s.SendChat(ctx, user, auth, req, func(name string, data any) error {
			switch name {
			case "meta", "delta", "done", "error":
			default:
				return newapibridge.ErrInvalid
			}
			select {
			case events <- workspaceStreamEvent{name, data}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	control := http.NewResponseController(w)
	_ = control.SetWriteDeadline(time.Now().Add(40 * time.Second))
	started, reported := false, false
	start := func() {
		if started {
			return
		}
		started = true
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	send := func(event workspaceStreamEvent) error {
		raw, e := json.Marshal(event.data)
		if e != nil {
			return e
		}
		if event.name == "done" || event.name == "error" {
			reported = true
		}
		start()
		_ = control.SetWriteDeadline(time.Now().Add(40 * time.Second))
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, raw); e != nil {
			return e
		}
		return control.Flush()
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case e := <-events:
			if send(e) != nil {
				return
			}
		case err := <-finished:
			// Drain any final event emitted just before the completion signal.
			for {
				select {
				case e := <-events:
					if send(e) != nil {
						return
					}
				default:
					goto drained
				}
			}
		drained:
			if err != nil && !errors.Is(err, context.Canceled) {
				if !started {
					workspaceError(w, err)
				} else if !reported {
					_ = send(workspaceStreamEvent{"error", map[string]any{"message": "本轮未完整结束，已保存的内容可刷新查看；请核对后再重试", "status": "unknown"}})
				}
			}
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			var identity *session.Identity
			var err error
			if fresh, ok := sessions.(freshSessionService); ok {
				identity, err = fresh.AuthenticateFresh(ctx, auth, false)
			} else {
				identity, err = sessions.Authenticate(ctx, auth, false)
			}
			if err != nil || identity == nil || identity.ID != user {
				cancel()
				if started {
					_ = send(workspaceStreamEvent{"error", map[string]any{"message": "登录已失效，请重新登录", "code": "unauthorized"}})
				} else {
					handleSessionError(w, session.ErrUnauthorized)
				}
				return
			}
			start()
			_ = control.SetWriteDeadline(time.Now().Add(40 * time.Second))
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if control.Flush() != nil {
				return
			}
		}
	}
}
