package aiops

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// A supplied route must be an object, not null. Reject ambiguous duplicate
// fields and unknown values before credentials are retained or encrypted.
func (r *ModelEndpointSaveRequest) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrValidation
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || (key != "base_url" && key != "api_key" && key != "clear_api_key") {
			return ErrValidation
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrValidation
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return ErrValidation
	}
	type plain ModelEndpointSaveRequest
	var value plain
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrValidation
	}
	*r = ModelEndpointSaveRequest(value)
	return nil
}

// modelEndpoint is a private storage projection; public configuration must use
// ModelEndpointConfig so neither encrypted nor plaintext keys leave the server.
type modelEndpoint struct {
	BaseURL      string `json:"base_url"`
	APIKeyCipher string `json:"api_key_cipher"`
}

// Model names are credential identities. Do not silently choose the last entry
// if a configuration contains the same model twice.
func (r *ConfigSaveRequest) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrValidation
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return ErrValidation
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return ErrValidation
		}
		if key == "fallback_models" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrValidation
		}
		if key == "model_endpoints" {
			entries := json.NewDecoder(bytes.NewReader(value))
			opening, e := entries.Token()
			if e != nil || opening != json.Delim('{') {
				return ErrValidation
			}
			models := map[string]bool{}
			for entries.More() {
				name, e := entries.Token()
				model, valid := name.(string)
				if e != nil || !valid || models[model] || len(models) >= 20 {
					return ErrValidation
				}
				models[model] = true
				var entry ModelEndpointSaveRequest
				if entries.Decode(&entry) != nil {
					return ErrValidation
				}
			}
			if closing, e := entries.Token(); e != nil || closing != json.Delim('}') || entries.Decode(new(any)) != io.EOF {
				return ErrValidation
			}
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return ErrValidation
	}
	type plain ConfigSaveRequest
	var value plain
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrValidation
	}
	*r = ConfigSaveRequest(value)
	return nil
}

func decodeModelEndpoints(raw string, models []string) (map[string]modelEndpoint, error) {
	endpoints := map[string]modelEndpoint{}
	if raw != "" && (json.Unmarshal([]byte(raw), &endpoints) != nil || endpoints == nil) {
		return nil, ErrUnavailable
	}
	if len(endpoints) > 20 {
		return nil, ErrUnavailable
	}
	allowed := map[string]bool{}
	for _, model := range models {
		allowed[model] = true
	}
	for model, endpoint := range endpoints {
		base, err := normalizeBaseURL(endpoint.BaseURL)
		if !allowed[model] || err != nil || base == "" || base != endpoint.BaseURL {
			return nil, ErrUnavailable
		}
	}
	return endpoints, nil
}

func publicModelEndpoints(endpoints map[string]modelEndpoint) map[string]ModelEndpointConfig {
	out := make(map[string]ModelEndpointConfig, len(endpoints))
	for model, endpoint := range endpoints {
		out[model] = ModelEndpointConfig{BaseURL: endpoint.BaseURL, HasAPIKey: endpoint.APIKeyCipher != ""}
	}
	return out
}

func (s *Service) normalizeModelEndpoints(requested *map[string]ModelEndpointSaveRequest, previous map[string]modelEndpoint, models []string) (map[string]modelEndpoint, error) {
	allowed := map[string]bool{}
	for _, model := range models {
		allowed[model] = true
	}
	out := map[string]modelEndpoint{}
	if requested == nil {
		for model, endpoint := range previous {
			// A legacy client removing a model cannot erase its saved routing and
			// credentials invisibly. Require an explicit replacement map.
			if !allowed[model] {
				return nil, ErrValidation
			}
			out[model] = endpoint
		}
		return out, nil
	}
	if len(*requested) > 20 {
		return nil, ErrValidation
	}
	for model, req := range *requested {
		if !allowed[model] || len(req.APIKey) > 8192 || strings.ContainsAny(req.APIKey, "\r\n") || (req.APIKey != "" && req.ClearAPIKey) {
			return nil, ErrValidation
		}
		base, err := normalizeBaseURL(req.BaseURL)
		if err != nil || base == "" {
			return nil, ErrValidation
		}
		old := previous[model]
		if old.APIKeyCipher != "" && old.BaseURL != base && req.APIKey == "" && !req.ClearAPIKey {
			return nil, ErrValidation
		}
		endpoint := modelEndpoint{BaseURL: base, APIKeyCipher: old.APIKeyCipher}
		if req.ClearAPIKey {
			endpoint.APIKeyCipher = ""
		}
		if req.APIKey != "" {
			endpoint.APIKeyCipher, err = s.encryptSecret(req.APIKey)
			if err != nil {
				return nil, err
			}
		}
		out[model] = endpoint
	}
	return out, nil
}

func readyModelIDs(models []string, base, cipher string, endpoints map[string]modelEndpoint) []string {
	ready := []string{}
	for _, model := range models {
		endpoint, specific := endpoints[model]
		if specific {
			if endpoint.BaseURL != "" && endpoint.APIKeyCipher != "" {
				ready = append(ready, model)
			}
		} else if base != "" && cipher != "" {
			ready = append(ready, model)
		}
	}
	return ready
}

func (cfg Config) modelReady(model string) bool {
	for _, ready := range cfg.ReadyModels {
		if ready == model {
			return true
		}
	}
	return false
}
