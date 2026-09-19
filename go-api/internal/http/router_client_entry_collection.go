package httpapi

import (
	"context"
	"net/http"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

// Separate from the broad admin interface so older implementations can fail
// closed rather than silently implementing a partial collection operation.
type clientEntryCollectionService interface {
	ListClientEntryCollections(context.Context) ([]admin.ClientEntryCollectionRecord, error)
	SaveClientEntryCollection(context.Context, admin.ClientEntryCollectionSaveRequest) (admin.ClientEntryCollectionRecord, error)
	DeleteClientEntryCollection(context.Context, int64, int64) (bool, error)
	RemoveClientEntryCollectionMember(context.Context, int64, int64, admin.ClientEntryCollectionItem) (bool, error)
}

var _ clientEntryCollectionService = (*admin.DBService)(nil)

func handleAdminClientEntryCollection(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service, action string) bool {
	method := http.MethodPost
	switch action {
	case "fetch":
		method = http.MethodGet
	case "save", "drop", "remove":
	default:
		return false
	}
	if !requireHTTPMethod(w, r, method) {
		return true
	}
	identity, ok := authenticateRequest(w, r, sessions, true)
	if !ok {
		return true
	}
	if identity == nil || identity.IsAdmin == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "仅管理员可管理入口合集"})
		return true
	}
	collections, ok := service.(clientEntryCollectionService)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "入口合集服务不可用，请更新后端"})
		return true
	}
	disableResponseCache(w)
	if action == "fetch" {
		data, err := collections.ListClientEntryCollections(r.Context())
		if err != nil {
			return handleAdminError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}
	if action == "save" {
		var payload admin.ClientEntryCollectionSaveRequest
		if err := readJSONBody(r, &payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "合集请求格式无效：" + err.Error()})
			return true
		}
		if payload.ID < 0 || (payload.ID > 0 && payload.Version <= 0) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "合集版本无效，请刷新后重试"})
			return true
		}
		data, err := collections.SaveClientEntryCollection(r.Context(), payload)
		if err != nil {
			return handleAdminError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}
	var payload struct {
		ID      int64                           `json:"id"`
		Version int64                           `json:"version"`
		Item    admin.ClientEntryCollectionItem `json:"item"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "合集请求格式无效：" + err.Error()})
		return true
	}
	if payload.ID <= 0 || payload.Version <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "合集或版本无效，请刷新后重试"})
		return true
	}
	var saved bool
	var err error
	if action == "remove" {
		if payload.Item.ID <= 0 || (payload.Item.Kind != "policy" && payload.Item.Kind != "split_group") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "合集成员无效"})
			return true
		}
		saved, err = collections.RemoveClientEntryCollectionMember(r.Context(), payload.ID, payload.Version, payload.Item)
	} else {
		saved, err = collections.DeleteClientEntryCollection(r.Context(), payload.ID, payload.Version)
	}
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}
