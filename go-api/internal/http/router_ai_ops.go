package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/session"
)

type aiOpsService interface {
	GetConfig(context.Context, int64) (aiops.Config, error)
	SaveConfig(context.Context, int64, aiops.ConfigSaveRequest) (aiops.Config, error)
	ListSessions(context.Context, int64) ([]aiops.Session, error)
	GetSession(context.Context, int64, int64) (aiops.SessionDetail, error)
	CreateSession(context.Context, int64, aiops.CreateSessionRequest) (aiops.Session, error)
	SendMessage(context.Context, int64, aiops.SendMessageRequest) (aiops.SendMessageResult, error)
	DeleteSession(context.Context, int64, int64) error
	ClearSessions(context.Context, int64) error
	ResetData(context.Context, int64) error
}

func WithAIOps(service aiOpsService) Option {
	return func(state *routerState) { state.aiOps = service }
}

func handleAIOps(w http.ResponseWriter, r *http.Request, sessions session.Service, service aiOpsService, action string) {
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	actor, ok := authenticateSupportSession(w, r, sessions, true)
	if !ok {
		return
	}
	if service == nil {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	expected := "POST"
	switch action {
	case "config", "sessions", "session", "servers":
		expected = "GET"
	case "config/save", "session/create", "chat/send", "session/delete", "session/clear", "reset", "server/save", "server/delete", "server/fingerprint", "action/approve", "action/reject":
	default:
		writeJSON(w, 404, map[string]string{"message": "Not found"})
		return
	}
	if r.Method != expected {
		w.Header().Set("Allow", expected)
		writeJSON(w, 405, map[string]string{"message": "Method not allowed"})
		return
	}
	// Chat/model and SSH command execution are asynchronous. Short management
	// operations get a bounded response window without changing other endpoints.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(35 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var result any
	var err error
	status := 200
	switch action {
	case "config":
		result, err = service.GetConfig(ctx, actor.ID)
	case "config/save":
		var req aiops.ConfigSaveRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = service.SaveConfig(ctx, actor.ID, req)
	case "sessions":
		result, err = service.ListSessions(ctx, actor.ID)
	case "session":
		id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if e != nil || id <= 0 {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		detail, e := service.GetSession(ctx, actor.ID, id)
		if e != nil {
			handleAIOpsError(w, e)
			return
		}
		tools, ok := service.(aiOpsToolsService)
		if !ok {
			handleAIOpsError(w, aiops.ErrUnavailable)
			return
		}
		actions, e := tools.ListActions(ctx, actor.ID, id)
		if e != nil {
			handleAIOpsError(w, e)
			return
		}
		result = struct {
			aiops.SessionDetail
			Actions []aiops.Action `json:"actions"`
		}{SessionDetail: detail, Actions: actions}
	case "session/create":
		var req aiops.CreateSessionRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = service.CreateSession(ctx, actor.ID, req)
	case "chat/send":
		var req aiops.SendMessageRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = service.SendMessage(ctx, actor.ID, req)
		status = 202
	case "session/delete", "session/clear", "reset":
		var req struct {
			ID          int64  `json:"id"`
			Confirm     bool   `json:"confirm"`
			ConfirmText string `json:"confirm_text"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if !req.Confirm || (action == "session/delete" && req.ID <= 0) || (action == "reset" && req.ConfirmText != "RESET_AI_DATA") {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		switch action {
		case "session/delete":
			err = service.DeleteSession(ctx, actor.ID, req.ID)
		case "session/clear":
			err = service.ClearSessions(ctx, actor.ID)
		case "reset":
			err = service.ResetData(ctx, actor.ID)
		}
		result = true
	default:
		handleAIOpsTools(w, r, service, actor.ID, action)
		return
	}
	if err != nil {
		handleAIOpsError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"data": result})
}
func handleAIOpsError(w http.ResponseWriter, err error) {
	status, msg := 500, "AI 运维操作失败，请核对配置或服务状态后重试"
	switch {
	case errors.Is(err, aiops.ErrForbidden):
		status, msg = 403, aiops.ErrForbidden.Error()
	case errors.Is(err, aiops.ErrValidation):
		status, msg = 400, aiops.ErrValidation.Error()
	case errors.Is(err, aiops.ErrNotFound):
		status, msg = 404, aiops.ErrNotFound.Error()
	case errors.Is(err, aiops.ErrConflict):
		status, msg = 409, aiops.ErrConflict.Error()
	case errors.Is(err, aiops.ErrBusy):
		status, msg = 429, aiops.ErrBusy.Error()
		w.Header().Set("Retry-After", "5")
	case errors.Is(err, aiops.ErrUnavailable):
		status, msg = 503, aiops.ErrUnavailable.Error()
	case errors.Is(err, context.DeadlineExceeded):
		status, msg = 504, "操作超时，请刷新核对结果；不要重复执行远程命令"
	}
	writeJSON(w, status, map[string]string{"message": msg})
}
