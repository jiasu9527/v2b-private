package admin

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestClientEntryGroupAuditValidation(t *testing.T) {
	const now int64 = 1790000000
	defaults, err := normalizeClientEntryGroupAudit(ClientEntryGroupAuditRequest{}, now)
	if err != nil || defaults.From != now-7*86400 || defaults.To != now || defaults.Current != 1 || defaults.PageSize != 20 {
		t.Fatal(defaults, err)
	}
	for _, req := range []ClientEntryGroupAuditRequest{
		{GroupIDs: []int64{0}}, {GroupIDs: []int64{1, 1}}, {ReplacementGroupIDs: []int64{1, 1}},
		{GroupIDs: []int64{1}, ReplacementGroupIDs: []int64{1}}, {GroupIDs: make([]int64, 101)},
		{From: now - 60}, {To: now}, {From: now, To: now - 1}, {From: now - 366*86400, To: now},
		{From: now - 60, To: now + 10}, {Current: -1}, {PageSize: 51},
		{ExpectedRevision: "bad"}, {ExpectedRevision: strings.Repeat("a", 64)},
	} {
		if _, err := normalizeClientEntryGroupAudit(req, now); !errors.Is(err, ErrClientEntryGroupAuditInvalid) {
			t.Fatalf("accepted invalid request %+v: %v", req, err)
		}
	}
	if _, err = normalizeClientEntryGroupAudit(ClientEntryGroupAuditRequest{Current: 2}, now); !errors.Is(err, ErrClientEntryGroupAuditConflict) {
		t.Fatal("accepted unpinned next page", err)
	}
	req := ClientEntryGroupAuditRequest{GroupIDs: []int64{3, 1}, ReplacementGroupIDs: []int64{8, 7}, From: now - 60, To: now, Current: 2, ExpectedRevision: strings.Repeat("a", 64)}
	got, err := normalizeClientEntryGroupAudit(req, now)
	if err != nil || !reflect.DeepEqual(got.GroupIDs, []int64{1, 3}) || !reflect.DeepEqual(req.GroupIDs, []int64{3, 1}) {
		t.Fatal("normalization mutated caller input", got, req, err)
	}
}
