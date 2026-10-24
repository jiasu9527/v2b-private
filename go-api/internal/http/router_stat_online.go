package httpapi

import (
	"context"
	"net/http"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

type onlineStatsService interface {
	GetOnlineStats(context.Context) (admin.OnlineStats, error)
}

var _ onlineStatsService = (*admin.DBService)(nil)

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
