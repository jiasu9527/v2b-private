package aiops

import (
	"encoding/json"
	"testing"
)

func TestSSHRecoveryCommandIdentity(t *testing.T) {
	key := func(name string, server int64, command, reason string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"server_id": server, "command": command, "reason": reason})
		return sshRecoveryCommandKey(ToolCall{Function: ToolFunction{Name: name, Arguments: string(raw)}})
	}
	first := key("propose_ssh_command", 1, "update fixture", "original")
	if first == "" || first != key("propose_ssh_command", 1, "  update fixture\n", "retry") {
		t.Fatal("same command can be replayed by changing reason or outer whitespace")
	}
	if first == key("propose_ssh_command", 2, "update fixture", "original") || first == key("propose_ssh_command", 1, "inspect fixture", "inspect") {
		t.Fatal("recovery must allow different servers and new diagnostic commands")
	}
	if key("ssh_status", 1, "", "inspect") != key("propose_ssh_command", 1, diagnosticCommand, "inspect") {
		t.Fatal("diagnostic tool aliases must share identity")
	}
	if key("panel_query", 1, "update fixture", "read") != "" || key("propose_ssh_command", 0, "update fixture", "invalid") != "" {
		t.Fatal("non SSH or invalid commands must not enter replay guard")
	}
}
