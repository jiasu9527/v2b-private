package aiops

import (
	"context"
	"encoding/json"
	"errors"
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

func TestKnowledgeWriteSchemasKeepCreateSeparate(t *testing.T) {
	s := &Service{}
	found := map[string]bool{}
	ticketTools := map[string]bool{}
	for _, tool := range ticketToolDefinitions() {
		ticketTools[tool.Function.Name] = true
	}
	for _, tool := range s.toolDefinitions() {
		var wire struct {
			Function map[string]json.RawMessage `json:"function"`
		}
		raw, err := json.Marshal(tool)
		if err != nil || json.Unmarshal(raw, &wire) != nil {
			t.Fatal("cannot read tool wire schema", err)
		}
		strict, exists := wire.Function["strict"]
		if strings.HasPrefix(tool.Function.Name, "knowledge_") {
			if !exists || string(strict) != "false" {
				t.Fatal("knowledge tools must explicitly preserve optional parameters", tool.Function.Name, string(strict))
			}
		} else if exists && !ticketTools[tool.Function.Name] {
			t.Fatal("knowledge compatibility changed other tool strict settings", tool.Function.Name)
		}
		if tool.Function.Name != "knowledge_create" && tool.Function.Name != "knowledge_save" {
			continue
		}
		found[tool.Function.Name] = true
		var schema struct {
			Properties map[string]struct {
				Type    string `json:"type"`
				Minimum int64  `json:"minimum"`
				Maximum int64  `json:"maximum"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties bool     `json:"additionalProperties"`
		}
		if json.Unmarshal(tool.Function.Parameters, &schema) != nil || schema.AdditionalProperties {
			t.Fatal("invalid knowledge write schema")
		}
		required := map[string]bool{}
		for _, key := range schema.Required {
			required[key] = true
		}
		for _, key := range []string{"title", "summary", "content", "category", "source_note"} {
			if !required[key] {
				t.Fatal("knowledge content requirement missing", tool.Function.Name, key)
			}
		}
		for _, key := range []string{"id", "expected_version"} {
			property, present := schema.Properties[key]
			if required[key] {
				t.Fatal("new knowledge requires update identity", tool.Function.Name, key)
			}
			if tool.Function.Name == "knowledge_create" && present {
				t.Fatal("create exposes update identity", key)
			}
			if tool.Function.Name == "knowledge_save" && (!present || property.Type != "integer" || property.Minimum != 1 || property.Maximum != 9007199254740991) {
				t.Fatal("save identity schema unsafe", key, property)
			}
		}
	}
	if !found["knowledge_create"] || !found["knowledge_save"] {
		t.Fatal("new and compatibility write tools both required", found)
	}
}

func TestKnowledgeToolIdentityFieldAllowlists(t *testing.T) {
	s := &Service{}
	base := `"title":"fixture","summary":"fixture","content":"fixture","category":"other","source_note":"fixture"`
	for _, name := range []string{"knowledge_create", "knowledge_save"} {
		if !s.validToolArgumentKeys(name, "{"+base+"}") {
			t.Fatal("create without identity rejected", name)
		}
		for _, extra := range []string{`"source_session_id":1`, `"source_turn_id":1`, `"created_by":1`, `"status":"active"`} {
			if s.validToolArgumentKeys(name, "{"+base+","+extra+"}") {
				t.Fatal("forged provenance accepted", name, extra)
			}
		}
	}
	for _, extra := range []string{`"id":1`, `"expected_version":1`, `"id":0`, `"id":null`, `"id":1,"expected_version":1`} {
		if s.validToolArgumentKeys("knowledge_create", "{"+base+","+extra+"}") {
			t.Fatal("create accepted update identity", extra)
		}
	}
	if !s.validToolArgumentKeys("knowledge_save", "{"+base+`,"id":1,"expected_version":2}`) {
		t.Fatal("versioned save rejected by declared fields")
	}
}

func TestKnowledgeWriteRuntimeRejectsAmbiguousIdentity(t *testing.T) {
	s := &Service{}
	ctx := context.WithValue(context.Background(), knowledgeTurnContextKey{}, int64(1))
	base := `"title":"fixture","summary":"fixture","content":"fixture","category":"other","source_note":"fixture"`
	for _, name := range []string{"knowledge_create", "knowledge_save"} {
		for _, extra := range []string{`"id":1`, `"expected_version":1`, `"id":0,"expected_version":0`, `"id":null,"expected_version":1`, `"id":1,"expected_version":null`, `"id":-1,"expected_version":1`, `"id":1,"expected_version":-1`, `"id":1.5,"expected_version":1`, `"id":"1","expected_version":1`, `"id":1,"expected_version":"1"`, `"id":9007199254740992,"expected_version":1`, `"id":1,"expected_version":9007199254740992`} {
			t.Run(name+"/"+extra, func(t *testing.T) {
				_, err := s.callKnowledgeTool(ctx, 1, 1, ToolCall{ID: "fixture", Type: "function", Function: ToolFunction{Name: name, Arguments: "{" + base + "," + extra + "}"}})
				if !errors.Is(err, ErrValidation) {
					t.Fatal("invalid identity reached service or was accepted", err)
				}
			})
		}
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
