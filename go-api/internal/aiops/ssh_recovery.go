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

// A failing host consumes its own bounded connection budget, not the entire
// user task. The worker owns this evidence; never infer it from error/output
// text or from model-provided JSON. Legacy actions remain fail-closed.
const sshSetupRecoveryInstruction = "SSH连接阶段失败，命令确定未发送；本批剩余调用已跳过。本轮已用完该服务器的连接预算，不得改命令、切换ssh_status或重复登记来重连；继续其他已授权且不依赖此操作成功的任务，最后明确列出该主机未完成。认证、指纹或协议错误需先由操作人修正服务器配置；不能声称任务全部成功。操作人发新消息后可按新轮次重新检查。"

func sshSetupActionRecovery(a Action) bool {
	f := a.SSHSetupFailure
	if a.Kind != "ssh_command" || a.Status != "failed" || a.TargetID <= 0 || a.ExitCode != nil || f == nil || f.ExecutionState != "not_executed" || f.Attempts < 1 || f.Attempts > sshConnectionAttempts {
		return false
	}
	switch f.Stage {
	case "dial", "handshake", "session":
	default:
		return false
	}
	switch f.Code {
	case "timeout", "network":
		return f.Retryable
	case "setup", "host_key", "authentication", "protocol":
		return !f.Retryable
	default:
		return false // user stop, total cancellation and authorization loss
	}
}

func sshRecoveryServerID(call ToolCall) int64 {
	if call.Function.Name != "propose_ssh_command" && call.Function.Name != "ssh_status" {
		return 0
	}
	var args struct {
		ServerID int64 `json:"server_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.ServerID <= 0 {
		return 0
	}
	return args.ServerID
}

// Keep one bounded context rather than one permanent system message per failed
// host: a large batch must not hit the model message limit merely by skipping
// unavailable servers. The in-memory guard remains authoritative after compaction.
func sshSetupRecoveryContext(messages []providerMessage, servers map[int64]int64) []providerMessage {
	out := make([]providerMessage, 0, len(messages)+1)
	for _, message := range messages {
		if message.contextKind != "ssh_setup_recovery" {
			out = append(out, message)
		}
	}
	raw, _ := json.Marshal(servers)
	return append(out, providerMessage{Role: "system", contextKind: "ssh_setup_recovery", Content: "本轮已跳过的SSH服务器ID及失败动作ID：" + string(raw) + "。这些命令未发送，只跳过相应服务器，不是整轮停止。" + sshSetupRecoveryInstruction})
}
