package admin

import (
	"encoding/json"
	"errors"
	"testing"

	"forest/go-api/internal/cliententry"
)

func retirementFixture() (ClientEntryRetirementPreviewRequest, retirementSnapshot) {
	member := []ClientEntryGroupMemberRecord{{ServerType: "v2node", ServerID: 14}}
	ua := []cliententry.Condition{{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{json.RawMessage(`"req"`)}}}
	root := int64(10)
	aSort, bSort, newSort := int64(100), int64(110), int64(20)
	snapshot := retirementSnapshot{Policies: []ClientEntryUserPolicyRecord{
		{ID: 1, Name: "old", Mode: "split", Action: "override", Enabled: 1, Sort: 100, Conditions: ua, Members: member, SplitGroups: []ClientEntryUserPolicySplitGroupRecord{
			{ID: 10, PolicyID: 1, Name: "parent"}, {ID: 11, PolicyID: 1, Name: "old A", ParentID: &root, IsLeaf: true, EntryHost: "192.0.2.11", GlobalSort: &aSort, UserCount: 2}, {ID: 12, PolicyID: 1, Name: "keep sibling", ParentID: &root, IsLeaf: true, EntryHost: "192.0.2.12", GlobalSort: &bSort, UserCount: 1}}},
		{ID: 2, Name: "replacement", Mode: "split", Action: "override", Enabled: 1, Sort: 20, Members: member, SplitGroups: []ClientEntryUserPolicySplitGroupRecord{{ID: 21, PolicyID: 2, IsLeaf: true, EntryHost: "192.0.2.21", GlobalSort: &newSort, UserCount: 1}}},
		{ID: 3, Name: "fallback", Mode: "standard", Action: "override", Enabled: 1, Sort: 1000, EntryHost: "192.0.2.3", Members: member},
	}, Assignments: []retirementAssignment{{ID: 1, PolicyID: 1, GroupID: 11, UserID: 101}, {ID: 2, PolicyID: 1, GroupID: 11, UserID: 102}, {ID: 3, PolicyID: 1, GroupID: 12, UserID: 103}, {ID: 4, PolicyID: 2, GroupID: 21, UserID: 101}}, Users: []retirementUser{{ID: 101, Email: "a@example.test", CreatedAt: 1}, {ID: 102, Email: "b@example.test", CreatedAt: 1}}, Pool: []retirementPool{{ID: 1, IP: "192.0.2.11"}}}
	return ClientEntryRetirementPreviewRequest{GroupIDs: []int64{11}, ReplacementGroupIDs: []int64{21}, AllowFallback: true, FallbackPolicyID: 3, Reason: "retire old assignment"}, snapshot
}

func TestRetirementCoverageAndTree(t *testing.T) {
	req, s := retirementFixture()
	p, err := buildRetirementPlan(req, s, 100000)
	if err != nil || !p.Preview.CanExecute || p.Preview.ExistingUsers != 2 || p.Preview.ReplacementCoveredUsers != 1 || p.Preview.FallbackUsers != 1 || len(p.GroupIDs) != 1 || len(p.PolicyIDs) != 0 || !p.Preview.IPs[0].Releasable {
		t.Fatalf("partial leaf plan=%+v err=%v", p, err)
	}
	req.AllowFallback = false
	req.FallbackPolicyID = 0
	p, err = buildRetirementPlan(req, s, 100000)
	if err != nil || p.Preview.CanExecute || p.Preview.UncoveredUsers != 1 {
		t.Fatal("uncovered silently allowed", p, err)
	}
	req.AllowFallback = true
	req.FallbackPolicyID = 3
	req.GroupIDs = []int64{11, 12}
	p, err = buildRetirementPlan(req, s, 100000)
	if err != nil || len(p.GroupIDs) != 3 || len(p.PolicyIDs) != 1 || len(p.Preview.RemovedAncestorIDs) != 1 {
		t.Fatal("empty ancestors not pruned", p, err)
	}
	s.Assignments = append(s.Assignments, retirementAssignment{PolicyID: 1, GroupID: 10, UserID: 9999})
	_, err = buildRetirementPlan(req, s, 100000)
	if !errors.Is(err, ErrClientEntryRetirementBlocked) {
		t.Fatal("parent assignment silently lost", err)
	}
}
func TestRetirementRejectFalseCoverage(t *testing.T) {
	for _, kind := range []string{"later", "missing-node", "different-ua", "disabled", "extra-node", "hide-before-fallback", "bad-fallback", "collection", "assignment-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			req, s := retirementFixture()
			req.AllowFallback = false
			req.FallbackPolicyID = 0
			s.Assignments = s.Assignments[:1]
			s.Assignments = append(s.Assignments, retirementAssignment{PolicyID: 2, GroupID: 21, UserID: 101})
			switch kind {
			case "later":
				v := int64(200)
				s.Policies[1].SplitGroups[0].GlobalSort = &v
			case "missing-node":
				s.Policies[1].Members = nil
			case "different-ua":
				s.Policies[1].Conditions = []cliententry.Condition{{Field: "ua", Operator: "empty"}}
			case "disabled":
				s.Policies[1].Enabled = 0
			case "extra-node":
				s.Policies[0].ExtraNodes = []string{"not-a-real-credential"}
			case "hide-before-fallback":
				req.AllowFallback = true
				req.FallbackPolicyID = 3
				req.ReplacementGroupIDs = nil
				s.Policies = append(s.Policies, ClientEntryUserPolicyRecord{ID: 4, Sort: 200, Mode: "standard", Action: "hide", Enabled: 1, Members: s.Policies[0].Members})
			case "bad-fallback":
				req.AllowFallback = true
				req.FallbackPolicyID = 3
				req.ReplacementGroupIDs = nil
				s.Policies[2].Conditions = []cliententry.Condition{{Field: "user_id", Operator: "lt", Value: json.RawMessage(`10`)}}
			case "collection":
				id := int64(21)
				s.CollectionMembers = []retirementCollectionMember{{GroupID: &id}}
			case "assignment-mismatch":
				s.Assignments[1].PolicyID = 999
			}
			p, err := buildRetirementPlan(req, s, 100000)
			if err == nil && p.Preview.CanExecute {
				t.Fatalf("unsafe coverage accepted %s %+v", kind, p)
			}
		})
	}
}
func TestRetirementSharedIPIsNotReleased(t *testing.T) {
	req, s := retirementFixture()
	s.Nodes = []retirementHost{{ID: 1, Host: "192.0.2.11"}}
	p, err := buildRetirementPlan(req, s, 100000)
	if err != nil || p.Preview.CanExecute || p.Preview.IPs[0].Releasable || p.Preview.IPs[0].RemainingReferences != 1 {
		t.Fatal(p, err)
	}
}
func TestRetirementRequestValidation(t *testing.T) {
	req, _ := retirementFixture()
	if _, err := normalizeRetirementRequest(req); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []ClientEntryRetirementPreviewRequest{{Reason: "x"}, {GroupIDs: []int64{1, 1}, Reason: "x"}, {GroupIDs: []int64{1}, ReplacementGroupIDs: []int64{1}, Reason: "x"}, {GroupIDs: []int64{1}, AllowFallback: true, Reason: "x"}, {GroupIDs: []int64{1}, FallbackPolicyID: 3, Reason: "x"}} {
		if _, err := normalizeRetirementRequest(invalid); !errors.Is(err, ErrClientEntryRetirementInvalid) {
			t.Fatal("invalid request accepted", invalid, err)
		}
	}
}
