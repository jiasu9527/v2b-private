package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
)

const maxObservationJSONBody = 64 << 10
const observationAllCapability = "all-v1"

func addObservationConfig(payload map[string]any, r *http.Request, settings nodeapi.ObservationNodeSettings) bool {
	if !settings.Configured {
		return true
	}
	// Older node builds treat an empty allowlist as "revoke everyone". Only
	// send all-user mode to nodes that explicitly advertise support for it.
	if settings.Mode == nodeapi.ObservationModeAll && r.Header.Get("X-Observation-Capability") != observationAllCapability {
		return false
	}
	interval := 60
	if settings.Mode == nodeapi.ObservationModeAll {
		interval = 10
	}
	payload["observation_audit"] = map[string]any{
		"enable": settings.Enabled, "mode": settings.Mode,
		"allowed_user_ids": settings.AllowedUserIDs, "report_interval": interval,
	}
	return true
}

func observationService(w http.ResponseWriter, service nodeapi.Service) (nodeapi.ObservationService, bool) {
	observations, ok := service.(nodeapi.ObservationService)
	if !ok || observations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "观察功能暂不可用"})
		return nil, false
	}
	return observations, true
}

func observationNodeFromQuery(r *http.Request) (string, int64, error) {
	if strings.TrimSpace(r.URL.Query().Get("node_type")) != "v2node" {
		return "", 0, nodeapi.ErrObservationInvalid
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("node_id")), 10, 64)
	if err != nil || id <= 0 {
		return "", 0, nodeapi.ErrObservationInvalid
	}
	return "v2node", id, nil
}

func handleServerObservation(w http.ResponseWriter, r *http.Request, service nodeapi.Service) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	observations, ok := observationService(w, service)
	if !ok {
		return true
	}
	nodeType, nodeID, err := observationNodeFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "节点参数无效"})
		return true
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "请求格式无效"})
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxObservationJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Events []nodeapi.ObservationEvent `json:"events"`
	}
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "观察事件格式无效或请求过大"})
		return true
	}
	inserted, err := observations.ReportObservation(r.Context(), nodeapi.ObservationReportRequest{
		NodeType: nodeType,
		NodeID:   nodeID,
		Token:    r.Header.Get("X-Observation-Token"),
		Events:   payload.Events,
	})
	if err != nil {
		switch {
		case errors.Is(err, nodeapi.ErrObservationUnauthorized):
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "观察节点未授权"})
		case errors.Is(err, nodeapi.ErrObservationInvalid), errors.Is(err, nodeapi.ErrObservationUserNotAllowed):
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "观察事件参数无效"})
		default:
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "观察事件暂时无法保存，请稍后重试"})
		}
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"inserted": inserted}})
	return true
}

func handleAdminObservationNodes(w http.ResponseWriter, r *http.Request, sessions session.Service, service nodeapi.Service) bool {
	disableResponseCache(w)
	if _, ok := authenticateRequest(w, r, sessions, true); !ok {
		return true
	}
	observations, ok := observationService(w, service)
	if !ok {
		return true
	}
	switch r.Method {
	case http.MethodGet:
		items, err := observations.ObservationNodes(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "观察节点配置暂时无法读取"})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var payload struct {
			NodeType       string  `json:"node_type"`
			NodeID         int64   `json:"node_id"`
			Enabled        bool    `json:"enabled"`
			Mode           string  `json:"mode"`
			AllowedUserIDs []int64 `json:"allowed_user_ids"`
		}
		if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "观察节点参数无效"})
			return true
		}
		token, err := observations.ProvisionObservation(r.Context(), payload.NodeType, payload.NodeID, payload.Enabled, payload.Mode, payload.AllowedUserIDs)
		if err != nil {
			if errors.Is(err, nodeapi.ErrObservationInvalid) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": "观察节点或用户参数无效"})
			} else {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "观察节点配置暂时无法保存"})
			}
			return true
		}
		mode := strings.ToLower(strings.TrimSpace(payload.Mode))
		if mode == "" {
			mode = nodeapi.ObservationModeAllowlist
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"node_type": "v2node", "node_id": payload.NodeID, "enabled": payload.Enabled, "mode": mode, "token": token}})
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
	}
	return true
}

func observationFilterFromQuery(r *http.Request) (nodeapi.ObservationFilter, error) {
	q := r.URL.Query()
	filter := nodeapi.ObservationFilter{NodeType: strings.TrimSpace(q.Get("node_type"))}
	for _, item := range []struct {
		key string
		out *int64
	}{
		{"node_id", &filter.NodeID}, {"user_id", &filter.UserID}, {"from", &filter.From}, {"to", &filter.To},
	} {
		if raw := strings.TrimSpace(q.Get(item.key)); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return filter, nodeapi.ErrObservationInvalid
			}
			*item.out = n
		}
	}
	for _, item := range []struct {
		key string
		out *int
	}{{"page", &filter.Page}, {"page_size", &filter.PageSize}} {
		if raw := strings.TrimSpace(q.Get(item.key)); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return filter, nodeapi.ErrObservationInvalid
			}
			*item.out = n
		}
	}
	return filter, nil
}

func handleAdminObservationRead(w http.ResponseWriter, r *http.Request, sessions session.Service, service nodeapi.Service, summary bool) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	if _, ok := authenticateRequest(w, r, sessions, true); !ok {
		return true
	}
	observations, ok := observationService(w, service)
	if !ok {
		return true
	}
	filter, err := observationFilterFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "查询范围无效"})
		return true
	}
	var result map[string]any
	if summary {
		result, err = observations.ObservationSummary(r.Context(), filter)
	} else {
		result, err = observations.ObservationEvents(r.Context(), filter)
	}
	if err != nil {
		if errors.Is(err, nodeapi.ErrObservationInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "查询范围无效"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "观察记录暂时无法读取"})
		}
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}
