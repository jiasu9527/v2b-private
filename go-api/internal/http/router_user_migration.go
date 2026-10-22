package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"forest/go-api/internal/session"
	"forest/go-api/internal/usermigration"
)

type userMigrationService interface {
	Inspect(context.Context, int64, usermigration.SourceConfig) (usermigration.Inspection, error)
	Preview(context.Context, int64, usermigration.PreviewRequest) (usermigration.PreviewResult, error)
	Apply(context.Context, int64, usermigration.ApplyRequest) (usermigration.Receipt, error)
	Receipt(context.Context, int64, string) (usermigration.Receipt, error)
}

func WithUserMigrationService(service *usermigration.DBService) Option {
	return func(state *routerState) {
		if service != nil {
			state.userMigration = service
		}
	}
}

func handleUserMigration(w http.ResponseWriter, r *http.Request, sessions session.Service, service userMigrationService, action string) {
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	// Migration credentials and session secrets are never accepted in a URL.
	// Authenticate before reading a potentially large or sensitive body.
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	if token == "" || len(token) > 8192 {
		handleSessionError(w, session.ErrUnauthorized)
		return
	}
	if sessions == nil {
		handleSessionError(w, session.ErrUnavailable)
		return
	}
	identity, ok := authenticateMigrationAdmin(w, r, sessions, token)
	if !ok {
		return
	}
	if identity.ID <= 0 || identity.IsAdmin != 1 {
		handleSessionError(w, session.ErrUnauthorized)
		return
	}
	method := http.MethodPost
	switch action {
	case "inspect", "preview", "apply":
	case "receipt":
		method = http.MethodGet
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "迁移接口不存在"})
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方法不支持"})
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || action != "receipt" && r.URL.RawQuery != "" || action == "receipt" && (len(query) != 1 || len(query["preview_id"]) != 1) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "迁移参数请通过 JSON 请求体提交，凭据禁止放在 URL 中"})
		return
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "用户迁移服务不可用"})
		return
	}
	// This bounded admin operation can exceed the normal API's 15s deadline.
	// Never extend all server routes, or detach writes from request cancellation.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(125 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	var result any
	var err error
	switch action {
	case "inspect":
		var req usermigration.SourceConfig
		if !decodeMigrationJSON(w, r, &req) {
			return
		}
		result, err = service.Inspect(ctx, identity.ID, req)
	case "preview":
		var req usermigration.PreviewRequest
		if !decodeMigrationJSON(w, r, &req) {
			return
		}
		result, err = service.Preview(ctx, identity.ID, req)
	case "apply":
		var req usermigration.ApplyRequest
		if !decodeMigrationJSON(w, r, &req) {
			return
		}
		if !req.Confirm || !validMigrationPreviewID(req.PreviewID) {
			writeMigrationError(w, usermigration.ErrInvalidParameter)
			return
		}
		result, err = service.Apply(ctx, identity.ID, req)
	case "receipt":
		id := query.Get("preview_id")
		if !validMigrationPreviewID(id) {
			writeMigrationError(w, usermigration.ErrInvalidParameter)
			return
		}
		result, err = service.Receipt(ctx, identity.ID, id)
	}
	if err != nil {
		writeMigrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func authenticateMigrationAdmin(w http.ResponseWriter, r *http.Request, sessions session.Service, token string) (*session.Identity, bool) {
	var identity *session.Identity
	var err error
	if fresh, ok := sessions.(freshSessionService); ok {
		identity, err = fresh.AuthenticateFresh(r.Context(), token, true)
	} else {
		identity, err = sessions.Authenticate(r.Context(), token, true)
	}
	if err != nil {
		switch {
		case errors.Is(err, session.ErrUnauthorized), errors.Is(err, session.ErrUnavailable):
			handleSessionError(w, err)
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "无法验证管理员身份，请稍后重试"})
		}
		return nil, false
	}
	if identity == nil {
		handleSessionError(w, session.ErrUnauthorized)
		return nil, false
	}
	return identity, true
}

func validMigrationPreviewID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func decodeMigrationJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"message": "请使用 application/json 提交迁移参数"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err = dec.Decode(target); err == nil {
		var extra any
		if trailing := dec.Decode(&extra); trailing != io.EOF {
			err = errors.New("trailing JSON")
		}
	}
	if err != nil {
		// Decoder errors can echo an unknown field containing a pasted password.
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "迁移参数格式无效或超过大小限制"})
		return false
	}
	return true
}

func writeMigrationError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "migration_failed", "迁移操作失败；请检查迁移结果后再操作"
	switch {
	case errors.Is(err, usermigration.ErrInvalidParameter):
		status, code, message = 400, "invalid_parameter", "迁移参数不完整或无效，请检查连接、套餐映射和确认选项"
	case errors.Is(err, usermigration.ErrUnavailable):
		status, code, message = 503, "unavailable", "用户迁移服务不可用"
	case errors.Is(err, usermigration.ErrSourceConnection):
		status, code, message = 400, "source_connection", "源 MySQL 连接失败，请检查地址、端口、凭据及网络权限"
	case errors.Is(err, usermigration.ErrSourceRead), errors.Is(err, usermigration.ErrSourceSchema):
		status, code, message = 400, "source_schema", "无法读取源 Xboard，请检查表结构及数据库只读权限"
	case errors.Is(err, usermigration.ErrSourceLimit):
		status, code, message = 400, "source_limit", "用户数超过单次迁移上限，请分批安排迁移"
	case errors.Is(err, usermigration.ErrBusy):
		status, code, message = 409, "busy", "已有迁移操作正在执行，请稍后查询结果"
	case errors.Is(err, usermigration.ErrPreviewExpired):
		status, code, message = 409, "preview_expired", "迁移预览已失效，请重新读取并预览"
	case errors.Is(err, usermigration.ErrPreviewBlocked):
		status, code, message = 409, "preview_blocked", "预览存在阻断项，请修正后重新预览"
	case errors.Is(err, usermigration.ErrPreviewStale):
		status, code, message = 409, "preview_stale", "目标用户或套餐发生变化，请重新预览"
	case errors.Is(err, usermigration.ErrReceiptNotFound):
		status, code, message = 404, "receipt_not_found", "尚未找到该任务的完成记录；请勿据此认定写入失败或立即重新导入"
	case errors.Is(err, usermigration.ErrCommitUnknown):
		status, code, message = 409, "commit_unknown", "提交结果暂时无法确认，请查询任务结果，不要重新创建导入任务"
	case errors.Is(err, usermigration.ErrTargetWrite):
		status, code, message = 500, "target_write", "迁移事务执行失败，未提交用户变更"
	case errors.Is(err, usermigration.ErrTargetRead):
		status, code, message = 503, "target_read", "无法读取目标数据库，请稍后重试查询"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code, message = 504, "timeout", "操作超时或中断；若已确认导入，请先查询任务结果"
	}
	writeJSON(w, status, map[string]any{"message": message, "code": code})
}
