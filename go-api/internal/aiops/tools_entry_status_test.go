package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"forest/go-api/internal/admin"
)

func entryStatusFixture(policyID, targetID int64, host string, port int64) admin.EntryMonitorInventorySource {
	success, latency, now := true, int64(53), int64(1794520800)
	refs := []admin.EntryMonitorReference{{Type: "policy", ID: policyID}}
	if targetID > 0 {
		refs = append(refs, admin.EntryMonitorReference{Type: "monitor_target", ID: targetID})
	}
	return admin.EntryMonitorInventorySource{
		PolicyID: policyID, SourceKey: fmt.Sprintf("policy:%d", policyID), Host: host, Port: port,
		PortBasis: "configured_monitor_port", PolicyEnabled: true, MonitorConfigured: true, MonitorEnabled: true,
		TargetCurrent: true, Status: "healthy", CoverageStatus: "ready", References: refs,
		Probes: []admin.SupportEntryProbe{{ProbeID: 3, ProbeName: "existing probe", Online: true, Status: "healthy", LastSuccess: &success, LastLatencyMS: &latency, LastReportedAt: &now}},
	}
}

func TestEntryPolicyStatusLinksRecordedPoliciesToRealTargets(t *testing.T) {
	v := admin.EntryMonitorInventoryOverview{GeneratedAt: 1794520801, Sources: []admin.EntryMonitorInventorySource{
		entryStatusFixture(488, 13615664, "192.0.2.199", 54101),
		entryStatusFixture(490, 13618896, "192.0.2.237", 54101),
		entryStatusFixture(493, 12401674, "192.0.2.35", 54101),
	}}
	for _, tc := range []struct{ policy, target int64 }{{488, 13615664}, {490, 13618896}, {493, 12401674}} {
		got := projectEntryPolicyStatus(v, tc.policy)
		if got.Status != "available" || got.SourcesTotal != 1 || len(got.Sources) != 1 {
			t.Fatalf("policy %d: %#v", tc.policy, got)
		}
		src := got.Sources[0]
		if src.TargetID == nil || *src.TargetID != tc.target || src.Port != 54101 || len(src.Probes) != 1 || src.Probes[0].ID != 3 || src.Probes[0].Status != "healthy" {
			t.Fatalf("real target and observations lost: %#v", src)
		}
		if !strings.Contains(got.ScopeNote, "不是历史实际下发") || !strings.Contains(got.ScopeNote, "不会刷新目标") {
			t.Fatal("scope boundary missing")
		}
	}
}

func TestEntryPolicyStatusKeepsSourceGapWithExactAddressReferences(t *testing.T) {
	unconfigured := entryStatusFixture(533, 0, "Entry.Example.COM.", 54101)
	unconfigured.MonitorConfigured, unconfigured.MonitorEnabled, unconfigured.TargetCurrent = false, false, false
	unconfigured.Status, unconfigured.CoverageStatus, unconfigured.PortBasis = "unmonitored", "unconfigured", "suggested_monitor_port"
	unconfigured.Probes = nil
	valid := entryStatusFixture(516, 13968918, "entry.example.com", 54101)
	offline := entryStatusFixture(511, 13968919, "entry.example.com", 54101)
	offline.Status, offline.Probes[0].Status, offline.Probes[0].Online = "stale", "stale", false
	wrongPort := entryStatusFixture(501, 13517644, "entry.example.com", 443)
	resolvedIP := entryStatusFixture(500, 13517640, "192.0.2.173", 54101)
	valid.Probes[0].LastResolvedIP = "192.0.2.173"
	v := admin.EntryMonitorInventoryOverview{Sources: []admin.EntryMonitorInventorySource{unconfigured, valid, wrongPort, offline, resolvedIP}}
	before, _ := json.Marshal(v)
	got := projectEntryPolicyStatus(v, 533)
	if len(got.Sources) != 1 {
		t.Fatalf("source missing: %#v", got)
	}
	src := got.Sources[0]
	if src.MonitorConfigured || src.MonitorEnabled || src.TargetID != nil || src.Status != "unmonitored" || src.CoverageStatus != "unconfigured" || len(src.Probes) != 0 {
		t.Fatalf("peer observation incorrectly repairs source coverage: %#v", src)
	}
	if src.AddressReferenceTotal != 2 || len(src.AddressReferences) != 2 {
		t.Fatalf("port or resolved IP leaked into exact-address references: %#v", src.AddressReferences)
	}
	for _, peer := range src.AddressReferences {
		if !peer.ReferenceOnly || peer.TargetID == nil {
			t.Fatalf("peer must remain reference-only with real target: %#v", peer)
		}
		if peer.PolicyID == 511 && (peer.Status != "stale" || peer.Probes[0].Online || peer.Probes[0].Status != "stale") {
			t.Fatal("offline observation promoted to healthy")
		}
	}
	after, _ := json.Marshal(v)
	if string(before) != string(after) {
		t.Fatal("projection mutated inventory")
	}
}

