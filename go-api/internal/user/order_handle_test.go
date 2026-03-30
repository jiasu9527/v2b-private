package user

import "testing"

func TestShouldAutoCancelOrder(t *testing.T) {
	now := int64(1_711_600_000)

	if !shouldAutoCancelOrder(0, now-(2*3600)-1, now) {
		t.Fatal("expected stale pending order to be auto cancelled")
	}
	if shouldAutoCancelOrder(0, now-(2*3600)+10, now) {
		t.Fatal("expected fresh pending order to remain open")
	}
	if shouldAutoCancelOrder(3, now-(24*3600), now) {
		t.Fatal("expected non-pending order to be ignored")
	}
}
