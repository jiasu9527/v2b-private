package user

import (
	"fmt"
	"testing"

	"forest/go-api/internal/cliententry"
)

func entryIDConditions(t *testing.T, raw string) []cliententry.Condition {
	t.Helper()
	conditions, err := cliententry.DecodeConditions(raw)
	if err != nil {
		t.Fatal(err)
	}
	return conditions
}

func entryIDVisibleNodes(servers []map[string]any, policies []clientEntryUserPolicy, userID int64) map[int64]map[string]any {
	visible := applyClientEntryUserPolicies(cloneServerMapsForTest(servers), cliententry.Subject{UserID: userID}, policies)
	result := make(map[int64]map[string]any, len(visible))
	for _, server := range visible {
		result[mapInt64(server["id"])] = server
	}
	return result
}

func TestEntryIDVisibilitySingleAllowRulePreservesOtherNodeRouting(t *testing.T) {
	servers := []map[string]any{
		{"id": int64(17), "type": "v2node", "host": "limited.example.test", "client_entry_only": int64(1)},
		{"id": int64(18), "type": "vmess", "host": "ordinary-a.example.test"},
		{"id": int64(19), "type": "trojan", "host": "ordinary-b.example.test"},
	}
	existing := clientEntryUserPolicy{ID: 20, Action: cliententry.ActionOverride, EntryHost: "existing-entry.example.test", Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 18}, {ServerType: "trojan", ServerID: 19}}}
	for _, tc := range []struct{ name, conditions string }{
		{"lte", `[{"field":"user_id","operator":"lte","value":100000}]`},
		{"between", `[{"field":"user_id","operator":"between","min":1,"max":100000}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limited := clientEntryUserPolicy{ID: 10, Action: cliententry.ActionOriginal, Conditions: entryIDConditions(t, tc.conditions), Members: []ClientEntryGroupMember{{ServerType: "v2node", ServerID: 17}}}
			for _, userID := range []int64{1, 99999, 100000, 100001} {
				t.Run(fmt.Sprint(userID), func(t *testing.T) {
					visible := entryIDVisibleNodes(servers, []clientEntryUserPolicy{limited, existing}, userID)
					target, found := visible[17]
					allowed := userID <= 100000
					if found != allowed {
						t.Fatal("single allow rule failed ID boundary", userID, found)
					}
					if found && (target["host"] != "limited.example.test" || mapInt64(target["client_entry_user_policy_id"]) != 10) {
						t.Fatal("original action changed target host or lost its matching rule", target)
					}
					wantCount := 2
					if allowed {
						wantCount++
					}
					if len(visible) != wantCount {
						t.Fatal("single-node allow rule changed unrelated visibility", visible)
					}
					for _, nodeID := range []int64{18, 19} {
						if node := visible[nodeID]; node["host"] != "existing-entry.example.test" || mapInt64(node["client_entry_user_policy_id"]) != 20 {
							t.Fatal("target-only rule displaced another node's existing override", nodeID, node)
						}
					}
				})
			}
		})
	}
}

func TestEntryIDVisibilitySingleHideRulePreservesExistingOverride(t *testing.T) {
	servers := []map[string]any{
		{"id": int64(17), "type": "v2node", "host": "public.example.test", "client_entry_only": int64(0)},
		{"id": int64(18), "type": "vmess", "host": "ordinary.example.test"},
	}
	hide := clientEntryUserPolicy{ID: 10, Action: cliententry.ActionHide, Conditions: entryIDConditions(t, `[{"field":"user_id","operator":"gt","value":100000}]`), Members: []ClientEntryGroupMember{{ServerType: "v2node", ServerID: 17}}}
	existing := clientEntryUserPolicy{ID: 20, Action: cliententry.ActionOverride, EntryHost: "existing-entry.example.test", Members: []ClientEntryGroupMember{{ServerType: "v2node", ServerID: 17}, {ServerType: "vmess", ServerID: 18}}}
	for _, userID := range []int64{99999, 100000, 100001} {
		t.Run(fmt.Sprint(userID), func(t *testing.T) {
			visible := entryIDVisibleNodes(servers, []clientEntryUserPolicy{hide, existing}, userID)
			target, found := visible[17]
			if found != (userID <= 100000) {
				t.Fatal("highest-priority hide rule failed ID boundary", userID, found)
			}
			if found && (target["host"] != "existing-entry.example.test" || mapInt64(target["client_entry_user_policy_id"]) != 20) {
				t.Fatal("unmatched hide rule replaced eligible user's existing target route", target)
			}
			if other := visible[18]; other["host"] != "existing-entry.example.test" || mapInt64(other["client_entry_user_policy_id"]) != 20 {
				t.Fatal("target hide rule changed another member of existing override", other)
			}
		})
	}
	// Ordering is part of the configuration: an earlier allow wins even when a
	// later hide condition matches. A hide rule cannot be appended as a veto.
	if visible := entryIDVisibleNodes(servers, []clientEntryUserPolicy{existing, hide}, 100001); visible[17] == nil {
		t.Fatal("expected earlier override to take precedence over later hide")
	}
}

func TestEntryIDVisibilityConditionsAreANDAndSeparateRulesCanGrant(t *testing.T) {
	servers := []map[string]any{{"id": int64(17), "type": "v2node", "host": "limited.example.test", "client_entry_only": int64(1)}}
	member := []ClientEntryGroupMember{{ServerType: "v2node", ServerID: 17}}
	conditions := entryIDConditions(t, `[{"field":"user_id","operator":"gt","value":99999},{"field":"user_id","operator":"lte","value":100000}]`)
	combined := clientEntryUserPolicy{ID: 10, Action: cliententry.ActionOriginal, Conditions: conditions, Members: member}
	for _, userID := range []int64{99999, 100000, 100001} {
		visible := entryIDVisibleNodes(servers, []clientEntryUserPolicy{combined}, userID)
		if (visible[17] != nil) != (userID == 100000) {
			t.Fatal("conditions within a single rule must be AND", userID, visible)
		}
	}
	// Splitting the lower and upper bounds into independent allow rules changes
	// the intersection into alternatives and admits users outside the interval.
	split := []clientEntryUserPolicy{
		{ID: 10, Action: cliententry.ActionOriginal, Conditions: conditions[:1], Members: member},
		{ID: 11, Action: cliententry.ActionOriginal, Conditions: conditions[1:], Members: member},
	}
	for _, userID := range []int64{99999, 100000, 100001} {
		if visible := entryIDVisibleNodes(servers, split, userID); visible[17] == nil {
			t.Fatal("independent allow rules should demonstrate broader membership", userID)
		}
	}
	limited := clientEntryUserPolicy{ID: 10, Action: cliententry.ActionOriginal, Conditions: entryIDConditions(t, `[{"field":"user_id","operator":"lte","value":100000}]`), Members: member}
	if visible := entryIDVisibleNodes(servers, []clientEntryUserPolicy{limited}, 100001); visible[17] != nil {
		t.Fatal("entry-only target leaked without another granting rule")
	}
	otherGrant := clientEntryUserPolicy{ID: 20, Action: cliententry.ActionOverride, EntryHost: "other-entry.example.test", Members: member}
	visible := entryIDVisibleNodes(servers, []clientEntryUserPolicy{limited, otherGrant}, 100001)
	if target := visible[17]; target["host"] != "other-entry.example.test" || mapInt64(target["client_entry_user_policy_id"]) != 20 {
		t.Fatal("entry-only flag must not be mistaken for denying another matching grant", target)
	}
}
