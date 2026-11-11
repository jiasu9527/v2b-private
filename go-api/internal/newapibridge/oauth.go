package newapibridge

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type OAuthRequest struct {
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	ResponseType        string `json:"response_type"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	Nonce               string `json:"nonce"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
}

type OAuthTokenRequest struct{ ClientID, ClientSecret, GrantType, Code, RedirectURI, CodeVerifier string }

type oauthGrant struct {
	UserID    int64        `json:"user_id"`
	AuthToken string       `json:"auth_token"`
	Revision  int64        `json:"revision"`
	Request   OAuthRequest `json:"request"`
}

type portalGrant struct {
	UserID    int64  `json:"user_id"`
	AuthToken string `json:"auth_token"`
}

type OAuthUser struct {
	Subject           string `json:"sub"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
}

func (s *Service) userIdentity(ctx context.Context, id int64) (OAuthUser, error) {
	var email string
	var banned int64
	err := s.db.QueryRowContext(ctx, `SELECT email,banned FROM v2_user WHERE id=$1`, id).Scan(&email, &banned)
	if errors.Is(err, sql.ErrNoRows) || banned != 0 {
		return OAuthUser{}, ErrForbidden
	}
	if err != nil {
		return OAuthUser{}, err
	}
	if id <= 0 || email == "" {
		return OAuthUser{}, ErrForbidden
	}
	// Keep the immutable subject for account ownership. New API can use the
	// email as a username when its length/uniqueness rules permit; longer email
	// addresses remain valid login identifiers through its separate email field.
	return OAuthUser{Subject: fmt.Sprintf("forest:%d", id), Name: fmt.Sprintf("Forest #%d", id), PreferredUsername: email, Email: email}, nil
}

func (s *Service) putToken(ctx context.Context, purpose string, payload any, ttl time.Duration) (string, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return "", err
	}
	token, err := opaqueToken()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encrypted, err := s.seal(raw)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO v2_newapi_token(token_hash,purpose,payload_cipher,expires_at) VALUES($1,$2,$3,$4)`, tokenHash(token), purpose, encrypted, time.Now().Add(ttl).Unix())
	return token, err
}

func (s *Service) readToken(ctx context.Context, purpose, token string, consume bool, payload any) error {
	if len(token) != 43 {
		return ErrForbidden
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	query := `SELECT payload_cipher FROM v2_newapi_token WHERE token_hash=$1 AND purpose=$2 AND expires_at>$3`
	if consume {
		query = `DELETE FROM v2_newapi_token WHERE token_hash=$1 AND purpose=$2 AND expires_at>$3 RETURNING payload_cipher`
	}
	var encrypted string
	err := s.db.QueryRowContext(ctx, query, tokenHash(token), purpose, time.Now().Unix()).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	raw, err := s.open(encrypted)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, payload) != nil {
		return ErrUnavailable
	}
	return nil
}

func validateOAuthRequest(c Settings, req OAuthRequest) error {
	if !c.Enabled {
		return ErrDisabled
	}
	if req.ClientID != c.ClientID || req.RedirectURI != c.APIURL+"/oauth/oidc" || req.ResponseType != "code" || len(req.State) < 8 || len(req.State) > 1024 || len(req.Nonce) > 256 {
		return ErrInvalid
	}
	scopes := strings.Fields(req.Scope)
	hasOpenID := false
	for _, scope := range scopes {
		switch scope {
		case "openid":
			hasOpenID = true
		case "profile", "email":
		default:
			return ErrInvalid
		}
	}
	if !hasOpenID {
		return ErrInvalid
	}
	if req.CodeChallenge != "" {
		b, e := base64.RawURLEncoding.DecodeString(req.CodeChallenge)
		if e != nil || len(b) != 32 || req.CodeChallengeMethod != "S256" {
			return ErrInvalid
		}
	} else if req.CodeChallengeMethod != "" {
		return ErrInvalid
	}
	return nil
}

