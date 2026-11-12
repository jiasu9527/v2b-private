package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

type clientEntryRetirementService interface {
	PreviewClientEntryRetirement(context.Context, int64, admin.ClientEntryRetirementPreviewRequest) (admin.ClientEntryRetirementPreview, error)
	ExecuteClientEntryRetirement(context.Context, int64, admin.ClientEntryRetirementExecuteRequest) (admin.ClientEntryRetirementReceipt, error)
	GetClientEntryRetirement(context.Context, int64, int64) (admin.ClientEntryRetirementReceipt, error)
}

type clientEntryGroupAuditService interface {
	AuditClientEntryGroups(context.Context, admin.ClientEntryGroupAuditRequest) (admin.ClientEntryGroupAuditResult, error)
}

func handleAdminClientEntryRetirement(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service, action string) {
	method := http.MethodPost
	switch action {
	case "preview", "execute":
	case "receipt", "audit":
		method = http.MethodGet
	default:
		http.NotFound(w, r)
		return
	}
	if !requireHTTPMethod(w, r, method) {
		return
	}
	actor, ok := authenticateRequest(w, r, sessions, true)
	if !ok {
		return
	}
	if actor == nil || actor.IsAdmin == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "仅管理员可审计和回收入口组"})
		return
	}
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if action == "audit" {
		backend, available := service.(clientEntryGroupAuditService)
		if !available {
			writeClientEntryRetirementError(w, r, admin.ErrUnavailable)
			return
		}
		req, err := parseClientEntryGroupAudit(r.URL.Query())
		if err != nil {
			writeClientEntryRetirementError(w, r, err)
			return
		}
		data, err := backend.AuditClientEntryGroups(ctx, req)
		if err != nil {
			writeClientEntryRetirementError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return
	}
	backend, available := service.(clientEntryRetirementService)
	if !available {
		writeClientEntryRetirementError(w, r, admin.ErrUnavailable)
		return
	}
	var data any
	var err error
	switch action {
	case "preview":
		var req admin.ClientEntryRetirementPreviewRequest
		if !decodeEntryEvidenceJSON(w, r, &req) {
			capturePanelOperationPreflight(ctx, "entry_retirement_invalid_arguments", false)
			return
		}
		data, err = backend.PreviewClientEntryRetirement(ctx, actor.ID, req)
	case "execute":
		var req admin.ClientEntryRetirementExecuteRequest
		if !decodeEntryEvidenceJSON(w, r, &req) {
			capturePanelOperationPreflight(ctx, "entry_retirement_invalid_arguments", false)
			return
		}
		data, err = backend.ExecuteClientEntryRetirement(ctx, actor.ID, req)
	case "receipt":
		id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if e != nil || id <= 0 || len(r.URL.Query()["id"]) != 1 {
			writeClientEntryRetirementError(w, r, admin.ErrClientEntryRetirementInvalid)
			return
		}
		data, err = backend.GetClientEntryRetirement(ctx, actor.ID, id)
	}
	if err != nil {
		writeClientEntryRetirementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func writeClientEntryRetirementError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusServiceUnavailable, "", "入口组操作结果暂无法确认，请查询回收回执与当前配置核对"
	switch {
	case errors.Is(err, admin.ErrClientEntryRetirementInvalid):
		status, code, message = http.StatusBadRequest, "entry_retirement_invalid_arguments", err.Error()
	case errors.Is(err, admin.ErrClientEntryRetirementConflict):
		status, code, message = http.StatusConflict, "entry_retirement_conflict", err.Error()
	case errors.Is(err, admin.ErrClientEntryRetirementBlocked):
		status, code, message = http.StatusConflict, "entry_retirement_blocked", err.Error()
	case errors.Is(err, admin.ErrClientEntryRetirementNotFound):
		status, code, message = http.StatusNotFound, "entry_retirement_not_found", err.Error()
	case errors.Is(err, admin.ErrClientEntryGroupAuditInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, admin.ErrClientEntryGroupAuditConflict):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, admin.ErrEntryEvidenceForbidden):
		status, message = http.StatusForbidden, "管理员身份或回收预览归属校验未通过"
	case errors.Is(err, admin.ErrUnavailable):
		message = "入口组审计与回收服务不可用，请更新后端"
	}
	// Only backend-owned typed pre-write errors prove non-execution. Never
	// interpret SQL errors, timeouts or arbitrary error text as safe to retry.
	if code != "" {
		capturePanelOperationPreflight(r.Context(), code, false)
	}
	writeJSON(w, status, map[string]any{"message": message, "code": code})
}