func TestEntrySimulationStatusRestrictsSplitHostAndExplainsAmbiguity(t *testing.T) {
	a := entryStatusFixture(533, 13968918, "192.0.2.173", 54101)
	a.SourceKey = "policy:533:split-group:10"
	b := entryStatusFixture(533, 13968919, "192.0.2.174", 54101)
	b.SourceKey = "policy:533:split-group:11"
	c := entryStatusFixture(533, 13968920, "192.0.2.173", 54101)
	c.SourceKey = "policy:533:split-group:12"
	v := admin.EntryMonitorInventoryOverview{Sources: []admin.EntryMonitorInventorySource{a, b, c}}
	matched := admin.ClientEntryUserPolicyRecord{ID: 533, Mode: admin.ClientEntryUserPolicyModeSplit, Action: "override", EntryHost: "192.0.2.173"}
	got := projectEntrySimulationStatus(v, matched)
	if got.SourcesTotal != 2 || len(got.Sources) != 2 || !strings.Contains(got.SelectionNote, "未返回叶子 ID") {
		t.Fatalf("ambiguous leaf assignment not declared: %#v", got)
	}
	for _, src := range got.Sources {
		if src.Host != matched.EntryHost {
			t.Fatal("unselected sibling leaf exposed as user's route")
		}
	}
	for _, host := range []string{"192.0.2.250", "", "bad host"} {
		matched.EntryHost = host
		got := projectEntrySimulationStatus(v, matched)
		if got.Status != "no_candidates" || len(got.Sources) != 0 || !strings.Contains(got.SelectionNote, "不能据此判定健康") {
			t.Fatalf("missing/invalid host widened: %#v", got)
		}
	}
}

func TestEntryStatusPreservesUnknownAndDisabledEvidence(t *testing.T) {
	for _, tc := range []struct{ coverage, status, issue string }{
		{"monitor_disabled", "disabled", "monitor_disabled"},
		{"target_address_mismatch", "stale", "target_address_mismatch"},
		{"ready", "stale", "observation_stale"},
		{"no_enabled_probes", "stale", "no_enabled_probes"},
	} {
		src := entryStatusFixture(493, 12401674, "192.0.2.35", 54101)
		src.CoverageStatus, src.Status, src.Issues = tc.coverage, tc.status, []string{tc.issue}
		src.Probes[0].Status = tc.status
		if tc.coverage == "monitor_disabled" {
			src.MonitorEnabled = false
		}
		if tc.coverage == "target_address_mismatch" {
			src.TargetCurrent = false
		}
		got := projectEntryPolicyStatus(admin.EntryMonitorInventoryOverview{Sources: []admin.EntryMonitorInventorySource{src}}, 493).Sources[0]
		if got.Status != tc.status || got.CoverageStatus != tc.coverage || !reflect.DeepEqual(got.Issues, src.Issues) || got.MonitorEnabled != src.MonitorEnabled || got.TargetCurrent != src.TargetCurrent {
			t.Fatalf("changed unknown/disabled evidence: %#v", got)
		}
	}
}

