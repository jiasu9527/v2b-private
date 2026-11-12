package httpapi

import (
	"context"
	"net/http"
	"net/url"

	"forest/go-api/internal/admin"
)

// Separate from entryEvidenceCases so older optional services remain compatible.
// The caller authenticates the current staff/admin session before dispatching.
type entryInvestigations interface {
	StartEntryInvestigation(context.Context, int64, admin.EntryInvestigationStartRequest) (admin.EntryInvestigation, error)
	GetEntryInvestigation(context.Context, int64, int64) (admin.EntryInvestigation, error)
	ListEntryInvestigations(context.Context, int64, admin.EntryEvidenceListRequest) (admin.EntryInvestigationsPage, error)
	RefreshEntryInvestigation(context.Context, int64, int64) (admin.EntryInvestigation, error)
	ConfirmEntryInvestigationFailure(context.Context, int64, admin.EntryInvestigationConfirmRequest) (admin.EntryInvestigation, error)
	NextEntryInvestigationRound(context.Context, int64, admin.EntryInvestigationNextRequest) (admin.EntryInvestigation, error)
}

func handleEntryInvestigation(w http.ResponseWriter, r *http.Request, service admin.Service, actor int64, action string) bool {
	switch action {
	case "investigation", "investigation-list", "investigation-start", "investigation-refresh", "investigation-confirm", "investigation-next":
	default:
		return false
	}
	s, ok := service.(entryInvestigations)
	if !ok {
		handleEntryEvidenceError(w, admin.ErrUnavailable)
		return true
	}
	var result any
	var err error
	switch action {
	case "investigation":
		if !validInvestigationQuery(r.URL.Query(), "case_id") {
			err = admin.ErrEntryEvidenceInvalid
			break
		}
		var id int64
		id, err = evidenceQueryInt(r.URL.Query(), "case_id", 0)
		if err == nil && id > 0 {
			result, err = s.GetEntryInvestigation(r.Context(), actor, id)
		} else {
			err = admin.ErrEntryEvidenceInvalid
		}
	case "investigation-list":
		if !validInvestigationQuery(r.URL.Query(), "current", "page_size") {
			err = admin.ErrEntryEvidenceInvalid
			break
		}
		var current, size int
		current, size, err = evidenceQueryPage(r.URL.Query())
		if err == nil {
			result, err = s.ListEntryInvestigations(r.Context(), actor, admin.EntryEvidenceListRequest{Current: current, PageSize: size})
		}
	case "investigation-start":
		var req admin.EntryInvestigationStartRequest
		if !decodeEntryEvidenceJSON(w, r, &req) {
			return true
		}
		result, err = s.StartEntryInvestigation(r.Context(), actor, req)
	case "investigation-refresh":
		var req struct {
			CaseID int64 `json:"case_id"`
		}
		if !decodeEntryEvidenceJSON(w, r, &req) {
			return true
		}
		if req.CaseID <= 0 {
			err = admin.ErrEntryEvidenceInvalid
			break
		}
		result, err = s.RefreshEntryInvestigation(r.Context(), actor, req.CaseID)
	case "investigation-confirm":
		var req admin.EntryInvestigationConfirmRequest
		if !decodeEntryEvidenceJSON(w, r, &req) {
			return true
		}
		result, err = s.ConfirmEntryInvestigationFailure(r.Context(), actor, req)
	case "investigation-next":
		var req admin.EntryInvestigationNextRequest
		if !decodeEntryEvidenceJSON(w, r, &req) {
			return true
		}
		result, err = s.NextEntryInvestigationRound(r.Context(), actor, req)
	}
	if err != nil {
		handleEntryEvidenceError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	}
	return true
}

func validInvestigationQuery(query url.Values, fields ...string) bool {
	allowed := map[string]bool{"auth_data": true}
	for _, field := range fields {
		allowed[field] = true
	}
	for field, values := range query {
		if !allowed[field] || len(values) != 1 {
			return false
		}
	}
	return true
}
