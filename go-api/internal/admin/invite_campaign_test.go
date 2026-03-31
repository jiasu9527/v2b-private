package admin

import (
	"database/sql"
	"testing"
)

func TestSerializeInviteCampaignTrimsPaddedInviteCode(t *testing.T) {
	payload := serializeInviteCampaign(inviteCampaignRow{
		ID:         11,
		UserID:     7,
		PlanID:     0,
		Period:     "month_price",
		InviteCode: sql.NullString{String: "ABCD1234                        ", Valid: true},
	}, nil)

	if payload["invite_code"] != "ABCD1234" {
		t.Fatalf("expected trimmed invite_code, got %#v", payload["invite_code"])
	}
}
