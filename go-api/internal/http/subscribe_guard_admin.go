package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
)

func handleAdminSubscribeGuardStats(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, nodeService nodeapi.Service, userService usersvc.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	stats := subscribeGuardStatsSnapshot(cfg)
	enrichSubscribeGuardUserRank(r.Context(), stats, userService, adminService)
	if nodeService != nil {
		sensitive, err := nodeService.SensitiveAccessStats(r.Context(), 20)
		if err == nil {
			stats["sensitive"] = sensitive
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
	return true
}

func enrichSubscribeGuardUserRank(ctx context.Context, stats map[string]any, userService usersvc.Service, adminService admin.Service) {
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
		banned := int64(0)
		if adminService != nil {
			if info, err := adminService.GetUserInfoByID(ctx, userID); err == nil {
				banned = anyInt64(info["banned"])
			}
		}
		next := map[string]any{
			"user_id":  userID,
			"email":    subscribe.Email,
			"token":    token,
			"count":    item["count"],
			"ua_count": item["ua_count"],
			"uas":      item["uas"],
			"ip_count": item["ip_count"],
			"ips":      item["ips"],
			"banned":   banned,
		}
		result = append(result, next)
	}
	if len(result) > 0 {
		stats["top_subscribe_users"] = result
	}
}

func handleAdminSubscribeGuardSetUserBanned(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "用户不存在"})
		return true
	}
	banned, err := strconv.ParseInt(strings.TrimSpace(inputs["banned"]), 10, 64)
	if err != nil || (banned != 0 && banned != 1) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "账号状态格式不正确"})
		return true
	}

	updated, err := adminService.SetUserBanned(r.Context(), id, banned)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}
