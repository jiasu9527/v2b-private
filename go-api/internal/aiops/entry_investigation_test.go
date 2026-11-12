package aiops

import (
	"os"
	"strings"
	"testing"
)

func TestEntryInvestigationUsesConversationAccess(t *testing.T) {
	// Investigation does not bypass authorization and also does not add a second
	// mandatory click gate on top of an operator's existing full-access session.
	for file, needle := range map[string]string{"chat.go": "result.Pending && full", "actions.go": "s.requireFullAccessTx(ctx, tx, actorID, sessionID, autoTurnID)"} {
		raw, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(raw), needle) {
			t.Fatal(file, err)
		}
		if strings.Contains(string(raw), "investigationManualOperation") || strings.Contains(string(raw), "ManualApprovalRequired") {
			t.Fatal("investigation-only manual gate returned", file)
		}
	}
	for _, needle := range []string{"聊天为主入口", "不要求用户打开页面", "完全访问模式", "逐次确认模式", "不能自行提升权限", "不是授权换IP", "investigation_list", "selected_user_ids", "use_backup_ip_pool=true", "split_preview", "split_execute", "investigation_next", "对照未核实用unknown"} {
		if !strings.Contains(entryRuleDeliveryCorePrompt, needle) {
			t.Fatal("conversation workflow guidance missing", needle)
		}
	}
	for _, forbidden := range []string{"即使完全访问也保留人工确认", "不能借全权限自动执行"} {
		if strings.Contains(entryRuleDeliveryCorePrompt, forbidden) {
			t.Fatal("stale click-through instruction", forbidden)
		}
	}
}
