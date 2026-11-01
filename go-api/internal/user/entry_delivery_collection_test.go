package user

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/entryevidence"
)

func TestEntryDeliveryCollectionSnapshotAndDNSCache(t *testing.T) {
	calls := 0
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls++
		return []netip.Addr{netip.MustParseAddr("192.0.2.19")}, nil
	})
	policy := clientEntryUserPolicy{ID: 1, Mode: "split", Action: cliententry.ActionOverride,
		AssignedGroupID: 9, AssignedEntryHost: "entry.example.test", ResolveEntryHost: true,
		CollectionID: 21, CollectionVersion: 4, Members: []ClientEntryGroupMember{{ServerType: "vless", ServerID: 3}}}
	var priorID, priorRevision string
	for i := 0; i < 3; i++ {
		c := entryevidence.NewCollector("subscription", "Forest")
		c.SetUser(15, "fixture@example.test")
		ctx := entryevidence.WithCollector(context.Background(), c)
		servers := applyClientEntryUserPoliciesWithResolver(ctx, []map[string]any{{"id": int64(3), "type": "vless", "host": "origin.example", "port": 54101}}, cliententry.Subject{UserID: 15}, []clientEntryUserPolicy{policy}, resolver)
		c.Emit("vless", 3, servers[0]["host"].(string), "54101")
		c.Coverage("complete", "server_write_completed")
		r, nodes := c.Finish(200, 50, "")
		if !r.ResponseComplete || r.ID == priorID || len(nodes) != 1 || nodes[0].CollectionID != 21 || nodes[0].GroupID != 9 || !nodes[0].Resolved || nodes[0].DeliveredHost != "192.0.2.19" {
			t.Fatal("cache hit omitted or changed historical delivery", r, nodes)
		}
		if i > 0 && nodes[0].RuleRevision != priorRevision {
			t.Fatal("cache hit changed revision")
		}
		priorID, priorRevision = r.ID, nodes[0].RuleRevision
	}
	if calls != 1 {
		t.Fatal("expected real DNS cache hits", calls)
	}
	policy.CollectionVersion++
	c := entryevidence.NewCollector("subscription", "Forest")
	applyClientEntryUserPoliciesWithResolver(entryevidence.WithCollector(context.Background(), c), []map[string]any{{"id": int64(3), "type": "vless", "host": "origin.example", "port": 54101}}, cliententry.Subject{UserID: 15}, []clientEntryUserPolicy{policy}, resolver)
	n, _ := c.Observed("vless", 3)
	if n.RuleRevision == priorRevision {
		t.Fatal("collection version not captured")
	}
}

func TestEntryDeliveryDefaultRevisionContainsOnlySafeIdentity(t *testing.T) {
	revision := func(updated int64, secret string, port int) string {
		c := entryevidence.NewCollector("subscription", "Forest")
		applyClientEntryUserPoliciesWithResolver(entryevidence.WithCollector(context.Background(), c), []map[string]any{{"id": int64(3), "type": "vless", "host": "origin.example", "port": port, "updated_at": updated, "password": secret, "uuid": secret}}, cliententry.Subject{}, nil, nil)
		n, ok := c.Observed("vless", 3)
		if !ok || len(n.RuleRevision) != 64 || n.Source != "default" || n.CollectionID != 0 {
			t.Fatal(n)
		}
		raw, _ := json.Marshal(n)
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret recorded")
		}
		return n.RuleRevision
	}
	a := revision(100, "SENTINEL_ONE", 54101)
	// Randomized ports and credential changes are not a routing rule revision.
	if a != revision(100, "SENTINEL_TWO", 54102) || a == revision(101, "SENTINEL_THREE", 54101) {
		t.Fatal("default digest included secrets or omitted edit version")
	}
}

func TestEntryDeliverySupportOverrideCollectionUsesFinalTarget(t *testing.T) {
	c := entryevidence.NewCollector("subscription", "Forest")
	ctx := entryevidence.WithCollector(context.Background(), c)
	members := []ClientEntryGroupMember{{ServerType: "vless", ServerID: 7}}
	policies := []clientEntryUserPolicy{{ID: 4, Action: cliententry.ActionOverride, EntryHost: "auto.example", CollectionID: 99, Members: members}}
	servers := applyClientEntryUserPoliciesWithResolver(ctx, []map[string]any{{"id": int64(7), "type": "vless", "host": "origin.example", "port": 54101}}, cliententry.Subject{UserID: 10}, policies, nil)
	target := SupportEntryTarget{Key: "group:23", PolicyID: 8, GroupID: 23, CollectionID: 21, CollectionVersion: 4, EntryHost: "manual.example", Members: members}
	records := []SupportEntryOverride{{SourcePolicyID: 4, TargetPolicyID: 8, TargetGroupID: 23, TargetKey: target.Key, Active: true, Version: 9}}
	applySupportEntryOverrides(ctx, servers, policies, records, []SupportEntryTarget{target}, 10, 100, nil)
	n, _ := c.Observed("vless", 7)
	if n.CollectionID != 21 || n.PolicyID != 8 || n.GroupID != 23 {
		t.Fatal("recorded original collection instead of actual target", n)
	}
	before := n.RuleRevision
	target.CollectionVersion++
	applySupportEntryOverrides(ctx, servers, policies, records, []SupportEntryTarget{target}, 10, 100, nil)
	n, _ = c.Observed("vless", 7)
	if n.RuleRevision == before {
		t.Fatal("support target collection version not captured")
	}
}
