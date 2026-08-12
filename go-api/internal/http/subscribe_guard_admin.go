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
	disableResponseCache(w)
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

func handleAdminSubscribeGuardUserSearch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
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
	keyword := strings.TrimSpace(inputs["keyword"])
	if keyword == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{}, "total": int64(0)})
		return true
	}
	if len(keyword) > 255 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "搜索内容过长"})
		return true
	}

	filter := admin.UserFilter{Key: "email", Condition: "模糊", Value: keyword}
	if isDecimalDigits(keyword) {
		userID, parseErr := strconv.ParseInt(keyword, 10, 64)
		if parseErr != nil || userID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "用户ID格式不正确"})
			return true
		}
		filter = admin.UserFilter{Key: "id", Condition: "=", Value: strconv.FormatInt(userID, 10)}
	}
	result, err := adminService.ListUsers(r.Context(), admin.UserFetchRequest{
		Current:  1,
		PageSize: 20,
		Sort:     "id",
		SortType: "DESC",
		Filters:  []admin.UserFilter{filter},
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	rows := make([]map[string]any, 0, len(result.Data))
	for _, user := range result.Data {
		rows = append(rows, subscribeGuardUserSearchPublicRow(user))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "total": result.Total})
	return true
}

func isDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func subscribeGuardUserSearchPublicRow(user map[string]any) map[string]any {
	row := map[string]any{}
	for _, key := range []string{
		"id", "email", "banned", "plan_id", "plan_name", "u", "d", "transfer_enable", "expired_at",
	} {
		if value, ok := user[key]; ok {
			row[key] = value
		}
	}
	return row
}

func handleAdminSubscribeGuardUserDetail(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
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
	userID, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "用户不存在"})
		return true
	}
	info, err := adminService.GetUserInfoByID(r.Context(), userID)
	if err != nil {
		return handleAdminError(w, err)
	}
	if anyInt64(info["deleted_user"]) != 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "用户不存在"})
		return true
	}
	user := subscribeGuardUserPublicDetail(info)
	stats := subscribeGuardUserStatsSnapshot(cfg, userID, subscribeGuardCanonicalToken(info))
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"user": user, "stats": stats}})
	return true
}

func subscribeGuardUserPublicDetail(info map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{
		"id", "email", "banned", "plan_id", "group_id", "u", "d", "transfer_enable",
		"device_limit", "expired_at", "t", "last_login_at", "balance", "commission_balance",
		"discount", "speed_limit", "invite_user_id", "invite_code", "remarks", "created_at",
		"updated_at", "is_admin", "is_staff",
	} {
		if value, ok := info[key]; ok {
			result[key] = value
		}
	}
	if inviteUser, ok := info["invite_user"].(map[string]any); ok {
		result["invite_user"] = map[string]any{
			"id":    inviteUser["id"],
			"email": inviteUser["email"],
		}
	}
	return result
}

func subscribeGuardCanonicalToken(info map[string]any) string {
	value, ok := info["token"]
	if !ok || value == nil {
		return ""
	}
	token, _ := value.(string)
	return strings.TrimSpace(token)
}

func enrichSubscribeGuardUserRank(ctx context.Context, stats map[string]any, userService usersvc.Service, adminService admin.Service) {
	if userService == nil {
		return
	}
	userIDItems, _ := stats["top_subscribe_user_ids"].([]map[string]any)
	result := make([]map[string]any, 0, len(userIDItems))
	for _, item := range userIDItems {
		userID := anyInt64(item["user_id"])
		if userID <= 0 {
			continue
		}
		if next, ok := subscribeGuardUserRankRow(ctx, userID, item, userService, adminService); ok {
			result = append(result, next)
		}
	}

	legacyItems, _ := stats["top_subscribe_tokens"].([]map[string]any)
	seenUserIDs := make(map[int64]struct{}, len(result))
	for _, item := range result {
		seenUserIDs[anyInt64(item["user_id"])] = struct{}{}
	}
	for _, item := range legacyItems {
		token, _ := item["token"].(string)
		if canonicalToken, ok := item["canonical_token"].(string); ok && strings.TrimSpace(canonicalToken) != "" {
			token = strings.TrimSpace(canonicalToken)
		}
		if token == "" {
			continue
		}
		userID, err := peekSubscribeGuardLegacyUserID(ctx, userService, token)
		if err != nil || userID <= 0 {
			continue
		}
		if _, exists := seenUserIDs[userID]; exists {
			continue
		}
		if next, ok := subscribeGuardUserRankRow(ctx, userID, item, userService, adminService); ok {
			result = append(result, next)
			seenUserIDs[userID] = struct{}{}
		}
	}
	stats["top_subscribe_users"] = result
}

func peekSubscribeGuardLegacyUserID(ctx context.Context, userService usersvc.Service, token string) (int64, error) {
	if resolver, ok := userService.(subscribeGuardUserResolver); ok {
		return resolver.PeekClientUserID(ctx, token)
	}
	return userService.ResolveClientUserID(ctx, token)
}

func subscribeGuardUserRankRow(ctx context.Context, userID int64, item map[string]any, userService usersvc.Service, adminService admin.Service) (map[string]any, bool) {
	subscribe, err := userService.Subscribe(ctx, userID)
	if err != nil {
		return nil, false
	}
	banned := int64(0)
	if adminService != nil {
		if info, err := adminService.GetUserInfoByID(ctx, userID); err == nil {
			banned = anyInt64(info["banned"])
		}
	}
	return map[string]any{
		"user_id":  userID,
		"email":    subscribe.Email,
		"count":    item["count"],
		"ua_count": item["ua_count"],
		"uas":      item["uas"],
		"ip_count": item["ip_count"],
		"ips":      item["ips"],
		"banned":   banned,
	}, true
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
