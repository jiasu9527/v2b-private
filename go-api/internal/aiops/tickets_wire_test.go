package aiops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

// Inspect the final encoded HTTP request, not only the definition builder.
// An omitted strict field is observably different from explicit strict:false.
func ticketContractAssertWire(t *testing.T, raw []byte) {
	t.Helper()
	var request struct {
		Tools []struct {
			Function struct {
				Name       string          `json:"name"`
				Strict     json.RawMessage `json:"strict"`
				Parameters struct {
					Required             []string                   `json:"required"`
					Properties           map[string]json.RawMessage `json:"properties"`
					AdditionalProperties bool                       `json:"additionalProperties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Error("cannot decode actual ticket provider request", err)
		return
	}
	wanted := map[string]bool{"tickets_list_global": false, "tickets_list": false, "ticket_get": false, "ticket_message_get": false, "ticket_settings": false, "propose_ticket_update": false, "propose_ticket_settings": false}
	for _, tool := range request.Tools {
		fn := tool.Function
		if _, exists := wanted[fn.Name]; !exists {
			continue
		}
		wanted[fn.Name] = true
		if string(fn.Strict) != "false" || fn.Parameters.AdditionalProperties {
			t.Errorf("%s must explicitly disable upstream strict normalization and unknown fields", fn.Name)
		}
		if fn.Name == "tickets_list" && len(fn.Parameters.Required) != 0 {
			t.Error("legacy ticket identity filters became mandatory", fn.Parameters.Required)
		}
		if fn.Name != "tickets_list_global" {
			continue
		}
		keys := make([]string, 0, len(fn.Parameters.Properties))
		for key := range fn.Parameters.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		sort.Strings(fn.Parameters.Required)
		expected := []string{"limit", "offset", "reply_status", "status"}
		if !reflect.DeepEqual(keys, expected) || !reflect.DeepEqual(fn.Parameters.Required, expected) {
			t.Error("global tool must expose exactly four explicit fields, never identity placeholders", keys, fn.Parameters.Required)
		}
		for field, expectedEnum := range map[string][]string{"status": {"all", "open", "closed"}, "reply_status": {"all", "pending", "replied"}} {
			var property struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			}
			if json.Unmarshal(fn.Parameters.Properties[field], &property) != nil {
				t.Error("cannot decode global status declaration", field)
				continue
			}
			sort.Strings(property.Enum)
			sort.Strings(expectedEnum)
			if property.Type != "string" || !reflect.DeepEqual(property.Enum, expectedEnum) {
				t.Error("global status must have an explicit all option", field, property)
			}
		}
	}
	for name, found := range wanted {
		if !found {
			t.Error("ticket tool missing from actual provider request", name)
		}
	}
}

func TestAITicketGlobalActualProviderWire(t *testing.T) {
	const arguments = `{"status":"open","reply_status":"all","limit":50,"offset":0}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxProviderRequestBytes+1))
		if err != nil || r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Error("invalid local relay request", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ticketContractAssertWire(t, raw)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "global-fixture", Type: "function", Function: ToolFunction{Name: "tickets_list_global", Arguments: arguments}}}}, "finish_reason": "tool_calls"}}})
	}))
	defer server.Close()
	s, mock := streamTestService(t, server.URL)
	result, err := s.complete(context.Background(), "gpt-6.1-sol", []providerMessage{{Role: "user", Content: "查询全站所有未关闭工单，包括已回复的工单，不按用户筛选。"}})
	if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "tickets_list_global" || result.ToolCalls[0].Function.Arguments != arguments {
		t.Fatal("global call changed while crossing the provider boundary", result, err)
	}
	if !s.validToolArgumentKeys(result.ToolCalls[0].Function.Name, result.ToolCalls[0].Function.Arguments) {
		t.Fatal("wire-compatible full global arguments rejected locally")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAITicketLegacyUserOneFilterNeverBecomesGlobal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\).*WHERE TRUE AND t.user_id=\$1 AND t.status=\$2 AND t.reply_status=\$3$`).WithArgs(int64(1), int64(0), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT t.id.*WHERE TRUE AND t.user_id=\$1 AND t.status=\$2 AND t.reply_status=\$3 ORDER BY t.id DESC LIMIT \$4 OFFSET \$5$`).WithArgs(int64(1), int64(0), int64(0), 50, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "subject", "level", "status", "reply_status", "created_at", "updated_at"}))
	result, err := s.callTicketTool(context.Background(), 1, 1, ToolCall{Function: ToolFunction{Name: "tickets_list", Arguments: `{"user_id":1,"email":"","status":0,"reply_status":0,"limit":50,"offset":0}`}})
	var page pageDTO[aiTicketDTO]
	if err != nil || json.Unmarshal([]byte(result.Content), &page) != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("legacy precise user filter was expanded or changed", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
