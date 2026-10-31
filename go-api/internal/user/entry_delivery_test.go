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

func TestEntryDeliveryRuntimeFirstMatchSplitDNSAndExtra(t *testing.T) {
	collector := entryevidence.NewCollector("subscription", "Forest")
	ctx := entryevidence.WithCollector(context.Background(), collector)
	calls := 0
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls++
		return []netip.Addr{netip.MustParseAddr("192.0.2.9")}, nil
	})
	policies := []clientEntryUserPolicy{
		{ID: 1, Action: cliententry.ActionHide, Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 2}}},
		{ID: 2, Mode: "split", Action: cliententry.ActionOverride, EntryHost: "not-used.example", AssignedEntryHost: "split.example", AssignedGroupID: 22, UpdatedAt: 4, AssignedUpdatedAt: 5, ResolveEntryHost: true, Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}, {ServerType: "vmess", ServerID: 2}}, ExtraNodes: []string{"trojan://DO-NOT-STORE-CREDENTIAL@extra.example:8443#extra"}},
		{ID: 3, Action: cliententry.ActionOverride, EntryHost: "later.example", Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}}},
	}
	result := applyClientEntryUserPoliciesWithResolver(ctx, []map[string]any{{"id": int64(1), "type": "vmess", "host": "origin.example", "port": 54101}, {"id": int64(2), "type": "vmess", "host": "hidden.example", "port": 443}, {"id": int64(3), "type": "trojan", "host": "default.example", "port": 443}}, cliententry.Subject{UserID: 10}, policies, resolver)
	if len(result) != 3 || calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
	n, ok := collector.Observed("vmess", 1)
	if !ok || n.PolicyID != 2 || n.GroupID != 22 || n.Source != "split" || n.SourceHost != "split.example" || n.DeliveredHost != "192.0.2.9" || !n.Resolved || len(n.RuleRevision) != 64 {
		t.Fatalf("wrong runtime provenance: %+v", n)
	}
	if _, ok := collector.Observed("vmess", 2); ok {
		t.Fatal("hidden node observed as delivered")
	}
	n, _ = collector.Observed("trojan", 3)
	if n.Source != "default" || n.PolicyID != 0 {
		t.Fatalf("default: %+v", n)
	}
	extra, ok := collector.Observed("trojan", -2001)
	if !ok || extra.Source != "extra" || extra.DeliveredHost != "extra.example" || extra.PolicyID != 2 {
		t.Fatalf("extra: %+v", extra)
	}
	collector.Emit("trojan", -2001, "extra.example", "8443")
	collector.Coverage("complete", "server_write_completed")
	request, nodes := collector.Finish(200, 10, "")
	raw, _ := json.Marshal(struct {
		Request entryevidence.Request
		Nodes   []entryevidence.Node
	}{request, nodes})
	if strings.Contains(string(raw), "DO-NOT-STORE-CREDENTIAL") {
		t.Fatal("credential stored")
	}
	for _, row := range result {
		for _, key := range []string{"rule_revision", "source_host", "delivered_host", "entry_evidence"} {
			if _, ok := row[key]; ok {
				t.Fatal("private provenance leaked to client map")
			}
		}
	}
}

func TestEntryDeliverySupportOverrideRecordsFinalTarget(t *testing.T) {
	collector := entryevidence.NewCollector("subscription", "Forest")
	ctx := entryevidence.WithCollector(context.Background(), collector)
	members := []ClientEntryGroupMember{{ServerType: "vless", ServerID: 7}}
	policies := []clientEntryUserPolicy{{ID: 4, Action: cliententry.ActionOverride, EntryHost: "auto.example", Members: members}}
	servers := applyClientEntryUserPoliciesWithResolver(ctx, []map[string]any{{"id": int64(7), "type": "vless", "host": "origin.example", "port": 54101}}, cliententry.Subject{UserID: 10}, policies, nil)
	target := SupportEntryTarget{Key: "group:23", PolicyID: 8, GroupID: 23, EntryHost: "manual.example", Members: members}
	records := []SupportEntryOverride{{SourcePolicyID: 4, TargetPolicyID: 8, TargetGroupID: 23, TargetKey: target.Key, Active: true, Version: 9}}
	result := applySupportEntryOverrides(ctx, servers, policies, records, []SupportEntryTarget{target}, 10, 100, nil)
	trace, _ := collector.Observed("vless", 7)
	if result[0]["host"] != "manual.example" || trace.Source != "support_override" || trace.PolicyID != 8 || trace.GroupID != 23 || trace.SourceHost != "manual.example" || trace.DeliveredHost != "manual.example" {
		t.Fatalf("final provenance %+v", trace)
	}
}
