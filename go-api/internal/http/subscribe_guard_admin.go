package httpapi

import (
	"context"
	"net/http"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
)

func handleAdminSubscribeGuardStats(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, nodeService nodeapi.Service, userService usersvc.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	stats := subscribeGuardStatsSnapshot(cfg)
	enrichSubscribeGuardUserRank(r.Context(), stats, userService)
	if nodeService != nil {
		sensitive, err := nodeService.SensitiveAccessStats(r.Context(), 20)
		if err == nil {
			stats["sensitive"] = sensitive
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
	return true
}

func enrichSubscribeGuardUserRank(ctx context.Context, stats map[string]any, userService usersvc.Service) {
	if userService == nil {
		return
	}
	rawItems, _ := stats["top_subscribe_tokens"].([]map[string]any)
	if len(rawItems) == 0 {
		return
	}
	result := make([]map[string]any, 0, len(rawItems))
	for _, item := range rawItems {
		token, _ := item["token"].(string)
		if token == "" {
			continue
		}
		userID, err := userService.ResolveClientUserID(ctx, token)
		if err != nil || userID <= 0 {
			continue
		}
		subscribe, err := userService.Subscribe(ctx, userID)
		if err != nil {
			continue
		}
		next := map[string]any{
			"user_id":  userID,
			"email":    subscribe.Email,
			"token":    token,
			"count":    item["count"],
			"ua_count": item["ua_count"],
			"uas":      item["uas"],
		}
		result = append(result, next)
	}
	if len(result) > 0 {
		stats["top_subscribe_users"] = result
	}
}
