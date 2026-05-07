package user

import "testing"

func TestPlanTransferEnableBytesNormalizesWithoutDoubleMultiplying(t *testing.T) {
	if got := planTransferEnableBytes(trafficGB); got != trafficGB {
		t.Fatalf("expected byte-valued plan transfer to stay %d, got %d", trafficGB, got)
	}
}

func TestPlanTransferEnableGBConvertsToBytes(t *testing.T) {
	if got := planTransferEnableBytes(2); got != 2*trafficGB {
		t.Fatalf("expected GB-valued plan transfer to convert to %d, got %d", 2*trafficGB, got)
	}
}
