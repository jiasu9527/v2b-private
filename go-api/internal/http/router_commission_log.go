package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

// Financial record visibility is distinct from settlement. This route never
// accepts a new balance or order commission status.
func handleAdminCommissionLog(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service, action string) bool {
	if sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "session service unavailable"})
		return true
	}
	token, err := readAuthToken(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "登录参数无效"})
		return true
	}
	identity, ok := authenticateSupportToken(w, r, sessions, token, true)
	if !ok {
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return true
	}
	logs, ok := service.(admin.CommissionLogAdminService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "返佣记录管理暂不可用"})
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	switch action {
	case "list":
		var req admin.CommissionLogListRequest
		if !decodeStrictDNSFailoverJSON(w, r, &req) {
			return true
		}
		result, err := logs.ListCommissionLogs(r.Context(), req)
		if err != nil {
			return writeCommissionLogError(w, err)
		}
		writeJSON(w, http.StatusOK, result)
	case "preview":
		var req admin.CommissionLogPreviewRequest
		if !decodeStrictDNSFailoverJSON(w, r, &req) {
			return true
		}
		result, err := logs.PreviewCommissionLogs(r.Context(), identity.ID, req)
		if err != nil {
			return writeCommissionLogError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case "delete", "delete-many", "restore":
		var req admin.CommissionLogApplyRequest
		if !decodeStrictDNSFailoverJSON(w, r, &req) {
			return true
		}
		headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if headerKey != "" {
			if req.IdempotencyKey != "" && req.IdempotencyKey != headerKey {
				return writeCommissionLogError(w, admin.ErrCommissionLogValidation)
			}
			req.IdempotencyKey = headerKey
		}
		if action == "delete" && (req.TargetID == nil || *req.TargetID <= 0) {
			return writeCommissionLogError(w, admin.ErrCommissionLogValidation)
		}
		req.ExpectedAction = "delete"
		if action == "restore" {
			req.ExpectedAction = "restore"
		}
		result, err := logs.ApplyCommissionLogs(r.Context(), identity.ID, req)
		if err != nil {
			return writeCommissionLogError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
	}
	return true
}

func writeCommissionLogError(w http.ResponseWriter, err error) bool {
	status, message := http.StatusInternalServerError, "返佣记录操作失败，请核对状态；不要重复提交写入"
	switch {
	case errors.Is(err, admin.ErrCommissionLogValidation):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, admin.ErrCommissionLogConflict):
		status, message = http.StatusConflict, "预览已过期、记录已变化或幂等键冲突，请重新查询；不要用旧预览重复执行"
	case errors.Is(err, admin.ErrCommissionLogForbidden):
		status, message = http.StatusForbidden, "当前账号无权处理该返佣记录预览"
	case errors.Is(err, admin.ErrCommissionLogNotFound):
		status, message = http.StatusNotFound, "返佣记录或预览不存在"
	case errors.Is(err, admin.ErrUnavailable):
		status, message = http.StatusServiceUnavailable, "返佣记录服务暂不可用"
	}
	writeJSON(w, status, map[string]any{"message": message})
	return true
}
