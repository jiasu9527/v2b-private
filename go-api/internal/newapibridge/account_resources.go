package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type WorkspaceKeyRequest struct {
	Name           string   `json:"name"`
	Quota          int64    `json:"quota"`
	UnlimitedQuota bool     `json:"unlimited_quota"`
	ExpiredTime    int64    `json:"expired_time"`
	Models         []string `json:"models"`
	RequestID      string   `json:"request_id"`
}

func workspaceRequestID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func workspacePick(m map[string]any, fields ...string) map[string]any {
	o := map[string]any{}
	for _, f := range fields {
		if v, ok := m[f]; ok {
			o[f] = v
		}
	}
	return o
}
func (s *Service) WorkspaceModels(ctx context.Context, id int64, auth string) (any, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return nil, e
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodGet, "/api/user/models", nil, "")
	if e != nil {
		return nil, e
	}
	var models []string
	if workspaceDecode(env.Data, &models) != nil {
		return nil, ErrRemoteUnavailable
	}
	items := []map[string]any{}
	for _, m := range models {
		if m != "" && len(m) <= 256 {
			items = append(items, map[string]any{"id": m})
		}
	}
	return map[string]any{"items": items}, nil
}
func (s *Service) WorkspaceUsage(ctx context.Context, id int64, auth string, current, pageSize int) (any, error) {
	if current < 1 || current > 100000 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalid
	}
	return s.workspaceList(ctx, id, auth, "/api/log/self?type=2&p="+strconv.Itoa(current)+"&page_size="+strconv.Itoa(pageSize), current, pageSize, []string{"id", "created_at", "type", "model_name", "prompt_tokens", "completion_tokens", "quota", "use_time", "is_stream", "token_name", "group"})
}
func (s *Service) WorkspaceKeys(ctx context.Context, id int64, auth string) (any, error) {
	return s.WorkspaceKeysPage(ctx, id, auth, 1, 20)
}
func (s *Service) WorkspaceKeysPage(ctx context.Context, id int64, auth string, current, pageSize int) (any, error) {
	if current < 1 || current > 100000 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalid
	}
	return s.workspaceList(ctx, id, auth, "/api/token/?p="+strconv.Itoa(current)+"&page_size="+strconv.Itoa(pageSize), current, pageSize, []string{"id", "name", "status", "created_time", "accessed_time", "expired_time", "remain_quota", "used_quota", "unlimited_quota", "model_limits_enabled", "model_limits"})
}

func (s *Service) workspaceList(ctx context.Context, id int64, auth, path string, current, size int, fields []string) (any, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return nil, e
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodGet, path, nil, "")
	if e != nil {
		return nil, e
	}
	var data struct {
		Items []map[string]any `json:"items"`
		Total int64            `json:"total"`
	}
	if workspaceDecode(env.Data, &data) != nil || data.Total < 0 {
		return nil, ErrRemoteUnavailable
	}
	items := []map[string]any{}
	for _, m := range data.Items {
		items = append(items, workspacePick(m, fields...))
	}
	return map[string]any{"items": items, "total": data.Total, "current": current, "page_size": size}, nil
}
func (s *Service) CreateWorkspaceKey(ctx context.Context, id int64, auth string, req WorkspaceKeyRequest) (any, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 50 || req.Quota < 0 || req.Quota > 100000000000000 || !workspaceRequestID(req.RequestID) || len(req.Models) > 100 || (req.ExpiredTime != -1 && req.ExpiredTime < time.Now().Unix()+60) {
		return nil, ErrInvalid
	}
	for _, m := range req.Models {
		if m == "" || len(m) > 256 || strings.ContainsAny(m, ",\r\n") {
			return nil, ErrInvalid
		}
	}
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return nil, e
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return nil, e
	}
	defer lock.Rollback()
	var key, state, enc string
	e = s.db.QueryRowContext(ctx, `SELECT id,state,payload_cipher FROM v2_newapi_workspace_action WHERE user_id=$1 AND api_url=$2 AND kind='key_create' AND request_id=$3`, id, cfg.APIURL, req.RequestID).Scan(&key, &state, &enc)
	if e == nil {
		var previous WorkspaceKeyRequest
		raw, x := s.open(enc)
		if x != nil || json.Unmarshal(raw, &previous) != nil {
			return nil, ErrUnavailable
		}
		want, _ := json.Marshal(req)
		got, _ := json.Marshal(previous)
		if string(want) != string(got) {
			return nil, ErrConflict
		}
		if state == "done" {
			return map[string]any{"created": true, "request_id": req.RequestID}, nil
		}
		return nil, ErrWorkspaceUnknown
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	key, e = s.saveWorkspaceAction(ctx, id, cfg.APIURL, "key_create", req.RequestID, req, time.Now().Add(24*time.Hour).Unix())
	if e != nil {
		return nil, e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE v2_newapi_workspace_action SET state='sending' WHERE id=$1`, key)
	if e != nil {
		return nil, e
	}
	body := map[string]any{"name": req.Name, "remain_quota": req.Quota, "unlimited_quota": req.UnlimitedQuota, "expired_time": req.ExpiredTime, "model_limits_enabled": len(req.Models) > 0, "model_limits": strings.Join(req.Models, ","), "allow_user_quota_query": true}
	if _, _, e = s.workspaceRequest(ctx, cfg, &c, http.MethodPost, "/api/token/", body, ""); e != nil {
		return nil, ErrWorkspaceUnknown
	}
	_, e = s.db.ExecContext(ctx, `UPDATE v2_newapi_workspace_action SET state='done' WHERE id=$1`, key)
	if e != nil {
		return nil, e
	}
	return map[string]any{"created": true, "request_id": req.RequestID}, nil
}
func (s *Service) RevealWorkspaceKey(ctx context.Context, id int64, auth string, keyID int64) (any, error) {
	if keyID <= 0 {
		return nil, ErrInvalid
	}
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return nil, e
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodPost, "/api/token/"+strconv.FormatInt(keyID, 10)+"/key", map[string]any{}, "")
	if e != nil {
		return nil, e
	}
	var v struct {
		Key string `json:"key"`
	}
	if workspaceDecode(env.Data, &v) != nil || v.Key == "" || len(v.Key) > 4096 {
		return nil, ErrRemoteUnavailable
	}
	if e = s.workspaceAudit(ctx, id, cfg.APIURL, "key_reveal", keyID); e != nil {
		return nil, e
	}
	return map[string]any{"key": v.Key}, nil
}
func (s *Service) DeleteWorkspaceKey(ctx context.Context, id int64, auth string, keyID int64) error {
	if keyID <= 0 {
		return ErrInvalid
	}
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return e
	}
	if _, e = s.workspaceSelf(ctx, cfg, c, id); e != nil {
		return e
	}
	_, _, e = s.workspaceRequest(ctx, cfg, &c, http.MethodDelete, "/api/token/"+strconv.FormatInt(keyID, 10), nil, "")
	if e != nil {
		return e
	}
	return s.workspaceAudit(ctx, id, cfg.APIURL, "key_delete", keyID)
}
func (s *Service) workspaceAudit(ctx context.Context, id int64, api, operation string, targetID int64) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO v2_newapi_workspace_audit(user_id,api_url,operation,target_id,created_at) VALUES($1,$2,$3,$4,$5)`, id, api, operation, targetID, time.Now().Unix())
	return e
}

// No generic arbitrary-path proxy is exposed to the browser.
func workspaceQuery(path string, params url.Values) string { return path + "?" + params.Encode() }
