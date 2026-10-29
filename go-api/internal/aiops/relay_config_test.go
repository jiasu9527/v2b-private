package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestRelayConfigValidation(t *testing.T) {
	s := NewService(config.Config{AppKey: "relay-config-fixture"}, nil, nil)
	defer s.Close()
	valid := RelaySaveRequest{Name: "备用一", BaseURL: "https://backup.example.test", APIKey: "backup-secret", Enabled: true}
	for _, tt := range []struct {
		name   string
		change func(*RelaySaveRequest)
	}{
		{"missing name", func(r *RelaySaveRequest) { r.Name = " " }},
		{"long name", func(r *RelaySaveRequest) { r.Name = strings.Repeat("站", 101) }},
		{"newline name", func(r *RelaySaveRequest) { r.Name = "a\nb" }},
		{"missing url", func(r *RelaySaveRequest) { r.BaseURL = "" }},
		{"unsafe url", func(r *RelaySaveRequest) { r.BaseURL = "http://backup.example.test" }},
		{"unknown id", func(r *RelaySaveRequest) { r.ID = "relay-guessed" }},
		{"primary id", func(r *RelaySaveRequest) { r.ID = "primary" }},
		{"unknown model", func(r *RelaySaveRequest) { r.Models = []string{"other-model"} }},
		{"duplicate model", func(r *RelaySaveRequest) { r.Models = []string{DefaultModels[0], DefaultModels[0]} }},
		{"missing enabled key", func(r *RelaySaveRequest) { r.APIKey = "" }},
		{"conflicting key", func(r *RelaySaveRequest) { r.ClearAPIKey = true }},
		{"header injection", func(r *RelaySaveRequest) { r.APIKey = "key\r\nheader" }},
		{"long key", func(r *RelaySaveRequest) { r.APIKey = strings.Repeat("x", 8193) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := valid
			tt.change(&r)
			requested := []RelaySaveRequest{r}
			if _, err := s.normalizeFallbackRelays(&requested, nil, DefaultModels); !errors.Is(err, ErrValidation) {
				t.Fatal("invalid relay accepted", err)
			}
		})
	}
	five := []RelaySaveRequest{valid, valid, valid, valid, valid}
	if _, err := s.normalizeFallbackRelays(&five, nil, DefaultModels); !errors.Is(err, ErrValidation) {
		t.Fatal("five fallback relays accepted", err)
	}
	four := five[:4]
	created, err := s.normalizeFallbackRelays(&four, nil, DefaultModels)
	if err != nil || len(created) != 4 {
		t.Fatal("four relays rejected", err)
	}
	seen := map[string]bool{}
	for _, relay := range created {
		if !strings.HasPrefix(relay.ID, "relay_") || len(relay.ID) != 38 || seen[relay.ID] {
			t.Fatal("relay IDs are not independently generated", relay.ID)
		}
		seen[relay.ID] = true
		plain, err := s.decryptSecret(relay.APIKeyCipher)
		if err != nil || plain != valid.APIKey {
			t.Fatal("wrong encrypted key", err)
		}
	}
	serialized, err := json.Marshal(publicRelays(created))
	if err != nil || strings.Contains(string(serialized), "api_key_cipher") || strings.Contains(string(serialized), valid.APIKey) || strings.Contains(string(serialized), created[0].APIKeyCipher) {
		t.Fatal("public projection contains a credential", err)
	}
}

func TestRelayConfigCredentialIdentity(t *testing.T) {
	s := NewService(config.Config{AppKey: "relay-config-fixture"}, nil, nil)
	defer s.Close()
	initial := []RelaySaveRequest{
		{Name: "一", BaseURL: "https://one.example.test", APIKey: "key-one", Enabled: true, Models: []string{DefaultModels[0]}},
		{Name: "二", BaseURL: "https://two.example.test", APIKey: "key-two", Enabled: true},
	}
	old, err := s.normalizeFallbackRelays(&initial, nil, DefaultModels)
	if err != nil {
		t.Fatal(err)
	}
	requests := []RelaySaveRequest{
		{ID: old[1].ID, Name: "二改名", BaseURL: old[1].BaseURL, Enabled: true},
		{ID: old[0].ID, Name: "一", BaseURL: old[0].BaseURL, Enabled: true, Models: old[0].Models},
	}
	reordered, err := s.normalizeFallbackRelays(&requests, old, DefaultModels)
	if err != nil || reordered[0].APIKeyCipher != old[1].APIKeyCipher || reordered[1].APIKeyCipher != old[0].APIKeyCipher {
		t.Fatal("keys followed array order instead of IDs", err)
	}
	requests[0].BaseURL = "https://other.example.test"
	if _, err = s.normalizeFallbackRelays(&requests, old, DefaultModels); !errors.Is(err, ErrValidation) {
		t.Fatal("old key carried into a different endpoint", err)
	}
	requests[0].ClearAPIKey, requests[0].Enabled = true, false
	cleared, err := s.normalizeFallbackRelays(&requests, old, DefaultModels)
	if err != nil || cleared[0].APIKeyCipher != "" {
		t.Fatal("explicit key clearing rejected", err)
	}
	requests[0].ClearAPIKey, requests[0].Enabled, requests[0].APIKey = false, true, "new-key"
	changed, err := s.normalizeFallbackRelays(&requests, old, DefaultModels)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.decryptSecret(changed[0].APIKeyCipher)
	if err != nil || plain != "new-key" {
		t.Fatal("new endpoint key mismatch", err)
	}
	duplicated := []RelaySaveRequest{requests[1], requests[1]}
	if _, err = s.normalizeFallbackRelays(&duplicated, old, DefaultModels); !errors.Is(err, ErrValidation) {
		t.Fatal("duplicate relay ID accepted", err)
	}
	if _, err = s.normalizeFallbackRelays(nil, old, DefaultModels[1:]); !errors.Is(err, ErrValidation) {
		t.Fatal("removed model silently broadened relay support", err)
	}
}

func TestRelayConfigNoRowDefaults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	mock.ExpectQuery(`SELECT enabled,base_url`).WillReturnError(sql.ErrNoRows)
	cfg, candidates, err := s.loadProviderCandidates(context.Background(), DefaultModels[0])
	if err != nil || cfg.FailoverEnabled || cfg.FailoverTimeoutSeconds != 30 || len(cfg.FallbackRelays) != 0 || len(candidates) != 1 || candidates[0].ID != "primary" {
		t.Fatal("unexpected default config", cfg, candidates, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
