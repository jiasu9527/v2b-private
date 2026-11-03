package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func modelEndpointRequest(t *testing.T, value map[string]any) ConfigSaveRequest {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var req ConfigSaveRequest
	if err = json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func modelEndpointPublic(t *testing.T, cfg Config) map[string]any {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func modelEndpointSnapshot(t *testing.T, f staffAIFixture) string {
	t.Helper()
	var raw string
	if err := f.db.QueryRow(`SELECT row_to_json(c)::TEXT FROM v2_ai_config c WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestModelEndpointPostgresSaveIsolationAndCompatibility(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	models := DefaultModels[:2]
	endpoints := map[string]any{
		models[0]: map[string]any{"base_url": "https://gpt.example.test", "api_key": "gpt-fixture-secret"},
		models[1]: map[string]any{"base_url": "https://claude.example.test/v1/chat/completions", "api_key": "claude-fixture-secret"},
	}
	request := func(overrides map[string]any) ConfigSaveRequest {
		value := map[string]any{"enabled": true, "base_url": "", "clear_api_key": true, "models": models, "model_endpoints": endpoints}
		for k, v := range overrides {
			value[k] = v
		}
		return modelEndpointRequest(t, value)
	}
	cfg, err := f.s.SaveConfig(ctx, f.admin, request(nil))
	if err != nil {
		t.Fatal("defaultless per-model save", err)
	}
	view := modelEndpointPublic(t, cfg)
	if cfg.HasAPIKey || cfg.BaseURL != "" || !reflect.DeepEqual(view["ready_models"], []any{models[0], models[1]}) {
		t.Fatal("primary readiness is incorrect", view)
	}
	public, ok := view["model_endpoints"].(map[string]any)
	if !ok || len(public) != 2 {
		t.Fatal("missing endpoint public projection", view)
	}
	for _, model := range models {
		entry := public[model].(map[string]any)
		if entry["has_api_key"] != true || len(entry) != 2 {
			t.Fatal("unexpected endpoint projection", entry)
		}
		_, candidates, e := f.s.loadProviderCandidates(ctx, model)
		if e != nil || len(candidates) != 1 {
			t.Fatal("defaultless candidate resolution", e)
		}
		key, e := f.s.decryptSecret(candidates[0].APIKeyCipher)
		prefix := "gpt"
		if model == models[1] {
			prefix = "claude"
		}
		if e != nil || key != prefix+"-fixture-secret" || candidates[0].BaseURL != "https://"+prefix+".example.test/v1" {
			t.Fatal("model address/key binding mismatch", model, e)
		}
	}
	snapshot := modelEndpointSnapshot(t, f)
	if strings.Contains(snapshot, "gpt-fixture-secret") || strings.Contains(snapshot, "claude-fixture-secret") {
		t.Fatal("plaintext model key persisted")
	}
	for _, actor := range []int64{f.admin, f.staff} {
		got, e := f.s.GetConfig(ctx, actor)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), "fixture-secret") || strings.Contains(string(raw), "api_key_cipher") {
			t.Fatal("key exposed in config")
		}
		if actor == f.staff {
			staff := modelEndpointPublic(t, got)
			if strings.Contains(string(raw), "example.test") || len(staff["model_endpoints"].(map[string]any)) != 0 || !reflect.DeepEqual(staff["ready_models"], []any{models[0], models[1]}) {
				t.Fatal("staff endpoint leak or readiness lost", staff)
			}
		}
	}
	if _, err = f.s.SaveConfig(ctx, f.staff, request(nil)); !errors.Is(err, ErrForbidden) || snapshot != modelEndpointSnapshot(t, f) {
		t.Fatal("staff save not rejected", err)
	}
	// An old client that knows nothing about overrides preserves them.
	legacy := modelEndpointRequest(t, map[string]any{"enabled": true, "base_url": "", "models": models})
	if _, err = f.s.SaveConfig(ctx, f.admin, legacy); err != nil {
		t.Fatal("omission discarded dedicated credentials", err)
	}
	blank := map[string]any{
		models[0]: map[string]any{"base_url": "https://gpt.example.test/v1", "api_key": ""},
		models[1]: map[string]any{"base_url": "https://claude.example.test/v1", "api_key": ""},
	}
	if _, err = f.s.SaveConfig(ctx, f.admin, request(map[string]any{"model_endpoints": blank})); err != nil {
		t.Fatal("same model+URL did not retain key", err)
	}
	for _, tc := range []struct {
		name    string
		changes map[string]any
	}{
		{"new URL cannot inherit key", map[string]any{"model_endpoints": map[string]any{models[0]: map[string]any{"base_url": "https://wrong.example.test/v1"}, models[1]: blank[models[1]]}}},
		{"new model cannot inherit other key", map[string]any{"model_endpoints": map[string]any{models[0]: map[string]any{"base_url": "https://claude.example.test/v1"}, models[1]: blank[models[1]]}}},
		{"unknown model", map[string]any{"model_endpoints": map[string]any{"unlisted": map[string]any{"base_url": "https://gpt.example.test/v1", "api_key": "fresh"}}}},
		{"empty map has no default", map[string]any{"model_endpoints": map[string]any{}}},
		{"missing second primary", map[string]any{"model_endpoints": map[string]any{models[0]: blank[models[0]]}}},
		{"empty URL", map[string]any{"model_endpoints": map[string]any{models[0]: map[string]any{"base_url": "", "api_key": "fresh"}}}},
		{"cleared enabled key", map[string]any{"model_endpoints": map[string]any{models[0]: map[string]any{"base_url": "https://gpt.example.test/v1", "clear_api_key": true}, models[1]: blank[models[1]]}}},
		{"key and clear conflict", map[string]any{"model_endpoints": map[string]any{models[0]: map[string]any{"base_url": "https://gpt.example.test/v1", "api_key": "fresh", "clear_api_key": true}, models[1]: blank[models[1]]}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := modelEndpointSnapshot(t, f)
			if _, e := f.s.SaveConfig(ctx, f.admin, request(tc.changes)); !errors.Is(e, ErrValidation) || before != modelEndpointSnapshot(t, f) {
				t.Fatal("invalid save did not fail atomically", e)
			}
		})
	}
	legacy.Models = models[:1]
	if _, err = f.s.SaveConfig(ctx, f.admin, legacy); !errors.Is(err, ErrValidation) {
		t.Fatal("implicit model removal accepted", err)
	}
	// Explicit endpoint removal restores the independently configured default.
	removed, err := f.s.SaveConfig(ctx, f.admin, request(map[string]any{"base_url": "https://default.example.test/v1", "api_key": "default-fixture-secret", "clear_api_key": false, "model_endpoints": map[string]any{}}))
	if err != nil || len(modelEndpointPublic(t, removed)["model_endpoints"].(map[string]any)) != 0 {
		t.Fatal("override removal failed", err)
	}
	_, candidates, err := f.s.loadProviderCandidates(ctx, models[0])
	if err != nil || len(candidates) != 1 || candidates[0].BaseURL != "https://default.example.test/v1" {
		t.Fatal("default not restored", err)
	}
	// A new identity after removal never resurrects the old model's secret.
	if _, err = f.s.SaveConfig(ctx, f.admin, request(map[string]any{"base_url": "https://default.example.test/v1", "clear_api_key": false, "model_endpoints": blank})); !errors.Is(err, ErrValidation) {
		t.Fatal("deleted override resurrected secret", err)
	}
	// Disabled configuration may be staged without credentials, but readiness
	// must not borrow the default's key for an explicit incomplete override.
	staged, err := f.s.SaveConfig(ctx, f.admin, request(map[string]any{"enabled": false, "base_url": "https://default.example.test/v1", "clear_api_key": false, "model_endpoints": blank}))
	if err != nil || len(modelEndpointPublic(t, staged)["ready_models"].([]any)) != 0 {
		t.Fatal("disabled empty override borrowed shared key", err)
	}
	if _, err = f.s.SaveConfig(ctx, f.admin, modelEndpointRequest(t, map[string]any{"enabled": true, "base_url": "https://default.example.test/v1", "models": models})); !errors.Is(err, ErrValidation) {
		t.Fatal("enabled incomplete dedicated config", err)
	}
}

func TestModelEndpointPostgresConcurrentSnapshots(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	model := DefaultModels[0]
	request := func(label string) ConfigSaveRequest {
		return modelEndpointRequest(t, map[string]any{"enabled": true, "base_url": "https://fixture.example.test/v1", "models": DefaultModels, "model_endpoints": map[string]any{model: map[string]any{"base_url": "https://" + label + ".example.test/v1", "api_key": label + "-key"}}})
	}
	if _, err := f.s.SaveConfig(ctx, f.admin, request("initial")); err != nil {
		t.Fatal(err)
	}
	requests := make([]ConfigSaveRequest, 12)
	for i := range requests {
		requests[i] = request(fmt.Sprintf("worker-%d", i))
	}
	results := make(chan error, 50)
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, req := range requests {
		group.Add(1)
		go func(req ConfigSaveRequest) {
			defer group.Done()
			<-start
			_, e := f.s.SaveConfig(ctx, f.admin, req)
			if e != nil {
				results <- e
			}
		}(req)
	}
	close(start)
	for i := 0; i < 25; i++ {
		_, endpoints, err := f.s.loadProviderCandidates(ctx, model)
		if err != nil || len(endpoints) != 1 {
			results <- fmt.Errorf("bad snapshot: %v", err)
			continue
		}
		label := strings.TrimSuffix(strings.TrimPrefix(endpoints[0].BaseURL, "https://"), ".example.test/v1")
		plain, e := f.s.decryptSecret(endpoints[0].APIKeyCipher)
		if e != nil || plain != label+"-key" {
			results <- fmt.Errorf("mixed endpoint/key snapshot: %v", e)
		}
	}
	group.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}
}

func TestModelEndpointPostgresUpgradePreservesDefault(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	_, original, err := f.s.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, `ALTER TABLE v2_ai_config DROP COLUMN model_endpoints`); err != nil {
		t.Fatal(err)
	}
	upgraded := NewService(f.s.cfg, f.db, f.s.panel)
	defer upgraded.Close()
	if err = upgraded.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, cipher, err := upgraded.loadConfig(ctx)
	if err != nil || cipher != original || !cfg.Enabled || !cfg.HasAPIKey || cfg.BaseURL != "https://fixture.example.test/v1" {
		t.Fatal("upgrade altered default", err)
	}
	if len(modelEndpointPublic(t, cfg)["model_endpoints"].(map[string]any)) != 0 {
		t.Fatal("upgrade created overrides")
	}
}