type entryStatusReadOnlyPanel struct {
	admin.Service
	inventory admin.EntryMonitorInventoryOverview
	err       error
	reads     int
}

func (p *entryStatusReadOnlyPanel) EntryMonitorInventory(context.Context) (admin.EntryMonitorInventoryOverview, error) {
	p.reads++
	return p.inventory, p.err
}

func (*entryStatusReadOnlyPanel) ListClientEntryMonitors(context.Context) (admin.ClientEntryMonitorOverview, error) {
	panic("status linkage must not refresh persisted monitor targets")
}

func TestEntryStatusReadsInventoryOnlyAndKeepsFailuresLocal(t *testing.T) {
	p := &entryStatusReadOnlyPanel{inventory: admin.EntryMonitorInventoryOverview{Sources: []admin.EntryMonitorInventorySource{entryStatusFixture(488, 13615664, "192.0.2.199", 54101)}}}
	s := &Service{panel: p}
	matched := admin.ClientEntryUserPolicyRecord{ID: 488, Action: "override", EntryHost: "192.0.2.199"}
	got := s.entryStatusForSimulation(context.Background(), matched)
	if p.reads != 1 || got.Status != "available" {
		t.Fatalf("did not use read-only inventory: %#v", got)
	}
	got = s.entryStatusForPolicy(context.Background(), matched.ID)
	if p.reads != 2 || got.Status != "available" || len(got.Sources) != 1 || got.Sources[0].TargetID == nil || *got.Sources[0].TargetID != 13615664 {
		t.Fatalf("policy enrichment lost real target: %#v", got)
	}
	p.err = errors.New("private database connection details")
	got = s.entryStatusForSimulation(context.Background(), matched)
	raw, _ := json.Marshal(got)
	if got.Status != "unavailable" || strings.Contains(string(raw), "private database") || got.PolicyID != matched.ID {
		t.Fatalf("failed enrichment leaked or erased primary scope: %s", raw)
	}
	got = (&Service{}).entryStatusForSimulation(context.Background(), matched)
	if got.Status != "unavailable" {
		t.Fatal("missing capability must not fail simulation")
	}
	if got = s.entryStatusForPolicy(context.Background(), matched.ID); got.Status != "unavailable" {
		t.Fatal("failed inventory read must remain local to policy enrichment")
	}
}

func TestEntryStatusProjectionIsBoundedWithVisibleTruncation(t *testing.T) {
	v := admin.EntryMonitorInventoryOverview{}
	for i := 0; i < 100; i++ {
		src := entryStatusFixture(488, int64(13615000+i), "192.0.2.199", 54101)
		src.SourceKey = fmt.Sprintf("policy:488:split-group:%d", i)
		for j := 0; j < 100; j++ {
			src.References = append(src.References, admin.EntryMonitorReference{ID: int64(j + 1), Type: strings.Repeat("界\x00", 100), Protocol: strings.Repeat("界\x00", 100)})
			probe := src.Probes[0]
			probe.ProbeID, probe.ProbeName = int64(j+5), strings.Repeat("界\x00", 100)
			src.Probes = append(src.Probes, probe)
		}
		v.Sources = append(v.Sources, src)
	}
	got := projectEntryPolicyStatus(v, 488)
	raw, err := json.Marshal(got)
	if err != nil || len(raw) > entryPolicyStatusBudget || !utf8.Valid(raw) || !got.SourcesTruncated || got.SourcesTotal != 100 || len(got.Sources) == 0 || got.ReadMore == "" {
		t.Fatalf("unbounded or silent truncation: bytes=%d result=%#v", len(raw), got)
	}
	for _, src := range got.Sources {
		if !src.ProbesTruncated || !src.ReferencesTruncated || !src.AddressRefsTruncated || src.ProbesTotal != 101 || src.ReferencesTotal != 102 || src.AddressReferenceTotal != 99 {
			t.Fatalf("nested counts/truncation lost: %#v", src)
		}
	}
}
