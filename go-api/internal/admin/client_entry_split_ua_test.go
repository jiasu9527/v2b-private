package admin

import (
	"testing"

	"forest/go-api/internal/cliententry"
)

func TestSplitUAGateMatchesSimulationAndSubscribeGuard(t *testing.T) {
	conditions, err := cliententry.DecodeConditions(`[{"field":"user_id","operator":"between","min":100,"max":200},{"field":"ua","operator":"contains_any","values":["Forest"]},{"field":"ua","operator":"excludes_any","values":["curl"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	split := guardSearchTestPolicy(9, 10, 1)
	split.Mode = ClientEntryUserPolicyModeSplit
	split.Conditions = conditions
	split.SplitGroups = []ClientEntryUserPolicySplitGroupRecord{{ID: 71, PolicyID: 9, Name: "A.1", IsLeaf: true, EntryHost: "a1.example.com"}}
	policies := []ClientEntryUserPolicyRecord{split, guardSearchTestPolicy(10, 20, 1)}
	snapshot := guardSearchTestSnapshot()
	snapshot.Policies = policies
	snapshot.Assignments[1] = map[int64]int64{9: 71}
	for _, tc := range []struct {
		ua   string
		want int64
	}{{"Forest Android", 9}, {"Forest curl", 10}, {"Clash", 10}, {"", 10}} {
		t.Run(tc.ua, func(t *testing.T) {
			match := selectClientEntryUserPolicySimulation(policies, snapshot.Assignments[1], cliententry.Subject{UserID: 1, UA: tc.ua}, "vmess", 1)
			if match == nil || match.ID != tc.want {
				t.Fatalf("simulation mismatch: %#v", match)
			}
			matches, unknown := snapshot.Match(1, tc.ua, true)
			if unknown || len(matches) != 1 || matches[0].ID != tc.want {
				t.Fatalf("guard mismatch: %#v unknown %v", matches, unknown)
			}
		})
	}
	if matches, unknown := snapshot.Match(1, "", false); !unknown || len(matches) != 0 {
		t.Fatalf("unknown UA must not claim split or lower-priority winner: %#v unknown %v", matches, unknown)
	}
	delete(snapshot.Assignments, 1)
	if matches, unknown := snapshot.Match(1, "", false); unknown || len(matches) != 1 || matches[0].ID != 10 {
		t.Fatalf("unassigned UA rule must not shadow fallback: %#v unknown %v", matches, unknown)
	}
}
