package aiops

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

// This opt-in test spends relay tokens, but never calls panel tools, connects to
// a business database/SSH host, or approves an action. Supply credentials only
// through the environment; neither credentials nor response bodies are logged.
func TestAIRelayLiveCompatibility(t *testing.T) {
	base, key := os.Getenv("FOREST_AIOPS_LIVE_BASE_URL"), os.Getenv("FOREST_AIOPS_LIVE_API_KEY")
	if base == "" || key == "" {
		t.Skip("requires FOREST_AIOPS_LIVE_BASE_URL and FOREST_AIOPS_LIVE_API_KEY; uses live relay tokens")
	}
	base, err := normalizeBaseURL(base)
	if err != nil || base == "" {
		t.Fatal("live relay Base URL is invalid; value omitted")
	}
	selected := DefaultModels
	if raw := strings.TrimSpace(os.Getenv("FOREST_AIOPS_LIVE_MODELS")); raw != "" {
		allowed, seen := map[string]bool{}, map[string]bool{}
		for _, model := range DefaultModels {
			allowed[model] = true
		}
		selected = nil
		for _, value := range strings.Split(raw, ",") {
			model := strings.TrimSpace(value)
			if !allowed[model] || seen[model] {
				t.Fatal("FOREST_AIOPS_LIVE_MODELS must select distinct exact default model IDs; value omitted")
			}
			seen[model] = true
			selected = append(selected, model)
		}
	}

	for _, model := range selected {
		t.Run(model, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal("failed to create isolated mock configuration store")
			}
			defer db.Close()
			s := NewService(config.Config{AppKey: "live-relay-isolated-test-key"}, db, nil)
			defer s.Close()
			cipher, err := s.encryptSecret(key)
			if err != nil {
				t.Fatal("failed to encrypt in-memory relay credential")
			}
			models, _ := json.Marshal(DefaultModels)
			transport := &liveRelayObservation{client: newProviderHTTPClient()}
			s.httpClient = transport
			knownTools := map[string]bool{}
			for _, definition := range s.toolDefinitions() {
				knownTools[definition.Function.Name] = true
			}
			if len(knownTools) != 46 {
				t.Fatal("update the explicit live test tool-count contract")
			}
			invoke := func(t *testing.T, stage string, messages []providerMessage) (providerMessage, bool) {
				t.Helper()
				mock.ExpectQuery(`SELECT enabled,base_url,models::TEXT,api_key_cipher`).WillReturnRows(sqlmock.NewRows([]string{"enabled", "base_url", "models", "key", "reasoning_efforts", "fallback_relays", "failover_enabled", "failover_timeout_seconds"}).AddRow(true, base, string(models), cipher, `{}`, `[]`, false, 30))
				transport.status, transport.model, transport.toolCount = 0, "", 0
				ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
				defer cancel()
				started := time.Now()
				result, err := s.complete(ctx, model, messages)
				names := make([]string, 0, len(result.ToolCalls))
				for _, call := range result.ToolCalls {
					if knownTools[call.Function.Name] {
						names = append(names, call.Function.Name)
					} else {
						names = append(names, "<unregistered>")
					}
				}
				category := liveRelayResultClass(transport.status, err != nil)
				t.Logf("stage=%s class=%s http=%d elapsed_ms=%d tools=%v", stage, category, transport.status, time.Since(started).Milliseconds(), names)
				if mock.ExpectationsWereMet() != nil {
					t.Errorf("stage=%s class=config_store_contract_failed", stage)
					return providerMessage{}, false
				}
				if err != nil {
					// Do not print errors from provider/client implementations: a
					// future transport may include a URL or credential in errors.
					t.Errorf("stage=%s class=%s", stage, category)
					return providerMessage{}, false
				}
				if transport.model != model || transport.toolCount != len(knownTools) {
					t.Errorf("stage=%s class=request_model_or_tool_schema_mismatch", stage)
					return providerMessage{}, false
				}
				return result, true
			}

			t.Run("readonly_tool_roundtrip", func(t *testing.T) {
				messages := []providerMessage{
					{Role: "system", Content: systemPrompt},
					{Role: "user", Content: "这是隔离的 API 兼容性测试，不连接业务服务。请立即且仅调用一次 panel_status，参数是空 JSON 对象，不调用其他工具，不猜测状态。收到工具结果后再回答。"},
				}
				result, ok := invoke(t, "readonly_proposal", messages)
				if !ok {
					return
				}
				if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "panel_status" || !s.validToolArgumentKeys("panel_status", result.ToolCalls[0].Function.Arguments) {
					t.Fatal("class=readonly_tool_selection_or_arguments_failed; no tool was executed")
				}
				messages = append(messages, result,
					providerMessage{Role: "tool", ToolCallID: result.ToolCalls[0].ID, Content: `{"fixture":true,"schedule":true,"queue_running":true,"failed_jobs":0,"verification_marker":"LIVE_READ_OK"}`},
					providerMessage{Role: "user", Content: "以上是合成测试结果，不是真实运维数据。现在不要调用任何工具，只原样回复工具结果中 verification_marker 的值。"},
				)
				summary, ok := invoke(t, "readonly_tool_result", messages)
				if !ok {
					return
				}
				if len(summary.ToolCalls) != 0 || !strings.Contains(summary.Content, "LIVE_READ_OK") {
					t.Fatal("class=tool_result_roundtrip_failed; response content omitted")
				}
			})

			t.Run("pending_node_proposal_only", func(t *testing.T) {
				result, ok := invoke(t, "node_proposal", []providerMessage{
					{Role: "system", Content: systemPrompt},
					{Role: "user", Content: "这是只验证工具调用格式的隔离测试，不会执行任何工具或连接节点。假设管理员已确认测试对象是 vmess 节点 #987654，请现在仅调用一次 propose_node_change，node_type 为 vmess，node_id 为 987654，changes 仅包含 host=192.0.2.10 和 port=54101，reason 填写隔离兼容性测试，生成待审批提案。不要查询其他工具，不要更改 show，不要声称已执行或批准。"},
				})
				if !ok {
					return
				}
				if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "propose_node_change" {
					t.Fatal("class=node_proposal_selection_failed; no tool was executed")
				}
				call := result.ToolCalls[0]
				if !s.validToolArgumentKeys(call.Function.Name, call.Function.Arguments) {
					t.Fatal("class=node_proposal_schema_failed; arguments omitted")
				}
				var args aiToolArgs
				decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&args) != nil || args.NodeType != "vmess" || args.NodeID != 987654 || strings.TrimSpace(args.Reason) == "" || args.Changes == nil || args.Changes.Host == nil || *args.Changes.Host != "192.0.2.10" || args.Changes.Port == nil || *args.Changes.Port != 54101 || args.Changes.Show != nil {
					t.Fatal("class=node_proposal_arguments_failed; no tool was executed")
				}
			})
		})
	}
}

// Observe only non-secret metadata while retaining the production HTTP client
// (including its redirect rejection). A copy of the request body is inspected;
// the request body and Authorization header are never logged or persisted.
type liveRelayObservation struct {
	client    providerHTTPClient
	status    int
	model     string
	toolCount int
}

func (o *liveRelayObservation) Do(req *http.Request) (*http.Response, error) {
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err == nil {
			var request completionRequest
			if json.NewDecoder(body).Decode(&request) == nil {
				o.model, o.toolCount = request.Model, len(request.Tools)
			}
			body.Close()
		}
	}
	response, err := o.client.Do(req)
	if response != nil {
		o.status = response.StatusCode
	}
	return response, err
}

func liveRelayResultClass(status int, failed bool) string {
	if !failed {
		return "ok"
	}
	switch {
	case status == 0:
		return "transport_or_local_validation_failed"
	case status == 401 || status == 403:
		return "authentication_or_access_rejected"
	case status == 429:
		return "quota_or_rate_limited"
	case status >= 300 && status < 400:
		return "redirect_rejected"
	case status >= 400 && status < 500:
		return "request_or_model_rejected"
	case status >= 500:
		return "upstream_failed"
	default:
		return "response_contract_failed"
	}
}
