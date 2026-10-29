package aiops

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"io"
	"sync"
	"time"
)

type Service struct {
	cfg         config.Config
	db          *sql.DB
	panel       admin.Service
	schemaMu    sync.Mutex
	schemaOK    bool
	mu          sync.Mutex
	workers     map[int64]context.CancelFunc
	workerTurns map[int64]int64
	closed      bool
	wg          sync.WaitGroup
	slots       chan struct{}
	httpClient  providerHTTPClient
}

func NewService(cfg config.Config, db *sql.DB, panel admin.Service) *Service {
	return &Service{cfg: cfg, db: db, panel: panel, workers: map[int64]context.CancelFunc{}, workerTurns: map[int64]int64{}, slots: make(chan struct{}, 4), httpClient: newProviderHTTPClient()}
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.workers {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) cancelWorker(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 {
		for key, cancel := range s.workers {
			if key > 0 {
				cancel()
			}
		}
	} else if cancel := s.workers[id]; cancel != nil {
		cancel()
	}
}
func (s *Service) requireAdmin(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	if id <= 0 {
		return ErrForbidden
	}
	var found int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE id=$1 AND is_admin=1 AND banned=0`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}
func (s *Service) requireAdminTx(ctx context.Context, tx *sql.Tx, id int64) error {
	if id <= 0 {
		return ErrForbidden
	}
	var found int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE id=$1 AND is_admin=1 AND banned=0 FOR SHARE`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}
func (s *Service) requireSessionTx(ctx context.Context, tx *sql.Tx, id int64) error {
	var found int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM v2_ai_session WHERE id=$1 FOR UPDATE`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s *Service) secretAEAD() (cipher.AEAD, error) {
	if s.cfg.AppKey == "" {
		return nil, ErrUnavailable
	}
	key := sha256.Sum256([]byte("forest-aiops-secret-v1\x00" + s.cfg.AppKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Service) encryptSecret(plain string) (string, error) {
	aead, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	raw := aead.Seal(nonce, nonce, []byte(plain), []byte("forest-aiops-v1"))
	return "v1:" + base64.RawStdEncoding.EncodeToString(raw), nil
}
func (s *Service) decryptSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	aead, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	if len(value) < 3 || value[:3] != "v1:" {
		return "", ErrUnavailable
	}
	raw, err := base64.RawStdEncoding.DecodeString(value[3:])
	if err != nil || len(raw) < aead.NonceSize() {
		return "", ErrUnavailable
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte("forest-aiops-v1"))
	if err != nil {
		return "", ErrUnavailable
	}
	return string(plain), nil
}

const maxSessionMessages = 400
const maxSessions = 200
const maxContentBytes = 16000
const turnLifetime = 150 * time.Second
