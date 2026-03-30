package user

import "testing"

func TestSubscribeAliveIPCountFromRuntimeState(t *testing.T) {
	count := subscribeAliveIPCount(`{
		"alive_ip": 4,
		"vmess1": {"aliveips": ["1.1.1.1_ios", "2.2.2.2_android"]}
	}`)

	if count != 4 {
		t.Fatalf("expected alive_ip 4, got %d", count)
	}
}

func TestSubscribeAliveIPCountFromRuntimeStateInvalidJSON(t *testing.T) {
	if count := subscribeAliveIPCount(`broken`); count != 0 {
		t.Fatalf("expected invalid payload to return 0, got %d", count)
	}
}
