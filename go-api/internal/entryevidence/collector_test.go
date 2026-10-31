package entryevidence

import (
	"context"
	"strings"
	"testing"
)

func TestEntryEvidenceCollectorUsesEmittedSubsetAndPreservesSnapshot(t *testing.T) {
	c := NewCollector("subscription", "Forest")
	c.SetUser(71, "user@example.test")
	c.Observe(Node{ServerType: "vless", ServerID: 1, PolicyID: 10, GroupID: 11, SourceHost: "entry.example.test", DeliveredHost: "192.0.2.1", Port: "443", Resolved: true, Source: "split", RuleRevision: Revision("rule-before")})
	c.Observe(Node{ServerType: "unsupported", ServerID: 2, DeliveredHost: "192.0.2.2", Port: "443", Source: "default"})
	c.Emit("vless", 1, "192.0.2.1", "54101")
	c.Coverage("complete", "server_write_completed")
	r, nodes := c.Finish(200, 50, "")
	if !r.ResponseComplete || r.NodeCount != 1 || len(nodes) != 1 || nodes[0].Port != "54101" || nodes[0].PolicyID != 10 || !nodes[0].Resolved {
		t.Fatal(r, nodes)
	}
	if FromContext(WithCollector(context.Background(), c)) != c {
		t.Fatal("collector context lost")
	}
	// Returned snapshots cannot be changed by callers.
	nodes[0].PolicyID = 99
	_, again := c.Finish(200, 50, "")
	if again[0].PolicyID != 10 {
		t.Fatal("caller changed collector snapshot")
	}
}

func TestEntryEvidenceCollectorDistinguishesIncompleteAnd304(t *testing.T) {
	for _, tc := range []struct {
		status  int
		failure string
		want    bool
	}{{200, "", true}, {200, "short_write", false}, {500, "", false}, {304, "", false}} {
		c := NewCollector("server_json", "Forest")
		c.SetUser(1, "")
		c.Observe(Node{ServerType: "vless", ServerID: 1, Source: "default"})
		c.Emit("vless", 1, "192.0.2.1", "443")
		c.Coverage("complete", "")
		r, nodes := c.Finish(tc.status, 50, tc.failure)
		if r.ResponseComplete != tc.want {
			t.Fatal(tc, r)
		}
		if tc.status == 304 && (r.Coverage != "not_emitted" || len(nodes) != 0) {
			t.Fatal("304 records a new delivery", r, nodes)
		}
	}
	c := NewCollector("subscription", "Forest")
	c.Emit("vless", 9, "192.0.2.1", "443")
	c.Coverage("complete", "")
	r, _ := c.Finish(200, 50, "")
	if r.Coverage != "partial" || !strings.Contains(r.CoverageReason, "missing") {
		t.Fatal("missing provenance upgraded to complete", r)
	}
	var absent *Collector
	absent.SetUser(1, "")
	absent.Observe(Node{})
	absent.Emit("", 0, "", "")
	absent.Coverage("complete", "")
	absent.Finish(200, 0, "")
}
