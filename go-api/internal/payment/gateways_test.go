package payment

import "testing"

func TestAlipayProductNameDefaultsToForest(t *testing.T) {
	if got := alipayProductName(map[string]string{}); got != "Forest - 订阅" {
		t.Fatalf("expected default product name to use Forest branding, got %q", got)
	}
}

func TestAlipayProductNameAllowsOverride(t *testing.T) {
	if got := alipayProductName(map[string]string{"product_name": "Custom Name"}); got != "Custom Name" {
		t.Fatalf("expected explicit product name override, got %q", got)
	}
}
