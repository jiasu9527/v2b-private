package admin

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"forest/go-api/internal/cliententry"
)

func TestEntryEvidenceQueryAndSelectionGuards(t *testing.T) {
	now := time.Now().Unix()
	valid := EntryEvidenceQueryRequest{Kind: "policy", TargetID: 1, Host: "EXAMPLE.TEST", Port: 54101, From: now - 60, To: now}
	out, err := normalizeEvidenceQuery(valid)
	if err != nil || out.Host != "example.test" || out.Current != 1 || out.PageSize != 20 {
		t.Fatal(out, err)
	}
	for _, mutate := range []func(*EntryEvidenceQueryRequest){func(q *EntryEvidenceQueryRequest) { q.Port = 0 }, func(q *EntryEvidenceQueryRequest) { q.Kind = "all" }, func(q *EntryEvidenceQueryRequest) { q.From = q.To + 1 }, func(q *EntryEvidenceQueryRequest) { q.From = q.To }, func(q *EntryEvidenceQueryRequest) { q.To = now + 3600 }, func(q *EntryEvidenceQueryRequest) { q.Host = "https://bad" }} {
		q := valid
		mutate(&q)
		if _, err := normalizeEvidenceQuery(q); err == nil {
			t.Fatal("invalid query accepted", q)
		}
	}
	items := []EntryEvidenceCandidate{{UserID: 1}, {UserID: 2}, {UserID: 3}, {UserID: 4}, {UserID: 5}}
	picked, count, err := evidenceSelect(items, []int64{2, 4, 5})
	if err != nil || count != 3 {
		t.Fatal(picked, count, err)
	}
	branches := []string{}
	for _, c := range picked {
		branches = append(branches, c.Branch)
	}
	if !reflect.DeepEqual(branches, []string{"R", "A", "R", "A", "B"}) {
		t.Fatal(branches)
	}
	if items[1].Selected || items[1].Branch != "" {
		t.Fatal("selection mutated input")
	}
	if one, count, err := evidenceSelect(items, []int64{1}); err != nil || count != 1 || !one[0].Selected {
		t.Fatal("single-user archival selection rejected", one, count, err)
	}
	for _, bad := range [][]int64{{}, {1, 1}, {1, 99}} {
		if _, _, err := evidenceSelect(items, bad); err == nil {
			t.Fatal("invalid selection accepted", bad)
		}
	}
}
func TestEntryEvidenceCoverageAndIdempotencyGuards(t *testing.T) {
	rule := AIEntryRuleSnapshot{Members: []AIEntryRuleMember{{ServerType: "vmess", ServerID: 1}}}
	candidate := EntryEvidenceCandidate{UserID: 5, UAs: []string{"Forest"}, Nodes: rule.Members}
	if err := evidenceCandidateCoverage(rule, []int64{5}, []EntryEvidenceCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	if err := evidenceCandidateCoverage(rule, []int64{6}, []EntryEvidenceCandidate{candidate}); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("nonmember accepted", err)
	}
	candidate.Nodes = nil
	if err := evidenceCandidateCoverage(rule, []int64{5}, []EntryEvidenceCandidate{candidate}); err == nil {
		t.Fatal("missing coverage accepted")
	}
	a := EntryEvidenceSplitRequest{CaseID: 1, Revision: 1, EntryHostA: "a.test", EntryHostB: "b.test"}
	b := a
	b.Revision = 2
	if !evidenceSplitSameRequest(a, b) {
		t.Fatal("repeated request must survive response revision")
	}
	b.EntryHostB = "other.test"
	if evidenceSplitSameRequest(a, b) {
		t.Fatal("different targets accepted")
	}
	rule.Kind = "policy"
	rule.Enabled = 1
	rule.Action = cliententry.ActionOverride
	rule.Sort = 10
	rule.EntryHost = "old.test"
	rule.Conditions = []cliententry.Condition{{Field: "plan_id", Operator: "eq"}}
	if err := evidenceRuleSafety(rule, "[]"); err == nil {
		t.Fatal("dynamic unsupported rule accepted")
	}
}
