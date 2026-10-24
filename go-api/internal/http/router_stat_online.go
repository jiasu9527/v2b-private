package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

type onlineStatsService interface {
	GetOnlineStats(context.Context) (admin.OnlineStats, error)
}

var _ onlineStatsService = (*admin.DBService)(nil)

type onlineUsersService interface {
	GetOnlineUsers(context.Context, admin.OnlineUsersRequest) (admin.OnlineUsers, error)
}

var _ onlineUsersService = (*admin.DBService)(nil)

func handleAdminStatOnline(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	disableResponseCache(w)
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	identity, ok := authenticateRequest(w, r, sessions, true)
	if !ok {
		return true
	}
	if identity == nil || identity.IsAdmin == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "仅管理员可查看在线人数分析"})
		return true
	}
	stats, ok := service.(onlineStatsService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "在线人数分析服务不可用，请更新后端"})
		return true
	}
	data, err := stats.GetOnlineStats(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminStatOnlineUsers(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	disableResponseCache(w)
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	identity, ok := authenticateRequest(w, r, sessions, true)
	if !ok {
		return true
	}
	if identity == nil || identity.IsAdmin == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "仅管理员可查看在线用户账号"})
		return true
	}
	users, ok := service.(onlineUsersService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "在线用户查询服务不可用，请更新后端"})
		return true
	}
	query := r.URL.Query()
	req := admin.OnlineUsersRequest{
		Scope: strings.TrimSpace(query.Get("scope")), CountryCode: query.Get("country_code"),
		ProvinceCode: query.Get("province_code"), City: query.Get("city"), Reason: query.Get("reason"), Search: query.Get("search"),
		Page: 1, PageSize: 20,
	}
	for key, target := range map[string]*int64{"page": &req.Page, "page_size": &req.PageSize} {
		if query.Has(key) {
			parsed, err := strconv.ParseInt(strings.TrimSpace(query.Get(key)), 10, 64)
			if err != nil || parsed < 1 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": "页码和每页人数必须为正整数"})
				return true
			}
			*target = parsed
		}
	}
	if ((req.Scope == "province" || req.Scope == "city") && !query.Has("province_code")) || (req.Scope == "city" && !query.Has("city")) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "请指定省份和城市；查询未知地区时传入空值"})
		return true
	}
	req, err := admin.NormalizeOnlineUsersRequest(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	data, err := users.GetOnlineUsers(r.Context(), req)
	if errors.Is(err, admin.ErrOnlineUsersRequest) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}
