package newapibridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

var workspaceAccountSchema = []string{
	`CREATE TABLE IF NOT EXISTS v2_newapi_workspace_audit(id BIGSERIAL PRIMARY KEY,user_id BIGINT NOT NULL,api_url TEXT NOT NULL,operation TEXT NOT NULL,target_id BIGINT NOT NULL,created_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_account(user_id BIGINT NOT NULL,api_url TEXT NOT NULL,remote_user_id BIGINT NOT NULL,username TEXT NOT NULL,credential_cipher TEXT NOT NULL,revision BIGINT NOT NULL DEFAULT 1,password_cipher TEXT NOT NULL DEFAULT '',password_session_id TEXT NOT NULL DEFAULT '',updated_at BIGINT NOT NULL,PRIMARY KEY(user_id,api_url),UNIQUE(api_url,remote_user_id))`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_workspace_action(id TEXT PRIMARY KEY,user_id BIGINT NOT NULL,api_url TEXT NOT NULL,kind TEXT NOT NULL,request_id TEXT NOT NULL,payload_cipher TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'ready',expires_at BIGINT NOT NULL,created_at BIGINT NOT NULL,UNIQUE(user_id,api_url,kind,request_id))`,
}

func (s *Service) workspaceContext(ctx context.Context, id int64, auth string) (Settings, error) {
	if id <= 0 || auth == "" {
		return Settings{}, ErrForbidden
	}
	if _, e := s.userIdentity(ctx, id); e != nil {
		return Settings{}, e
	}
	c, e := s.loadSettings(ctx)
	if e != nil {
		return c, e
	}
	if !c.Enabled {
		return c, ErrDisabled
	}
	return c, nil
}

var workspacePoolGates sync.Map

type workspaceLockGuard struct {
	*sql.Tx
	gate chan struct{}
	once sync.Once
}

func (g *workspaceLockGuard) Rollback() error {
	e := g.Tx.Rollback()
	g.once.Do(func() { <-g.gate })
	return e
}
func (s *Service) workspaceLock(ctx context.Context, id int64, api string) (*workspaceLockGuard, error) {
	// Keep SQL capacity for each lock owner's subsequent wallet transaction and
	// short reads, including when different users contend on a small shared pool.
	max := s.db.Stats().MaxOpenConnections
	if max == 1 {
		return nil, ErrUnavailable
	}
	slots := 8
	if max > 0 {
		slots = max / 3
		if slots < 1 {
			slots = 1
		}
	}
	value, _ := workspacePoolGates.LoadOrStore(s.db, make(chan struct{}, slots))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	acquired := false
	defer func() {
		if !acquired {
			<-gate
		}
	}()
	for attempt := 0; attempt < 1200; attempt++ {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		var locked bool
		e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,763918205))`, fmt.Sprintf("%d:%s", id, api)).Scan(&locked)
		if e != nil {
			tx.Rollback()
			return nil, e
		}
		if locked {
			acquired = true
			return &workspaceLockGuard{Tx: tx, gate: gate}, nil
		}
		tx.Rollback()
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, ErrConflict
}

