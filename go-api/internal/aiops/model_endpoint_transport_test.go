package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelEndpointPostgresRealTransportRoutingAndFailover(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	models := DefaultModels[:3]
	type observed struct{ Endpoint, Model, Authorization string }
	var mu sync.Mutex
	var calls []observed
	var primaryFails atomic.Bool
	server := func(name, key string, accept []string) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload completionRequest
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&payload) != nil {
				t.Error("invalid compatible request")
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			calls = append(calls, observed{name, payload.Model, r.Header.Get("Authorization")})
			mu.Unlock()
			allowed := false
			for _, model := range accept {
				if payload.Model == model {
					allowed = true
				}
			}
			if !allowed || r.Header.Get("Authorization") != "Bearer "+key {
				t.Error("model or credential reached wrong endpoint", name)
				w.WriteHeader(400)
				return
			}
			if name == "gpt" && primaryFails.Load() {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "completed at " + name}, "finish_reason": "stop"}}})
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	gpt := server("gpt", "gpt-private-fixture", models[:1])
	claude := server("claude", "claude-private-fixture", models[1:2])
	shared := server("shared", "shared-private-fixture", models[2:3])
	backup := server("gpt-backup", "backup-private-fixture", models[:1])
	wrong := server("wrong-backup", "wrong-private-fixture", models[1:2])
	settings := map[string]any{"enabled": true, "base_url": shared.URL, "api_key": "shared-private-fixture", "models": models, "model_endpoints": map[string]any{models[0]: map[string]any{"base_url": gpt.URL, "api_key": "gpt-private-fixture"}, models[1]: map[string]any{"base_url": claude.URL, "api_key": "claude-private-fixture"}}, "fallback_relays": []any{map[string]any{"name": "wrong model first", "base_url": wrong.URL, "api_key": "wrong-private-fixture", "models": models[1:2], "enabled": true}, map[string]any{"name": "GPT only", "base_url": backup.URL, "api_key": "backup-private-fixture", "models": models[:1], "enabled": true}}, "failover_enabled": false}
	if _, err := f.s.SaveConfig(ctx, f.admin, modelEndpointRequest(t, settings)); err != nil {
		t.Fatal(err)
	}
	for i, model := range models {
		out, err := f.s.complete(ctx, model, []providerMessage{{Role: "user", Content: "synthetic routing check"}})
		if err != nil || out.Content != "completed at "+[]string{"gpt", "claude", "shared"}[i] {
			t.Fatal("wrong selected provider", i, err)
		}
	}
	primaryFails.Store(true)
	if _, err := f.s.complete(ctx, models[0], []providerMessage{{Role: "user", Content: "synthetic no fallback"}}); err == nil {
		t.Fatal("failed model silently fell back despite disabled failover")
	}
	mu.Lock()
	count := len(calls)
	mu.Unlock()
	if count != 4 {
		t.Fatal("unexpected backup/shared calls", count)
	}
	// Omit endpoint/backup settings: enable failover without rebinding credentials.
	if _, err := f.s.SaveConfig(ctx, f.admin, modelEndpointRequest(t, map[string]any{"enabled": true, "base_url": shared.URL, "models": models, "failover_enabled": true})); err != nil {
		t.Fatal(err)
	}
	out, err := f.s.complete(ctx, models[0], []providerMessage{{Role: "user", Content: "synthetic fallback"}})
	if err != nil || out.Content != "completed at gpt-backup" {
		t.Fatal("matching backup was not selected", err)
	}
	mu.Lock()
	recorded := append([]observed(nil), calls...)
	mu.Unlock()
	names := []string{}
	for _, call := range recorded {
		names = append(names, call.Endpoint)
	}
	if !reflect.DeepEqual(names, []string{"gpt", "claude", "shared", "gpt", "gpt", "gpt-backup"}) {
		t.Fatal("unrelated default/backup got request", names)
	}
	// Remove the shared provider entirely and prove normal chat admission works.
	primaryFails.Store(false)
	settings = map[string]any{"enabled": true, "base_url": "", "clear_api_key": true, "models": models[:2], "model_endpoints": map[string]any{models[0]: map[string]any{"base_url": gpt.URL}, models[1]: map[string]any{"base_url": claude.URL}}, "fallback_relays": []any{}, "failover_enabled": false}
	if _, err = f.s.SaveConfig(ctx, f.admin, modelEndpointRequest(t, settings)); err != nil {
		t.Fatal("dedicated-only save", err)
	}
	sent, err := f.s.SendMessage(ctx, f.staff, SendMessageRequest{Model: models[1], Content: "synthetic dedicated-only chat", RequestID: fmt.Sprintf("model-endpoint-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal("dedicated-only chat admission rejected", err)
	}
	turn := continuousWait(t, f, sent.Turn.ID)
	if turn.Status != "completed" {
		t.Fatal("dedicated-only turn failed", turn.Status, turn.Error)
	}
	mu.Lock()
	last := calls[len(calls)-1]
	mu.Unlock()
	if last.Endpoint != "claude" || last.Model != models[1] || last.Authorization != "Bearer claude-private-fixture" {
		t.Fatal("staff conversation not routed correctly")
	}
}

func TestModelEndpointPostgresSameProviderDifferentKeysAndCorruptConfig(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	models := DefaultModels[:2]
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
			w.WriteHeader(400)
			return
		}
		want := map[string]string{models[0]: "Bearer same-host-gpt-key", models[1]: "Bearer same-host-claude-key"}[req.Model]
		if want == "" || r.Header.Get("Authorization") != want {
			t.Error("same host key crossed model boundary")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"same host correct key"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	settings := map[string]any{"enabled": true, "base_url": server.URL, "api_key": "shared-must-not-be-sent", "models": models, "model_endpoints": map[string]any{models[0]: map[string]any{"base_url": server.URL, "api_key": "same-host-gpt-key"}, models[1]: map[string]any{"base_url": server.URL, "api_key": "same-host-claude-key"}}}
	if _, err := f.s.SaveConfig(ctx, f.admin, modelEndpointRequest(t, settings)); err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if _, err := f.s.complete(ctx, model, []providerMessage{{Role: "user", Content: "synthetic exact identity"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Invalid stored routing must not send the shared credential anywhere.
	if _, err := f.db.Exec(`UPDATE v2_ai_config SET model_endpoints=jsonb_set(model_endpoints,ARRAY[$1,'base_url'],$2::jsonb) WHERE id=1`, models[0], `"https://wrong.example.test/?token=forbidden"`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.complete(ctx, models[0], []providerMessage{{Role: "user", Content: "synthetic corrupt config"}}); err == nil {
		t.Fatal("invalid stored endpoint accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("corrupt dedicated config fell back to shared provider", calls.Load())
	}
}
