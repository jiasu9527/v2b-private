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

// Only synthetic tickets are sent to the relay. Returned tools are checked,
// never dispatched: no business records, SSH sessions or mutations are used.
func TestAITicketGlobalRelayLive(t *testing.T) {
	base, key := os.Getenv("FOREST_AIOPS_LIVE_BASE_URL"), os.Getenv("FOREST_AIOPS_LIVE_API_KEY")
	if base == "" || key == "" {
		t.Skip("requires live relay environment; uses relay tokens with synthetic data only")
	}
	base, err := normalizeBaseURL(base)
	if err != nil || base == "" {
		t.Fatal("invalid relay URL; value omitted")
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal("cannot initialize isolated config fixture")
	}
	defer db.Close()
	s := NewService(config.Config{AppKey: "ticket-live-isolated-fixture"}, db, nil)
	defer s.Close()
	cipher, err := s.encryptSecret(key)
	if err != nil {
		t.Fatal("cannot prepare isolated credential")
	}
	models, _ := json.Marshal(DefaultModels)
	transport := &liveRelayObservation{client: newProviderHTTPClient()}
	s.httpClient = transport
	model := DefaultModels[0]
	messages := []providerMessage{
		{Role: "system", Content: systemPrompt + "\n" + fullAccessModePrompt + "\n" + paymentConfigurationBoundaryPrompt + "\n" + ticketWorkflowPrompt + "\n" + panelOperationsPrompt},
		{Role: "user", Content: "请查看全站未关闭工单里的提现申请，整理用户、金额、申请时间和处理进展。只查询，不回复或修改工单。"},
		{Role: "assistant", Content: "要继续直接查询，需要补齐全站未关闭工单查询能力，允许不传用户 ID。目前尚未取得全站工单。"},
		{Role: "user", Content: "继续查询全站未关闭工单，相关能力已经更新。不要让我逐个提供用户 ID，请按实际查询结果整理。"},
	}
	invoke := func(stage, expected string) ToolCall {
		t.Helper()
		mock.ExpectQuery(`SELECT enabled,base_url,models::TEXT,api_key_cipher`).WillReturnRows(sqlmock.NewRows([]string{"enabled", "base_url", "models", "key", "reasoning_efforts", "fallback_relays", "failover_enabled", "failover_timeout_seconds"}).AddRow(true, base, string(models), cipher, `{}`, `[]`, false, 30))
		ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
		defer cancel()
		started := time.Now()
		result, err := s.complete(ctx, model, messages)
		t.Logf("stage=%s class=%s http=%d elapsed_ms=%d tool_calls=%d", stage, liveRelayResultClass(transport.status, err != nil), transport.status, time.Since(started).Milliseconds(), len(result.ToolCalls))
		if err != nil {
			t.Fatal("relay request failed; response omitted")
		}
		if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != expected || !s.validToolArgumentKeys(expected, result.ToolCalls[0].Function.Arguments) {
			names := []string{}
			for _, call := range result.ToolCalls {
				if toolCapabilityAllowed(call.Function.Name) {
					names = append(names, call.Function.Name)
				} else {
					names = append(names, "<unregistered>")
				}
			}
			t.Fatalf("expected %s, got %v; no tools executed", expected, names)
		}
		if transport.model != model || transport.toolCount != len(s.toolDefinitions()) || mock.ExpectationsWereMet() != nil {
			t.Fatal("request/configuration contract mismatch")
		}
		messages = append(messages, result)
		return result.ToolCalls[0]
	}
	call := invoke("global_open_tickets", "tickets_list_global")
	args, err := decodeTicketArgs(call.Function.Name, call.Function.Arguments)
	if err != nil || args.Status == nil || *args.Status != 0 || args.ReplyStatus != nil || args.UserID != 0 || args.Email != "" || args.Offset != 0 {
		t.Fatal("global open tickets query added a user restriction or wrong status")
	}
	messages = append(messages, providerMessage{Role: "tool", ToolCallID: call.ID, Content: `{"items":[{"id":987654,"user_id":987650,"email":"fixture@example.test","subject":"提现申请","status":0,"reply_status":0,"created_at":1793203200,"updated_at":1793203200}],"total":1,"limit":50,"offset":0,"truncated":false}`})
	call = invoke("ticket_messages", "ticket_get")
	args, err = decodeTicketArgs(call.Function.Name, call.Function.Arguments)
	if err != nil || args.TicketID != 987654 || args.Offset != 0 {
		t.Fatal("wrong ticket selected")
	}
	messages = append(messages, providerMessage{Role: "tool", ToolCallID: call.ID, Content: `{"ticket":{"id":987654,"user_id":987650,"email":"fixture@example.test","subject":"提现申请","status":0,"reply_status":0,"created_at":1793203200},"messages":{"items":[{"id":987651,"user_id":987650,"from_staff":false,"message":"提现申请的详细说明较长，申请金额在消息后半部分，请读取余文后统计。","total_characters":2300,"truncated":true,"next_content_offset":2000,"created_at":1793203200}],"total":1,"limit":50,"offset":0,"truncated":false}}`})
	call = invoke("long_message_continuation", "ticket_message_get")
	var contentArgs struct {
		TicketID      int64 `json:"ticket_id"`
		MessageID     int64 `json:"message_id"`
		ContentOffset int   `json:"content_offset"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &contentArgs) != nil || contentArgs.TicketID != 987654 || contentArgs.MessageID != 987651 || contentArgs.ContentOffset != 2000 {
		t.Fatal("long message continuation does not match returned cursor")
	}
}
