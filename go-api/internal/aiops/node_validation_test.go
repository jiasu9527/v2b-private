package aiops

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
)

func TestNodeValidationToolErrorExplainsRejectionWithoutInputValues(t *testing.T) {
	params := map[string]any{
		"name": "fixture", "host": "192.0.2.180", "group_id": []any{"1"},
		"port": "443", "server_port": 443, "rate": 1, "tls": 1,
		"network": "tcp", "tls_settings": map[string]any{"private_key": "PRIVATE_SENTINEL"},
	}
	err := admin.ValidateManagedServerSavePayload("v2node", params)
	var validation *admin.ManagedServerValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected a definite pre-write rejection, got %v", err)
	}
	// Wrapping context and submitted secrets must not enter a tool result.
	safe := safeAIError(fmt.Errorf("RAW_SECRET: %w", err))
	var result map[string]string
	raw := toolErrorContent(safe)
	if json.Unmarshal([]byte(raw), &result) != nil || result["code"] != "invalid_arguments" || !strings.Contains(result["message"], "协议不能为空") || !strings.Contains(result["message"], "未执行节点保存") {
		t.Fatalf("missing actionable validation result: %s", raw)
	}
	if strings.Contains(raw, "SECRET") || strings.Contains(raw, "PRIVATE_SENTINEL") || strings.Contains(raw, params["host"].(string)) {
		t.Fatalf("validation disclosed submitted values: %s", raw)
	}
}