func (s *Service) loadWorkspaceSession(ctx context.Context, id int64, api string) (workspaceSession, error) {
	var encrypted string
	e := s.db.QueryRowContext(ctx, `SELECT credential_cipher FROM v2_newapi_account WHERE user_id=$1 AND api_url=$2`, id, api).Scan(&encrypted)
	if errors.Is(e, sql.ErrNoRows) {
		return workspaceSession{}, ErrWorkspaceNotConnected
	}
	if e != nil {
		return workspaceSession{}, e
	}
	raw, e := s.open(encrypted)
	var c workspaceSession
	if e != nil || json.Unmarshal(raw, &c) != nil {
		return c, ErrUnavailable
	}
	return c, nil
}
func (s *Service) storeWorkspaceSession(ctx context.Context, id int64, cfg Settings, c workspaceSession, u workspaceUser) error {
	raw, e := json.Marshal(c)
	if e != nil {
		return e
	}
	enc, e := s.seal(raw)
	if e != nil {
		return e
	}
	result, e := s.db.ExecContext(ctx, `INSERT INTO v2_newapi_account(user_id,api_url,remote_user_id,username,credential_cipher,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,api_url) DO UPDATE SET username=EXCLUDED.username,credential_cipher=EXCLUDED.credential_cipher,revision=v2_newapi_account.revision+1,updated_at=EXCLUDED.updated_at WHERE v2_newapi_account.remote_user_id=EXCLUDED.remote_user_id`, id, cfg.APIURL, c.RemoteID, u.Username, enc, time.Now().Unix())
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Service) workspaceCredentials(ctx context.Context, id int64, auth string) (Settings, workspaceSession, error) {
	cfg, e := s.workspaceContext(ctx, id, auth)
	if e != nil {
		return cfg, workspaceSession{}, e
	}
	cred, e := s.loadWorkspaceSession(ctx, id, cfg.APIURL)
	if e != nil {
		return cfg, cred, e
	}
	if cred.ExpiresAt > time.Now().Add(time.Minute).Unix() {
		return cfg, cred, nil
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return cfg, cred, e
	}
	defer lock.Rollback()
	cred, e = s.loadWorkspaceSession(ctx, id, cfg.APIURL)
	if e != nil {
		return cfg, cred, e
	}
	if cred.ExpiresAt > time.Now().Add(time.Minute).Unix() {
		return cfg, cred, nil
	}
	env, cookies, e := s.workspaceRequest(ctx, cfg, &cred, http.MethodPost, "/api/user/auth/refresh", map[string]any{}, "")
	if e != nil {
		return cfg, cred, e
	}
	next, e := workspaceSessionFrom(env, cookies, id, &cred)
	if e != nil {
		return cfg, cred, e
	}
	u, e := s.workspaceSelf(ctx, cfg, next, id)
	if e != nil {
		return cfg, cred, e
	}
	if e = s.storeWorkspaceSession(ctx, id, cfg, next, u); e != nil {
		return cfg, cred, e
	}
	return cfg, next, nil
}

// The current, freshly authenticated Forest user is the only subject this
// server-side OIDC loop can authorize. No arbitrary upstream ID is accepted.
func (s *Service) workspaceOIDC(ctx context.Context, cfg Settings, id int64, auth, intent, scope string, cred *workspaceSession) (workspaceEnvelope, []*http.Cookie, error) {
	body := map[string]any{"provider": "oidc", "intent": intent}
	if scope != "" {
		body["scope"] = scope
	}
	env, _, e := s.workspaceRequest(ctx, cfg, cred, http.MethodPost, "/api/oauth/state", body, "")
	if e != nil {
		return env, nil, e
	}
	var state struct {
		Token string `json:"flow_token"`
	}
	if workspaceDecode(env.Data, &state) != nil || state.Token == "" {
		return env, nil, ErrRemoteUnavailable
	}
	target, e := s.Authorize(ctx, id, auth, OAuthRequest{ClientID: cfg.ClientID, RedirectURI: cfg.APIURL + "/oauth/oidc", ResponseType: "code", Scope: "openid profile email", State: state.Token})
	if e != nil {
		return env, nil, e
	}
	parsed, e := url.Parse(target)
	if e != nil {
		return env, nil, ErrRemoteUnavailable
	}
	return s.workspaceRequest(ctx, cfg, cred, http.MethodGet, "/api/oauth/oidc?"+parsed.RawQuery, nil, "")
}

func (s *Service) ConnectAccount(ctx context.Context, id int64, auth string) (map[string]any, error) {
	cfg, e := s.workspaceContext(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return nil, e
	}
	defer lock.Rollback()
	if cred, e := s.loadWorkspaceSession(ctx, id, cfg.APIURL); e == nil && cred.ExpiresAt > time.Now().Unix() {
		if u, e := s.workspaceSelf(ctx, cfg, cred, id); e == nil {
			if e = s.workspaceWalletOnly(ctx, cfg, cred, true); e != nil {
				return nil, e
			}
			return s.workspaceAccountView(ctx, cfg, cred, u, id)
		}
	}
	env, cookies, e := s.workspaceOIDC(ctx, cfg, id, auth, "login", "", nil)
	if e != nil {
		return nil, e
	}
	var challenge struct {
		Required bool             `json:"require_verification"`
		Flow     string           `json:"flow_token"`
		Expires  int64            `json:"expires_at"`
		Methods  []map[string]any `json:"methods"`
	}
	_ = json.Unmarshal(env.Data, &challenge)
	if challenge.Required {
		key, e := s.saveWorkspaceAction(ctx, id, cfg.APIURL, "login", "", challenge, time.Now().Add(10*time.Minute).Unix())
		if e != nil {
			return nil, e
		}
		return map[string]any{"connected": false, "api_url": cfg.APIURL, "challenge": map[string]any{"id": key, "methods": challenge.Methods, "expires_at": challenge.Expires, "external_url": cfg.APIURL + "/login"}}, nil
	}
	cred, e := workspaceSessionFrom(env, cookies, id, nil)
	if e != nil {
		return nil, e
	}
	u, e := s.workspaceSelf(ctx, cfg, cred, id)
	if e != nil {
		return nil, e
	}
	if e = s.workspaceWalletOnly(ctx, cfg, cred, true); e != nil {
		return nil, e
	}
	if e = s.storeWorkspaceSession(ctx, id, cfg, cred, u); e != nil {
		return nil, e
	}
	return s.workspaceAccountView(ctx, cfg, cred, u, id)
}

