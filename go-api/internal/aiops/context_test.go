package aiops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAIContextCompactionPreservesCurrentInputActionsAndSystem(t *testing.T) {
	messages := []providerMessage{
		{Role: "system", Content: "BASE POLICY\nFULL ACCESS POLICY"},
		{Role: "user", Content: "old question"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "old-call", Type: "function", Function: ToolFunction{Name: "entry_health", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "old-call", Content: strings.Repeat("<>&", 1500)},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "CURRENT USER"},
		{Role: "user", Content: `{"action_id":7,"status":"unknown"}`, contextKind: "actions"},
	}
	before, _ := json.Marshal(messages)
	out, err := compactProviderContext(messages, 3000)
	if err != nil {
		t.Fatal(err)
	}
	assertContextToolPairs(t, out)
	raw, _ := json.Marshal(out)
	if len(raw) > 3000 || out[0].Content != messages[0].Content || strings.Contains(string(raw), "old-call") || strings.Contains(string(raw), "old answer") || !strings.Contains(string(raw), "CURRENT USER") || !strings.Contains(string(raw), "unknown") {
		t.Fatal("context not compacted atomically", string(raw))
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("compaction mutated caller history")
	}
	again, err := compactProviderContext(out, 3000)
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, message := range again {
		if message.contextKind == "omitted" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatal("compaction duplicated notice", notices)
	}
}

func TestAIContextCompactionToolBatchAndMessageLimit(t *testing.T) {
	messages := []providerMessage{{Role: "system", Content: "policy"}, {Role: "user", Content: "current"}}
	for i := 0; i < 170; i++ {
		messages = append(messages, providerMessage{Role: "assistant", Content: "old intermediate"})
	}
	messages = append(messages, providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "latest", Type: "function", Function: ToolFunction{Name: "panel_status", Arguments: `{}`}}}}, providerMessage{Role: "tool", ToolCallID: "latest", Content: `{"healthy":true}`})
	out, err := compactProviderContext(messages, 240000)
	if err != nil || len(out) > maxContextMessages {
		t.Fatal("message cap", len(out), err)
	}
	assertContextToolPairs(t, out)
	if out[len(out)-1].ToolCallID != "latest" {
		t.Fatal("latest tool batch unnecessarily dropped")
	}
	for _, bad := range [][]providerMessage{
		{{Role: "user", Content: "current"}, {Role: "tool", ToolCallID: "missing", Content: "{}"}},
		{{Role: "user", Content: "current"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "missing"}}}},
	} {
		if _, err := compactProviderContext(bad, 240000); !errors.Is(err, errContextInvalid) {
			t.Fatal("invalid tool structure accepted", err)
		}
	}
	if _, err := compactProviderContext([]providerMessage{{Role: "user", Content: strings.Repeat("x", 2000)}}, 1000); !errors.Is(err, errContextTooLarge) {
		t.Fatal("current administrator input silently shortened", err)
	}
}

func TestAIContextTextTruncationPreservesUTF8(t *testing.T) {
	for i := 0; i < 50; i++ {
		out := contextText(strings.Repeat("中文😀", 20), i)
		if len(out) > i || !utf8.ValidString(out) {
			t.Fatalf("invalid truncated text %d %q", i, out)
		}
	}
}
