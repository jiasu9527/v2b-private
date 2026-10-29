package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
)

// This is the failed action's exact field/value shape. Saving node metadata
// never connects to the host or TLS server name; only a disposable PostgreSQL
// database and the local synthetic model HTTP server are used in this test.
const nodeSaveReportedParameters = `{"tls":1,"host":"23.144.12.64","name":"美国-9929 3","port":"443","rate":1,"show":0,"network":"tcp","group_id":["1"],"listen_ip":"0.0.0.0","server_port":443,"tls_settings":{"fingerprint":"safari","server_name":"download-porter.hoyoverse.com","server_port":"443","allow_insecure":"1","reject_unknown_sni":"0"},"network_settings":{},"client_entry_only":1}`

func TestPanelOperationsNodeSaveHTTPPostgres(t *testing.T) {
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
	var userCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_user`).Scan(&userCount); err != nil || userCount != 0 {
		t.Fatal("requires an isolated test database with no users", userCount, err)
	}
	var actorID int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err := db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES($1,'fixture','fixture',$2,1,1,1) RETURNING id`, "node-save-"+marker+"@example.test", "node-save-"+marker).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actorID)
	cfg := config.Config{AdminPath: "node-save-test", AppKey: "isolated-node-save-key"}
	service := admin.NewDBService(cfg, db)
	ai := aiops.NewService(cfg, db, service)
	defer func() {
		ai.Close()
		if err := ai.ResetData(ctx, actorID); err != nil {
			t.Error("clean AI fixture", err)
		}
	}()
	NewRouter(cfg, WithAIOps(ai), WithAdminService(service))
	runtime := newPanelOperations(cfg, &routerState{admin: service})
	actor := aiops.PanelOperationActor{ID: actorID, IsAdmin: true}
	const operation = "nodes.v2node.save"
	var params map[string]any
	decoder := json.NewDecoder(strings.NewReader(nodeSaveReportedParameters))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		t.Fatal(err)
	}
	countNodes := func() int {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM v2_server_v2node`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	initialCount := countNodes()
	defer db.Exec(`DELETE FROM v2_server_v2node WHERE name=$1`, "node-save-complete-"+marker)
	assertNodeValidation := func(t *testing.T, err error) {
		t.Helper()
		var validation *admin.ManagedServerValidationError
		if !errors.As(err, &validation) {
			t.Errorf("expected definite pre-dispatch node input rejection, got %T: %v", err, err)
		}
	}

	t.Run("reported fields reject before dispatch and do not write", func(t *testing.T) {
		var descriptor aiops.PanelOperation
		for _, candidate := range runtime.Operations() {
			if candidate.Name == operation {
				descriptor = candidate
				break
			}
		}
		if descriptor.Path != "/server/v2node/save" || descriptor.Method != http.MethodPost || !descriptor.Mutating {
			t.Fatal("catalog does not select the expected node save handler", descriptor)
		}
		assertNodeValidation(t, runtime.Validate(ctx, actor, operation, json.RawMessage(nodeSaveReportedParameters)))
		_, err := runtime.Execute(ctx, actor, operation, json.RawMessage(nodeSaveReportedParameters))
		assertNodeValidation(t, err)
		// The ordinary admin route must also classify this as an input error,
		// independently of the AI operation catalog's earlier validation.
		router := NewRouter(cfg, WithAdminService(service), func(state *routerState) { state.session = panelOperationSession{actor: actor} })
		request := httptest.NewRequest(http.MethodPost, "/api/v1/"+cfg.AdminPath+"/server/v2node/save", bytes.NewBufferString(nodeSaveReportedParameters))
		request.Header.Set("Authorization", "internal-operation-only")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "协议不能为空") {
			t.Errorf("business input validation was reported as uncertain backend failure: HTTP %d %s", recorder.Code, recorder.Body.String())
		}
		if got := countNodes(); got != initialCount {
			t.Fatal("incomplete payload wrote a node", initialCount, got)
		}
	})

	params["protocol"] = "vless"
	params["disable_sni"], params["zero_rtt_handshake"] = 0, 0
	params["name"] = "node-save-complete-" + marker
	complete, _ := json.Marshal(params)
	for _, field := range []string{"protocol", "tls", "network", "disable_sni", "zero_rtt_handshake"} {
		t.Run("catalog requires "+field, func(t *testing.T) {
			missing := make(map[string]any, len(params))
			for key, value := range params {
				if key != field {
					missing[key] = value
				}
			}
			raw, _ := json.Marshal(missing)
			assertNodeValidation(t, runtime.Validate(ctx, actor, operation, raw))
		})
	}

	t.Run("full access corrects reported fields and creates exactly one node", func(t *testing.T) {
		if err := runtime.Validate(ctx, actor, operation, complete); err != nil {
			t.Fatal("complete payload rejected by declared schema", err)
		}
		var modelCalls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Messages []struct {
					Role, Content string
					ToolCallID    string `json:"tool_call_id"`
				} `json:"messages"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid model request")
				w.WriteHeader(400)
				return
			}
			message := map[string]any{"role": "assistant", "content": "fixture node metadata saved"}
			switch modelCalls.Add(1) {
			case 1:
				args, _ := json.Marshal(map[string]any{"operation": operation, "parameters": json.RawMessage(nodeSaveReportedParameters), "reason": "isolated validation fixture"})
				message = map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: "node-save-invalid", Type: "function", Function: aiops.ToolFunction{Name: "propose_panel_operation", Arguments: string(args)}}}}
			case 2:
				found := false
				for _, item := range request.Messages {
					if item.Role == "tool" && item.ToolCallID == "node-save-invalid" {
						var result map[string]any
						if json.Unmarshal([]byte(item.Content), &result) != nil || result["code"] != "invalid_arguments" || result["status"] != "error" || !strings.Contains(fmt.Sprint(result["message"]), "协议不能为空") || !strings.Contains(fmt.Sprint(result["message"]), "未执行节点保存") {
							t.Error("model lost the correctable validation reason", item.Content)
						}
						found = true
					}
				}
				if !found {
					t.Error("model did not receive the rejected node tool result")
				}
				var actionCount, nodeCount int
				if err := db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action WHERE actor_id=$1`, actorID).Scan(&actionCount); err != nil || actionCount != 0 {
					t.Error("invalid proposal created an action", actionCount, err)
				}
				if err := db.QueryRow(`SELECT COUNT(*) FROM v2_server_v2node`).Scan(&nodeCount); err != nil || nodeCount != initialCount {
					t.Error("invalid proposal wrote a node", nodeCount, err)
				}
				args, _ := json.Marshal(map[string]any{"operation": operation, "parameters": params, "reason": "isolated node metadata fixture"})
				message = map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: "node-save-once", Type: "function", Function: aiops.ToolFunction{Name: "propose_panel_operation", Arguments: string(args)}}}}
			default:
				found := false
				for _, item := range request.Messages {
					if item.Role == "tool" && item.ToolCallID == "node-save-once" {
						var result map[string]any
						if json.Unmarshal([]byte(item.Content), &result) != nil || result["status"] != "succeeded" {
							t.Error("node operation not reported as successful", item.Content)
						}
						found = true
					}
				}
				if !found {
					t.Error("model did not receive node action result")
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
		}))
		defer relay.Close()
		if _, err := ai.SaveConfig(ctx, actorID, aiops.ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "fixture-local-relay", Models: aiops.DefaultModels}); err != nil {
			t.Fatal(err)
		}
		started, err := ai.SendMessage(ctx, actorID, aiops.SendMessageRequest{Model: aiops.DefaultModels[0], Content: "仅在测试数据库保存合成节点，不要连接节点", RequestID: "node-save-" + marker, AccessMode: "full"})
		if err != nil {
			t.Fatal(err)
		}
		var detail aiops.SessionDetail
		for deadline := time.Now().Add(12 * time.Second); ; {
			detail, err = ai.GetSession(ctx, actorID, started.Session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.Turns) > 0 && detail.Turns[0].Status != "running" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("node save flow did not finish")
			}
			time.Sleep(15 * time.Millisecond)
		}
		actions, err := ai.ListActions(ctx, actorID, started.Session.ID)
		if err != nil || detail.Turns[0].Status != "completed" || len(actions) != 1 || actions[0].Status != "succeeded" || modelCalls.Load() != 3 {
			t.Fatalf("node save flow failed or retried: turn=%+v actions=%+v calls=%d err=%v", detail.Turns[0], actions, modelCalls.Load(), err)
		}
		if count := countNodes(); count != initialCount+1 {
			t.Fatal("node created zero or multiple times", initialCount, count)
		}
		var host, port, protocol, network, listen, tlsRaw, networkRaw, groupRaw string
		var serverPort, tls, show, entryOnly, disableSNI, zeroRTT int
		if err := db.QueryRow(`SELECT host,port,server_port,protocol,network,listen_ip,tls,"show",client_entry_only,disable_sni,zero_rtt_handshake,tls_settings::TEXT,network_settings::TEXT,group_id::TEXT FROM v2_server_v2node WHERE name=$1`, params["name"]).Scan(&host, &port, &serverPort, &protocol, &network, &listen, &tls, &show, &entryOnly, &disableSNI, &zeroRTT, &tlsRaw, &networkRaw, &groupRaw); err != nil {
			t.Fatal(err)
		}
		if host != "23.144.12.64" || port != "443" || serverPort != 443 || protocol != "vless" || network != "tcp" || listen != "0.0.0.0" || tls != 1 || show != 0 || entryOnly != 1 || disableSNI != 0 || zeroRTT != 0 {
			t.Fatal("saved node connection settings differ", host, port, serverPort, protocol, network, listen, tls, show, entryOnly, disableSNI, zeroRTT)
		}
		var actualTLS map[string]any
		if json.Unmarshal([]byte(tlsRaw), &actualTLS) != nil {
			t.Fatal("saved TLS settings invalid", tlsRaw)
		}
		for key, want := range params["tls_settings"].(map[string]any) {
			if fmt.Sprint(actualTLS[key]) != fmt.Sprint(want) {
				t.Error("TLS setting changed", key, actualTLS[key], want)
			}
		}
		var groups []any
		if json.Unmarshal([]byte(groupRaw), &groups) != nil || len(groups) != 1 || fmt.Sprint(groups[0]) != "1" || strings.TrimSpace(networkRaw) != "{}" {
			t.Fatal("group or network settings changed", groupRaw, networkRaw)
		}
	})
}
