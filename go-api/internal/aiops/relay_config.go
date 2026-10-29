package aiops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const defaultFailoverTimeoutSeconds = 30
const maxFallbackRelays = 4

// RelayEndpoint is internal provider configuration. Public responses always use
// RelayConfig so that neither ciphertext nor plaintext credentials are returned.
type RelayEndpoint struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	APIKeyCipher string   `json:"api_key_cipher"`
	Models       []string `json:"models"`
	Enabled      bool     `json:"enabled"`
}

func publicRelays(endpoints []RelayEndpoint) []RelayConfig {
	result := make([]RelayConfig, 0, len(endpoints))
	for _, r := range endpoints {
		result = append(result, RelayConfig{ID: r.ID, Name: r.Name, BaseURL: r.BaseURL,
			Models: append([]string{}, r.Models...), Enabled: r.Enabled, HasAPIKey: r.APIKeyCipher != ""})
	}
	return result
}

func decodeRelayEndpoints(raw string) ([]RelayEndpoint, error) {
	result := []RelayEndpoint{}
	if raw == "" {
		return result, nil
	}
	if json.Unmarshal([]byte(raw), &result) != nil || len(result) > maxFallbackRelays {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, relay := range result {
		if relay.ID == "" || relay.ID == "primary" || seen[relay.ID] {
			return nil, ErrUnavailable
		}
		seen[relay.ID] = true
	}
	return result, nil
}

func normalizeRelayModels(models, globalModels []string) ([]string, error) {
	if len(models) == 0 {
		return []string{}, nil
	}
	normalized, err := normalizeModels(models)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(globalModels))
	for _, model := range globalModels {
		allowed[model] = true
	}
	for _, model := range normalized {
		if !allowed[model] {
			return nil, ErrValidation
		}
	}
	return normalized, nil
}

func (s *Service) normalizeFallbackRelays(requested *[]RelaySaveRequest, previous []RelayEndpoint, globalModels []string) ([]RelayEndpoint, error) {
	if requested == nil {
		// Omitted configuration keeps the same endpoints and credentials. Reject
		// model removals that would silently broaden a relay's routing scope.
		for _, relay := range previous {
			if _, err := normalizeRelayModels(relay.Models, globalModels); err != nil {
				return nil, err
			}
		}
		return append([]RelayEndpoint{}, previous...), nil
	}
	if len(*requested) > maxFallbackRelays {
		return nil, ErrValidation
	}
	existing := make(map[string]RelayEndpoint, len(previous))
	for _, relay := range previous {
		existing[relay.ID] = relay
	}
	seen := map[string]bool{}
	result := make([]RelayEndpoint, 0, len(*requested))
	for _, req := range *requested {
		name := strings.TrimSpace(req.Name)
		if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\t") ||
			len(req.APIKey) > 8192 || strings.ContainsAny(req.APIKey, "\r\n") || (req.APIKey != "" && req.ClearAPIKey) {
			return nil, ErrValidation
		}
		base, err := normalizeBaseURL(req.BaseURL)
		if err != nil || base == "" {
			return nil, ErrValidation
		}
		models, err := normalizeRelayModels(req.Models, globalModels)
		if err != nil {
			return nil, err
		}
		relay := RelayEndpoint{ID: req.ID, Name: name, BaseURL: base, Models: models, Enabled: req.Enabled}
		if req.ID == "" {
			var id [16]byte
			if _, err = rand.Read(id[:]); err != nil {
				return nil, err
			}
			relay.ID = "relay_" + hex.EncodeToString(id[:])
		} else {
			old, exists := existing[req.ID]
			if !exists || seen[req.ID] {
				return nil, ErrValidation
			}
			if old.APIKeyCipher != "" && old.BaseURL != base && req.APIKey == "" && !req.ClearAPIKey {
				return nil, ErrValidation
			}
			relay.APIKeyCipher = old.APIKeyCipher
		}
		seen[relay.ID] = true
		if req.ClearAPIKey {
			relay.APIKeyCipher = ""
		}
		if req.APIKey != "" {
			relay.APIKeyCipher, err = s.encryptSecret(req.APIKey)
			if err != nil {
				return nil, err
			}
		}
		if relay.Enabled && relay.APIKeyCipher == "" {
			return nil, ErrValidation
		}
		result = append(result, relay)
	}
	return result, nil
}

func (s *Service) loadProviderCandidates(ctx context.Context, model string) (Config, []RelayEndpoint, error) {
	cfg, cipher, fallbacks, err := s.loadConfigState(ctx)
	if err != nil {
		return cfg, nil, err
	}
	candidates := []RelayEndpoint{{ID: "primary", Name: "主站", BaseURL: cfg.BaseURL,
		APIKeyCipher: cipher, Models: append([]string{}, cfg.Models...), Enabled: cfg.Enabled}}
	if !cfg.FailoverEnabled {
		return cfg, candidates, nil
	}
	for _, relay := range fallbacks {
		if !relay.Enabled || relay.BaseURL == "" || relay.APIKeyCipher == "" {
			continue
		}
		models := relay.Models
		if len(models) == 0 {
			models = cfg.Models
		}
		for _, supported := range models {
			if supported == model {
				candidates = append(candidates, relay)
				break
			}
		}
	}
	return cfg, candidates, nil
}
