package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
)

type targetRecoveryPanel struct {
	admin.Service
	view      admin.ClientEntryMonitorOverview
	inventory admin.EntryMonitorInventoryOverview
	reads     int
}

func (p *targetRecoveryPanel) ReadClientEntryMonitors(context.Context) (admin.ClientEntryMonitorOverview, error) {
	p.reads++
	return p.view, nil
}

func (p *targetRecoveryPanel) EntryMonitorInventory(context.Context) (admin.EntryMonitorInventoryOverview, error) {
	return p.inventory, nil
}

func recoveryFixture() *targetRecoveryPanel {
	p := &targetRecoveryPanel{view: admin.ClientEntryMonitorOverview{Revision: 71}}
	for _, pair := range [][2]int64{{488, 13615664}, {490, 13618896}, {493, 12401674}} {
		key := fmt.Sprintf("policy:%d:override", pair[0])
		host := fmt.Sprintf("entry-%d.example.test", pair[0])
		p.view.Policies = append(p.view.Policies, admin.ClientEntryMonitorPolicy{ID: pair[0], Enabled: true, Targets: []admin.ClientEntryMonitorCandidateTarget{{SourceKey: key, Host: host, SuggestedPort: 54101}}})
		p.view.Items = append(p.view.Items, admin.ClientEntryMonitorRecord{PolicyID: pair[0], Enabled: true, Targets: []admin.ClientEntryMonitorTarget{{ID: pair[1], SourceKey: key, Host: host, Port: 54101}}})
		p.inventory.Sources = append(p.inventory.Sources, admin.EntryMonitorInventorySource{PolicyID: pair[0], SourceKey: key, Host: host, Port: 54101, PolicyEnabled: true, MonitorConfigured: true, MonitorEnabled: true, TargetCurrent: true, CoverageStatus: "ready", Status: "healthy", References: []admin.EntryMonitorReference{{Type: "monitor_target", ID: pair[1]}}})
	}
	p.view.Policies = append(p.view.Policies, admin.ClientEntryMonitorPolicy{ID: 533, Enabled: true})
	p.inventory.Sources = append(p.inventory.Sources, admin.EntryMonitorInventorySource{PolicyID: 533, SourceKey: "policy:533:override", Host: "unmonitored.example.test", Port: 54101, PolicyEnabled: true, CoverageStatus: "unconfigured", Status: "unknown"})
	return p
}

func TestProbeTargetRecoveryRecordedWrongIDs(t *testing.T) {
	for _, tc := range []struct {
		policy, wrong, target int64
	}{
		{488, 2426, 13615664}, {490, 490, 13618896}, {493, 1, 12401674},
	} {
		p := recoveryFixture()
		s := &Service{panel: p}
		out, err := s.callProbeTool(context.Background(), 1, 1, entryProgressCall("entry_probe_targets", fmt.Sprintf(`{"policy_id":%d,"target_id":%d}`, tc.policy, tc.wrong)))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Targets  pageDTO[probeTargetDTO] `json:"targets"`
			Recovery probeTargetRecoveryDTO  `json:"recovery"`
			Status   entryPolicyStatusDTO    `json:"policy_status"`
		}
		if err := json.Unmarshal([]byte(out.Content), &result); err != nil {
			t.Fatal(err)
		}
		if result.Targets.Total != 0 || len(result.Targets.Items) != 0 || result.Recovery.Code != "target_not_found" || !result.Recovery.FiltersUnchanged || result.Recovery.RequestedTargetID != tc.wrong {
			t.Fatalf("filter silently changed or missing diagnosis: %s", out.Content)
		}
		candidates := result.Recovery.Candidates
		if candidates == nil || candidates.Total != 1 || len(candidates.Items) != 1 || candidates.Items[0].TargetID != tc.target || candidates.Items[0].PolicyID != tc.policy || !candidates.Items[0].TargetCurrent || !candidates.Items[0].Enabled {
			t.Fatalf("real same-policy target missing: %s", out.Content)
		}
		if len(result.Status.Sources) != 1 || result.Status.Sources[0].TargetID == nil || *result.Status.Sources[0].TargetID != tc.target || result.Status.Sources[0].Port != 54101 {
			t.Fatalf("model still has to guess identity/port: %s", out.Content)
		}
		if _, guessed := result.Recovery.NextQuery["target_id"]; guessed || out.Pending || out.ActionID != 0 || p.reads != 1 {
			t.Fatal("recovery must remain a single read, not an action/retry")
		}
	}
}

