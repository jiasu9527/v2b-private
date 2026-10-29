package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAICapabilityCatalogCoversExistingToolsAndActions(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	declared := s.declaredToolDefinitions()
	if len(declared) != 37 || len(declared) != len(toolCapabilities) || len(s.toolDefinitions()) != len(declared) {
		t.Fatalf("tool capability coverage: declared=%d catalog=%d exposed=%d", len(declared), len(toolCapabilities), len(s.toolDefinitions()))
	}
	seen := map[string]bool{}
	actions := map[string]bool{}
	for _, definition := range declared {
		name := definition.Function.Name
		capability, exists := toolCapabilities[name]
		if !exists || seen[name] || !toolCapabilityAllowed(name) {
			t.Fatal("missing, duplicate or denied existing capability", name)
		}
		seen[name] = true
		if capability.ActionKind != "" {
			if actionCapabilities[capability.ActionKind] != capability.Domain || !actionCapabilityAllowed(capability.ActionKind) {
				t.Fatal("tool/action domain mismatch", name, capability)
			}
			actions[capability.ActionKind] = true
		}
	}
	if len(actions) != len(actionCapabilities) {
		t.Fatal("orphan action capability", actions, actionCapabilities)
	}
	for _, domain := range []capabilityDomain{domainPaymentConfiguration, "future_domain", ""} {
		if capabilityDomainAllowed(domain) {
			t.Fatal("protected or unknown domain allowed", domain)
		}
	}

	// New schema entries remain unavailable until explicitly classified.
	for _, name := range []string{"payment_configuration", "payment_config_get", "payment_config_save", "future_tool"} {
		definition := ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}}
		if len(permittedToolDefinitions([]ToolDefinition{definition})) != 0 || s.validToolArgumentKeys(name, `{}`) {
			t.Fatal("unclassified schema exposed", name)
		}
	}
}

func TestAICapabilityRejectsUnknownBeforeSideEffects(t *testing.T) {
	// No database or panel service is available: denial must precede schema,
	// authorization queries, action persistence and external service dispatch.
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	for _, name := range []string{"payment_configuration", "payment_config_get", "payment_config_save", "future_tool"} {
		if _, err := s.callTool(context.Background(), 1, 2, ToolCall{ID: "fixture", Function: ToolFunction{Name: name, Arguments: `{}`}}); !errors.Is(err, ErrForbidden) {
			t.Fatal("tool not rejected before dependencies", name, err)
		}
		if _, err := s.proposeAction(context.Background(), 1, 2, "fixture", actionSpec{Kind: name, Payload: json.RawMessage(`{}`), Reason: "fixture"}); !errors.Is(err, ErrForbidden) {
			t.Fatal("proposal not rejected before persistence", name, err)
		}
		if _, _, _, err := s.captureActionTarget(context.Background(), nil, actionSpec{Kind: name}); !errors.Is(err, ErrForbidden) {
			t.Fatal("capture accepted forbidden kind", name, err)
		}
		if err := applyPanelAction(context.Background(), nil, Action{Kind: name}); !errors.Is(err, ErrForbidden) {
			t.Fatal("panel mutation accepted forbidden kind", name, err)
		}
	}
	for _, key := range []string{"payment_id", "payment_config", "merchant_id", "merchant_key", "notify_url"} {
		if _, err := normalizeUserChanges(map[string]json.RawMessage{key: json.RawMessage(`"fixture"`)}); !errors.Is(err, ErrValidation) {
			t.Fatal("payment field writable through user update", key, err)
		}
	}
}

type boundaryPanel struct {
	Panel
	called int
	row    map[string]any
}

func (p *boundaryPanel) FetchOrders(context.Context, admin.OrderFetchRequest) (admin.OrderListResult, error) {
	p.called++
	return admin.OrderListResult{Total: 1, Data: []map[string]any{p.row}}, nil
}

func (p *boundaryPanel) ListUsers(context.Context, admin.UserFetchRequest) (admin.UserListResult, error) {
	p.called++
	return admin.UserListResult{Total: 1, Data: []map[string]any{p.row}}, nil
}

func TestAICapabilityOrderAndUserToolsExcludePaymentConfiguration(t *testing.T) {
	for _, tool := range []string{"orders_list", "user_lookup"} {
		t.Run(tool, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			row := map[string]any{"id": int64(9), "trade_no": "order-123", "user_id": int64(7), "total_amount": int64(1200), "status": int64(3), "email": "user@example.test"}
			for _, key := range []string{"payment_id", "callback_no", "payment_config", "merchant_id", "merchant_key", "notify_url", "gateway_url", "private_key", "public_key", "password", "uuid", "token", "subscribe_url"} {
				row[key] = "PAYMENT_OR_CREDENTIAL_SENTINEL"
			}
			panel := &boundaryPanel{row: row}
			s := NewService(config.Config{}, db, panel)
			defer s.Close()
			mock.ExpectQuery(`SELECT id FROM v2_user`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			mock.ExpectQuery(`SELECT s.id FROM v2_ai_session s JOIN v2_user`).WithArgs(int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
			arguments := `{}`
			if tool == "user_lookup" {
				arguments = `{"query":"user@example.test"}`
			}
			result, err := s.callTool(context.Background(), 1, 2, ToolCall{ID: "fixture", Function: ToolFunction{Name: tool, Arguments: arguments}})
			if err != nil || panel.called != 1 || strings.Contains(result.Content, "SENTINEL") || strings.Contains(result.Content, "payment_") || strings.Contains(result.Content, "callback_no") {
				t.Fatal("sensitive fields reached model", result, panel.called, err)
			}
			if tool == "orders_list" && (!strings.Contains(result.Content, `"trade_no":"order-123"`) || !strings.Contains(result.Content, `"total_amount_cents":1200`)) {
				t.Fatal("ordinary order information lost", result)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
