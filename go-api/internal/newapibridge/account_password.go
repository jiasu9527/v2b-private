package newapibridge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"time"
)

type WorkspacePasswordRequest struct {
	ChallengeID      string `json:"challenge_id"`
	Method           string `json:"method"`
	Code             string `json:"code"`
	OriginalPassword string `json:"original_password"`
}
type workspacePasswordChallenge struct {
	Scope       string `json:"scope"`
	SessionID   string `json:"session_id"`
	HasPassword bool   `json:"has_password"`
	Password    string `json:"password,omitempty"`
}
type workspaceVerificationRequirements struct {
	Methods []struct {
		Method    string `json:"method"`
		Available bool   `json:"available"`
	} `json:"methods"`
	Providers []struct {
		Slug string `json:"slug"`
	} `json:"oauth_providers"`
	Encrypted bool `json:"password_encryption_enabled"`
}

func (s *Service) workspaceRequirements(ctx context.Context, cfg Settings, c workspaceSession, scope string) (workspaceVerificationRequirements, error) {
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodGet, "/api/verify/methods?scope="+url.QueryEscape(scope), nil, "")
	var v workspaceVerificationRequirements
	if e != nil {
		return v, e
	}
	e = workspaceDecode(env.Data, &v)
	return v, e
}
func (s *Service) PrepareWorkspacePassword(ctx context.Context, id int64, auth string, reset bool) (map[string]any, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	u, e := s.workspaceSelf(ctx, cfg, c, id)
	if e != nil {
		return nil, e
	}
	if u.HasPassword && !reset {
		return nil, ErrConflict
	}
	scope := "account.password.set"
	if u.HasPassword {
		scope = "account.password.change"
	}
	req, e := s.workspaceRequirements(ctx, cfg, c, scope)
	if e != nil {
		return nil, e
	}
	methods := []map[string]any{}
	oidc := false
	for _, p := range req.Providers {
		if p.Slug == "oidc" {
			oidc = true
		}
	}
	for _, m := range req.Methods {
		available := m.Available
		if m.Method == "oauth" && !oidc {
			available = false
		}
		if m.Method == "passkey" {
			available = false
		}
		methods = append(methods, map[string]any{"method": m.Method, "available": available})
	}
	expires := time.Now().Add(5 * time.Minute).Unix()
	key, e := s.saveWorkspaceAction(ctx, id, cfg.APIURL, "password", "", workspacePasswordChallenge{Scope: scope, SessionID: c.SessionID, HasPassword: u.HasPassword}, expires)
	if e != nil {
		return nil, e
	}
	var managed bool
	_ = s.db.QueryRowContext(ctx, `SELECT password_cipher<>'' AND password_session_id=$3 FROM v2_newapi_account WHERE user_id=$1 AND api_url=$2`, id, cfg.APIURL, c.SessionID).Scan(&managed)
	return map[string]any{"challenge_id": key, "has_password": u.HasPassword, "methods": methods, "original_password_required": u.HasPassword && !managed, "expires_at": expires, "external_url": cfg.APIURL + "/settings"}, nil
}
func (s *Service) workspaceManagedPassword(ctx context.Context, id int64, cfg Settings, c workspaceSession) (string, error) {
	var enc, sid string
	e := s.db.QueryRowContext(ctx, `SELECT password_cipher,password_session_id FROM v2_newapi_account WHERE user_id=$1 AND api_url=$2`, id, cfg.APIURL).Scan(&enc, &sid)
	if e != nil {
		return "", e
	}
	if enc == "" || sid != c.SessionID {
		return "", ErrConflict
	}
	raw, e := s.open(enc)
	if e != nil {
		return "", e
	}
	return string(raw), nil
}
func (s *Service) workspaceVerifyInput(ctx context.Context, cfg Settings, c workspaceSession, scope, method, password, code string, encrypted bool) (string, error) {
	body := map[string]any{"scope": scope, "method": method}
	if method == "2fa" {
		if code == "" || len(code) > 128 {
			return "", ErrInvalid
		}
		body["code"] = code
	} else if method == "password" {
		if password == "" || len(password) > 1024 {
			return "", ErrInvalid
		}
		if !encrypted {
			body["password"] = password
		} else {
			env, _, e := s.workspaceRequest(ctx, cfg, nil, http.MethodGet, "/api/user/login/encryption-key", nil, "")
			if e != nil {
				return "", e
			}
			var key struct {
				Enabled bool   `json:"enabled"`
				Kid     string `json:"kid"`
				Public  string `json:"public_key"`
			}
			if workspaceDecode(env.Data, &key) != nil || !key.Enabled || key.Kid == "" {
				return "", ErrRemoteUnavailable
			}
			block, _ := pem.Decode([]byte(key.Public))
			if block == nil {
				return "", ErrRemoteUnavailable
			}
			parsed, e := x509.ParsePKIXPublicKey(block.Bytes)
			if e != nil {
				return "", ErrRemoteUnavailable
			}
			rsaKey, ok := parsed.(*rsa.PublicKey)
			if !ok || rsaKey.N.BitLen() < 2048 {
				return "", ErrRemoteUnavailable
			}
			raw, e := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, []byte(password), nil)
			if e != nil {
				return "", ErrInvalid
			}
			body["password_encrypted"] = base64.StdEncoding.EncodeToString(raw)
			body["encryption_key_id"] = key.Kid
		}
	} else {
		return "", ErrWorkspaceVerification
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, http.MethodPost, "/api/verify", body, "")
	if e != nil {
		return "", e
	}
	var proof struct {
		Token string `json:"proof_token"`
	}
	if workspaceDecode(env.Data, &proof) != nil || proof.Token == "" {
		return "", ErrWorkspaceVerification
	}
	return proof.Token, nil
}
func (s *Service) ConfirmWorkspacePassword(ctx context.Context, id int64, auth string, req WorkspacePasswordRequest) (map[string]any, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return nil, e
	}
	defer lock.Rollback()
	var ch workspacePasswordChallenge
	state, e := s.readWorkspaceAction(ctx, id, cfg.APIURL, req.ChallengeID, "password", &ch)
	if e != nil {
		return nil, e
	}
	if state != "ready" {
		return nil, ErrWorkspaceUnknown
	}
	if ch.SessionID != c.SessionID {
		return nil, ErrConflict
	}
	u, e := s.workspaceSelf(ctx, cfg, c, id)
	if e != nil {
		return nil, e
	}
	if u.HasPassword != ch.HasPassword {
		return nil, ErrConflict
	}
	requirements, e := s.workspaceRequirements(ctx, cfg, c, ch.Scope)
	if e != nil {
		return nil, e
	}
	allowed := false
	for _, m := range requirements.Methods {
		if m.Method == req.Method && m.Available {
			allowed = true
		}
	}
	if !allowed {
		return nil, ErrWorkspaceVerification
	}
	original := req.OriginalPassword
	if ch.HasPassword && original == "" {
		original, _ = s.workspaceManagedPassword(ctx, id, cfg, c)
	}
	if ch.HasPassword && original == "" {
		return nil, ErrInvalid
	}
	proof := ""
	if req.Method == "oauth" {
		provider := false
		for _, p := range requirements.Providers {
			if p.Slug == "oidc" {
				provider = true
			}
		}
		if !provider {
			return nil, ErrWorkspaceVerification
		}
		env, _, e := s.workspaceOIDC(ctx, cfg, id, auth, "verify", ch.Scope, &c)
		if e != nil {
			return nil, e
		}
		var p struct {
			Token string `json:"proof_token"`
		}
		if workspaceDecode(env.Data, &p) != nil || p.Token == "" {
			return nil, ErrWorkspaceVerification
		}
		proof = p.Token
	} else {
		proof, e = s.workspaceVerifyInput(ctx, cfg, c, ch.Scope, req.Method, original, req.Code, requirements.Encrypted)
		if e != nil {
			return nil, e
		}
	}
	generated, e := opaqueToken()
	if e != nil {
		return nil, e
	}
	ch.Password = generated
	raw, _ := json.Marshal(ch)
	enc, e := s.seal(raw)
	if e != nil {
		return nil, e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE v2_newapi_workspace_action SET state='sending',payload_cipher=$2 WHERE id=$1 AND state='ready'`, req.ChallengeID, enc)
	if e != nil {
		return nil, e
	}
	env, cookies, e := s.workspaceRequest(ctx, cfg, &c, http.MethodPut, "/api/user/self", map[string]any{"username": u.Username, "display_name": u.DisplayName, "password": generated, "original_password": original}, proof)
	if e != nil {
		return nil, ErrWorkspaceUnknown
	}
	var rotation struct {
		Access  string `json:"access_token"`
		Expires int64  `json:"access_expires_at"`
		Session struct {
			SID string `json:"sid"`
		} `json:"session"`
	}
	if workspaceDecode(env.Data, &rotation) != nil || rotation.Access == "" || rotation.Expires <= time.Now().Unix() || rotation.Session.SID == "" {
		return nil, ErrWorkspaceUnknown
	}
	next := c
	next.Access = rotation.Access
	next.ExpiresAt = rotation.Expires
	next.SessionID = rotation.Session.SID
	for _, cookie := range cookies {
		if cookie.Name == "new_api_refresh" && cookie.Value != "" {
			next.Refresh = cookie.Value
		}
	}
	current, e := s.workspaceSelf(ctx, cfg, next, id)
	if e != nil || !current.HasPassword {
		return nil, ErrWorkspaceUnknown
	}
	if e = s.storeWorkspaceSession(ctx, id, cfg, next, current); e != nil {
		return nil, ErrWorkspaceUnknown
	}
	passEnc, e := s.seal([]byte(generated))
	if e != nil {
		return nil, e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE v2_newapi_account SET password_cipher=$3,password_session_id=$4 WHERE user_id=$1 AND api_url=$2`, id, cfg.APIURL, passEnc, next.SessionID)
	if e != nil {
		return nil, ErrWorkspaceUnknown
	}
	ch.Password = ""
	cleanRaw, _ := json.Marshal(ch)
	cleanCipher, cleanErr := s.seal(cleanRaw)
	if cleanErr != nil {
		return nil, cleanErr
	}
	if _, e = s.db.ExecContext(ctx, `UPDATE v2_newapi_workspace_action SET state='done',payload_cipher=$2 WHERE id=$1`, req.ChallengeID, cleanCipher); e != nil {
		return nil, e
	}
	if e = s.workspaceAudit(ctx, id, cfg.APIURL, "password_generate_reset", next.RemoteID); e != nil {
		return nil, e
	}
	return map[string]any{"password": generated, "username": current.Username, "has_password": true}, nil
}
func (s *Service) RevealWorkspacePassword(ctx context.Context, id int64, auth string) (map[string]any, error) {
	cfg, c, e := s.workspaceCredentials(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	u, e := s.workspaceSelf(ctx, cfg, c, id)
	if e != nil {
		return nil, e
	}
	if !u.HasPassword {
		return nil, ErrConflict
	}
	password, e := s.workspaceManagedPassword(ctx, id, cfg, c)
	if e != nil {
		return nil, e
	}
	if e = s.workspaceAudit(ctx, id, cfg.APIURL, "password_reveal", u.ID); e != nil {
		return nil, e
	}
	return map[string]any{"password": password, "username": u.Username}, nil
}