func TestProbeTargetRecoveryDoesNotEnableObsoleteOrDisabledTargets(t *testing.T) {
	for _, mode := range []string{"policy_disabled", "monitor_disabled", "host_changed", "source_removed"} {
		p := recoveryFixture()
		switch mode {
		case "policy_disabled":
			p.view.Policies[0].Enabled = false
		case "monitor_disabled":
			p.view.Items[0].Enabled = false
		case "host_changed":
			p.view.Policies[0].Targets[0].Host = "new.example.test"
		case "source_removed":
			p.view.Policies[0].Targets = nil
		}
		got := probeTargetRecovery(p.view, probeToolArgs{PolicyID: 488, TargetID: 1}, 0).Candidates.Items[0]
		if got.Enabled || ((mode == "host_changed" || mode == "source_removed") && got.TargetCurrent) {
			t.Fatalf("%s: stale/disabled target promoted: %+v", mode, got)
		}
	}
}

func TestProbeTargetRecoveryKeepsScopesAndNoMonitorUnknown(t *testing.T) {
	p := recoveryFixture()
	for _, tc := range []struct {
		args  probeToolArgs
		code  string
		total int64
	}{
		{probeToolArgs{PolicyID: 488, TargetID: 12401674}, "target_policy_mismatch", 1},
		{probeToolArgs{PolicyID: 999, TargetID: 1}, "policy_not_found", 0},
		{probeToolArgs{TargetID: 1}, "target_not_found", 0},
		{probeToolArgs{PolicyID: 533}, "no_configured_targets", 0},
		{probeToolArgs{PolicyID: 533, TargetID: 1}, "target_not_found", 0},
	} {
		r := probeTargetRecovery(p.view, tc.args, 0)
		if r == nil || r.Code != tc.code {
			t.Fatalf("%+v: %+v", tc.args, r)
		}
		if r.Candidates != nil {
			if r.Candidates.Total != tc.total {
				t.Fatal("wrong candidate count")
			}
			for _, row := range r.Candidates.Items {
				if row.PolicyID != tc.args.PolicyID {
					t.Fatal("cross-policy recovery")
				}
			}
		}
		if tc.args.PolicyID == 0 && (r.Candidates != nil || r.NextQuery != nil) {
			t.Fatal("unknown policy expanded to all rules")
		}
	}
	if probeTargetRecovery(p.view, probeToolArgs{PolicyID: 488, Offset: 100}, 1) != nil {
		t.Fatal("past-end page treated as nonexistent target")
	}
	out, err := (&Service{panel: p}).callProbeTool(context.Background(), 1, 1, entryProgressCall("entry_probe_targets", `{"policy_id":533,"target_id":1}`))
	if err != nil || !strings.Contains(out.Content, `"coverage_status":"unconfigured"`) || !strings.Contains(out.Content, `"status":"unknown"`) {
		t.Fatal("missing-monitor evidence lost", out.Content, err)
	}
}

func TestProbeTargetRecoveryOutputBudget(t *testing.T) {
	p := recoveryFixture()
	long := strings.Repeat("\x00<&🚀", 300)
	p.view.Items[0].PolicyName = long
	p.view.Items[0].Targets = nil
	for i := 0; i < 50; i++ {
		p.view.Items[0].Targets = append(p.view.Items[0].Targets, admin.ClientEntryMonitorTarget{ID: int64(i + 1000), SourceKey: long, Name: long, Host: long, Port: 54101})
		p.view.Probes = append(p.view.Probes, admin.DNSProbeRecord{ID: int64(i + 1), Name: long, Enabled: true})
	}
	for _, args := range []string{`{"policy_id":488,"target_id":1}`, `{"policy_id":488,"limit":50}`} {
		out, err := (&Service{panel: p}).callProbeTool(context.Background(), 1, 1, entryProgressCall("entry_probe_targets", args))
		if err != nil || len(out.Content) > maxToolOutputBytes || !json.Valid([]byte(out.Content)) {
			t.Fatal("unbounded read", len(out.Content), err)
		}
		if !strings.Contains(out.Content, `"truncated":true`) {
			t.Fatal("missing truncation")
		}
	}
}
