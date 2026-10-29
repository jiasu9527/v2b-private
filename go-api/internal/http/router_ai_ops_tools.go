package httpapi

import (
	"context"
	"forest/go-api/internal/aiops"
	"net/http"
)

type aiOpsToolsService interface {
	ListServers(context.Context, int64) ([]aiops.Server, error)
	SaveServer(context.Context, int64, aiops.ServerSaveRequest) (aiops.Server, error)
	DeleteServer(context.Context, int64, int64, int64) error
	ProbeHostKey(context.Context, int64, aiops.HostKeyRequest) (aiops.HostKeyResult, error)
	ListActions(context.Context, int64, int64) ([]aiops.Action, error)
	ApproveAction(context.Context, int64, int64, int64) (aiops.Action, error)
	RejectAction(context.Context, int64, int64, int64) (aiops.Action, error)
}

func handleAIOpsTools(w http.ResponseWriter, r *http.Request, base aiOpsService, actorID int64, action string) {
	service, ok := base.(aiOpsToolsService)
	if !ok {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	var result any
	var err error
	status := 200
	ctx := r.Context()
	switch action {
	case "servers":
		result, err = service.ListServers(ctx, actorID)
	case "server/save":
		var req aiops.ServerSaveRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = service.SaveServer(ctx, actorID, req)
	case "server/delete":
		var req struct {
			ID      int64 `json:"id"`
			Version int64 `json:"version"`
			Confirm bool  `json:"confirm"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if !req.Confirm || req.ID <= 0 || req.Version <= 0 {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		err = service.DeleteServer(ctx, actorID, req.ID, req.Version)
		result = true
	case "server/fingerprint":
		var req aiops.HostKeyRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = service.ProbeHostKey(ctx, actorID, req)
	case "action/approve", "action/reject":
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
			result, err = service.ApproveAction(ctx, actorID, req.SessionID, req.ID)
			status = 202
		} else {
			result, err = service.RejectAction(ctx, actorID, req.SessionID, req.ID)
		}
	default:
		writeJSON(w, 404, map[string]string{"message": "Not found"})
		return
	}
	if err != nil {
		handleAIOpsError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"data": result})
}
