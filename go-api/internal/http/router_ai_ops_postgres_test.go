package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

// Use only an empty, disposable database seeded from install.pgsql.sql. The test
// exercises the real service through HTTP and cleans its own AI fixture data.
func TestAIOpsHTTPPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_HTTP_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_HTTP_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var existing int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_user`).Scan(&existing); err != nil || existing != 0 {
		t.Fatal("requires empty disposable database", err)
	}
	var actor, node int64
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES('ai-http@example.test','unused','fixture','ai-http-fixture',1,1,1) RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM v2_user WHERE id=$1`, actor)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,created_at,updated_at) VALUES('[]','AI HTTP fixture','before.example.test','54101',54101,'1','tcp',1,1) RETURNING id`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, node)
	var relayCalls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer ai-http-secret" {
			t.Error("relay request path/auth mismatch")
		}
		var req struct {
			Model string `json:"model"`
			Tools []any  `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "glm-5.3" || len(req.Tools) == 0 {
			t.Error("model/tool mapping", err)
		}
		args, _ := json.Marshal(map[string]any{"node_type": "vmess", "node_id": node, "changes": map[string]any{"host": "after.example.test", "port": 54102}, "reason": "HTTP integration test"})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: "http-call", Type: "function", Function: aiops.ToolFunction{Name: "propose_node_change", Arguments: string(args)}}}}}}})
	}))
	defer relay.Close()
	cfg := config.Config{AdminPath: "test", AppKey: "ai-http-app-key"}
	ai := aiops.NewService(cfg, db, admin.NewDBService(cfg, db))
	defer ai.Close()
	defer ai.ResetData(ctx, actor)
	router := NewRouter(cfg, WithAIOps(ai), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: actor, IsAdmin: 1}}))
	server := httptest.NewServer(router)
	defer server.Close()
	request := func(method, path string, input any, want int, result any) []byte {
		t.Helper()
		var raw []byte
		if input != nil {
			raw, _ = json.Marshal(input)
		}
		req, _ := http.NewRequest(method, server.URL+"/api/v1/test/ai-ops/"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err = io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s: status=%d want=%d body=%s error=%v", path, response.StatusCode, want, raw, err)
		}
		if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			t.Fatal("response cache enabled")
		}
		if result != nil {
			var wrapper struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(raw, &wrapper) != nil || json.Unmarshal(wrapper.Data, result) != nil {
				t.Fatalf("invalid envelope: %s", raw)
			}
		}
		return raw
	}
	raw := request("POST", "config/save", aiops.ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "ai-http-secret", Models: aiops.DefaultModels}, 200, nil)
	if bytes.Contains(raw, []byte("ai-http-secret")) {
		t.Fatal("key echoed")
	}
	var encrypted string
	if err = db.QueryRowContext(ctx, `SELECT api_key_cipher FROM v2_ai_config WHERE id=1`).Scan(&encrypted); err != nil || encrypted == "" || strings.Contains(encrypted, "ai-http-secret") {
		t.Fatal("key not encrypted", err)
	}
	input := aiops.SendMessageRequest{Content: "调整测试节点", Model: "glm-5.3", RequestID: "http-fixture-send-1"}
	var sent, retried aiops.SendMessageResult
	request("POST", "chat/send", input, 202, &sent)
	request("POST", "chat/send", input, 202, &retried)
	if sent.Turn.ID != retried.Turn.ID {
		t.Fatal("HTTP retry created duplicate turn")
	}
	var detail struct {
		aiops.SessionDetail
		Actions []aiops.Action `json:"actions"`
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		request("GET", fmt.Sprintf("session?id=%d", sent.Session.ID), nil, 200, &detail)
		if detail.Session.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if relayCalls.Load() != 1 || len(detail.Actions) != 1 || detail.Actions[0].Status != "pending" {
		t.Fatalf("proposal state: %+v", detail)
	}
	var host, port string
	if err = db.QueryRowContext(ctx, `SELECT host,port FROM v2_server_vmess WHERE id=$1`, node).Scan(&host, &port); err != nil || host != "before.example.test" {
		t.Fatal("unapproved proposal executed", err)
	}
	approve := map[string]any{"session_id": sent.Session.ID, "id": detail.Actions[0].ID}
	var approved aiops.Action
	request("POST", "action/approve", approve, 202, &approved)
	if approved.Status != "succeeded" {
		t.Fatalf("action not committed: %+v", approved)
	}
	request("POST", "action/approve", approve, 409, nil)
	if err = db.QueryRowContext(ctx, `SELECT host,port FROM v2_server_vmess WHERE id=$1`, node).Scan(&host, &port); err != nil || host != "after.example.test" || port != "54102" {
		t.Fatal("node changes not persisted", err)
	}
	// Permission changes come only from the authenticated administrator, and
	// stale browser requests cannot reinstate a grant after it is revoked.
	var fullSession, standardSession aiops.Session
	request("POST", "session/access", map[string]any{"id": sent.Session.ID, "mode": "full", "expected_version": sent.Session.AccessVersion}, 200, &fullSession)
	if fullSession.AccessMode != "full" || fullSession.FullAccessBy != actor || fullSession.AccessVersion <= sent.Session.AccessVersion {
		t.Fatalf("full grant not persisted: %+v", fullSession)
	}
	request("POST", "session/access", map[string]any{"id": sent.Session.ID, "mode": "standard", "expected_version": fullSession.AccessVersion}, 200, &standardSession)
	request("POST", "session/access", map[string]any{"id": sent.Session.ID, "mode": "full", "expected_version": fullSession.AccessVersion}, 409, nil)
	request("POST", "chat/send", map[string]any{"session_id": sent.Session.ID, "model": "glm-5.3", "content": "stale full request", "request_id": "stale-full-http-request", "access_mode": "full", "access_version": fullSession.AccessVersion}, 409, nil)
	request("POST", "chat/stop", map[string]any{"id": sent.Session.ID}, 200, nil)
	if standardSession.AccessMode != "standard" || standardSession.FullAccessBy != 0 || relayCalls.Load() != 1 {
		t.Fatal("revocation or stale send executed unexpectedly")
	}
	request("POST", "session/delete", map[string]any{"id": sent.Session.ID, "confirm": true}, 200, nil)
	request("GET", fmt.Sprintf("session?id=%d", sent.Session.ID), nil, 404, nil)
	request("POST", "reset", map[string]any{"confirm": true, "confirm_text": "RESET_AI_DATA"}, 200, nil)
	var after aiops.Config
	request("GET", "config", nil, 200, &after)
	if after.Enabled || after.HasAPIKey {
		t.Fatal("reset retained config")
	}
	if err = db.QueryRowContext(ctx, `SELECT host FROM v2_server_vmess WHERE id=$1`, node).Scan(&host); err != nil || host != "after.example.test" {
		t.Fatal("AI reset touched business node", err)
	}
	t.Log("HTTP → actual PostgreSQL → mock relay → pending node change → approve once → cleanup passed")
}

func TestAIOpsTypedNilUnavailable(t *testing.T) {
	var ai *aiops.Service
	router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(ai), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}))
	for _, path := range []string{"config", "sessions", "servers"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/test/ai-ops/"+path, nil)
		r.Header.Set("Authorization", "Bearer fixture")
		router.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
}
