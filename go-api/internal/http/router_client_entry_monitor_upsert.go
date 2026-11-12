package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
)

func handleClientEntryMonitorConfig(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodGet {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet)
	}
	s, ok := any(service).(admin.ClientEntryMonitorConfigService)
	if !ok || s == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "入口检测精简配置服务暂不可用"})
		return true
	}
	values := map[string]int64{"policy_id": 0, "limit": 10, "offset": 0}
	for key := range values {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 || (key != "offset" && n == 0) || (key == "limit" && n > 20) || (key == "offset" && n > 10000) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": "监测配置查询范围无效"})
				return true
			}
			values[key] = n
		}
	}
	disableResponseCache(w)
	page, err := s.ReadClientEntryMonitorConfig(r.Context(), values["policy_id"], int(values["limit"]), int(values["offset"]))
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "入口检测配置暂时无法读取，请稍后重试"
		if errors.Is(err, admin.ErrClientEntryMonitorRevisionConflict) {
			status, message = http.StatusConflict, "读取期间监测配置发生变化，请重新查询"
		}
		writeJSON(w, status, map[string]any{"message": message})
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": page})
	return true
}

func handleClientEntryMonitorUpsert(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodPut {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodPut)
	}
	s, ok := any(service).(admin.ClientEntryMonitorUpsertService)
	if !ok || s == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "入口检测增量保存服务暂不可用"})
		return true
	}
	// Validate presence as well as types before decoding booleans into zero values.
	// A forgotten auto_split_enabled must never silently turn off an existing flag.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDNSFailoverJSONBody))
	params, parseErr := panelOperationParams(raw)
	schemaRaw, _ := json.Marshal(panelMonitorSaveSchema(true))
	var schema map[string]any
	_ = json.Unmarshal(schemaRaw, &schema)
	if err != nil || parseErr != nil || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") || !panelSchemaMatches(params, schema) {
		capturePanelOperationPreflight(r.Context(), "entry_monitor_invalid_arguments", false)
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "监测增量参数无效：需最新revision及1至20条完整items，每条保留全部targets、port和auto_split_enabled"})
		return true
	}
	var request admin.ClientEntryMonitorSaveRequest
	if json.Unmarshal(raw, &request) != nil {
		capturePanelOperationPreflight(r.Context(), "entry_monitor_invalid_arguments", false)
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "监测增量参数无效"})
		return true
	}
	receipt, err := s.UpsertClientEntryMonitorReceipt(r.Context(), request)
	if err != nil {
		return writeClientEntryMonitorMutationError(w, r, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": receipt})
	return true
}

func writeClientEntryMonitorMutationError(w http.ResponseWriter, r *http.Request, err error) bool {
	var validation *admin.ClientEntryMonitorValidationError
	switch {
	case errors.Is(err, admin.ErrClientEntryMonitorRevisionConflict):
		capturePanelOperationPreflight(r.Context(), "entry_monitor_conflict", false)
		writeJSON(w, http.StatusConflict, map[string]any{"message": err.Error()})
	case errors.As(err, &validation):
		capturePanelOperationPreflight(r.Context(), "entry_monitor_invalid_arguments", false)
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": validation.Error()})
	default:
		// Unknown DB/commit errors must not claim that no write occurred.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "入口检测保存结果暂无法确认，请查询配置核对，不要重复提交"})
	}
	return true
}
