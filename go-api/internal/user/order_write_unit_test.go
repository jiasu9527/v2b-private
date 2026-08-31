package user

import (
	"context"
	"database/sql"
	"testing"

	"forest/go-api/internal/config"
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

func TestApplyOrderCompletionEventUsesConfiguredActionForOrderType(t *testing.T) {
	tests := []struct {
		name      string
		orderType int64
		cfg       config.Config
		wantReset bool
	}{
		{name: "new purchase", orderType: 1, cfg: config.Config{NewOrderEventID: 1}, wantReset: true},
		{name: "renewal", orderType: 2, cfg: config.Config{RenewOrderEventID: 1}, wantReset: true},
		{name: "plan change", orderType: 3, cfg: config.Config{ChangeOrderEventID: 1}, wantReset: true},
		{name: "disabled action", orderType: 2, cfg: config.Config{RenewOrderEventID: 0}, wantReset: false},
		{name: "different order event", orderType: 2, cfg: config.Config{NewOrderEventID: 1}, wantReset: false},
		{name: "traffic reset package is handled separately", orderType: 4, cfg: config.Config{NewOrderEventID: 1, RenewOrderEventID: 1, ChangeOrderEventID: 1}, wantReset: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewDBService(test.cfg, nil)
			userRow := userRecord{U: 12, D: 34}
			service.applyOrderCompletionEvent(&userRow, test.orderType)
			if test.wantReset {
				if userRow.U != 0 || userRow.D != 0 {
					t.Fatalf("expected traffic usage reset, got u=%d d=%d", userRow.U, userRow.D)
				}
				return
			}
			if userRow.U != 12 || userRow.D != 34 {
				t.Fatalf("expected traffic usage preserved, got u=%d d=%d", userRow.U, userRow.D)
			}
		})
	}
}
