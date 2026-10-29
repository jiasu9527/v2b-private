package aiops

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAIAutomaticActionSummaryPreservesStatusWithLargeOutput(t *testing.T) {
	for _, output := range []string{strings.Repeat("中文完整输出", 5000), strings.Repeat("<>&", 12000)} {
		raw := automaticActionSummary(Action{ID: 8, Kind: "ssh_command", Status: "succeeded", ApprovalMode: "full_access", Output: output})
		var decoded map[string]any
		if len(raw) > maxToolOutputBytes || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &decoded) != nil || decoded["status"] != "succeeded" || decoded["output_truncated"] != true {
			t.Fatalf("summary lost result status: %d bytes", len(raw))
		}
	}
}
