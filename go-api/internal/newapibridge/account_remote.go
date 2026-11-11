package newapibridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrWorkspaceNotConnected   = errors.New("请先连接本人的 AI 账号")
	ErrWorkspaceVerification   = errors.New("AI 账号需要完成本人安全验证")
	ErrWorkspaceUnknown        = errors.New("远端操作结果尚未确认，未自动重试；请查看记录后核对")
	ErrWorkspaceAmount         = errors.New("当前充值费率无法生成该金额，请选择更高金额或在 AI 站查看充值档位")
	ErrWorkspaceQuota          = errors.New("AI 余额不足，请先转入余额")
	ErrWorkspaceRateLimit      = errors.New("AI 请求过于频繁，请稍后重试")
	ErrWorkspaceContext        = errors.New("模型无法接受当前上下文或请求参数，请缩短对话或调整模型")
	ErrWorkspaceModel          = errors.New("当前模型暂时无可用渠道，请选择其他模型")
	ErrWorkspaceCapacity       = errors.New("该模型上游暂无可用服务资源，请稍后重试或手动切换其他模型")
	ErrWorkspaceInvalidRequest = errors.New("上游拒绝当前请求参数，请检查模型和参数后再试")
	ErrWorkspaceResponse       = errors.New("模型上游返回了无效响应，请核对已保存消息与用量；同一请求不会自动重发")
)

type workspaceSession struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	SessionID string `json:"session_id"`
	ExpiresAt int64  `json:"expires_at"`
	RemoteID  int64  `json:"remote_id"`
	Subject   string `json:"subject"`
}
type workspaceUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	OIDCID      string `json:"oidc_id"`
	Status      int    `json:"status"`
	Quota       int64  `json:"quota"`
	UsedQuota   int64  `json:"used_quota"`
	HasPassword bool   `json:"has_password"`
}
type workspaceEnvelope struct {
	Success *bool           `json:"success"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`
	URL     string          `json:"url"`
}

// All dashboard calls use the end user's session. AdminToken is deliberately
// absent from this transport. A missing response is never automatically retried.
func (s *Service) workspaceRequest(ctx context.Context, cfg Settings, cred *workspaceSession, method, path string, body any, proof string) (workspaceEnvelope, []*http.Cookie, error) {
	var out workspaceEnvelope
	u, e := url.Parse(cfg.APIURL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return out, nil, ErrInvalid
	}
	target := strings.TrimRight(cfg.APIURL, "/") + path
	var raw []byte
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil || len(raw) > 2<<20 {
			return out, nil, ErrInvalid
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(raw))
	if e != nil {
		return out, nil, ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", cfg.APIURL)
	req.Header.Set("User-Agent", "Forest-AI-Workspace/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cred != nil {
		req.Header.Set("Authorization", "Bearer "+cred.Access)
		req.Header.Set("X-Auth-Session", cred.SessionID)
		if strings.HasPrefix(path, "/api/user/auth/refresh") && cred.Refresh != "" {
			req.AddCookie(&http.Cookie{Name: "new_api_refresh", Value: cred.Refresh})
		}
	}
	if proof != "" {
		req.Header.Set("X-Security-Proof", proof)
	}
	client := http.Client{Timeout: 20 * time.Second}
	if s.client != nil {
		client = *s.client
		client.Timeout = 20 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return out, nil, ctx.Err()
		}
		return out, nil, ErrRemoteUnavailable
	}
	defer res.Body.Close()
	raw, e = io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil || len(raw) > 2<<20 {
		return out, nil, ErrRemoteUnavailable
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, nil, workspaceRemoteError(res.StatusCode, raw)
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, nil, ErrRemoteUnavailable
	}
	if out.Success != nil && !*out.Success {
		return out, nil, workspaceRemoteError(res.StatusCode, raw)
	}
	if out.Success == nil && out.Message != "success" {
		return out, nil, ErrRemoteUnavailable
	}
	return out, res.Cookies(), nil
}

func workspaceDecode(data json.RawMessage, target any) error {
	if len(data) == 0 || json.Unmarshal(data, target) != nil {
		return ErrRemoteUnavailable
	}
	return nil
}
func (s *Service) workspaceSelf(ctx context.Context, cfg Settings, cred workspaceSession, userID int64) (workspaceUser, error) {
	env, _, e := s.workspaceRequest(ctx, cfg, &cred, http.MethodGet, "/api/user/self", nil, "")
	var u workspaceUser
	if e != nil {
		return u, e
	}
	if workspaceDecode(env.Data, &u) != nil || u.ID <= 0 || u.Status != 1 || u.ID != cred.RemoteID || u.OIDCID != "forest:"+strconv.FormatInt(userID, 10) || u.UsedQuota < 0 {
		return u, ErrForbidden
	}
	return u, nil
}

func workspaceSessionFrom(env workspaceEnvelope, cookies []*http.Cookie, userID int64, old *workspaceSession) (workspaceSession, error) {
	var v struct {
		Access  string `json:"access_token"`
		Expires int64  `json:"access_expires_at"`
		Session struct {
			ID string `json:"sid"`
		} `json:"session"`
		User workspaceUser `json:"user"`
	}
	if workspaceDecode(env.Data, &v) != nil || v.Access == "" || len(v.Access) > 16384 || v.Expires <= time.Now().Unix() || v.Session.ID == "" || v.User.ID <= 0 || v.User.Status != 1 || v.User.OIDCID != "forest:"+strconv.FormatInt(userID, 10) {
		return workspaceSession{}, ErrForbidden
	}
	c := workspaceSession{Access: v.Access, ExpiresAt: v.Expires, SessionID: v.Session.ID, RemoteID: v.User.ID, Subject: v.User.OIDCID}
	if old != nil {
		if old.RemoteID != c.RemoteID || old.Subject != c.Subject {
			return c, ErrForbidden
		}
		c.Refresh = old.Refresh
	}
	for _, cookie := range cookies {
		if cookie.Name == "new_api_refresh" && cookie.Value != "" {
			c.Refresh = cookie.Value
		}
	}
	if c.Refresh == "" || len(c.Refresh) > 8192 {
		return c, ErrRemoteUnavailable
	}
	return c, nil
}

