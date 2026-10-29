package aiops

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

// Opt-in end-to-end verification against a real relay and a freshly installed,
// disposable PostgreSQL database. It never registers or connects to SSH hosts.
// Only a node created by this test can be approved, and no production dataset is
// accepted. It spends at most one relay completion per turn, two turns total.
func TestAIRelayLivePostgresFlow(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_LIVE_TEST_DSN")
	base, key := os.Getenv("FOREST_AIOPS_LIVE_BASE_URL"), os.Getenv("FOREST_AIOPS_LIVE_API_KEY")
	if dsn == "" || base == "" || key == "" {
		t.Skip("requires disposable FOREST_AIOPS_LIVE_TEST_DSN and live relay Base URL/API Key; uses relay tokens")
	}
	base, err := normalizeBaseURL(base)
	if err != nil || base == "" {
		t.Fatal("class=invalid_relay_base_url; value omitted")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("class=database_open_failed; DSN omitted")
	}
	defer db.Close()
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer setupCancel()
	for _, table := range []string{"v2_user", "v2_server_vmess"} {
		var count int
		if err = db.QueryRowContext(setupCtx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("class=unsafe_test_database; use an empty disposable database initialized from install.pgsql.sql")
		}
	}
	// Keep the encryption key only in memory, so any deleted fixture ciphertext
	// left in the disposable database's WAL cannot be decrypted from this source.
	appKeyBytes := make([]byte, 32)
	if _, err = rand.Read(appKeyBytes); err != nil {
		t.Fatal("class=random_key_generation_failed")
	}
	cfg := config.Config{AppKey: hex.EncodeToString(appKeyBytes)}
	s := NewService(cfg, db, admin.NewDBService(cfg, db))
	transport := &livePostgresRelayBudget{client: newProviderHTTPClient()}
	s.httpClient = transport
	var actor, nodeID int64
	defer func() {
		// Stop all worker goroutines before deleting their records, including on
		// test failure, so neither credentials nor late results are retained.
		s.Close()
		if actor == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, cleanup := range []struct {
			query string
			id    int64
		}{
			{`DELETE FROM v2_ai_session WHERE created_by=$1`, actor},
			{`DELETE FROM v2_ai_server WHERE actor_id=$1`, actor},
			{`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor},
			{`DELETE FROM v2_server_vmess WHERE id=$1`, nodeID},
			{`DELETE FROM v2_user WHERE id=$1`, actor},
		} {
			if _, err := db.ExecContext(ctx, cleanup.query, cleanup.id); err != nil {
				t.Error("class=fixture_cleanup_failed; dispose the isolated test database")
			}
		}
	}()
	if err = s.ensureSchema(setupCtx); err != nil {
		t.Fatal("class=ai_schema_initialization_failed")
	}
	for _, table := range []string{"v2_ai_config", "v2_ai_session", "v2_ai_server"} {
		var count int
		if err = db.QueryRowContext(setupCtx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("class=unsafe_existing_ai_data; use a clean disposable test database")
		}
	}
	fixture := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRowContext(setupCtx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES($1,'unused','live-test',$2,1,1,1) RETURNING id`, "ai-live-"+fixture+"@example.test", fixture).Scan(&actor); err != nil {
		t.Fatal("class=admin_fixture_creation_failed")
	}
	if err = db.QueryRowContext(setupCtx, `INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,created_at,updated_at) VALUES('[]','Isolated AI live fixture','before.example.test','54102',54102,'1','tcp',1,1) RETURNING id`).Scan(&nodeID); err != nil {
		t.Fatal("class=node_fixture_creation_failed")
	}
	saved, err := s.SaveConfig(setupCtx, actor, ConfigSaveRequest{Enabled: true, BaseURL: base, Models: DefaultModels, APIKey: key})
	if err != nil || !saved.Enabled || !saved.HasAPIKey || len(saved.Models) != 4 {
		t.Fatal("class=save_encrypted_configuration_failed")
	}
	publicConfig, _ := json.Marshal(saved)
	if strings.Contains(string(publicConfig), key) {
		t.Fatal("class=credential_in_configuration_dto")
	}
	var encrypted string
	if err = db.QueryRowContext(setupCtx, `SELECT api_key_cipher FROM v2_ai_config WHERE id=1`).Scan(&encrypted); err != nil || encrypted == "" || strings.Contains(encrypted, key) || !strings.HasPrefix(encrypted, "v1:") {
		t.Fatal("class=credential_not_encrypted")
	}
	decrypted, err := s.decryptSecret(encrypted)
	if err != nil || decrypted != key {
		t.Fatal("class=credential_roundtrip_failed")
	}
	for index, model := range saved.Models {
		if model != DefaultModels[index] {
			t.Fatal("class=config_model_ids_changed")
		}
	}
	t.Log("stage=configuration class=ok models=4 encrypted_key=true")

	waitTurn := func(sessionID, turnID int64, expected string) SessionDetail {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
		defer cancel()
		started := time.Now()
		for {
			detail, err := s.GetSession(ctx, actor, sessionID)
			if err != nil {
				t.Fatal("class=session_poll_failed; no message content logged")
			}
			for _, turn := range detail.Turns {
				if turn.ID != turnID || turn.Status == "running" {
					continue
				}
				if turn.Status != expected {
					t.Fatalf("stage=turn class=unexpected_terminal_state expected=%s elapsed_ms=%d http=%d", expected, time.Since(started).Milliseconds(), transport.status.Load())
				}
				t.Logf("stage=turn class=ok state=%s elapsed_ms=%d http=%d", expected, time.Since(started).Milliseconds(), transport.status.Load())
				return detail
			}
			select {
			case <-ctx.Done():
				t.Fatal("class=turn_timeout; no retry attempted")
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	readNode := func(wantHost, wantPort string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var host, port string
		if err := db.QueryRowContext(ctx, `SELECT host,port FROM v2_server_vmess WHERE id=$1`, nodeID).Scan(&host, &port); err != nil || host != wantHost || port != wantPort {
			t.Fatal("class=fixture_node_state_mismatch")
		}
	}
	transport.remaining.Store(1)
	first, err := s.SendMessage(context.Background(), actor, SendMessageRequest{
		Model: "gpt-6.1-sol", RequestID: "live-propose-" + fixture,
		Content: fmt.Sprintf("这是一次性数据库内的隔离运维测试，没有任何真实服务器。测试管理员已经提供准确节点信息：协议 vmess，节点 ID %d。现在请且仅调用一次 propose_node_change，node_type=vmess，node_id=%d，changes 仅包含 host=192.0.2.10、port=54101，reason 为隔离兼容性测试；不要查询其他工具，不要修改 show，不要执行 SSH，不要批准或声称变更已经生效。你的工具调用只生成待人工批准的卡片。", nodeID, nodeID),
	})
	if err != nil {
		t.Fatal("class=proposal_send_failed")
	}
	waitTurn(first.Session.ID, first.Turn.ID, "pending")
	if transport.sent.Load() != 1 || transport.denied.Load() != 0 {
		t.Fatal("class=unexpected_extra_relay_request; request budget blocked retries")
	}
	actions, err := s.ListActions(context.Background(), actor, first.Session.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != "pending" || actions[0].Kind != "node_change" || actions[0].TargetID != nodeID || actions[0].TargetType != "vmess" {
		t.Fatal("class=unexpected_pending_action; nothing approved")
	}
	var payload nodePayload
	if json.Unmarshal(actions[0].Payload, &payload) != nil || payload.NodeType != "vmess" || payload.NodeID != nodeID || payload.Changes.Host == nil || *payload.Changes.Host != "192.0.2.10" || payload.Changes.Port == nil || *payload.Changes.Port != 54101 || payload.Changes.Show != nil {
		t.Fatal("class=unsafe_proposal_arguments; nothing approved")
	}
	readNode("before.example.test", "54102")
	t.Log("stage=proposal class=ok node_unchanged=true")
	// TEST ONLY: approve precisely this freshly created local fixture after
	// checking every mutable argument. No model-controlled target is approved.
	approved, err := s.ApproveAction(context.Background(), actor, first.Session.ID, actions[0].ID)
	if err != nil || approved.Status != "succeeded" || approved.ApprovedBy != actor {
		t.Fatal("class=local_fixture_approval_failed")
	}
	if _, err = s.ApproveAction(context.Background(), actor, first.Session.ID, actions[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatal("class=repeated_approval_not_rejected")
	}
	readNode("192.0.2.10", "54101")
	t.Log("stage=approval class=ok repeated_approval_rejected=true")

	transport.remaining.Store(1)
	second, err := s.SendMessage(context.Background(), actor, SendMessageRequest{
		SessionID: first.Session.ID, Model: "gpt-6.1-sol", RequestID: "live-summary-" + fixture,
		Content: "仅根据本次消息附带的最新动作状态数据进行总结，不要再次查询或调用任何工具。如果最新动作状态为 succeeded，请只回复 LIVE_FLOW_OK succeeded；否则只回复 LIVE_FLOW_NOT_SUCCESS。不要重试或创建新提案。",
	})
	if err != nil {
		t.Fatal("class=summary_send_failed")
	}
	detail := waitTurn(second.Session.ID, second.Turn.ID, "completed")
	if transport.sent.Load() != 2 || transport.denied.Load() != 0 {
		t.Fatal("class=unexpected_extra_relay_request; request budget blocked retries")
	}
	matchedSummary := false
	for _, message := range detail.Messages {
		if message.TurnID != second.Turn.ID {
			continue
		}
		if len(message.ToolCalls) != 0 {
			t.Fatal("class=unexpected_summary_tool_call; no additional action approved")
		}
		if message.Role == "assistant" && strings.Contains(message.Content, "LIVE_FLOW_OK") && strings.Contains(message.Content, "succeeded") {
			matchedSummary = true
		}
	}
	if !matchedSummary {
		t.Fatal("class=action_result_not_understood; response content omitted")
	}
	t.Log("stage=result_context class=ok completed_action_seen=true")
	// Workers must be quiescent before the deliberate deletion checks, avoiding
	// races with a worker's final persistence transaction in a real live test.
	s.Close()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cleanupCancel()
	if err = s.DeleteSession(cleanupCtx, actor, first.Session.ID); err != nil {
		t.Fatal("class=session_delete_failed")
	}
	if _, err = s.GetSession(cleanupCtx, actor, first.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("class=deleted_session_still_visible")
	}
	if err = s.ResetData(cleanupCtx, actor); err != nil {
		t.Fatal("class=ai_reset_failed")
	}
	for _, table := range []string{"v2_ai_config", "v2_ai_session", "v2_ai_turn", "v2_ai_message", "v2_ai_action", "v2_ai_server"} {
		var count int
		if err = db.QueryRowContext(cleanupCtx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("class=ai_reset_left_data")
		}
	}
	readNode("192.0.2.10", "54101")
	t.Log("stage=cleanup class=ok ai_rows=0 business_fixture_preserved=true")
}

type livePostgresRelayBudget struct {
	client    providerHTTPClient
	remaining atomic.Int32
	sent      atomic.Int32
	denied    atomic.Int32
	status    atomic.Int32
}

func (b *livePostgresRelayBudget) Do(req *http.Request) (*http.Response, error) {
	if !b.remaining.CompareAndSwap(1, 0) {
		b.denied.Add(1)
		return nil, errors.New("live integration request budget exhausted")
	}
	b.sent.Add(1)
	response, err := b.client.Do(req)
	if response != nil {
		b.status.Store(int32(response.StatusCode))
	}
	return response, err
}
