package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/session"
)

type entryEvidenceCases interface {
	EntryEvidenceContext(context.Context, int64, admin.EntryEvidenceQueryRequest) (admin.EntryEvidenceContextPage, error)
	PreviewEntryEvidenceCandidates(context.Context, int64, admin.EntryEvidenceQueryRequest) (admin.EntryEvidenceCandidatePreview, error)
	CreateEntryEvidenceCase(context.Context, int64, admin.CreateEntryEvidenceCaseRequest) (admin.EntryEvidenceCase, error)
	ListEntryEvidenceCases(context.Context, int64, admin.EntryEvidenceListRequest) (admin.EntryEvidenceCasesPage, error)
	GetEntryEvidenceCase(context.Context, int64, int64) (admin.EntryEvidenceCase, error)
	ListEntryEvidenceCandidates(context.Context, int64, admin.EntryEvidenceCandidatesRequest) (admin.EntryEvidenceCandidatesPage, error)
	PreviewEntryEvidenceSplit(context.Context, int64, admin.EntryEvidenceSplitRequest) (admin.EntryEvidenceSplitPreview, error)
	ExecuteEntryEvidenceSplit(context.Context, int64, admin.EntryEvidenceSplitRequest) (admin.EntryEvidenceCase, error)
	UpdateEntryEvidenceObservation(context.Context, int64, admin.EntryEvidenceObservationRequest) (admin.EntryEvidenceCase, error)
	DeleteEntryEvidenceCase(context.Context, int64, int64) error
}

type entryEvidenceReader interface {
	List(context.Context, entryevidence.Filter) (entryevidence.Page, error)
	Status(context.Context) (entryevidence.Status, error)
	SaveSettings(context.Context, int) (entryevidence.Settings, error)
}

