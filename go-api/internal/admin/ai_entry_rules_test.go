package admin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestAIEntryRuleRejectsAmbiguousPatchesBeforeDatabase(t *testing.T) {
	before := AIEntryRuleSnapshot{Kind: "policy", Name: "fixture", Action: "override", EntryHost: "old.example.test", Enabled: 1, Members: []AIEntryRuleMember{{ServerType: "vmess", ServerID: 1}}}
	for _, raw := range []string{
		`{}`, `{"name":null}`, `{"enabled":true}`, `{"enabled":2}`, `{"sort":-1}`, `{"conditions":null}`, `{"conditions":[{"field":"ua","operator":"contains","value":"Forest"}]}`, `{"conditions":[{"field":"email","operator":"in","values":[],"unknown":true}]}`, `{"members":[]}`, `{"members":[{"server_type":"vmess","server_id":1,"password":"secret"}]}`, `{"extra_nodes":["vmess://secret"]}`, `{"mode":"standard"}`, `{"name":"x\u0000y"}`,
	} {
		var changes map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &changes); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareAIEntryRuleChanges(context.Background(), nil, before, changes); !errors.Is(err, ErrAIEntryRuleInvalid) {
			t.Errorf("%s: %v", raw, err)
		}
	}
	before.Kind = "split_group"
	for _, raw := range []string{`{"conditions":[]}`, `{"action":"hide"}`} {
		var changes map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &changes)
		if _, err := PrepareAIEntryRuleChanges(context.Background(), nil, before, changes); !errors.Is(err, ErrAIEntryRuleInvalid) {
			t.Errorf("split %s: %v", raw, err)
		}
	}
}
