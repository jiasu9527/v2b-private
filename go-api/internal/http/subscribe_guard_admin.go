package httpapi

import (
	"net/http"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func handleAdminSubscribeGuardStats(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": subscribeGuardStatsSnapshot(cfg)})
	return true
}
