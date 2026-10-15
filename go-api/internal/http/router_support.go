package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

type supportService interface {
	IsSupportStaff(context.Context, int64) (bool, error)
	ConsumeSupportLimit(context.Context, int64, string) error
	SupportEntryOverview(context.Context) (admin.SupportEntryOverview, error)
	LookupSupportUser(context.Context, int64, string, string) (admin.SupportUserResult, error)
	ChangeSupportEntry(context.Context, int64, admin.SupportEntryChangeRequest) (admin.SupportUserResult, error)
	GetSupportEntitlementOptions(context.Context, int64) ([]admin.SupportEntitlementPlan, error)
	ChangeSupportEntitlement(context.Context, int64, admin.SupportEntitlementChangeRequest) (admin.SupportEntitlementSnapshot, error)
	ListSupportOperations(context.Context, int64, int64) (admin.SupportOperationResult, error)
}

func WithSupportService(service supportService) Option {
	return func(state *routerState) { state.support = service }
}

type freshSessionService interface {
	AuthenticateFresh(context.Context, string, bool) (*session.Identity, error)
}

func authenticateSupportSession(w http.ResponseWriter, r *http.Request, sessions session.Service, requireAdmin bool) (*session.Identity, bool) {
	if sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "session service unavailable"})
		return nil, false
	}
	// Never parse a write payload as auth input before strict decoding.
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("auth_data"))
	}
	if token == "" {
		handleSessionError(w, session.ErrUnauthorized)
		return nil, false
	}
	return authenticateSupportToken(w, r, sessions, token, requireAdmin)
}

func authenticateSupportToken(w http.ResponseWriter, r *http.Request, sessions session.Service, token string, requireAdmin bool) (*session.Identity, bool) {
	var identity *session.Identity
	var err error
	if fresh, ok := sessions.(freshSessionService); ok {
		identity, err = fresh.AuthenticateFresh(r.Context(), token, requireAdmin)
	} else {
		identity, err = sessions.Authenticate(r.Context(), token, requireAdmin)
	}
	if err != nil {
		handleSessionError(w, err)
		return nil, false
	}
	if identity == nil || (requireAdmin && identity.IsAdmin == 0) {
		handleSessionError(w, session.ErrUnauthorized)
		return nil, false
	}
	return identity, true
}

func handleSupport(w http.ResponseWriter, r *http.Request, sessions session.Service, service supportService, action string, adminOnly bool) bool {
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	identity, ok := authenticateSupportSession(w, r, sessions, adminOnly)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, 503, map[string]any{"message": "support service unavailable"})
		return true
	}
	if !adminOnly {
		if identity.IsStaff == 0 || identity.IsAdmin != 0 {
			return handleSupportError(w, admin.ErrSupportForbidden)
		}
		allowed, err := service.IsSupportStaff(r.Context(), identity.ID)
		if err != nil {
			return handleSupportError(w, err)
		}
		if !allowed {
			return handleSupportError(w, admin.ErrSupportForbidden)
		}
	}
	if (adminOnly && action != "operations") || (!adminOnly && action != "overview" && action != "user" && action != "entry" && action != "entitlement/plans" && action != "entitlement" && action != "operations") {
		writeJSON(w, 404, map[string]any{"message": "Not found"})
		return true
	}
	expected := http.MethodGet
	if action == "entry" || action == "entitlement" {
		expected = http.MethodPost
	}
	if r.Method != expected {
		w.Header().Set("Allow", expected)
		writeJSON(w, 405, map[string]any{"message": "Method not allowed"})
		return true
	}
	var result any
	var err error
	switch {
	case !adminOnly && action == "overview":
		result, err = service.SupportEntryOverview(r.Context())
	case !adminOnly && action == "user":
		if err = service.ConsumeSupportLimit(r.Context(), identity.ID, "lookup"); err == nil {
			result, err = service.LookupSupportUser(r.Context(), identity.ID, r.URL.Query().Get("query"), r.URL.Query().Get("ua"))
		}
	case !adminOnly && action == "entry":
		var req admin.SupportEntryChangeRequest
		if !decodeSupportJSON(w, r, &req) {
			return true
		}
		if err = service.ConsumeSupportLimit(r.Context(), identity.ID, "change"); err == nil {
			result, err = service.ChangeSupportEntry(r.Context(), identity.ID, req)
		}
	case !adminOnly && action == "entitlement/plans":
		result, err = service.GetSupportEntitlementOptions(r.Context(), identity.ID)
	case !adminOnly && action == "entitlement":
		var req admin.SupportEntitlementChangeRequest
		if !decodeSupportJSON(w, r, &req) {
			return true
		}
		if err = service.ConsumeSupportLimit(r.Context(), identity.ID, "change"); err == nil {
			result, err = service.ChangeSupportEntitlement(r.Context(), identity.ID, req)
		}
	case action == "operations":
		page := int64(1)
		if value := r.URL.Query().Get("page"); value != "" {
			page, err = strconv.ParseInt(value, 10, 64)
			if err != nil || page <= 0 {
				return handleSupportError(w, admin.ErrSupportEntryInvalid)
			}
		}
		actor := identity.ID
		if adminOnly {
			actor = 0
		}
		result, err = service.ListSupportOperations(r.Context(), actor, page)
	default:
		writeJSON(w, 404, map[string]any{"message": "Not found"})
		return true
	}
	if err != nil {
		return handleSupportError(w, err)
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return true
}

func decodeSupportJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeJSON(w, 415, map[string]any{"message": "Content-Type must be application/json"})
		return false
	}
	if err := decodeDNSProbeJSON(w, r, 32<<10, target); err != nil {
		writeJSON(w, 400, map[string]any{"message": "请求格式无效，仅接受声明字段的 JSON 对象"})
		return false
	}
	return true
}

func handleSupportError(w http.ResponseWriter, err error) bool {
	status := http.StatusInternalServerError
	message := "员工服务暂不可用，请稍后重试"
	switch {
	case errors.Is(err, admin.ErrSupportForbidden):
		status, message = 403, err.Error()
	case errors.Is(err, admin.ErrSupportRateLimited):
		status, message = 429, err.Error()
		w.Header().Set("Retry-After", "60")
	case errors.Is(err, admin.ErrSupportEntryConflict):
		status, message = 409, err.Error()
	case errors.Is(err, admin.ErrSupportEntryNotFound):
		status, message = 404, err.Error()
	case errors.Is(err, admin.ErrSupportEntryInvalid):
		status, message = 400, err.Error()
	case errors.Is(err, admin.ErrUnavailable):
		status = 503
	}
	writeJSON(w, status, map[string]any{"message": message})
	return true
}
