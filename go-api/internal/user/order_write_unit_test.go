package user

import (
	"context"
	"database/sql"
	"testing"
)

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

func TestDepositOrderDoesNotGenerateInviteCommission(t *testing.T) {
	service := &DBService{}
	userRow := userRecord{ID: 5, InviteUserID: sql.NullInt64{Int64: 8, Valid: true}}
	order := orderDraft{UserID: 5, Type: 9, Period: "deposit", TotalAmount: 10000}

	if err := service.setInviteTx(context.Background(), nil, userRow, &order); err != nil {
		t.Fatalf("set deposit invite metadata: %v", err)
	}
	if !order.InviteUserID.Valid || order.InviteUserID.Int64 != 8 {
		t.Fatalf("expected inviter attribution to remain available for audit: %#v", order.InviteUserID)
	}
	if order.CommissionBalance != 0 {
		t.Fatalf("deposit order must not generate commission, got %d", order.CommissionBalance)
	}
}
