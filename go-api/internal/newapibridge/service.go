// Package newapibridge connects the panel wallet and identity to an independently
// deployed New API instance. It never writes subscription orders or commissions.
package newapibridge

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"forest/go-api/internal/config"
)

var (
	ErrInvalid     = errors.New("New API 参数无效")
	ErrDisabled    = errors.New("New API 接入尚未启用")
	ErrUnavailable = errors.New("New API 接入暂时不可用")
	ErrConflict    = errors.New("配置或交易已变化，请刷新核对")
	ErrForbidden   = errors.New("无权操作此账号或交易")
)

type Service struct {
	db          *sql.DB
	appKey      string
	client      *http.Client
	siteURL     string
	schemaMu    sync.Mutex
	schemaReady bool
}

func NewService(cfg config.Config, db *sql.DB) *Service {
	return &Service{db: db, appKey: cfg.AppKey, siteURL: cfg.AppURL, client: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Service) seal(data []byte) (string, error) {
	if s == nil || s.appKey == "" {
		return "", ErrUnavailable
	}
	key := sha256.Sum256([]byte("forest/newapi/v1\x00" + s.appKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, data, []byte("newapi/v1"))), nil
}

func (s *Service) open(value string) ([]byte, error) {
	if s == nil || s.appKey == "" {
		return nil, ErrUnavailable
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrUnavailable
	}
	key := sha256.Sum256([]byte("forest/newapi/v1\x00" + s.appKey))
	block, _ := aes.NewCipher(key[:])
	gcm, _ := cipher.NewGCM(block)
	if len(raw) < gcm.NonceSize() {
		return nil, ErrUnavailable
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte("newapi/v1"))
	if err != nil {
		return nil, ErrUnavailable
	}
	return plain, nil
}

func opaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(763918201)`); err != nil {
		return err
	}
	statements := append(append(append([]string{}, schemaStatements...), workspaceAccountSchema...), workspaceChatSchema...)
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("newapi schema: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.schemaReady = true
	return nil
}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS v2_newapi_settings (id INTEGER PRIMARY KEY CHECK(id=1), revision BIGINT NOT NULL, config_cipher TEXT NOT NULL, updated_by BIGINT NOT NULL, updated_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_token (token_hash TEXT PRIMARY KEY, purpose TEXT NOT NULL, payload_cipher TEXT NOT NULL, expires_at BIGINT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_token_expiry ON v2_newapi_token(expires_at)`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_checkout(id TEXT PRIMARY KEY, api_url TEXT NOT NULL, remote_trade_no TEXT NOT NULL, user_id BIGINT NOT NULL, remote_user_id BIGINT NOT NULL, amount_fen BIGINT NOT NULL CHECK(amount_fen>0), payment_method TEXT NOT NULL, config_revision BIGINT NOT NULL, config_cipher TEXT NOT NULL, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL, UNIQUE(api_url,remote_trade_no))`,
	`CREATE TABLE IF NOT EXISTS v2_newapi_transfer(id BIGSERIAL PRIMARY KEY, checkout_id TEXT NOT NULL UNIQUE REFERENCES v2_newapi_checkout(id), api_url TEXT NOT NULL, remote_trade_no TEXT NOT NULL, user_id BIGINT NOT NULL, remote_user_id BIGINT NOT NULL, amount_fen BIGINT NOT NULL CHECK(amount_fen>0), balance_before BIGINT NOT NULL, balance_after BIGINT NOT NULL CHECK(balance_after>=0), status TEXT NOT NULL CHECK(status IN ('pending','succeeded')), attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at BIGINT NOT NULL, lease_until BIGINT NOT NULL DEFAULT 0, lease_token TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL, completed_at BIGINT NOT NULL DEFAULT 0, last_retry_by BIGINT NOT NULL DEFAULT 0, UNIQUE(api_url,remote_trade_no))`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_transfer_user ON v2_newapi_transfer(user_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS v2_newapi_transfer_pending ON v2_newapi_transfer(status,next_attempt_at,lease_until)`,
}
