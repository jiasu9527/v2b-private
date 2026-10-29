package httpapi

import (
	"context"
	"mime"
	"net/http"
	"strconv"

	"forest/go-api/internal/aiops"
)

type aiOpsKnowledgeService interface {
	ListKnowledge(context.Context, int64, aiops.KnowledgeListRequest) (aiops.KnowledgeListResult, error)
	GetKnowledge(context.Context, int64, int64) (aiops.KnowledgeEntry, error)
	SaveKnowledge(context.Context, int64, aiops.KnowledgeSaveRequest) (aiops.KnowledgeEntry, error)
	DeleteKnowledge(context.Context, int64, int64, int64) error
}

func handleAIOpsKnowledge(w http.ResponseWriter, r *http.Request, base aiOpsService, actor int64, isAdmin bool, action string) {
	service, ok := base.(aiOpsKnowledgeService)
	if !ok {
		handleAIOpsError(w, aiops.ErrUnavailable)
		return
	}
	var result any
	var err error
	switch action {
	case "knowledge":
		query := r.URL.Query()
		request := aiops.KnowledgeListRequest{Query: query.Get("query"), Status: query.Get("status"), Category: query.Get("category")}
		for key, dst := range map[string]*int{"current": &request.Current, "page_size": &request.PageSize} {
			if raw := query.Get(key); raw != "" {
				value, e := strconv.Atoi(raw)
				if e != nil {
					handleAIOpsError(w, aiops.ErrValidation)
					return
				}
				*dst = value
			}
		}
		result, err = service.ListKnowledge(r.Context(), actor, request)
	case "knowledge/detail":
		id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if e != nil || id <= 0 {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		result, err = service.GetKnowledge(r.Context(), actor, id)
	case "knowledge/save":
		var request aiops.KnowledgeSaveRequest
		if !decodeAIOpsKnowledgeJSON(w, r, &request) {
			return
		}
		if !isAdmin && request.ID != 0 {
			handleAIOpsError(w, aiops.ErrForbidden)
			return
		}
		result, err = service.SaveKnowledge(r.Context(), actor, request)
	case "knowledge/delete":
		var request struct {
			ID              int64 `json:"id"`
			ExpectedVersion int64 `json:"expected_version"`
			Confirm         bool  `json:"confirm"`
		}
		if !decodeSupportJSON(w, r, &request) {
			return
		}
		if !isAdmin {
			handleAIOpsError(w, aiops.ErrForbidden)
			return
		}
		if !request.Confirm || request.ID <= 0 || request.ExpectedVersion <= 0 {
			handleAIOpsError(w, aiops.ErrValidation)
			return
		}
		err = service.DeleteKnowledge(r.Context(), actor, request.ID, request.ExpectedVersion)
		result = true
	default:
		writeJSON(w, 404, map[string]string{"message": "Not found"})
		return
	}
	if err != nil {
		handleAIOpsError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": result})
}

func decodeAIOpsKnowledgeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeJSON(w, 415, map[string]string{"message": "Content-Type must be application/json"})
		return false
	}
	// 12,000 Unicode characters may exceed the generic 32 KiB support body.
	if err := decodeDNSProbeJSON(w, r, 128<<10, target); err != nil {
		writeJSON(w, 400, map[string]string{"message": "请求格式无效，仅接受声明字段的 JSON 对象"})
		return false
	}
	return true
}