func (s *Service) ValidateOAuth(ctx context.Context, req OAuthRequest) error {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return e
	}
	return validateOAuthRequest(c, req)
}

func (s *Service) Authorize(ctx context.Context, userID int64, authToken string, req OAuthRequest) (string, error) {
	c, err := s.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	if err = validateOAuthRequest(c, req); err != nil {
		return "", err
	}
	if authToken == "" {
		return "", ErrForbidden
	}
	if _, err = s.userIdentity(ctx, userID); err != nil {
		return "", err
	}
	code, err := s.putToken(ctx, "oauth_code", oauthGrant{UserID: userID, AuthToken: authToken, Revision: c.Revision, Request: req}, 2*time.Minute)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(req.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	q.Set("state", req.State)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Service) ExchangeCode(ctx context.Context, req OAuthTokenRequest) (map[string]any, error) {
	c, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, ErrDisabled
	}
	if req.GrantType != "authorization_code" || req.ClientID != c.ClientID || subtle.ConstantTimeCompare([]byte(req.ClientSecret), []byte(c.ClientSecret)) != 1 || req.RedirectURI != c.APIURL+"/oauth/oidc" {
		return nil, ErrForbidden
	}
	var grant oauthGrant
	// Only an authenticated, configured client can consume a single-use code.
	if err = s.readToken(ctx, "oauth_code", req.Code, true, &grant); err != nil {
		return nil, err
	}
	if grant.Revision != c.Revision || grant.Request.ClientID != req.ClientID || grant.Request.RedirectURI != req.RedirectURI {
		return nil, ErrForbidden
	}
	if grant.Request.CodeChallenge != "" {
		sum := sha256.Sum256([]byte(req.CodeVerifier))
		actual := base64.RawURLEncoding.EncodeToString(sum[:])
		if len(req.CodeVerifier) < 43 || len(req.CodeVerifier) > 128 || subtle.ConstantTimeCompare([]byte(actual), []byte(grant.Request.CodeChallenge)) != 1 {
			return nil, ErrForbidden
		}
	}
	user, err := s.userIdentity(ctx, grant.UserID)
	if err != nil {
		return nil, err
	}
	access, err := s.putToken(ctx, "oauth_access", grant, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	key, err := parseSigningKey(c.SigningKey)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	claims := jwt.MapClaims{"iss": c.SiteURL + "/new-api", "sub": user.Subject, "aud": c.ClientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	if grant.Request.Nonce != "" {
		claims["nonce"] = grant.Request.Nonce
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = signingKeyID(key)
	idToken, err := token.SignedString(key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "scope": grant.Request.Scope, "id_token": idToken}, nil
}

// OAuthAuthentication returns the original panel session for a fresh revocation
// check by HTTP middleware. Tokens themselves are scoped to these endpoints.
func (s *Service) OAuthAuthentication(ctx context.Context, token string) (int64, string, error) {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return 0, "", e
	}
	if !c.Enabled {
		return 0, "", ErrDisabled
	}
	var g oauthGrant
	if e = s.readToken(ctx, "oauth_access", token, false, &g); e != nil {
		return 0, "", e
	}
	if c.Revision != g.Revision {
		return 0, "", ErrForbidden
	}
	return g.UserID, g.AuthToken, nil
}

func (s *Service) UserInfo(ctx context.Context, id int64) (OAuthUser, error) {
	return s.userIdentity(ctx, id)
}

func (s *Service) IssuePortalTicket(ctx context.Context, id int64, auth string) (string, error) {
	if auth == "" {
		return "", ErrForbidden
	}
	c, e := s.loadSettings(ctx)
	if e != nil {
		return "", e
	}
	if !c.Enabled {
		return "", ErrDisabled
	}
	if _, e = s.userIdentity(ctx, id); e != nil {
		return "", e
	}
	token, e := s.putToken(ctx, "portal_ticket", portalGrant{UserID: id, AuthToken: auth}, time.Minute)
	if e != nil {
		return "", e
	}
	// A fragment is not transmitted in HTTP requests or ordinary access logs.
	return c.SiteURL + "/new-api/#ticket=" + url.QueryEscape(token), nil
}

func (s *Service) RedeemPortalTicket(ctx context.Context, token string) (int64, string, error) {
	var g portalGrant
	if e := s.readToken(ctx, "portal_ticket", token, true, &g); e != nil {
		return 0, "", e
	}
	return g.UserID, g.AuthToken, nil
}

func (s *Service) IssuePortalSession(ctx context.Context, id int64, auth string) (string, error) {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return "", e
	}
	if !c.Enabled {
		return "", ErrDisabled
	}
	if auth == "" {
		return "", ErrForbidden
	}
	if _, e = s.userIdentity(ctx, id); e != nil {
		return "", e
	}
	token, e := s.putToken(ctx, "portal_session", portalGrant{UserID: id, AuthToken: auth}, 30*time.Minute)
	if e != nil {
		return "", e
	}
	return "nap_" + token, nil
}

func (s *Service) PortalAuthentication(ctx context.Context, token string) (int64, string, error) {
	if !strings.HasPrefix(token, "nap_") {
		return 0, "", ErrForbidden
	}
	var g portalGrant
	if e := s.readToken(ctx, "portal_session", strings.TrimPrefix(token, "nap_"), false, &g); e != nil {
		return 0, "", e
	}
	return g.UserID, g.AuthToken, nil
}

func (s *Service) RevokePortalSession(ctx context.Context, token string) error {
	if !strings.HasPrefix(token, "nap_") {
		return ErrInvalid
	}
	_, e := s.db.ExecContext(ctx, `DELETE FROM v2_newapi_token WHERE token_hash=$1 AND purpose='portal_session'`, tokenHash(strings.TrimPrefix(token, "nap_")))
	return e
}

func parseSigningKey(encoded string) (*rsa.PrivateKey, error) {
	raw, e := base64.RawStdEncoding.DecodeString(encoded)
	if e != nil {
		return nil, ErrUnavailable
	}
	key, e := x509.ParsePKCS1PrivateKey(raw)
	if e != nil {
		return nil, ErrUnavailable
	}
	return key, nil
}
func signingKeyID(key *rsa.PrivateKey) string {
	return tokenHash(base64.RawURLEncoding.EncodeToString(key.N.Bytes()))[:16]
}

func (s *Service) Discovery(ctx context.Context) (map[string]any, error) {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return nil, e
	}
	if !c.Enabled {
		return nil, ErrDisabled
	}
	return map[string]any{"issuer": c.SiteURL + "/new-api", "authorization_endpoint": c.SiteURL + "/new-api/authorize", "token_endpoint": c.SiteURL + "/api/v1/new-api/oauth/token", "userinfo_endpoint": c.SiteURL + "/api/v1/new-api/oauth/userinfo", "jwks_uri": c.SiteURL + "/api/v1/new-api/oauth/jwks", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "scopes_supported": []string{"openid", "profile", "email"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"}, "code_challenge_methods_supported": []string{"S256"}}, nil
}

func (s *Service) JWKS(ctx context.Context) (map[string]any, error) {
	c, e := s.loadSettings(ctx)
	if e != nil {
		return nil, e
	}
	if !c.Enabled {
		return nil, ErrDisabled
	}
	key, e := parseSigningKey(c.SigningKey)
	if e != nil {
		return nil, e
	}
	return map[string]any{"keys": []any{map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": signingKeyID(key), "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}, nil
}

// Cleanup removes only short-lived identity grants, never financial history.
func (s *Service) Cleanup(ctx context.Context) error {
	if e := s.EnsureSchema(ctx); e != nil {
		return e
	}
	_, e := s.db.ExecContext(ctx, `DELETE FROM v2_newapi_token WHERE expires_at<$1`, time.Now().Unix())
	if e != nil {
		return e
	}
	return s.CleanupWorkspaceActions(ctx)
}
