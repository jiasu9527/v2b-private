package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/session"
)

type aiTelegramMiniService interface {
	AuthenticateMini(context.Context, string) (aiops.MiniAuthResult, error)
	AuthenticateMiniToken(context.Context, string) (aiops.MiniIdentity, error)
	MiniSendMessage(context.Context, string, aiops.SendMessageRequest) (aiops.SendMessageResult, error)
	MiniApproveAction(context.Context, string, int64, int64) (aiops.Action, error)
	MiniRejectAction(context.Context, string, int64, int64) (aiops.Action, error)
	RunMiniOperation(context.Context, string, func(context.Context) error) error
}

// This adapter is used only on /api/v1/ai-ops-mini/. It never replaces the
// application's normal session service, so Mini tokens cannot log into the
// ordinary administrator, payment, support or user APIs.
type miniSessionAdapter struct{ bridge aiTelegramMiniService }

func (a miniSessionAdapter) Authenticate(ctx context.Context, token string, admin bool) (*session.Identity, error) {
	return a.AuthenticateFresh(ctx, token, admin)
}
func (a miniSessionAdapter) AuthenticateFresh(ctx context.Context, token string, admin bool) (*session.Identity, error) {
	token = miniBearerToken(token)
	if token == "" {
		return nil, session.ErrUnauthorized
	}
	v, err := a.bridge.AuthenticateMiniToken(ctx, token)
	if err != nil {
		if errors.Is(err, aiops.ErrForbidden) || errors.Is(err, aiops.ErrNotFound) || errors.Is(err, aiops.ErrValidation) || errors.Is(err, aiops.ErrConflict) {
			return nil, session.ErrUnauthorized
		}
		return nil, session.ErrUnavailable
	}
	if admin && v.IsAdmin != 1 {
		return nil, session.ErrUnauthorized
	}
	return &session.Identity{ID: v.ID, Email: v.Email, IsAdmin: v.IsAdmin, IsStaff: v.IsStaff}, nil
}
func (miniSessionAdapter) ListSessions(context.Context, int64) (map[string]session.SessionMeta, error) {
	return nil, session.ErrUnauthorized
}
func (miniSessionAdapter) RemoveSession(context.Context, int64, string) (bool, error) {
	return false, session.ErrUnauthorized
}
func (miniSessionAdapter) RevalidateEveryStreamTick() bool { return true }

func miniBearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "tma_") {
		return ""
	}
	return parts[1]
}

func handleAITelegramMini(w http.ResponseWriter, r *http.Request, bridgeBase aiTelegramService, ai aiOpsService, action string) {
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("Referrer-Policy", "no-referrer")
	bridge, ok := bridgeBase.(aiTelegramMiniService)
	if !ok || ai == nil {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(35 * time.Second))
	if action == "auth" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method not allowed"})
			return
		}
		var req struct {
			InitData string `json:"init_data"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err := bridge.AuthenticateMini(ctx, req.InitData)
		if err != nil {
			if errors.Is(err, aiops.ErrForbidden) || errors.Is(err, aiops.ErrValidation) || errors.Is(err, aiops.ErrNotFound) || errors.Is(err, aiops.ErrConflict) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Telegram 身份无效、已过期或未绑定运维账号，请从 Bot 重新打开并核对授权"})
			} else {
				handleAIOpsError(w, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
		return
	}
	// Reject URL credentials even if a valid browser auth cookie is present.
	token := miniBearerToken(r.Header.Get("Authorization"))
	if token == "" || r.URL.Query().Has("auth_data") {
		miniAuthError(w, session.ErrUnauthorized)
		return
	}
	identity, err := bridge.AuthenticateMiniToken(ctx, token)
	if err != nil {
		if errors.Is(err, aiops.ErrForbidden) || errors.Is(err, aiops.ErrNotFound) || errors.Is(err, aiops.ErrValidation) || errors.Is(err, aiops.ErrConflict) {
			miniAuthError(w, session.ErrUnauthorized)
		} else {
			miniAuthError(w, session.ErrUnavailable)
		}
		return
	}
	if action == "identity" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": identity})
		return
	}
	// Explicit scope: no recursive Telegram binding/configuration, ordinary
	// backend APIs or separate session creation through a Mini credential.
	switch action {
	case "config", "config/save", "sessions", "session", "session/stream", "session/access", "session/delete", "session/clear", "reset", "chat/stop", "servers", "server/save", "server/delete", "server/fingerprint", "knowledge", "knowledge/detail", "knowledge/save", "knowledge/delete":
		if r.Method == http.MethodPost {
			err = bridge.RunMiniOperation(ctx, token, func(guarded context.Context) error {
				handleAIOps(w, r.WithContext(guarded), miniSessionAdapter{bridge}, ai, action)
				return nil
			})
			if err != nil {
				miniAuthError(w, err)
			}
		} else {
			handleAIOps(w, r, miniSessionAdapter{bridge}, ai, action)
		}
		return
	case "chat/send", "action/approve", "action/reject":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method not allowed"})
			return
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not found"})
		return
	}
	var result any
	status := http.StatusAccepted
	if action == "chat/send" {
		var req aiops.SendMessageRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if identity.IsAdmin != 1 && req.ReasoningEffort != "" && req.ReasoningEffort != "default" {
			handleAIOpsError(w, aiops.ErrForbidden)
			return
		}
		result, err = bridge.MiniSendMessage(ctx, token, req)
	} else {
		var req struct {
			SessionID int64 `json:"session_id"`
			ID        int64 `json:"id"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if req.SessionID <= 0 || req.ID <= 0 {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		if action == "action/approve" {
			result, err = bridge.MiniApproveAction(ctx, token, req.SessionID, req.ID)
		} else {
			result, err = bridge.MiniRejectAction(ctx, token, req.SessionID, req.ID)
			status = http.StatusOK
		}
	}
	if err != nil {
		handleAIOpsError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"data": result})
}

func miniAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrUnauthorized) || errors.Is(err, aiops.ErrForbidden) || errors.Is(err, aiops.ErrNotFound) || errors.Is(err, aiops.ErrValidation) || errors.Is(err, aiops.ErrConflict) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "auth_invalid", "message": "Telegram 运维登录已失效，请从 Bot 重新打开"})
		return
	}
	handleAIOpsError(w, aiops.ErrUnavailable)
}
