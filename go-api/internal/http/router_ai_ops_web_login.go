package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/session"
)

type aiOpsWebLoginService interface {
	GetWebLoginStatus(context.Context, int64) (aiops.WebLoginStatus, error)
	RunWebLogin(context.Context, int64, aiops.WebLoginRequest) (aiops.WebLoginResult, error)
}

// No password in a query string, request log or chat payload. This endpoint
// is only registered on the standard AI Ops admin path, not Telegram.
func handleAIOpsWebLogin(w http.ResponseWriter, r *http.Request, sessions session.Service, service aiOpsService, action string) {
	if r.URL.RawQuery != "" || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin") {
		handleAIOpsError(w, aiops.ErrForbidden)
		return
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		handleSessionError(w, session.ErrUnauthorized)
		return
	}
	actor, ok := authenticateSupportSession(w, r, sessions, true)
	if !ok {
		return
	}
	browser, ok := service.(aiOpsWebLoginService)
	if !ok {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	if action == "web-login/status" && r.Method == http.MethodGet {
		state, err := browser.GetWebLoginStatus(r.Context(), actor.ID)
		if err != nil {
			handleAIOpsError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": state})
		return
	}
	if action != "web-login/run" || r.Method != http.MethodPost {
		if action == "web-login/run" {
			w.Header().Set("Allow", http.MethodPost)
		} else {
			w.Header().Set("Allow", http.MethodGet)
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method not allowed"})
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"message": "Content-Type must be application/json"})
		return
	}
	var request aiops.WebLoginRequest
	if decodeDNSProbeJSON(w, r, 32<<10, &request) != nil {
		handleAIOpsError(w, aiops.ErrValidation)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(145 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 138*time.Second)
	defer cancel()
	out, err := browser.RunWebLogin(ctx, actor.ID, request)
	if err != nil {
		status, code, message := 0, "", ""
		switch {
		case errors.Is(err, aiops.ErrWebLoginBlocked):
			status, code, message = 422, "invalid_target", aiops.ErrWebLoginBlocked.Error()
		case errors.Is(err, aiops.ErrWebLoginUnsupported):
			status, code, message = 422, "unsupported_login", aiops.ErrWebLoginUnsupported.Error()
		case errors.Is(err, aiops.ErrWebLoginVerification):
			status, code, message = 422, "verification_required", aiops.ErrWebLoginVerification.Error()
		case errors.Is(err, aiops.ErrWebLoginFailed):
			status, code, message = 422, "login_failed", aiops.ErrWebLoginFailed.Error()
		case errors.Is(err, aiops.ErrWebLoginNoContent):
			status, code, message = 422, "no_content", aiops.ErrWebLoginNoContent.Error()
		case errors.Is(err, aiops.ErrWebLoginModel):
			status, code, message = 502, "model_unavailable", aiops.ErrWebLoginModel.Error()
		case errors.Is(err, context.DeadlineExceeded):
			status, code, message = 504, "deadline_exceeded", "网页登录或分析超时，没有自动重试"
		default:
			handleAIOpsError(w, err)
		}
		if status != 0 {
			// These fixed strings are generated locally (never from the site).
			writeJSON(w, status, map[string]string{"code": code, "message": message})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
