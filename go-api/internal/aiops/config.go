package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrValidation
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
			return "", ErrValidation
		}
	}
	if u.Path == "" {
		u.Path = "/v1"
	}
	if strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func normalizeModels(models []string) ([]string, error) {
	if len(models) == 0 || len(models) > 20 {
		return nil, ErrValidation
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range models {
		if m == "" || len(m) > 120 || strings.TrimSpace(m) != m || strings.ContainsAny(m, "\r\n\t") || seen[m] {
			return nil, ErrValidation
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}
func (s *Service) loadConfig(ctx context.Context) (Config, string, error) {
	result := Config{Models: append([]string{}, DefaultModels...)}
	result.ReasoningEfforts, _ = normalizeReasoningEfforts(result.Models, nil)
	var raw, cipher, reasoningRaw string
	err := s.db.QueryRowContext(ctx, `SELECT enabled,base_url,models::TEXT,api_key_cipher,reasoning_efforts::TEXT FROM v2_ai_config WHERE id=1`).Scan(&result.Enabled, &result.BaseURL, &raw, &cipher, &reasoningRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, "", nil
	}
	if err != nil {
		return result, "", err
	}
	if json.Unmarshal([]byte(raw), &result.Models) != nil {
		return result, "", ErrUnavailable
	}
	var stored map[string]string
	if json.Unmarshal([]byte(reasoningRaw), &stored) != nil {
		return result, "", ErrUnavailable
	}
	result.ReasoningEfforts, err = normalizeReasoningEfforts(result.Models, stored)
	if err != nil {
		return result, "", ErrUnavailable
	}
	result.HasAPIKey = cipher != ""
	return result, cipher, nil
}
func (s *Service) GetConfig(ctx context.Context, actorID int64) (Config, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return Config{}, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return Config{}, err
	}
	cfg, _, err := s.loadConfig(ctx)
	if err == nil {
		var isAdmin bool
		err = s.db.QueryRowContext(ctx, `SELECT is_admin=1 FROM v2_user WHERE id=$1 AND banned=0 AND (is_admin=1 OR is_staff=1)`, actorID).Scan(&isAdmin)
		if !isAdmin {
			cfg.BaseURL = ""
		}
	}
	return cfg, err
}
func (s *Service) SaveConfig(ctx context.Context, actorID int64, req ConfigSaveRequest) (Config, error) {
	var empty Config
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return empty, err
	}
	base, err := normalizeBaseURL(req.BaseURL)
	if err != nil {
		return empty, err
	}
	models, err := normalizeModels(req.Models)
	if err != nil {
		return empty, err
	}
	var efforts map[string]string
	if req.ReasoningEfforts != nil {
		efforts, err = normalizeReasoningEfforts(models, *req.ReasoningEfforts)
		if err != nil {
			return empty, err
		}
	}
	if len(req.APIKey) > 8192 || strings.ContainsAny(req.APIKey, "\r\n") || (req.APIKey != "" && req.ClearAPIKey) {
		return empty, ErrValidation
	}
	if err = s.ensureSchema(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return empty, err
	}
	if err = s.requireAdminTx(ctx, tx, actorID); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102903)`); err != nil {
		return empty, err
	}
	var cipher, oldBase, oldEfforts string
	err = tx.QueryRowContext(ctx, `SELECT api_key_cipher,base_url,reasoning_efforts::TEXT FROM v2_ai_config WHERE id=1 FOR UPDATE`).Scan(&cipher, &oldBase, &oldEfforts)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if req.ReasoningEfforts == nil {
		stored := map[string]string{}
		if oldEfforts != "" && json.Unmarshal([]byte(oldEfforts), &stored) != nil {
			return empty, ErrUnavailable
		}
		retained := map[string]string{}
		for _, model := range models {
			if value, exists := stored[model]; exists {
				retained[model] = value
			}
		}
		efforts, err = normalizeReasoningEfforts(models, retained)
		if err != nil {
			return empty, ErrUnavailable
		}
	}
	if cipher != "" && oldBase != base && req.APIKey == "" && !req.ClearAPIKey {
		return empty, ErrValidation
	}
	if req.ClearAPIKey {
		cipher = ""
	}
	if req.APIKey != "" {
		cipher, err = s.encryptSecret(req.APIKey)
		if err != nil {
			return empty, err
		}
	}
	if req.Enabled && (base == "" || cipher == "") {
		return empty, ErrValidation
	}
	raw, _ := json.Marshal(models)
	reasoningRaw, _ := json.Marshal(efforts)
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_ai_config(id,enabled,base_url,models,api_key_cipher,updated_by,updated_at,reasoning_efforts) VALUES(1,$1,$2,$3,$4,$5,$6,$7::JSONB) ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled,base_url=EXCLUDED.base_url,models=EXCLUDED.models,api_key_cipher=EXCLUDED.api_key_cipher,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at,reasoning_efforts=EXCLUDED.reasoning_efforts`, req.Enabled, base, string(raw), cipher, actorID, time.Now().Unix(), string(reasoningRaw))
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	if !req.Enabled {
		s.cancelWorker(0)
	}
	return Config{Enabled: req.Enabled, BaseURL: base, Models: models, HasAPIKey: cipher != "", ReasoningEfforts: efforts}, nil
}
