package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/session"
)

type newAPIBridgeService interface {
	GetConfig(context.Context) (map[string]any, error)
	SaveConfig(context.Context, int64, newapibridge.SettingsSaveRequest) (map[string]any, error)
	PublicInfo(context.Context) (map[string]any, error)
	Overview(context.Context, int64) (map[string]any, error)
	CheckConnection(context.Context) (newapibridge.CheckResult, error)
	ListBindings(context.Context, int, int) (newapibridge.BindingPage, error)
	ListTransfers(context.Context, newapibridge.TransferQuery) (newapibridge.TransferPage, error)
	RetryTransfer(context.Context, int64, int64) error
	CreateCheckout(context.Context, url.Values) (newapibridge.Checkout, error)
	GetCheckout(context.Context, int64, string) (newapibridge.Checkout, error)
	Pay(context.Context, int64, string) (newapibridge.Transfer, error)
	ValidateOAuth(context.Context, newapibridge.OAuthRequest) error
	Authorize(context.Context, int64, string, newapibridge.OAuthRequest) (string, error)
	ExchangeCode(context.Context, newapibridge.OAuthTokenRequest) (map[string]any, error)
	OAuthAuthentication(context.Context, string) (int64, string, error)
	UserInfo(context.Context, int64) (newapibridge.OAuthUser, error)
	Discovery(context.Context) (map[string]any, error)
	JWKS(context.Context) (map[string]any, error)
	IssuePortalTicket(context.Context, int64, string) (string, error)
	RedeemPortalTicket(context.Context, string) (int64, string, error)
	IssuePortalSession(context.Context, int64, string) (string, error)
	PortalAuthentication(context.Context, string) (int64, string, error)
	RevokePortalSession(context.Context, string) error
}

func WithNewAPIBridge(s newAPIBridgeService) Option {
	return func(state *routerState) { state.newAPI = s }
}

func newAPIHeaders(w http.ResponseWriter) {
	disableResponseCache(w)
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func newAPIError(w http.ResponseWriter, err error) {
	status, msg := http.StatusServiceUnavailable, "New API 接入暂时不可用，请稍后重试；已扣款订单请查看记录，不要重复充值"
	switch {
	case errors.Is(err, newapibridge.ErrForbidden):
		status, msg = 403, "账号授权无效或不是本人的充值订单，请重新登录"
	case errors.Is(err, newapibridge.ErrInvalid):
		status, msg = 400, "参数无效，请检查地址、金额、分页及接入配置"
	case errors.Is(err, newapibridge.ErrConflict):
		status, msg = 409, "配置或订单状态已变化，请刷新核对"
	case errors.Is(err, newapibridge.ErrDisabled):
		status, msg = 503, "管理员尚未启用 New API 接入"
	case errors.Is(err, newapibridge.ErrInsufficientBalance):
		status, msg = 400, "V2B 余额不足，请先充值本站余额"
	case errors.Is(err, newapibridge.ErrLimitExceeded):
		status, msg = 400, "金额超出单笔或滚动 24 小时划转限额"
	case errors.Is(err, newapibridge.ErrExpired):
		status, msg = 410, "付款确认已过期，请在 AI 服务重新发起充值"
	}
	writeJSON(w, status, map[string]any{"message": msg})
}

func newAPIRequireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeJSON(w, 405, map[string]string{"message": "Method not allowed"})
	return false
}

func newAPIIdentity(w http.ResponseWriter, r *http.Request, sessions session.Service, svc newAPIBridgeService, admin bool) (*session.Identity, string, bool) {
	// Authorization only: no bearer credentials in query strings or JSON bodies.
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	token = strings.TrimPrefix(token, "Bearer ")
	if token == "" || sessions == nil {
		handleSessionError(w, session.ErrUnauthorized)
		return nil, "", false
	}
	var expectedID int64
	if strings.HasPrefix(token, "nap_") {
		if admin || svc == nil {
			newAPIError(w, newapibridge.ErrForbidden)
			return nil, "", false
		}
		var err error
		expectedID, token, err = svc.PortalAuthentication(r.Context(), token)
		if err != nil {
			newAPIError(w, err)
			return nil, "", false
		}
	}
	identity, ok := authenticateSupportToken(w, r, sessions, token, admin)
	if !ok {
		return nil, "", false
	}
	if identity.ID <= 0 || (expectedID > 0 && expectedID != identity.ID) {
		newAPIError(w, newapibridge.ErrForbidden)
		return nil, "", false
	}
	return identity, token, true
}