func parseClientEntryGroupAudit(q url.Values) (admin.ClientEntryGroupAuditRequest, error) {
	out := admin.ClientEntryGroupAuditRequest{Current: 1, PageSize: 20}
	for key, values := range q {
		if key == "auth_data" || key == "group_ids" || key == "group_ids[]" || key == "replacement_group_ids" || key == "replacement_group_ids[]" || strings.HasPrefix(key, "group_ids[") || strings.HasPrefix(key, "replacement_group_ids[") {
			continue
		}
		if len(values) != 1 {
			return out, admin.ErrClientEntryGroupAuditInvalid
		}
		switch key {
		case "expected_revision":
			out.ExpectedRevision = values[0]
		case "from", "to", "current", "page_size":
			n, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || n <= 0 {
				return out, admin.ErrClientEntryGroupAuditInvalid
			}
			switch key {
			case "from":
				out.From = n
			case "to":
				out.To = n
			case "current":
				if n > 10000 {
					return out, admin.ErrClientEntryGroupAuditInvalid
				}
				out.Current = int(n)
			case "page_size":
				if n > 50 {
					return out, admin.ErrClientEntryGroupAuditInvalid
				}
				out.PageSize = int(n)
			}
		default:
			return out, admin.ErrClientEntryGroupAuditInvalid
		}
	}
	var err error
	out.GroupIDs, err = parseClientEntryGroupIDs(q, "group_ids")
	if err != nil {
		return out, err
	}
	out.ReplacementGroupIDs, err = parseClientEntryGroupIDs(q, "replacement_group_ids")
	return out, err
}

// The panel adapter encodes arrays as ids[0], ids[1]. Support repeated ids[]
// for browser clients too, but reject mixed, sparse or malformed arrays.
func parseClientEntryGroupIDs(q url.Values, name string) ([]int64, error) {
	var raw []string
	format := ""
	indexed := map[int]string{}
	for key, values := range q {
		if key != name && !strings.HasPrefix(key, name+"[") {
			continue
		}
		kind := "indexed"
		if key == name || key == name+"[]" {
			kind = key
		}
		if format != "" && format != kind {
			return nil, admin.ErrClientEntryGroupAuditInvalid
		}
		format = kind
		if kind != "indexed" {
			raw = append(raw, values...)
			continue
		}
		if !strings.HasSuffix(key, "]") || len(values) != 1 {
			return nil, admin.ErrClientEntryGroupAuditInvalid
		}
		i, err := strconv.Atoi(key[len(name)+1 : len(key)-1])
		if err != nil || i < 0 || i >= 100 || key != name+"["+strconv.Itoa(i)+"]" {
			return nil, admin.ErrClientEntryGroupAuditInvalid
		}
		indexed[i] = values[0]
	}
	if format == "indexed" {
		for i := 0; i < len(indexed); i++ {
			value, ok := indexed[i]
			if !ok {
				return nil, admin.ErrClientEntryGroupAuditInvalid
			}
			raw = append(raw, value)
		}
	}
	if len(raw) > 100 {
		return nil, admin.ErrClientEntryGroupAuditInvalid
	}
	ids := make([]int64, 0, len(raw))
	seen := map[int64]bool{}
	for _, value := range raw {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			return nil, admin.ErrClientEntryGroupAuditInvalid
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}
