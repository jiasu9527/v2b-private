package aiops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestModelEndpointsCredentialIdentityAndReadiness(t *testing.T) {
	s := NewService(config.Config{AppKey: "model-fixture-key"}, nil, nil)
	defer s.Close()
	models := []string{"first", "second"}
	requests := map[string]ModelEndpointSaveRequest{
		"first":  {BaseURL: "https://first.example.test", APIKey: "key-first"},
		"second": {BaseURL: "https://second.example.test/v1/chat/completions", APIKey: "key-second"},
	}
	stored, err := s.normalizeModelEndpoints(&requests, nil, models)
	if err != nil || stored["first"].BaseURL != "https://first.example.test/v1" || stored["second"].BaseURL != "https://second.example.test/v1" {
		t.Fatal(stored, err)
	}
	for model, req := range requests {
		plain, e := s.decryptSecret(stored[model].APIKeyCipher)
		if e != nil || plain != req.APIKey {
			t.Fatal("key bound to wrong model", model, e)
		}
		requests[model] = ModelEndpointSaveRequest{BaseURL: stored[model].BaseURL}
	}
	kept, err := s.normalizeModelEndpoints(&requests, stored, models)
	if err != nil || !reflect.DeepEqual(kept, stored) {
		t.Fatal("blank keys not preserved by model", err)
	}
	if ready := readyModelIDs(models, "", "", stored); !reflect.DeepEqual(ready, models) {
		t.Fatal(ready)
	}
	if ready := readyModelIDs(models, "https://default/v1", "default-key", map[string]modelEndpoint{"first": {BaseURL: "https://first/v1"}}); !reflect.DeepEqual(ready, []string{"second"}) {
		t.Fatal("empty explicit key fell back", ready)
	}
	copyPublic, _ := json.Marshal(publicModelEndpoints(stored))
	for _, forbidden := range []string{"key-first", "key-second", "api_key_cipher", stored["first"].APIKeyCipher} {
		if strings.Contains(string(copyPublic), forbidden) {
			t.Fatal("public projection leaks secret")
		}
	}
	requests["first"] = ModelEndpointSaveRequest{BaseURL: "https://other.example.test/v1"}
	if _, err = s.normalizeModelEndpoints(&requests, stored, models); !errors.Is(err, ErrValidation) {
		t.Fatal("old key followed URL", err)
	}
	requests["first"] = ModelEndpointSaveRequest{BaseURL: "https://other.example.test/v1", ClearAPIKey: true}
	cleared, err := s.normalizeModelEndpoints(&requests, stored, models)
	if err != nil || cleared["first"].APIKeyCipher != "" || cleared["second"].APIKeyCipher != stored["second"].APIKeyCipher {
		t.Fatal("clear changed unrelated model", err)
	}
	if _, err = s.normalizeModelEndpoints(nil, stored, []string{"second"}); !errors.Is(err, ErrValidation) {
		t.Fatal("implicit model removal accepted", err)
	}
	empty := map[string]ModelEndpointSaveRequest{}
	if result, err := s.normalizeModelEndpoints(&empty, stored, models); err != nil || len(result) != 0 {
		t.Fatal("explicit reset failed", err)
	}
	retained, err := s.normalizeModelEndpoints(nil, stored, models)
	if err != nil || !reflect.DeepEqual(retained, stored) {
		t.Fatal("legacy omitted config lost", err)
	}
}

func TestModelEndpointsValidationAndStrictJSON(t *testing.T) {
	s := NewService(config.Config{AppKey: "model-fixture-key"}, nil, nil)
	defer s.Close()
	for _, req := range []ModelEndpointSaveRequest{
		{}, {BaseURL: "http://untrusted.example.test/v1"}, {BaseURL: "https://x.test/v1?key=foo"},
		{BaseURL: "https://x.test/v1", APIKey: "bad\r\nkey"}, {BaseURL: "https://x.test/v1", APIKey: strings.Repeat("a", 8193)},
		{BaseURL: "https://x.test/v1", APIKey: "key", ClearAPIKey: true},
	} {
		requested := map[string]ModelEndpointSaveRequest{"model": req}
		if _, err := s.normalizeModelEndpoints(&requested, nil, []string{"model"}); !errors.Is(err, ErrValidation) {
			t.Fatal("invalid config accepted")
		}
	}
	unknown := map[string]ModelEndpointSaveRequest{"other": {BaseURL: "https://x.test/v1", APIKey: "secret"}}
	if _, err := s.normalizeModelEndpoints(&unknown, nil, []string{"model"}); !errors.Is(err, ErrValidation) {
		t.Fatal("unknown model accepted", err)
	}
	for _, raw := range []string{`null`, `[]`, `{"api_key_cipher":"injected"}`, `{"base_url":null}`, `{"api_key":null}`, `{"clear_api_key":null}`, `{"base_url":"a","base_url":"b"}`, `{"api_key":123}`} {
		var req ModelEndpointSaveRequest
		if json.Unmarshal([]byte(raw), &req) == nil {
			t.Fatal("bad JSON accepted", raw)
		}
	}
	var req ModelEndpointSaveRequest
	if err := json.Unmarshal([]byte(`{"base_url":"https://example.test/v1","api_key":"secret"}`), &req); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `[]`, `{"other":{"base_url":"https://x.test/v1"}}`, `{"model":{"base_url":"http://x.test/v1"}}`} {
		if _, err := decodeModelEndpoints(raw, []string{"model"}); !errors.Is(err, ErrUnavailable) {
			t.Fatal("bad persisted routing accepted", raw, err)
		}
	}
	// URL normalization is stable when an administrator pastes the completion URL.
	for _, raw := range []string{"https://x.test/chat/completions", "https://x.test/v1/chat/completions", "https://x.test"} {
		base, err := normalizeBaseURL(raw)
		if err != nil {
			t.Fatal(err)
		}
		again, err := normalizeBaseURL(base)
		if err != nil || again != base {
			t.Fatal("URL is not canonical", base, again, err)
		}
	}
}

func TestModelEndpointsConfigJSONIdentities(t *testing.T) {
	for _, raw := range []string{
		`{"model_endpoints":null}`, `{"model_endpoints":[]}`,
		`{"model_endpoints":{"model":null}}`,
		`{"model_endpoints":{"model":{},"model":{}}}`,
		`{"model_endpoints":{},"model_endpoints":{}}`,
		`{"model_endpoints":{"model":{"api_key_cipher":"injected"}}}`,
		`{"unknown":"must still reject unknown fields"}`,
	} {
		var req ConfigSaveRequest
		if json.Unmarshal([]byte(raw), &req) == nil {
			t.Fatal("ambiguous config JSON accepted", raw)
		}
	}
	for _, tc := range []struct {
		raw     string
		present bool
	}{{`{}`, false}, {`{"model_endpoints":{}}`, true}} {
		var req ConfigSaveRequest
		if err := json.Unmarshal([]byte(tc.raw), &req); err != nil || (req.ModelEndpoints != nil) != tc.present {
			t.Fatal("omission/reset distinction lost", err)
		}
	}
}