func handleEntryEvidence(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service, store entryEvidenceReader, action string) {
	// A nil optional *Store becomes non-nil when assigned to this interface.
	if concrete, ok := store.(*entryevidence.Store); ok && concrete == nil {
		store = nil
	}
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	method := http.MethodGet
	switch action {
	case "status", "deliveries", "candidates", "cases", "case", "context":
	case "freeze", "split-preview", "split-execute", "case-update", "case-delete", "settings":
		method = http.MethodPost
	default:
		http.NotFound(w, r)
		return
	}
	if !requireHTTPMethod(w, r, method) {
		return
	}
	actor, ok := authenticateSupportSession(w, r, sessions, false)
	if !ok {
		return
	}
	if actor.IsAdmin == 0 && actor.IsStaff == 0 || actor.IsAdmin == 0 && (action == "case-delete" || action == "settings") {
		handleEntryEvidenceError(w, admin.ErrEntryEvidenceForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var result any
	var err error
	switch action {
	case "status":
		if store == nil {
			handleEntryEvidenceError(w, admin.ErrUnavailable)
			return
		}
		var status entryevidence.Status
		status, err = store.Status(ctx)
		warnings := []string{"仅记录功能启用后的实际生成与服务端写入证据；历史订阅放行日志不等于历史下发。", "响应写入完成不能证明客户端已接收、导入或连接；TCP 失败不能单独证明封锁或恶意行为。"}
		if status.WriteFailures > 0 {
			warnings = append(warnings, "当前后端进程曾有证据写入失败，存在记录缺口；缺少记录不能排除用户获得过入口。")
		}
		result = map[string]any{"enabled": true, "keep_days": status.KeepDays, "available_from": status.FirstAt, "available_to": status.LastAt, "recorded": status.Requests, "completed": status.Completed, "partial": status.Partial, "failed": status.Failed, "write_failures": status.WriteFailures, "last_write_failure_at": status.LastWriteFailureAt, "warnings": warnings}
	case "settings":
		if store == nil {
			handleEntryEvidenceError(w, admin.ErrUnavailable)
			return
		}
		var req struct {
			KeepDays int `json:"keep_days"`
		}
		if !decodeEntryEvidenceJSON(w, r, &req) {
			return
		}
		result, err = store.SaveSettings(ctx, req.KeepDays)
	case "deliveries":
		if store == nil {
			handleEntryEvidenceError(w, admin.ErrUnavailable)
			return
		}
		var filter entryevidence.Filter
		filter, err = parseEntryDeliveryFilter(r.URL.Query())
		if err != nil {
			break
		}
		var page entryevidence.Page
		page, err = store.List(ctx, filter)
		result = map[string]any{"items": page.Items, "total": page.Total, "current": filter.Offset/filter.Limit + 1, "page_size": filter.Limit, "warnings": []string{"此处展示真实采集的节点元数据。coverage 不完整、304 或响应失败的记录不能视为本次已完整下发。"}}
	default:
		cases, ok := service.(entryEvidenceCases)
		if !ok {
			handleEntryEvidenceError(w, admin.ErrUnavailable)
			return
		}
		switch action {
		case "context":
			var req admin.EntryEvidenceQueryRequest
			req, err = parseEntryEvidenceQuery(r.URL.Query())
			if err == nil {
				result, err = cases.EntryEvidenceContext(ctx, actor.ID, req)
			}
		case "candidates":
			var caseID int64
			caseID, err = evidenceQueryInt(r.URL.Query(), "case_id", 0)
			if err != nil {
				break
			}
			if caseID > 0 {
				var current, pageSize int
				current, pageSize, err = evidenceQueryPage(r.URL.Query())
				if err != nil {
					break
				}
				req := admin.EntryEvidenceCandidatesRequest{CaseID: caseID, Current: current, PageSize: pageSize}
				if raw := r.URL.Query().Get("selected"); raw != "" {
					var selected bool
					selected, err = strconv.ParseBool(raw)
					if err != nil {
						err = admin.ErrEntryEvidenceInvalid
						break
					}
					req.Selected = &selected
				}
				result, err = cases.ListEntryEvidenceCandidates(ctx, actor.ID, req)
			} else {
				var req admin.EntryEvidenceQueryRequest
				req, err = parseEntryEvidenceQuery(r.URL.Query())
				if err == nil {
					result, err = cases.PreviewEntryEvidenceCandidates(ctx, actor.ID, req)
				}
			}
		case "cases":
			var current, pageSize int
			current, pageSize, err = evidenceQueryPage(r.URL.Query())
			if err == nil {
				result, err = cases.ListEntryEvidenceCases(ctx, actor.ID, admin.EntryEvidenceListRequest{Current: current, PageSize: pageSize})
			}
		case "case":
			var id int64
			id, err = evidenceQueryInt(r.URL.Query(), "case_id", 0)
			if err == nil && id > 0 {
				result, err = cases.GetEntryEvidenceCase(ctx, actor.ID, id)
			} else {
				err = admin.ErrEntryEvidenceInvalid
			}
		case "freeze":
			var req admin.CreateEntryEvidenceCaseRequest
			if !decodeEntryEvidenceJSON(w, r, &req) {
				return
			}
			result, err = cases.CreateEntryEvidenceCase(ctx, actor.ID, req)
		case "split-preview", "split-execute":
			var req admin.EntryEvidenceSplitRequest
			if !decodeEntryEvidenceJSON(w, r, &req) {
				return
			}
			if action == "split-preview" {
				result, err = cases.PreviewEntryEvidenceSplit(ctx, actor.ID, req)
			} else {
				result, err = cases.ExecuteEntryEvidenceSplit(ctx, actor.ID, req)
			}
		case "case-update":
			var req admin.EntryEvidenceObservationRequest
			if !decodeEntryEvidenceJSON(w, r, &req) {
				return
			}
			result, err = cases.UpdateEntryEvidenceObservation(ctx, actor.ID, req)
		case "case-delete":
			var req struct {
				CaseID int64 `json:"case_id"`
			}
			if !decodeEntryEvidenceJSON(w, r, &req) {
				return
			}
			if req.CaseID <= 0 {
				err = admin.ErrEntryEvidenceInvalid
				break
			}
			err = cases.DeleteEntryEvidenceCase(ctx, actor.ID, req.CaseID)
			result = err == nil
		}
	}
	if err != nil {
		handleEntryEvidenceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func evidenceQueryInt(q url.Values, key string, fallback int64) (int64, error) {
	values, exists := q[key]
	if !exists {
		return fallback, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, admin.ErrEntryEvidenceInvalid
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < 0 {
		return 0, admin.ErrEntryEvidenceInvalid
	}
	return value, nil
}

func decodeEntryEvidenceJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeJSON(w, 415, map[string]any{"message": "Content-Type must be application/json"})
		return false
	}
	// Up to 10,000 selected IDs can exceed the generic 32 KiB support limit.
	if err := decodeDNSProbeJSON(w, r, 256<<10, target); err != nil {
		writeJSON(w, 400, map[string]any{"message": "请求格式无效，仅接受声明字段的 JSON 对象（最大 256 KiB）"})
		return false
	}
	return true
}

func evidenceQueryPage(q url.Values) (int, int, error) {
	current, err := evidenceQueryInt(q, "current", 1)
	if err != nil {
		return 0, 0, err
	}
	pageSize, err := evidenceQueryInt(q, "page_size", 20)
	if err != nil || current < 1 || current > 10000 || pageSize < 1 || pageSize > 100 {
		return 0, 0, admin.ErrEntryEvidenceInvalid
	}
	return int(current), int(pageSize), nil
}

func parseEntryEvidenceQuery(q url.Values) (admin.EntryEvidenceQueryRequest, error) {
	out := admin.EntryEvidenceQueryRequest{Kind: q.Get("kind"), Host: strings.TrimSpace(q.Get("host")), UA: strings.TrimSpace(q.Get("ua")), Account: strings.TrimSpace(q.Get("account"))}
	for name, dst := range map[string]*int64{"target_id": &out.TargetID, "port": &out.Port, "from": &out.From, "to": &out.To} {
		value, err := evidenceQueryInt(q, name, 0)
		if err != nil {
			return out, err
		}
		*dst = value
	}
	var err error
	out.Current, out.PageSize, err = evidenceQueryPage(q)
	return out, err
}

func parseEntryDeliveryFilter(q url.Values) (entryevidence.Filter, error) {
	out := entryevidence.Filter{Host: strings.TrimSpace(q.Get("host")), Port: strings.TrimSpace(q.Get("port")), UA: strings.TrimSpace(q.Get("ua")), Email: strings.TrimSpace(q.Get("account"))}
	for name, dst := range map[string]*int64{"from": &out.From, "to": &out.To, "policy_id": &out.PolicyID, "group_id": &out.GroupID, "user_id": &out.UserID} {
		value, err := evidenceQueryInt(q, name, 0)
		if err != nil {
			return out, err
		}
		*dst = value
	}
	if kind := q.Get("kind"); kind != "" {
		id, err := evidenceQueryInt(q, "target_id", 0)
		if err != nil || id == 0 {
			return out, admin.ErrEntryEvidenceInvalid
		}
		switch kind {
		case "policy":
			out.PolicyID = id
		case "split_group":
			out.GroupID = id
		default:
			return out, admin.ErrEntryEvidenceInvalid
		}
	}
	if out.Email != "" && isDecimalDigits(out.Email) {
		id, err := strconv.ParseInt(out.Email, 10, 64)
		if err != nil || id <= 0 || out.UserID > 0 && id != out.UserID {
			return out, admin.ErrEntryEvidenceInvalid
		}
		out.UserID, out.Email = id, ""
	}
	if raw := q.Get("complete_only"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return out, admin.ErrEntryEvidenceInvalid
		}
		out.CompleteOnly = value
	}
	current, size, err := evidenceQueryPage(q)
	if err != nil {
		return out, err
	}
	out.Limit, out.Offset = size, (current-1)*size
	if out.Offset > 10000 {
		return out, admin.ErrEntryEvidenceInvalid
	}
	return out, nil
}

func handleEntryEvidenceError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "入口证据操作失败，请核对当前档案状态后重试查询"
	switch {
	case errors.Is(err, admin.ErrEntryEvidenceForbidden):
		status, message = http.StatusForbidden, "仅管理员或员工可访问；删除证据与保留设置仅管理员可操作"
	case errors.Is(err, admin.ErrEntryEvidenceConflict), errors.Is(err, admin.ErrAIEntryRuleConflict):
		status, message = http.StatusConflict, "规则、候选名单或档案已变化，请重新查询预览，不要重复执行旧写入"
	case errors.Is(err, admin.ErrClientEntryBackupIPInsufficient):
		status, message = http.StatusConflict, "备用 IP 池缺少当前端口可用的健康入口，请补充检测合格的同端口 IP，或手动指定两个不同入口"
	case errors.Is(err, admin.ErrEntryEvidenceInvalid), errors.Is(err, entryevidence.ErrValidation), errors.Is(err, admin.ErrAIEntryRuleInvalid):
		status, message = http.StatusBadRequest, "排查条件或操作参数无效，请检查规则、入口、时间范围、分页和版本"
	case errors.Is(err, admin.ErrEntryEvidenceNotFound), errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "排查档案或目标不存在"
	case errors.Is(err, admin.ErrUnavailable):
		status, message = http.StatusServiceUnavailable, "入口证据服务不可用，请更新并重启后端"
	}
	writeJSON(w, status, map[string]any{"message": message})
}
