package main

import (
	"math"
	"testing"
)

func TestBuildDemoSeedSpecCoversCoreAdminData(t *testing.T) {
	spec := buildDemoSeedSpec(1711843200)

	if len(spec.Groups) < 2 {
		t.Fatalf("expected at least 2 groups, got %d", len(spec.Groups))
	}
	if len(spec.Routes) < 1 {
		t.Fatalf("expected at least 1 route, got %d", len(spec.Routes))
	}
	if len(spec.Plans) < 2 {
		t.Fatalf("expected at least 2 plans, got %d", len(spec.Plans))
	}
	if len(spec.Users) < 4 {
		t.Fatalf("expected at least 4 users, got %d", len(spec.Users))
	}
	if len(spec.Payments) < 1 {
		t.Fatalf("expected at least 1 payment, got %d", len(spec.Payments))
	}
	if len(spec.Coupons) < 1 {
		t.Fatalf("expected at least 1 coupon, got %d", len(spec.Coupons))
	}
	if len(spec.Giftcards) < 1 {
		t.Fatalf("expected at least 1 giftcard, got %d", len(spec.Giftcards))
	}
	if len(spec.Notices) < 2 {
		t.Fatalf("expected at least 2 notices, got %d", len(spec.Notices))
	}
	if len(spec.Knowledge) < 2 {
		t.Fatalf("expected at least 2 knowledge articles, got %d", len(spec.Knowledge))
	}
	if len(spec.Tickets) < 1 {
		t.Fatalf("expected at least 1 ticket, got %d", len(spec.Tickets))
	}
	if len(spec.Orders) < 3 {
		t.Fatalf("expected at least 3 orders, got %d", len(spec.Orders))
	}
	if len(spec.InviteCampaigns) < 1 {
		t.Fatalf("expected at least 1 invite campaign, got %d", len(spec.InviteCampaigns))
	}
	if len(spec.StatRecords) < 2 {
		t.Fatalf("expected at least 2 stat records, got %d", len(spec.StatRecords))
	}
	if len(spec.ManagedServers) != 8 {
		t.Fatalf("expected 8 managed servers, got %d", len(spec.ManagedServers))
	}

	serverTypes := map[string]bool{}
	for _, item := range spec.ManagedServers {
		serverTypes[item.ServerType] = true
	}
	for _, want := range []string{"vmess", "trojan", "shadowsocks", "tuic", "hysteria", "vless", "anytls", "v2node"} {
		if !serverTypes[want] {
			t.Fatalf("missing managed server type %s", want)
		}
	}

	orderStatuses := map[int64]bool{}
	for _, item := range spec.Orders {
		orderStatuses[item.Status] = true
	}
	for _, want := range []int64{0, 2, 3} {
		if !orderStatuses[want] {
			t.Fatalf("missing order status %d", want)
		}
	}

	for _, item := range spec.Plans {
		if item.TransferEnable > math.MaxInt32 {
			t.Fatalf("plan %s transfer_enable exceeds int32: %d", item.Name, item.TransferEnable)
		}
	}
}

func TestBuildDemoSeedSpecIncludesNotifyVerificationGateways(t *testing.T) {
	spec := buildDemoSeedSpec(1711843200)

	paymentKeys := map[string]bool{}
	for _, item := range spec.Payments {
		paymentKeys[item.Key] = true
	}
	for _, want := range []string{"epay", "coinpayments", "stripecheckout"} {
		if !paymentKeys[want] {
			t.Fatalf("missing demo payment key %s", want)
		}
	}

	pendingOrders := map[string]bool{}
	for _, item := range spec.Orders {
		if item.Status == 0 {
			pendingOrders[item.PaymentKey] = true
		}
	}
	for _, want := range []string{"epay", "coinpayments", "stripecheckout"} {
		if !pendingOrders[want] {
			t.Fatalf("missing pending demo order for payment %s", want)
		}
	}
}

func TestBuildDemoSeedSpecOrderTradeNoFitsSchema(t *testing.T) {
	spec := buildDemoSeedSpec(1711843200)

	for _, item := range spec.Orders {
		if len(item.TradeNo) > 36 {
			t.Fatalf("order %s trade_no exceeds varchar(36): %d", item.Key, len(item.TradeNo))
		}
	}
}
