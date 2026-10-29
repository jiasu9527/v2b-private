package aiops

import (
	"strings"
	"testing"
)

func TestPanelMessagesRedactDeclaredCredentialsWithoutChangingExecution(t *testing.T) {
	args := `{"operation":"config.save","parameters":{"email_password":"mail-secret-123","email_host":"smtp.example.test"},"reason":"set mail-secret-123"}`
	batch := []providerMessage{
		{Role: "assistant", Content: "准备保存 mail-secret-123 到 smtp.example.test", ToolCalls: []ToolCall{{Function: ToolFunction{Name: "propose_panel_operation", Arguments: args}}}},
		{Role: "tool", Content: `{"status":"pending"}`},
		{Role: "assistant", Content: "mail-secret-123 等待确认"},
	}
	redacted := redactPanelMessages(batch)
	for _, message := range redacted {
		if strings.Contains(message.Content, "mail-secret-123") {
			t.Fatal("credential repeated in assistant text")
		}
		for _, call := range message.ToolCalls {
			if strings.Contains(call.Function.Arguments, "mail-secret-123") {
				t.Fatal("credential repeated in persisted arguments")
			}
		}
	}
	if batch[0].ToolCalls[0].Function.Arguments != args || !strings.Contains(batch[0].Content, "mail-secret-123") || !strings.Contains(redacted[0].Content, "smtp.example.test") {
		t.Fatal("redaction changed execution arguments or non-secret data")
	}
}
