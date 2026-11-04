package admin

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func orderTestRows() []ClientEntryOrderRow {
	return []ClientEntryOrderRow{
		{Kind: "policy", ID: 1, PolicyID: 1, Name: "protected", Sort: 10},
		{Kind: "policy", ID: 2, PolicyID: 2, Name: "内鬼假地址", Sort: 20},
		{Kind: "policy", ID: 3, PolicyID: 3, Sort: 30},
		{Kind: "policy", ID: 5, PolicyID: 5, Sort: 40, Enabled: 0},
		{Kind: "split_group", ID: 2, PolicyID: 4, Sort: 50},
		{Kind: "split_group", ID: 6, PolicyID: 4, Sort: 60},
	}
}
func orderKeys(rows []ClientEntryOrderRow) []ClientEntryUserPolicySortItem {
	out := []ClientEntryUserPolicySortItem{}
	for _, r := range rows {
		out = append(out, ClientEntryUserPolicySortItem{Kind: r.Kind, ID: r.ID})
	}
	return out
}
func TestClientEntryRelativeOrderPreservesOtherRowsAndKinds(t *testing.T) {
	rows := orderTestRows()
	original := append([]ClientEntryOrderRow(nil), rows...)
	req := ClientEntryOrderMoveRequest{Items: []ClientEntryUserPolicySortItem{{Kind: "split_group", ID: 2}, {Kind: "split_group", ID: 6}}, After: ClientEntryUserPolicySortItem{Kind: "policy", ID: 2}, ExpectedRevision: clientEntryOrderRevision(rows)}
	moved, changed, err := planClientEntryOrderMove(rows, req)
	want := []ClientEntryUserPolicySortItem{{Kind: "policy", ID: 1}, {Kind: "policy", ID: 2}, {Kind: "split_group", ID: 2}, {Kind: "split_group", ID: 6}, {Kind: "policy", ID: 3}, {Kind: "policy", ID: 5}}
	if err != nil || !changed || !reflect.DeepEqual(orderKeys(moved), want) || !reflect.DeepEqual(rows, original) {
		t.Fatal(moved, changed, err)
	}
	req.ExpectedRevision = clientEntryOrderRevision(moved)
	if _, changed, err = planClientEntryOrderMove(moved, req); err != nil || changed {
		t.Fatal("already at position must be no-op", changed, err)
	}
	req.Items[0], req.Items[1] = req.Items[1], req.Items[0]
	got, changed, err := planClientEntryOrderMove(moved, req)
	if err != nil || !changed || got[2].ID != 6 {
		t.Fatal("explicit selected order not respected", got, err)
	}
}
func TestClientEntryOrderRejectsInvalidAndStaleBeforeMutation(t *testing.T) {
	rows := orderTestRows()
	valid := ClientEntryOrderMoveRequest{Items: []ClientEntryUserPolicySortItem{{Kind: "split_group", ID: 2}}, After: ClientEntryUserPolicySortItem{Kind: "policy", ID: 2}, ExpectedRevision: clientEntryOrderRevision(rows)}
	for _, test := range []struct {
		name   string
		change func(*ClientEntryOrderMoveRequest)
		want   error
	}{
		{"empty", func(r *ClientEntryOrderMoveRequest) { r.Items = nil }, ErrClientEntryOrderInvalid},
		{"bad kind", func(r *ClientEntryOrderMoveRequest) { r.After.Kind = "split" }, ErrClientEntryOrderInvalid},
		{"invalid ID", func(r *ClientEntryOrderMoveRequest) { r.After.ID = 0 }, ErrClientEntryOrderInvalid},
		{"duplicate", func(r *ClientEntryOrderMoveRequest) { r.Items = append(r.Items, r.Items[0]) }, ErrClientEntryOrderInvalid},
		{"anchor selected", func(r *ClientEntryOrderMoveRequest) { r.After = r.Items[0] }, ErrClientEntryOrderInvalid},
		{"missing revision", func(r *ClientEntryOrderMoveRequest) { r.ExpectedRevision = "" }, ErrClientEntryOrderInvalid},
		{"revision changed", func(r *ClientEntryOrderMoveRequest) { r.ExpectedRevision = strings.Repeat("a", 64) }, ErrClientEntryOrderConflict},
		{"hidden parent", func(r *ClientEntryOrderMoveRequest) {
			r.Items = []ClientEntryUserPolicySortItem{{Kind: "policy", ID: 4}}
		}, ErrClientEntryOrderConflict},
		{"nonleaf", func(r *ClientEntryOrderMoveRequest) {
			r.Items = []ClientEntryUserPolicySortItem{{Kind: "split_group", ID: 99}}
		}, ErrClientEntryOrderConflict},
		{"unknown anchor", func(r *ClientEntryOrderMoveRequest) { r.After.ID = 999 }, ErrClientEntryOrderConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			req.Items = append([]ClientEntryUserPolicySortItem(nil), valid.Items...)
			test.change(&req)
			if _, _, err := planClientEntryOrderMove(rows, req); !errors.Is(err, test.want) {
				t.Fatal(err)
			}
		})
	}
}