func parseNewAPIQuery(r *http.Request) (newapibridge.TransferQuery, error) {
	q := newapibridge.TransferQuery{Current: 1, PageSize: 20}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, newapibridge.ErrInvalid
	}
	for key, v := range values {
		if len(v) != 1 {
			return q, newapibridge.ErrInvalid
		}
		switch key {
		case "current", "page_size", "user_id", "status", "trade_no":
		default:
			return q, newapibridge.ErrInvalid
		}
	}
	if raw := values.Get("current"); raw != "" {
		q.Current, err = strconv.Atoi(raw)
		if err != nil {
			return q, newapibridge.ErrInvalid
		}
	}
	if raw := values.Get("page_size"); raw != "" {
		q.PageSize, err = strconv.Atoi(raw)
		if err != nil {
			return q, newapibridge.ErrInvalid
		}
	}
	if raw := values.Get("user_id"); raw != "" {
		q.UserID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || q.UserID <= 0 {
			return q, newapibridge.ErrInvalid
		}
	}
	q.Status = values.Get("status")
	q.TradeNo = values.Get("trade_no")
	if q.Current < 1 || q.Current > 100000 || q.PageSize < 1 || q.PageSize > 100 || (q.Status != "" && q.Status != "pending" && q.Status != "succeeded") || len(q.TradeNo) > 255 {
		return q, newapibridge.ErrInvalid
	}
	return q, nil
}