type WorkspaceLoginVerification struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

func (s *Service) VerifyWorkspaceLogin(ctx context.Context, id int64, auth string, req WorkspaceLoginVerification) (map[string]any, error) {
	cfg, e := s.workspaceContext(ctx, id, auth)
	if e != nil {
		return nil, e
	}
	if req.Code == "" || len(req.Code) > 128 {
		return nil, ErrInvalid
	}
	lock, e := s.workspaceLock(ctx, id, cfg.APIURL)
	if e != nil {
		return nil, e
	}
	defer lock.Rollback()
	var ch struct {
		Flow string `json:"flow_token"`
	}
	_, e = s.readWorkspaceAction(ctx, id, cfg.APIURL, req.ChallengeID, "login", &ch)
	if e != nil {
		return nil, e
	}
	env, cookies, e := s.workspaceRequest(ctx, cfg, nil, http.MethodPost, "/api/user/login/verify", map[string]any{"flow_token": ch.Flow, "method": "2fa", "code": req.Code}, "")
	if e != nil {
		return nil, e
	}
	cred, e := workspaceSessionFrom(env, cookies, id, nil)
	if e != nil {
		return nil, e
	}
	u, e := s.workspaceSelf(ctx, cfg, cred, id)
	if e != nil {
		return nil, e
	}
	if e = s.workspaceWalletOnly(ctx, cfg, cred, true); e != nil {
		return nil, e
	}
	if e = s.storeWorkspaceSession(ctx, id, cfg, cred, u); e != nil {
		return nil, e
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE v2_newapi_workspace_action SET state='done' WHERE id=$1`, req.ChallengeID)
	return s.workspaceAccountView(ctx, cfg, cred, u, id)
}

func (s *Service) WorkspaceAccount(ctx context.Context, id int64, auth string) (map[string]any, error) {
	cfg, cred, e := s.workspaceCredentials(ctx, id, auth)
	if errors.Is(e, ErrWorkspaceNotConnected) {
		var balance int64
		if e = s.db.QueryRowContext(ctx, `SELECT balance FROM v2_user WHERE id=$1`, id).Scan(&balance); e != nil {
			return nil, e
		}
		pending, e := s.pendingWorkspaceTransferQuote(ctx, id, cfg.APIURL)
		if e != nil {
			return nil, e
		}
		return map[string]any{"connected": false, "api_url": cfg.APIURL, "balance_fen": balance, "pending_transfer_quote": pending}, nil
	}
	if e != nil {
		return nil, e
	}
	u, e := s.workspaceSelf(ctx, cfg, cred, id)
	if e != nil {
		return nil, e
	}
	return s.workspaceAccountView(ctx, cfg, cred, u, id)
}
func (s *Service) workspaceAccountView(ctx context.Context, cfg Settings, c workspaceSession, u workspaceUser, id int64) (map[string]any, error) {
	var bal int64
	var managed bool
	if e := s.db.QueryRowContext(ctx, `SELECT balance FROM v2_user WHERE id=$1`, id).Scan(&bal); e != nil {
		return nil, e
	}
	_ = s.db.QueryRowContext(ctx, `SELECT password_cipher<>'' AND password_session_id=$3 FROM v2_newapi_account WHERE user_id=$1 AND api_url=$2`, id, cfg.APIURL, c.SessionID).Scan(&managed)
	unit, currency, display := int64(0), "quota", strconv.FormatInt(u.Quota, 10)
	displayRate := "1"
	if env, _, e := s.workspaceRequest(ctx, cfg, nil, http.MethodGet, "/api/status", nil, ""); e == nil {
		var v struct {
			Unit int64       `json:"quota_per_unit"`
			Type string      `json:"quota_display_type"`
			Rate json.Number `json:"usd_exchange_rate"`
		}
		if json.Unmarshal(env.Data, &v) == nil && v.Unit > 0 {
			unit = v.Unit
			if v.Type == "USD" {
				currency = "USD"
				displayRate = "1"
				display = workspaceDecimal(u.Quota, unit, "1")
			}
			if v.Type == "CNY" {
				if r, ok := new(big.Rat).SetString(v.Rate.String()); ok && r.Sign() > 0 {
					currency = "CNY"
					displayRate = v.Rate.String()
					display = workspaceDecimal(u.Quota, unit, v.Rate.String())
				}
			}
		}
	}
	pending, e := s.pendingWorkspaceTransferQuote(ctx, id, cfg.APIURL)
	if e != nil {
		return nil, e
	}
	return map[string]any{"connected": true, "remote_user_id": u.ID, "username": u.Username, "api_url": cfg.APIURL, "quota": u.Quota, "used_quota": u.UsedQuota, "quota_per_unit": unit, "currency": currency, "display_balance": display, "display_rate": displayRate, "has_password": u.HasPassword, "managed_password_available": managed && u.HasPassword, "updated_at": time.Now().Unix(), "balance_fen": bal, "wallet_billing_enabled": s.workspaceWalletOnly(ctx, cfg, c, false) == nil, "pending_transfer_quote": pending}, nil
}
func workspaceDecimal(n, unit int64, rate string) string {
	r, ok := new(big.Rat).SetString(rate)
	if !ok || unit <= 0 {
		return strconv.FormatInt(n, 10)
	}
	r.Mul(r, new(big.Rat).SetFrac(big.NewInt(n), big.NewInt(unit)))
	return r.FloatString(6)
}

func (s *Service) saveWorkspaceAction(ctx context.Context, id int64, api, kind, requestID string, payload any, expires int64) (string, error) {
	key, e := opaqueToken()
	if e != nil {
		return "", e
	}
	if requestID == "" {
		requestID = key
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	encrypted, e := s.seal(raw)
	if e != nil {
		return "", e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO v2_newapi_workspace_action(id,user_id,api_url,kind,request_id,payload_cipher,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, key, id, api, kind, requestID, encrypted, expires, time.Now().Unix())
	return key, e
}
func (s *Service) readWorkspaceAction(ctx context.Context, id int64, api, key, kind string, payload any) (string, error) {
	var enc, state string
	var expires int64
	e := s.db.QueryRowContext(ctx, `SELECT payload_cipher,state,expires_at FROM v2_newapi_workspace_action WHERE id=$1 AND user_id=$2 AND api_url=$3 AND kind=$4`, key, id, api, kind).Scan(&enc, &state, &expires)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	if e != nil {
		return "", e
	}
	if expires < time.Now().Unix() && state == "ready" {
		return state, ErrExpired
	}
	raw, e := s.open(enc)
	if e != nil {
		return state, e
	}
	if json.Unmarshal(raw, payload) != nil {
		return state, ErrUnavailable
	}
	return state, nil
}

// Forest checks wallet billing before generation. External preference changes
// between this check and upstream settlement remain governed by New API.
func (s *Service) workspaceWalletOnly(ctx context.Context, cfg Settings, c workspaceSession, set bool) error {
	method, path := http.MethodGet, "/api/subscription/self"
	var body any
	if set {
		method, path = http.MethodPut, "/api/subscription/self/preference"
		body = map[string]any{"billing_preference": "wallet_only"}
	}
	env, _, e := s.workspaceRequest(ctx, cfg, &c, method, path, body, "")
	if e != nil {
		return e
	}
	var v struct {
		Preference string `json:"billing_preference"`
	}
	if workspaceDecode(env.Data, &v) != nil || v.Preference != "wallet_only" {
		return ErrConflict
	}
	return nil
}

// Only short-lived, conclusively unused/completed challenges are removed.
// Financial/key idempotency and uncertain writes remain available for review.
func (s *Service) CleanupWorkspaceActions(ctx context.Context) error {
	if e := s.EnsureSchema(ctx); e != nil {
		return e
	}
	_, e := s.db.ExecContext(ctx, `DELETE FROM v2_newapi_workspace_action WHERE kind IN ('login','password') AND state IN ('ready','done') AND expires_at<$1`, time.Now().Unix())
	return e
}

// Server-side recovery survives cleared browser storage and different Forest
// sessions. Return only an owned opaque ID and state, never signed Epay data.
func (s *Service) pendingWorkspaceTransferQuote(ctx context.Context, id int64, api string) (map[string]any, error) {
	var key, state string
	e := s.db.QueryRowContext(ctx, `SELECT id,state FROM v2_newapi_workspace_action WHERE user_id=$1 AND api_url=$2 AND kind='transfer_quote' AND state IN ('sending','created','checkout') ORDER BY created_at DESC,id DESC LIMIT 1`, id, api).Scan(&key, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return map[string]any{"quote_id": key, "status": state}, nil
}
