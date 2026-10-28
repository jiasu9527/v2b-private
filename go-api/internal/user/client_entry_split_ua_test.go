package user

import (
	"testing"

	"forest/go-api/internal/cliententry"
)

func TestSplitEntryUAIsCheckedForEveryRequestAndContinuedLeaf(t *testing.T) {
	conditions, err := cliententry.DecodeConditions(`[{"field":"user_id","operator":"between","min":100,"max":200},{"field":"ua","operator":"contains_any","values":["Forest"]},{"field":"ua","operator":"excludes_any","values":["curl"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	members := []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}, {ServerType: "vmess", ServerID: 2}}
	for _, leafHost := range []string{"a.example.com", "a1.example.com"} {
		for _, tc := range []struct {
			ua                  string
			assigned, wantSplit bool
		}{
			{"Forest Android", true, true}, {"Forest curl", true, false}, {"Clash", true, false}, {"", true, false}, {"Forest", false, false},
		} {
			t.Run(leafHost+"/"+tc.ua, func(t *testing.T) {
				assignedHost := leafHost
				if !tc.assigned {
					assignedHost = ""
				}
				policies := []clientEntryUserPolicy{
					{ID: 9, Mode: "split", Action: "override", AssignedEntryHost: assignedHost, Conditions: conditions, Members: members},
					{ID: 10, Action: "override", EntryHost: "default.example.com", Members: members[:1]},
				}
				servers := []map[string]any{{"type": "vmess", "id": int64(1), "host": "original.example.com"}, {"type": "vmess", "id": int64(2), "host": "private.example.com", "client_entry_only": int64(1)}}
				// Saved membership, rather than the retained source range, is authoritative.
				result := applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 999, UA: tc.ua}, policies)
				if tc.wantSplit {
					if len(result) != 2 || result[0]["host"] != leafHost || result[1]["host"] != leafHost {
						t.Fatalf("matching request lost fixed leaf: %#v", result)
					}
				} else if len(result) != 1 || result[0]["host"] != "default.example.com" {
					t.Fatalf("UA mismatch or missing assignment bypassed the request gate: %#v", result)
				}
			})
		}
	}
}
