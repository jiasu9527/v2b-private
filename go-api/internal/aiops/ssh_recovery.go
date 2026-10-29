package aiops

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Only suppress a previously failed command on the same registered server in
// this turn. This is an exact replay guard, not a shell semantic classifier.
func sshRecoveryCommandKey(call ToolCall) string {
	if call.Function.Name != "propose_ssh_command" && call.Function.Name != "ssh_status" {
		return ""
	}
	var args struct {
		ServerID int64  `json:"server_id"`
		Command  string `json:"command"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.ServerID <= 0 {
		return ""
	}
	if call.Function.Name == "ssh_status" {
		args.Command = diagnosticCommand
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return ""
	}
	return hashOperation(fmt.Sprintf("%d\x00%s", args.ServerID, command))
}
