package admin

import (
	"sort"
	"strings"
	"testing"
)

func TestMergeManagedServerRuntimeFieldsUsesAvailabilityRules(t *testing.T) {
	now := int64(1000)
	item := map[string]any{
		"id":        int64(11),
		"parent_id": int64(22),
		"type":      "vmess",
	}
	online := int64(8)
	lastCheck := now - 120
	lastPush := now - 30

	mergeManagedServerRuntimeFields(item, &online, &lastCheck, &lastPush, now)

	if item["online"] != int64(8) {
		t.Fatalf("expected online user count, got %#v", item["online"])
	}
	if item["last_check_at"] != lastCheck {
		t.Fatalf("expected last_check_at %d, got %#v", lastCheck, item["last_check_at"])
	}
	if item["last_push_at"] != lastPush {
		t.Fatalf("expected last_push_at %d, got %#v", lastPush, item["last_push_at"])
	}
	if item["available_status"] != int64(2) {
		t.Fatalf("expected available status 2, got %#v", item["available_status"])
	}
}

func TestMergeManagedServerRuntimeFieldsMarksWarningWhenPushExpired(t *testing.T) {
	now := int64(1000)
	item := map[string]any{"id": int64(11), "type": "trojan"}
	lastCheck := now - 30
	lastPush := now - 301

	mergeManagedServerRuntimeFields(item, nil, &lastCheck, &lastPush, now)

	if item["available_status"] != int64(1) {
		t.Fatalf("expected available status 1, got %#v", item["available_status"])
	}
}

func TestMergeManagedServerRuntimeFieldsMarksOfflineWhenCheckExpired(t *testing.T) {
	now := int64(1000)
	item := map[string]any{"id": int64(11), "type": "shadowsocks"}
	lastCheck := now - 301
	lastPush := now - 10

	mergeManagedServerRuntimeFields(item, nil, &lastCheck, &lastPush, now)

	if item["available_status"] != int64(0) {
		t.Fatalf("expected available status 0, got %#v", item["available_status"])
	}
}

func TestAdminAliveIPSummaryParsesRuntimeState(t *testing.T) {
	count, ips := adminAliveIPSummary(`{
		"alive_ip": 3,
		"vmess12": {"aliveips": ["1.1.1.1_ios", "2.2.2.2"]},
		"trojan9": {"aliveips": ["3.3.3.3_android"]}
	}`)

	if count != 3 {
		t.Fatalf("expected alive count 3, got %d", count)
	}
	parts := strings.Split(ips, ", ")
	sort.Strings(parts)
	expected := []string{"1.1.1.1_vmess12", "2.2.2.2_vmess12", "3.3.3.3_trojan9"}
	if len(parts) != len(expected) {
		t.Fatalf("expected %d ips, got %#v", len(expected), parts)
	}
	for index := range expected {
		if parts[index] != expected[index] {
			t.Fatalf("unexpected ips summary: %#v", parts)
		}
	}
}

func TestAdminAliveIPSummaryIgnoresInvalidPayload(t *testing.T) {
	count, ips := adminAliveIPSummary(`{broken`)

	if count != 0 || ips != "" {
		t.Fatalf("expected empty alive summary, got count=%d ips=%q", count, ips)
	}
}
