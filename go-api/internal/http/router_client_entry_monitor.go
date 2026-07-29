package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
)

func clientEntryMonitorService(w http.ResponseWriter, service admin.Service) (admin.ClientEntryMonitorAdminService, bool) {
	result, ok := any(service).(admin.ClientEntryMonitorAdminService)
	if !ok || result == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "用户入口检测服务暂不可用"})
		return nil, false
	}
	return result, true
}

func handleClientEntryMonitors(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	monitorService, ok := clientEntryMonitorService(w, service)
	if !ok {
		return true
	}
	switch r.Method {
	case http.MethodGet:
		result, err := monitorService.ListClientEntryMonitors(r.Context())
		if err != nil {
			return writeClientEntryMonitorError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPut:
		var request admin.ClientEntryMonitorSaveRequest
		if !decodeStrictDNSFailoverJSON(w, r, &request) {
			return true
		}
		if request.Items == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "入口检测规则列表不能为空"})
			return true
		}
		result, err := monitorService.SaveClientEntryMonitors(r.Context(), request)
		if err != nil {
			return writeClientEntryMonitorError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet+", "+http.MethodPut)
	}
	return true
}

func handleClientEntryMonitorRun(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodPost {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodPost)
	}
	monitorService, ok := clientEntryMonitorService(w, service)
	if !ok {
		return true
	}
	var request struct {
		PolicyIDs []int64 `json:"policy_ids"`
	}
	if !decodeStrictDNSFailoverJSON(w, r, &request) {
		return true
	}
	runID, err := monitorService.StartClientEntryMonitorRunForPolicies(r.Context(), request.PolicyIDs, 0, 0)
	if err != nil {
		return writeClientEntryMonitorError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"run_id": runID}})
	return true
}

func handleClientEntryMonitorRuns(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodGet {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet)
	}
	monitorService, ok := clientEntryMonitorService(w, service)
	if !ok {
		return true
	}
	limit := int64(20)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "limit 无效"})
			return true
		}
		limit = value
	}
	result, err := monitorService.ListClientEntryMonitorRuns(r.Context(), limit)
	if err != nil {
		return writeClientEntryMonitorError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": result}})
	return true
}

func writeClientEntryMonitorError(w http.ResponseWriter, err error) bool {
	status := http.StatusBadRequest
	if errors.Is(err, admin.ErrUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, admin.ErrClientEntryMonitorRevisionConflict) || strings.Contains(err.Error(), "用户入口检测正在进行") {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"message": err.Error()})
	return true
}
