package aiops

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

// Opt-in: only synthetic operation descriptions go to the relay. No returned
// tool is dispatched, and no announcement, business data or SSH host is touched.
func TestAIPanelOperationsRelayLive(t *testing.T) {
	base, key := os.Getenv("FOREST_AIOPS_LIVE_BASE_URL"), os.Getenv("FOREST_AIOPS_LIVE_API_KEY")
	if base == "" || key == "" {
		t.Skip("requires live relay credentials; synthetic data only")
	}
	base, err := normalizeBaseURL(base)
	if err != nil || base == "" {
		t.Fatal("invalid relay URL; value omitted")
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal("cannot initialize isolated fixture")
	}
	defer db.Close()
	s := NewService(config.Config{AppKey: "panel-live-isolated-fixture"}, db, nil)
	defer s.Close()
	cipher, err := s.encryptSecret(key)
	if err != nil {
		t.Fatal("cannot prepare isolated credential")
	}
	models, _ := json.Marshal(DefaultModels)
	transport := &liveRelayObservation{client: newProviderHTTPClient()}
	s.httpClient = transport
	messages := []providerMessage{
		{Role: "system", Content: systemPrompt + "\n" + fullAccessModePrompt + "\n" + paymentConfigurationBoundaryPrompt + "\n" + ticketWorkflowPrompt + "\n" + panelOperationsPrompt},
		{Role: "user", Content: "在后台新增一条公告，标题为“运维验证公告”，正文为“这是合成测试通知，请忽略。”，不需要图片或标签。先通过后台能力目录核对可用操作与参数，再创建公告。"},
	}
	listed, described := false, false
	for round := 0; round < 5; round++ {
		mock.ExpectQuery(`SELECT enabled,base_url,models::TEXT,api_key_cipher`).WillReturnRows(sqlmock.NewRows([]string{"enabled", "base_url", "models", "key", "reasoning_efforts"}).AddRow(true, base, string(models), cipher, `{}`))
		ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
		started := time.Now()
		response, err := s.complete(ctx, DefaultModels[0], messages)
		cancel()
		t.Logf("round=%d class=%s http=%d elapsed_ms=%d tool_calls=%d", round+1, liveRelayResultClass(transport.status, err != nil), transport.status, time.Since(started).Milliseconds(), len(response.ToolCalls))
		if err != nil || mock.ExpectationsWereMet() != nil {
			t.Fatal("relay/configuration failed; response omitted")
		}
		if len(response.ToolCalls) == 0 {
			t.Fatal("model did not use the available operation catalog; response omitted")
		}
		messages = append(messages, response)
		for _, call := range response.ToolCalls {
			if !s.validToolArgumentKeys(call.Function.Name, call.Function.Arguments) {
				t.Fatal("invalid tool arguments; response omitted")
			}
			var args struct {
				Operation  string          `json:"operation"`
				Parameters json.RawMessage `json:"parameters"`
				Reason     string          `json:"reason"`
			}
			if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
				t.Fatal("invalid arguments JSON")
			}
			var result string
			switch call.Function.Name {
			case "panel_operations_list":
				listed = true
				result = `{"items":[{"name":"notices.save","category":"notices","description":"新增或修改公告；新增时省略id，title与content必填。","mutating":true,"admin_only":false}],"total":1,"limit":20,"offset":0,"truncated":false}`
			case "panel_operation_describe":
				if !listed || args.Operation != "notices.save" {
					t.Fatal("operation was not discovered before describing it")
				}
				described = true
				result = `{"name":"notices.save","category":"notices","description":"新增或修改公告；新增时省略id。","method":"POST","path":"/notice/save","mutating":true,"admin_only":false,"parameters":{"type":"object","properties":{"id":{"type":"integer","minimum":1},"title":{"type":"string"},"content":{"type":"string"},"img_url":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}},"required":["title","content"],"additionalProperties":false}}`
			case "propose_panel_operation":
				var params map[string]any
				if !described || args.Operation != "notices.save" || args.Reason == "" || json.Unmarshal(args.Parameters, &params) != nil || params["title"] != "运维验证公告" || params["content"] != "这是合成测试通知，请忽略。" || params["id"] != nil {
					t.Fatal("proposal does not preserve the requested operation and data")
				}
				t.Log("catalog discovery, parameter lookup and mutation proposal passed; no action executed")
				return
			default:
				t.Fatal("unexpected tool instead of catalog workflow; no tools executed")
			}
			messages = append(messages, providerMessage{Role: "tool", ToolCallID: call.ID, Content: result})
		}
	}
	t.Fatal("model did not complete catalog workflow within five requests")
}
