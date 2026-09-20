package admin

import (
	"forest/go-api/internal/cliententry"
	"testing"
)

func TestClientEntrySimulationRespectsLeafResolveSetting(t *testing.T) {
	on, off := int64(1), int64(0)
	for _, test := range []struct {
		name     string
		parent   int64
		leaf     *int64
		expected int64
	}{
		{"collection enabled leaf", 0, &on, 1},
		{"collection disabled leaf", 1, &off, 0},
		{"legacy leaf inherits root", 1, nil, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			policies := []ClientEntryUserPolicyRecord{{ID: 1, Mode: ClientEntryUserPolicyModeSplit, Enabled: 1, ResolveEntryHost: test.parent, Members: []ClientEntryGroupMemberRecord{{ServerType: "vmess", ServerID: 11}}, SplitGroups: []ClientEntryUserPolicySplitGroupRecord{{ID: 9, Name: "leaf", IsLeaf: true, EntryHost: "entry.example.com", ResolveEntryHost: test.leaf}}}}
			got := selectClientEntryUserPolicySimulation(policies, map[int64]int64{1: 9}, cliententry.Subject{UserID: 5}, "vmess", 11)
			if got == nil || got.ResolveEntryHost != test.expected {
				t.Fatalf("got %#v; want resolve=%d", got, test.expected)
			}
			if policies[0].ResolveEntryHost != test.parent {
				t.Fatal("simulation mutated parent setting")
			}
		})
	}
}
