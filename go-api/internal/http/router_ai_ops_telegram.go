package httpapi

import (
	"context"
	"net/http"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/session"
)

type aiTelegramService interface {
	Status(context.Context, int64) (aiops.TelegramStatus, error)
	SaveConfig(context.Context, int64, aiops.TelegramConfig) (aiops.TelegramConfig, error)
	CreateBindingCode(context.Context, int64, int64) (aiops.TelegramBindingCode, error)
	RevokeBinding(context.Context, int64, int64) error
}

func WithAIOpsTelegram(service aiTelegramService) Option {
	return func(state *routerState) { state.aiTelegram = service }
}

func handleAITelegram(w http.ResponseWriter, r *http.Request, sessions session.Service, service aiTelegramService, panel admin.Service, action string) {
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	actor, ok := authenticateSupportSession(w, r, sessions, false)
	if !ok {
		return
	}
	if actor.IsAdmin == 0 && actor.IsStaff == 0 {
		handleAIOpsError(w, aiops.ErrForbidden)
		return
	}
	expected := http.MethodPost
	switch action {
	case "status":
		expected = http.MethodGet
	case "config", "binding-code", "webhook":
		if actor.IsAdmin != 1 {
			handleAIOpsError(w, aiops.ErrForbidden)
			return
		}
	case "revoke":
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not found"})
		return
	}
	if r.Method != expected {
		w.Header().Set("Allow", expected)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method not allowed"})
		return
	}
	if service == nil {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(35 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var result any
	var err error
	switch action {
	case "status":
		result, err = service.Status(ctx, actor.ID)
	case "config":
		var req aiops.TelegramConfig
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = service.SaveConfig(ctx, actor.ID, req)
	case "binding-code", "revoke":
		var req struct {
			ActorID int64 `json:"actor_id"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if req.ActorID <= 0 {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		if actor.IsAdmin != 1 && req.ActorID != actor.ID {
			handleAIOpsError(w, aiops.ErrForbidden)
			return
		}
		if action == "binding-code" {
			result, err = service.CreateBindingCode(ctx, actor.ID, req.ActorID)
		} else {
			err = service.RevokeBinding(ctx, actor.ID, req.ActorID)
			result = true
		}
	case "webhook":
		var req struct{}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if panel == nil {
			err = aiops.ErrUnavailable
		} else {
			result, err = panel.SetTelegramWebhook(ctx, "")
		}
	}
	if err != nil {
		handleAIOpsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}
