package entryevidence

import "testing"

func TestCollectorResolvedRequiresDomainToFinalIP(t *testing.T) {
	for _, tc := range []struct {
		source, observed, final string
		want                    bool
	}{
		{"entry.example.test", "192.0.2.1", "192.0.2.2", true},
		{"entry.example.test", "192.0.2.1", "entry.example.test", false},
		{"2001:0db8::1", "2001:db8::1", "2001:db8::1", false},
		{"192.0.2.1", "192.0.2.2", "192.0.2.2", false},
		{"", "192.0.2.1", "192.0.2.1", false},
	} {
		c := NewCollector("subscription", "Forest")
		c.Observe(Node{ServerType: "vless", ServerID: 1, Source: "policy", SourceHost: tc.source, DeliveredHost: tc.observed, Resolved: true})
		c.Emit("vless", 1, tc.final, "54101")
		_, nodes := c.Finish(200, 10, "")
		if len(nodes) != 1 || nodes[0].Resolved != tc.want {
			t.Fatal(tc, nodes)
		}
	}
}
