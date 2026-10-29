package aiops

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKnowledgeSearchEscapingPreservesPageCapacity(t *testing.T) {
	items := []knowledgeToolSummary{}
	for i := 0; i < 10; i++ {
		entry := KnowledgeEntry{ID: int64(i + 1), Title: strings.Repeat("<", 120), Summary: strings.Repeat("&", 500), Category: "troubleshooting", Version: 9007199254740991, UpdatedAt: 1793260800, Pinned: true}
		item := knowledgeSummary(entry)
		if !item.SummaryTruncated || !utf8.ValidString(item.Summary) {
			t.Fatal("truncated search summary was not marked or was malformed")
		}
		items = append(items, item)
	}
	raw, err := json.Marshal(map[string]any{"data": items, "total": 500, "current": 1, "page_size": 10, "next_current": 2, "truncated": true})
	if err != nil || len(raw) > maxToolOutputBytes {
		t.Fatal("a legal full search page would be rejected or skip records", len(raw), err)
	}
}

func TestKnowledgeCredentialErrorsAllowCorrectionWithoutEcho(t *testing.T) {
	var response map[string]string
	if err := json.Unmarshal([]byte(toolErrorContent(ErrKnowledgeCredential)), &response); err != nil {
		t.Fatal(err)
	}
	if response["code"] != "knowledge_contains_credentials" || !strings.Contains(response["message"], "删除凭据") {
		t.Fatal("knowledge credential rejection lacks specific correction", response)
	}
}
