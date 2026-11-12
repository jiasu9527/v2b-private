package admin

import "testing"

func TestEntryInvestigationDecisionBoundaries(t *testing.T) {
	now := int64(1000000)
	base := func() (EntryInvestigation, EntryEvidenceCase, EntryInvestigationReport) {
		task := EntryInvestigation{Round: 1, Settings: EntryInvestigationStartRequest{MinObserveSeconds: 3600, MaxRounds: 5, StopUsers: 8}, Failures: []EntryInvestigationFailure{{Branch: "A", BlockedAt: now - 120, ControlStatus: "healthy"}}}
		c := EntryEvidenceCase{Query: EntryEvidenceQueryRequest{Port: 443}}
		r := investigationBaseReport(now)
		r.ExecutedAt = now - 10000
		r.Branches = []EntryInvestigationBranch{{Branch: "A", GroupID: 1, Host: "192.0.2.1", PreFailureUsers: 20, Health: "down"}, {Branch: "B", GroupID: 2, Host: "192.0.2.2", AcquiredUsers: 20, Health: "healthy"}}
		return task, c, r
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*EntryInvestigation, *EntryInvestigationReport)
		delivered  bool
		first      int64
		down       bool
	}{
		{"ready", "ready_next", func(*EntryInvestigation, *EntryInvestigationReport) {}, true, now - 9000, true},
		{"shared", "needs_review", func(_ *EntryInvestigation, r *EntryInvestigationReport) { r.Blockers = []string{"shared"} }, true, now - 9000, true},
		{"both failed", "both_failed", func(t *EntryInvestigation, _ *EntryInvestigationReport) {
			t.Failures = append(t.Failures, EntryInvestigationFailure{Branch: "B"})
		}, true, now - 9000, true},
		{"no control", "needs_review", func(t *EntryInvestigation, _ *EntryInvestigationReport) { t.Failures[0].ControlStatus = "unknown" }, true, now - 9000, true},
		{"no evidence before failure", "needs_review", func(_ *EntryInvestigation, r *EntryInvestigationReport) { r.Branches[0].PreFailureUsers = 0 }, true, now - 9000, true},
		{"recovered", "needs_review", func(_ *EntryInvestigation, r *EntryInvestigationReport) { r.Branches[0].Health = "healthy" }, true, now - 9000, true},
		{"peer unmonitored", "needs_review", func(_ *EntryInvestigation, r *EntryInvestigationReport) { r.Branches[1].Health = "unmonitored" }, true, now - 9000, true},
		{"no update", "waiting_delivery", func(*EntryInvestigation, *EntryInvestigationReport) {}, false, 0, true},
		{"late update", "observing", func(*EntryInvestigation, *EntryInvestigationReport) {}, true, now - 60, true},
		{"small set", "review_candidates", func(_ *EntryInvestigation, r *EntryInvestigationReport) { r.Branches[0].PreFailureUsers = 8 }, true, now - 9000, true},
		{"round budget", "round_limit", func(t *EntryInvestigation, _ *EntryInvestigationReport) { t.Round = 5 }, true, now - 9000, true},
		{"tcp not confirmation", "awaiting_block_confirmation", func(t *EntryInvestigation, _ *EntryInvestigationReport) { t.Failures = nil }, true, now - 9000, true},
		{"stable not innocence", "observing", func(t *EntryInvestigation, _ *EntryInvestigationReport) { t.Failures = nil }, true, now - 9000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, c, r := base()
			tc.mutate(&task, &r)
			out := decideEntryInvestigation(task, c, r, tc.delivered, tc.first, tc.down, now)
			if out.State != tc.want {
				t.Fatal(out)
			}
			if tc.want == "ready_next" {
				if out.NextQuery == nil || out.NextQuery.From != r.ExecutedAt || out.NextQuery.To != task.Failures[0].BlockedAt || out.NextQuery.TargetID != 1 {
					t.Fatal("next scope", out)
				}
			} else if out.NextQuery != nil {
				t.Fatal("unsafe next suggestion", out)
			}
		})
	}
}
func TestEntryInvestigationStartValidation(t *testing.T) {
	r := EntryInvestigationStartRequest{CaseID: 1, Revision: 1, BlockedAt: 100, Note: "confirmed by operator"}
	out, err := normalizeInvestigationStart(r, 101)
	if err != nil || out.MinObserveSeconds != 86400 || out.MaxRounds != 10 || out.StopUsers != 8 {
		t.Fatal(out, err)
	}
	for _, mutate := range []func(*EntryInvestigationStartRequest){func(r *EntryInvestigationStartRequest) { r.Note = "" }, func(r *EntryInvestigationStartRequest) { r.BlockedAt = 102 }, func(r *EntryInvestigationStartRequest) { r.MinObserveSeconds = 299 }, func(r *EntryInvestigationStartRequest) { r.MaxRounds = 31 }, func(r *EntryInvestigationStartRequest) { r.StopUsers = 1 }} {
		x := r
		mutate(&x)
		if _, err := normalizeInvestigationStart(x, 101); err == nil {
			t.Fatal("accepted invalid", x)
		}
	}
}
