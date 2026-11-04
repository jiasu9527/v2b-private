package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

type clientEntryOrderService interface {
	ListClientEntryVisibleOrder(context.Context, int, int) (admin.ClientEntryOrderPage, error)
	MoveClientEntryVisibleRows(context.Context, admin.ClientEntryOrderMoveRequest) (admin.ClientEntryOrderMoveResult, error)
}

var _ clientEntryOrderService = (*admin.DBService)(nil)

func writeClientEntryOrderError(w http.ResponseWriter, r *http.Request, err error) bool {
	code, status := "", http.StatusBadRequest
	if errors.Is(err, admin.ErrClientEntryOrderInvalid) {
		code = "entry_order_invalid_arguments"
	}
	if errors.Is(err, admin.ErrClientEntryOrderConflict) {
		code, status = "entry_order_conflict", http.StatusConflict
	}
	if code == "" {
		return handleAdminError(w, err)
	}
	// Only typed business errors returned before any order write are recoverable.
	capturePanelOperationPreflight(r.Context(), code, false)
	writeJSON(w, status, map[string]any{"message": err.Error(), "code": code})
	return true
}
func handleAdminClientEntryOrder(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service, move bool) bool {
	method := http.MethodGet
	if move {
		method = http.MethodPost
	}
	if !requireHTTPMethod(w, r, method) {
		return true
	}
	identity, ok := authenticateRequest(w, r, sessions, true)
	if !ok {
		return true
	}
	if identity == nil || identity.IsAdmin == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "仅管理员可调整全局入口顺序"})
		return true
	}
	disableResponseCache(w)
	backend, ok := service.(clientEntryOrderService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "入口相对排序服务不可用，请更新后端"})
		return true
	}
	if !move {
		current, size := 1, 50
		for key, target := range map[string]*int{"current": &current, "page_size": &size} {
			if raw := r.URL.Query().Get(key); raw != "" {
				n, err := strconv.Atoi(raw)
				if err != nil {
					return writeClientEntryOrderError(w, r, admin.ErrClientEntryOrderInvalid)
				}
				*target = n
			}
		}
		if current < 1 || current > 10000 || size < 1 || size > 100 {
			return writeClientEntryOrderError(w, r, admin.ErrClientEntryOrderInvalid)
		}
		data, err := backend.ListClientEntryVisibleOrder(r.Context(), current, size)
		if err != nil {
			return writeClientEntryOrderError(w, r, err)
		}
		if expected := r.URL.Query().Get("expected_revision"); expected != "" && expected != data.Revision {
			return writeClientEntryOrderError(w, r, admin.ErrClientEntryOrderConflict)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}
	var request admin.ClientEntryOrderMoveRequest
	if !decodeEntryEvidenceJSON(w, r, &request) {
		capturePanelOperationPreflight(r.Context(), "entry_order_invalid_arguments", false)
		return true
	}
	if err := admin.ValidateClientEntryOrderMove(request); err != nil {
		return writeClientEntryOrderError(w, r, err)
	}
	data, err := backend.MoveClientEntryVisibleRows(r.Context(), request)
	if err != nil {
		return writeClientEntryOrderError(w, r, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}
