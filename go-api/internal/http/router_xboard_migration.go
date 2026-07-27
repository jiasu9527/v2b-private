package httpapi

import (
	"context"
	"net/http"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

type xboardMigrationAdminService interface {
	PreviewXBoardMigration(context.Context, admin.XBoardMigrationRequest) (admin.XBoardMigrationPreview, error)
	ExecuteXBoardMigration(context.Context, admin.XBoardMigrationRequest) (admin.XBoardMigrationResult, error)
}

func handleAdminXBoardMigrationPreview(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	service, ok := adminService.(xboardMigrationAdminService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "迁移服务不可用"})
		return true
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方法无效"})
		return true
	}
	var req admin.XBoardMigrationRequest
	if err := readJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	result, err := service.PreviewXBoardMigration(r.Context(), req)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminXBoardMigrationExecute(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	service, ok := adminService.(xboardMigrationAdminService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "迁移服务不可用"})
		return true
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方法无效"})
		return true
	}
	var req admin.XBoardMigrationRequest
	if err := readJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	result, err := service.ExecuteXBoardMigration(r.Context(), req)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}