// OpenWorkspaceChat returns the upstream's original SSE; a partial generation
// is never replayed. The caller owns cancellation and closing the body.
func (s *Service) OpenWorkspaceChat(ctx context.Context, userID int64, auth string, payload map[string]any) (io.ReadCloser, error) {
	cfg, cred, e := s.workspaceCredentials(ctx, userID, auth)
	if e != nil {
		return nil, e
	}
	if _, e = s.workspaceSelf(ctx, cfg, cred, userID); e != nil {
		return nil, e
	}
	models, _, e := s.workspaceRequest(ctx, cfg, &cred, http.MethodGet, "/api/user/models", nil, "")
	if e != nil {
		return nil, e
	}
	var available []string
	if workspaceDecode(models.Data, &available) != nil {
		return nil, ErrRemoteUnavailable
	}
	requested, _ := payload["model"].(string)
	allowed := false
	for _, model := range available {
		if model == requested {
			allowed = true
		}
	}
	if !allowed {
		return nil, ErrWorkspaceModel
	}
	if e = s.workspaceWalletOnly(ctx, cfg, cred, false); e != nil {
		return nil, e
	}
	if stream, ok := payload["stream"].(bool); !ok || !stream {
		return nil, ErrInvalid
	}
	if model, ok := payload["model"].(string); !ok || model == "" || len(model) > 256 {
		return nil, ErrInvalid
	}
	raw, e := json.Marshal(payload)
	if e != nil || len(raw) > 2<<20 {
		return nil, ErrInvalid
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL+"/pg/chat/completions", bytes.NewReader(raw))
	if e != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+cred.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Origin", cfg.APIURL)
	client := http.Client{}
	if s.client != nil {
		client = *s.client
	}
	client.Timeout = 0
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrRemoteUnavailable
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 65536))
		res.Body.Close()
		if readErr != nil {
			return nil, ErrRemoteUnavailable
		}
		err := workspaceRemoteError(res.StatusCode, raw)
		if res.StatusCode == http.StatusOK && errors.Is(err, ErrRemoteUnavailable) {
			return nil, ErrWorkspaceResponse
		}
		return nil, err
	}
	return &workspaceBoundedBody{ReadCloser: res.Body, remaining: 32 << 20}, nil
}

type workspaceBoundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *workspaceBoundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, ErrRemoteUnavailable
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, e := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, e
}

// Only fixed classifications cross the bridge; upstream messages may contain
// provider endpoints, request contents or credentials and are never forwarded.
func workspaceRemoteError(status int, raw []byte) error {
	if status == 401 {
		return ErrWorkspaceNotConnected
	}
	if status == 402 {
		return ErrWorkspaceQuota
	}
	if status == 429 {
		return ErrWorkspaceRateLimit
	}
	var v struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Code string `json:"code"`
		} `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &v)
	code := strings.ToLower(v.Code + " " + v.Data.Code + " " + v.Error.Code + " " + v.Error.Type)
	msg := strings.ToLower(v.Message + " " + v.Error.Message)
	// SECURITY_CONTEXT_INVALID refers to a security proof/session context,
	// not the model's token window. Match structured verification codes first.
	if strings.Contains(code, "security_") || strings.Contains(code, "verification") || strings.Contains(code, "password_") || strings.Contains(code, "auth_flow") {
		return ErrWorkspaceVerification
	}
	if strings.Contains(code, "quota") || strings.Contains(msg, "insufficient quota") || strings.Contains(msg, "余额不足") || strings.Contains(msg, "额度不足") {
		return ErrWorkspaceQuota
	}
	for _, structuredCode := range []string{v.Code, v.Data.Code, v.Error.Code, v.Error.Type} {
		switch strings.ToLower(strings.TrimSpace(structuredCode)) {
		case "context_length_exceeded", "context_window_exceeded", "max_context_length_exceeded":
			return ErrWorkspaceContext
		}
	}
	// Preserve known provider token-window errors without treating arbitrary
	// request/security context codes or every HTTP 400 as context exhaustion.
	if strings.Contains(msg, "context length exceeded") || strings.Contains(msg, "maximum context length") {
		return ErrWorkspaceContext
	}
	if strings.Contains(code, "channel_not_found") || strings.Contains(msg, "no available channel") || strings.Contains(msg, "无可用渠道") {
		return ErrWorkspaceModel
	}
	// Only explicit capacity signals identify a rejected generation. Generic
	// server/transport failures remain unknown and must not imply no charge.
	if status == http.StatusServiceUnavailable {
		if strings.Contains(msg, "号池当前没有可用账号") || strings.Contains(msg, "no available accounts") {
			return ErrWorkspaceCapacity
		}
		for _, structuredCode := range []string{v.Code, v.Data.Code, v.Error.Code, v.Error.Type} {
			switch strings.ToLower(strings.TrimSpace(structuredCode)) {
			case "model_capacity_unavailable", "no_available_accounts", "account_pool_exhausted", "insufficient_capacity":
				return ErrWorkspaceCapacity
			}
		}
	}
	if status == 403 {
		return ErrForbidden
	}
	if status == 400 || status == 422 {
		return ErrWorkspaceInvalidRequest
	}
	return ErrRemoteUnavailable
}
