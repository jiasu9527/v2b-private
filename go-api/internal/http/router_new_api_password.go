package httpapi

import (
	"context"
	"net/http"
	"strings"

	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/passport"
)

type newAPIWorkspacePasswordService interface {
	PrepareWorkspacePassword(context.Context, int64, string, bool) (map[string]any, error)
	ConfirmWorkspacePassword(context.Context, int64, string, newapibridge.WorkspacePasswordRequest) (map[string]any, error)
	RevealWorkspacePassword(context.Context, int64, string) (map[string]any, error)
}
type newAPIPasswordVerifier interface {
	VerifyCurrentPassword(context.Context, int64, string) error
}

func handleWorkspacePassword(w http.ResponseWriter, r *http.Request, passports passport.Service, svc newAPIBridgeService, user int64, auth, action string) {
	passwords, ok := svc.(newAPIWorkspacePasswordService)
	if !ok {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	verifier, ok := passports.(newAPIPasswordVerifier)
	if !ok {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	var req struct {
		PanelPassword    string `json:"panel_password"`
		Reset            bool   `json:"reset"`
		ChallengeID      string `json:"challenge_id"`
		Method           string `json:"method"`
		Code             string `json:"code"`
		OriginalPassword string `json:"original_password"`
	}
	if !decodeWorkspaceJSON(w, r, 8192, &req) {
		return
	}
	if (action != "password/prepare" && req.Reset) || (action != "password/confirm" && (req.ChallengeID != "" || req.Method != "" || req.Code != "" || req.OriginalPassword != "")) {
		newAPIError(w, newapibridge.ErrInvalid)
		return
	}
	if len(req.PanelPassword) > 1024 || strings.TrimSpace(req.PanelPassword) == "" {
		writeJSON(w, 403, map[string]any{"message": "请验证当前本站登录密码"})
		return
	}
	if err := verifier.VerifyCurrentPassword(r.Context(), user, req.PanelPassword); err != nil {
		handlePassportError(w, err)
		return
	}
	req.PanelPassword = ""
	var result any
	var err error
	switch action {
	case "password/prepare":
		result, err = passwords.PrepareWorkspacePassword(r.Context(), user, auth, req.Reset)
	case "password/confirm":
		result, err = passwords.ConfirmWorkspacePassword(r.Context(), user, auth, newapibridge.WorkspacePasswordRequest{ChallengeID: req.ChallengeID, Method: req.Method, Code: req.Code, OriginalPassword: req.OriginalPassword})
	case "password/reveal":
		result, err = passwords.RevealWorkspacePassword(r.Context(), user, auth)
	default:
		newAPIError(w, newapibridge.ErrInvalid)
		return
	}
	if err != nil {
		workspaceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": result})
}
