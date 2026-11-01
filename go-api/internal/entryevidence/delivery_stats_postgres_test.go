package entryevidence

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDeliveryStatisticsPostgresHistoricalUnionAndVersions(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_ENTRY_EVIDENCE_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := NewStore(db)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`) })
	old := Node{ServerType: "vless", ServerID: 1, PolicyID: 7, GroupID: 11, ProviderID: 4, CollectionID: 3, RuleRevision: Revision("old"), SourceHost: "entry.example.test", DeliveredHost: "192.0.2.7", Port: "54101", Source: "split", Resolved: true}
	second := old
	second.ServerID = 2
	otherMember := old
	otherMember.PolicyID = 8
	otherMember.GroupID = 12
	otherMember.ServerID = 3
	otherMember.DeliveredHost = "192.0.2.8"
	write := func(id string, user, at int64, ua string, nodes ...Node) {
		t.Helper()
		r := Request{ID: id, UserID: user, EmailAtEvent: fmt.Sprintf("%d-%d@example.test", user, at), UA: ua, RequestedAt: at, CompletedAt: at, Transport: "subscription", Status: 200, ResponseComplete: true, Coverage: "complete"}
		if err := store.Record(ctx, r, nodes); err != nil {
			t.Fatal(err)
		}
	}
	// One user pulls the same two nodes 100 times; this is one user and 100
	// requests, not 200 requests. Different members share a collection.
	for i := 0; i < 100; i++ {
		write(fmt.Sprintf("repeat-%d", i), 71, int64(100+i), "Forest/1", old, second, otherMember)
	}
	write("other-user", 72, 205, "Clash/1", old, otherMember)
	newer := old
	newer.RuleRevision = Revision("new")
	newer.DeliveredHost = "192.0.2.9"
	write("new-entry", 71, 210, "Forest/1", newer)
	write("reused-old-entry", 73, 220, "Forest/1", old)
	// Same endpoint+rule from another provider/source must be a distinct
	// snapshot. Its node ID is deliberately negative for extra nodes.
	extra := old
	extra.ProviderID = 5
	extra.SourceHost = "other.example.test"
	extra.ServerID = -7001
	extra.Source = "extra"
	write("other-provider", 74, 230, "Forest/1", extra)
	unknown := old
	unknown.RuleRevision = ""
	write("revision-unknown", 75, 240, "Forest/1", unknown)
	// Requests which cannot prove complete delivery never enter counts.
	for _, mode := range []string{"partial", "failed", "blocked", "304", "missing", "badhost", "unknownsource", "empty"} {
		r := Request{ID: "excluded-" + mode, UserID: 99, RequestedAt: 250, CompletedAt: 250, Transport: "subscription", Status: 200, ResponseComplete: true, Coverage: "complete"}
		nodes := []Node{old}
		switch mode {
		case "partial":
			r.Coverage = "partial"
		case "failed":
			r.WriteError = "short_write"
		case "blocked":
			r.Status = 403
		case "304":
			r.Status = 304
		case "missing":
			nodes[0].DeliveredHost = ""
		case "badhost":
			nodes[0].DeliveredHost = "https://example.test/?token=SECRET"
		case "unknownsource":
			nodes[0].Source = "unexpected"
		case "empty":
			nodes = nil
		}
		if err := store.Record(ctx, r, nodes); err != nil {
			t.Fatal(err)
		}
	}
	assertStats := func(f Filter, users, requests, first, last int64) {
		t.Helper()
		got, err := store.Statistics(ctx, f)
		if err != nil || got.Users != users || got.Requests != requests || got.FirstAt != first || got.LastAt != last {
			t.Fatalf("filter=%+v got=%+v err=%v want=%d/%d/%d/%d", f, got, err, users, requests, first, last)
		}
	}
	assertStats(Filter{CollectionID: 3}, 5, 105, 100, 240)
	assertStats(Filter{Host: "192.0.2.7", PolicyID: 7, ProviderID: 4, RuleRevision: old.RuleRevision}, 3, 102, 100, 220)
	assertStats(Filter{Host: "192.0.2.7", UA: "Forest", From: 150, To: 200}, 1, 50, 150, 199)
	assertStats(Filter{PolicyID: 7, Host: "192.0.2.8"}, 0, 0, 0, 0)
	assertStats(Filter{ServerID: -7001, ServerType: "vless", ProviderID: 5}, 1, 1, 230, 230)
	assertStats(Filter{Transport: "forest_provider"}, 0, 0, 0, 0)
	assertStats(Filter{UA: "%"}, 0, 0, 0, 0)
	users, err := store.Users(ctx, Filter{Host: "192.0.2.7", ProviderID: 4, RuleRevision: old.RuleRevision, Limit: 1, Offset: 2})
	if err != nil || users.Total != 3 || len(users.Items) != 1 || users.Items[0].UserID != 71 || users.Items[0].Requests != 100 || users.Items[0].EmailAtEvent != "71-199@example.test" {
		t.Fatal(users, err)
	}
	users, err = store.Users(ctx, Filter{UserID: 71, Limit: 1, Offset: 9999})
	if err != nil || users.Total != 1 || len(users.Items) != 0 {
		t.Fatal("empty page lost total", users, err)
	}
	versions, err := store.Versions(ctx, Filter{Host: "192.0.2.7"})
	if err != nil || versions.Total != 3 || len(versions.Items) != 3 {
		t.Fatal(versions, err)
	}
	var oldVersion string
	for _, v := range versions.Items {
		if len(v.Version) != 32 {
			t.Fatal(v)
		}
		if v.RuleRevision == old.RuleRevision && v.ProviderID == 4 {
			oldVersion = v.Version
			if v.Users != 3 || v.Requests != 102 || v.FirstAt != 100 || v.LastAt != 220 {
				t.Fatal(v)
			}
			assertStats(Filter{Version: v.Version}, 3, 102, 100, 220)
		} else if v.RuleRevision == "" {
			if v.Requests != 1 || v.Users != 1 {
				t.Fatal(v)
			}
			assertStats(Filter{Version: v.Version}, 1, 1, 240, 240)
		} else {
			assertStats(Filter{Version: v.Version}, 1, 1, 230, 230)
		}
	}
	if oldVersion == "" {
		t.Fatal("missing original metadata version")
	}
	list, err := store.List(ctx, Filter{Version: oldVersion, CompleteOnly: true, Limit: 100})
	if err != nil || list.Total != 102 || len(list.Items) != 100 {
		t.Fatal("List failed extended snapshot filter", list.Total, err)
	}
	// No current user/policy rows were inserted; historical results work even
	// after current accounts/rules disappear. Invalid snapshots remain visible
	// for diagnostics but are downgraded from complete and excluded from stats.
	for _, id := range []string{"excluded-missing", "excluded-badhost", "excluded-unknownsource"} {
		r, err := store.Get(ctx, id)
		if err != nil || r.Coverage != "partial" {
			t.Fatal(r, err)
		}
		if strings.Contains(fmt.Sprint(r), "SECRET") {
			t.Fatal("unsafe endpoint stored")
		}
	}
}

func TestDeliveryStatisticsPostgresVersionIncludesEverySourceField(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_ENTRY_EVIDENCE_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`) })
	base := Node{ServerType: "vless", ServerID: 1, PolicyID: 7, GroupID: 11, ProviderID: 4, CollectionID: 3, RuleRevision: Revision("snapshot"), SourceHost: "entry.example.test", DeliveredHost: "192.0.2.7", Port: "54101", Source: "split", Resolved: true}
	variants := []Node{base}
	for _, change := range []func(*Node){
		func(n *Node) { n.PolicyID++ },
		func(n *Node) { n.GroupID++ },
		func(n *Node) { n.ProviderID++ },
		func(n *Node) { n.CollectionID++ },
		func(n *Node) { n.RuleRevision = Revision("different") },
		func(n *Node) { n.SourceHost = "another.example.test" },
		func(n *Node) { n.DeliveredHost = "192.0.2.8" },
		func(n *Node) { n.Port = "54102" },
		func(n *Node) { n.Source = "policy" },
		func(n *Node) { n.Resolved = false },
	} {
		n := base
		change(&n)
		variants = append(variants, n)
	}
	for i, n := range variants {
		r := Request{ID: fmt.Sprint("version-field-", i), UserID: int64(i + 1), RequestedAt: 100, CompletedAt: 100, Transport: "subscription", Status: 200, ResponseComplete: true, Coverage: "complete"}
		duplicate := n
		duplicate.ServerID = -7001
		duplicate.ServerType = "vmess"
		duplicate.SourceHost = strings.ToUpper(duplicate.SourceHost)
		if err = store.Record(ctx, r, []Node{n, duplicate}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Versions(ctx, Filter{Limit: 100})
	if err != nil || int(page.Total) != len(variants) || len(page.Items) != len(variants) {
		t.Fatal(page, err)
	}
	seen := map[string]bool{}
	for _, v := range page.Items {
		if seen[v.Version] || v.Users != 1 || v.Requests != 1 {
			t.Fatal("version collision or duplicate node counted", v)
		}
		seen[v.Version] = true
		got, err := store.Statistics(ctx, Filter{Version: v.Version})
		if err != nil || got.Users != 1 || got.Requests != 1 {
			t.Fatal("version drilldown mixed source metadata", v, got, err)
		}
	}
}
