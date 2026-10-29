package aiops

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestAIEntrySplitToolContractsAndReasons(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	for _, tt := range []struct {
		name, args string
		want       bool
	}{
		{"entry_split_options", `{"policy_id":497}`, true},
		{"entry_split_operation", `{"operation_id":123}`, true},
		{"propose_entry_manual_split", `{"target_id":497,"revision":"revision","reason":"manual"}`, true},
		{"propose_entry_manual_split", `{"target_id":497,"revision":"revision","enabled":true,"reason":"manual"}`, false},
		{"propose_entry_auto_split", `{"target_id":497,"revision":"revision","enabled":false,"reason":"toggle"}`, true},
		{"propose_entry_split", `{"kind":"split_group","target_id":497,"revision":"revision","use_backup_ip_pool":true,"reason":"split"}`, true},
		{"propose_entry_split", `{"kind":"split_group","target_id":497,"revision":"revision","reason":"split"}`, false},
	} {
		if got := s.validToolArgumentKeys(tt.name, tt.args); got != tt.want {
			t.Errorf("%s %s accepted=%v", tt.name, tt.args, got)
		}
	}
	known := fmt.Errorf("%w：可用且健康的备用 IP 不足 2 个，未二分、未领取 IP", admin.ErrAIEntrySplitInvalid)
	if result := toolErrorContent(safeAIError(known)); !strings.Contains(result, "不足 2 个") || !strings.Contains(result, "split_unavailable") {
		t.Fatal("reason lost", result)
	}
	if result := toolErrorContent(errors.New("database password=fixture-secret")); strings.Contains(result, "fixture-secret") {
		t.Fatal("unsafe error exposed", result)
	}
}
