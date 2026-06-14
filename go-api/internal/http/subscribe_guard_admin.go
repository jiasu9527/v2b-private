package httpapi

import (
	"net/http"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
)

func handleAdminSubscribeGuardStats(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, nodeService nodeapi.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	stats := subscribeGuardStatsSnapshot(cfg)
	if nodeService != nil {
		sensitive, err := nodeService.SensitiveAccessStats(r.Context(), 20)
		if err == nil {
			stats["sensitive"] = sensitive
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
	return true
}