func handleNewAPIAdmin(w http.ResponseWriter, r *http.Request, sessions session.Service, svc newAPIBridgeService, action string) {
	newAPIHeaders(w)
	actor, _, ok := newAPIIdentity(w, r, sessions, svc, true)
	if !ok {
		return
	}
	if svc == nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	var result any
	var err error
	switch action {
	case "config":
		if r.Method == http.MethodGet {
			result, err = svc.GetConfig(r.Context())
		} else {
			if !newAPIRequireMethod(w, r, http.MethodPost) {
				return
			}
			var req newapibridge.SettingsSaveRequest
			if !decodeSupportJSON(w, r, &req) {
				return
			}
			result, err = svc.SaveConfig(r.Context(), actor.ID, req)
		}
	case "check":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		result, err = svc.CheckConnection(r.Context())
	case "transfers", "bindings":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		q, e := parseNewAPIQuery(r)
		if e != nil {
			newAPIError(w, e)
			return
		}
		if action == "transfers" {
			result, err = svc.ListTransfers(r.Context(), q)
		} else {
			if q.UserID != 0 || q.Status != "" || q.TradeNo != "" {
				newAPIError(w, newapibridge.ErrInvalid)
				return
			}
			result, err = svc.ListBindings(r.Context(), q.Current, q.PageSize)
		}
	case "retry":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			ID int64 `json:"id"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if req.ID <= 0 {
			newAPIError(w, newapibridge.ErrInvalid)
			return
		}
		err = svc.RetryTransfer(r.Context(), req.ID, actor.ID)
		result = true
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		newAPIError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": result})
}

func handleNewAPIUser(w http.ResponseWriter, r *http.Request, sessions session.Service, svc newAPIBridgeService, action string) {
	newAPIHeaders(w)
	actor, auth, ok := newAPIIdentity(w, r, sessions, svc, false)
	if !ok {
		return
	}
	if svc == nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	var result any
	var err error
	switch action {
	case "info":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err = svc.Overview(r.Context(), actor.ID)
	case "launch":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		var target string
		target, err = svc.IssuePortalTicket(r.Context(), actor.ID, auth)
		result = map[string]any{"url": target}
	case "session":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		var token string
		token, err = svc.IssuePortalSession(r.Context(), actor.ID, auth)
		result = map[string]any{"token": token, "expires_in": 1800}
	case "logout":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		err = svc.RevokePortalSession(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		result = true
	case "authorize":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		var req newapibridge.OAuthRequest
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		var target string
		target, err = svc.Authorize(r.Context(), actor.ID, auth, req)
		result = map[string]string{"redirect_url": target}
	case "checkout":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(q) != 1 || len(q["id"]) != 1 {
			newAPIError(w, newapibridge.ErrInvalid)
			return
		}
		result, err = svc.GetCheckout(r.Context(), actor.ID, q.Get("id"))
	case "pay":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		var req struct {
			CheckoutID string `json:"checkout_id"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		result, err = svc.Pay(r.Context(), actor.ID, req.CheckoutID)
	case "transfers":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		q, e := parseNewAPIQuery(r)
		if e != nil {
			newAPIError(w, e)
			return
		}
		if q.UserID != 0 && q.UserID != actor.ID {
			newAPIError(w, newapibridge.ErrForbidden)
			return
		}
		q.UserID = actor.ID
		result, err = svc.ListTransfers(r.Context(), q)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		newAPIError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": result})
}

// A small bounded limiter supplements existing account login limits. It is not
// used as the wallet's idempotency or concurrency guarantee.
var newAPILimit = struct {
	sync.Mutex
	hits map[string]struct {
		count   int
		expires int64
	}
}{hits: make(map[string]struct {
	count   int
	expires int64
})}

func allowNewAPIPublic(r *http.Request) bool {
	key := requestIP(r) + "|" + r.URL.Path
	now := time.Now().Unix()
	newAPILimit.Lock()
	defer newAPILimit.Unlock()
	if len(newAPILimit.hits) >= 4096 {
		for k, v := range newAPILimit.hits {
			if v.expires <= now {
				delete(newAPILimit.hits, k)
			}
		}
		if len(newAPILimit.hits) >= 4096 {
			return false
		}
	}
	v := newAPILimit.hits[key]
	if v.expires <= now {
		v.count = 0
		v.expires = now + 60
	}
	v.count++
	newAPILimit.hits[key] = v
	// Token exchanges originate from the single New API backend, not from each
	// browser. Keep their budget separate and higher than interactive login.
	limit := 30
	if r.URL.Path == "/api/v1/new-api/oauth/token" {
		limit = 300
	}
	return v.count <= limit
}

func handleNewAPIPublic(w http.ResponseWriter, r *http.Request, sessions session.Service, passports passport.Service, svc newAPIBridgeService, action string) {
	newAPIHeaders(w)
	if svc == nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var result any
	var err error
	rawResult := false
	switch action {
	case "info":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err = svc.PublicInfo(ctx)
	case "oauth/discovery":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err = svc.Discovery(ctx)
		rawResult = true
	case "oauth/jwks":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err = svc.JWKS(ctx)
		rawResult = true
	case "oauth/token":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		if !allowNewAPIPublic(r) {
			writeJSON(w, 429, map[string]string{"error": "temporarily_unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil || len(r.URL.Query()) != 0 {
			writeJSON(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		for k, v := range r.PostForm {
			if len(v) != 1 {
				writeJSON(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
			switch k {
			case "client_id", "client_secret", "grant_type", "code", "redirect_uri", "code_verifier":
			default:
				writeJSON(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
		}
		id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		if basicID, basicSecret, ok := r.BasicAuth(); ok {
			if id != "" || secret != "" {
				writeJSON(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
			var e error
			id, e = url.QueryUnescape(basicID)
			if e != nil {
				writeJSON(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
			secret, e = url.QueryUnescape(basicSecret)
			if e != nil {
				writeJSON(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
		}
		result, err = svc.ExchangeCode(ctx, newapibridge.OAuthTokenRequest{ClientID: id, ClientSecret: secret, GrantType: r.PostForm.Get("grant_type"), Code: r.PostForm.Get("code"), RedirectURI: r.PostForm.Get("redirect_uri"), CodeVerifier: r.PostForm.Get("code_verifier")})
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		rawResult = true
	case "oauth/userinfo":
		if !newAPIRequireMethod(w, r, http.MethodGet) {
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var id int64
		var original string
		id, original, err = svc.OAuthAuthentication(ctx, token)
		if err != nil {
			writeJSON(w, 401, map[string]string{"error": "invalid_token"})
			return
		}
		if sessions == nil {
			newAPIError(w, newapibridge.ErrUnavailable)
			return
		}
		actor, ok := authenticateSupportToken(w, r, sessions, original, false)
		if !ok {
			return
		}
		if actor.ID != id {
			newAPIError(w, newapibridge.ErrForbidden)
			return
		}
		result, err = svc.UserInfo(ctx, id)
		rawResult = true
	case "portal/exchange":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		if !allowNewAPIPublic(r) {
			writeJSON(w, 429, map[string]string{"message": "请求过于频繁"})
			return
		}
		var req struct {
			Ticket string `json:"ticket"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		var id int64
		var original string
		id, original, err = svc.RedeemPortalTicket(ctx, req.Ticket)
		if err != nil {
			newAPIError(w, err)
			return
		}
		if sessions == nil {
			newAPIError(w, newapibridge.ErrUnavailable)
			return
		}
		actor, ok := authenticateSupportToken(w, r, sessions, original, false)
		if !ok {
			return
		}
		if actor.ID != id {
			newAPIError(w, newapibridge.ErrForbidden)
			return
		}
		var token string
		token, err = svc.IssuePortalSession(ctx, id, original)
		result = map[string]any{"token": token, "expires_in": 1800}
	case "portal/login":
		if !newAPIRequireMethod(w, r, http.MethodPost) {
			return
		}
		if !allowNewAPIPublic(r) {
			writeJSON(w, 429, map[string]string{"message": "登录请求过于频繁，请稍后再试"})
			return
		}
		if passports == nil || sessions == nil {
			newAPIError(w, newapibridge.ErrUnavailable)
			return
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decodeSupportJSON(w, r, &req) {
			return
		}
		if len(req.Email) > 254 || len(req.Password) > 1024 {
			newAPIError(w, newapibridge.ErrInvalid)
			return
		}
		info, e := svc.PublicInfo(ctx)
		if e != nil {
			newAPIError(w, e)
			return
		}
		if info["enabled"] != true {
			newAPIError(w, newapibridge.ErrDisabled)
			return
		}
		auth, e := passports.Login(ctx, passport.LoginRequest{Email: req.Email, Password: req.Password, IP: requestIP(r), UserAgent: r.UserAgent()})
		if e != nil {
			handlePassportError(w, e)
			return
		}
		actor, ok := authenticateSupportToken(w, r, sessions, auth.AuthData, false)
		if !ok {
			return
		}
		var token string
		token, err = svc.IssuePortalSession(ctx, actor.ID, auth.AuthData)
		result = map[string]any{"token": token, "expires_in": 1800}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		newAPIError(w, err)
		return
	}
	if rawResult {
		writeJSON(w, 200, result)
	} else {
		writeJSON(w, 200, map[string]any{"data": result})
	}
}

func handleNewAPISubmit(w http.ResponseWriter, r *http.Request, svc newAPIBridgeService) {
	newAPIHeaders(w)
	if r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, map[string]string{"message": "Method not allowed"})
		return
	}
	if svc == nil {
		newAPIError(w, newapibridge.ErrUnavailable)
		return
	}
	if !allowNewAPIPublic(r) {
		writeJSON(w, 429, map[string]string{"message": "请求过于频繁"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		newAPIError(w, newapibridge.ErrInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	checkout, err := svc.CreateCheckout(ctx, r.Form)
	if err != nil {
		newAPIError(w, err)
		return
	}
	http.Redirect(w, r, "/new-api/?checkout="+url.QueryEscape(checkout.ID), http.StatusSeeOther)
}
