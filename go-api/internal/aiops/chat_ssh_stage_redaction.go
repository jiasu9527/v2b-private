package aiops

import (
	"encoding/json"
)

const sshStageHistoryNotice = "[SSH 资料已加密暂存；工具参数中的密码和私钥已从聊天记录移除。]"

// These values are used only to scrub the assistant's accompanying prose.
// The stage tool arguments themselves are removed in full before persistence,
// even when the model emitted malformed or unexpected JSON.
func sshStageCallSecrets(calls []ToolCall) []string {
	var secrets []string
	for _, call := range calls {
		if call.Function.Name != "ssh_server_stage" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(call.Function.Arguments), &value) != nil {
			continue
		}
		var visit func(any)
		visit = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for key, child := range x {
					if oneOf(key, "password", "private_key", "passphrase", "key_passphrase") {
						if secret, ok := child.(string); ok && secret != "" {
							secrets = append(secrets, secret)
						}
					} else {
						visit(child)
					}
				}
			case []any:
				for _, child := range x {
					visit(child)
				}
			}
		}
		visit(value)
	}
	return secrets
}

func redactSSHStageMessages(messages []providerMessage) []providerMessage {
	var secrets []string
	for _, message := range messages {
		secrets = append(secrets, sshStageCallSecrets(message.ToolCalls)...)
	}
	out := append([]providerMessage(nil), messages...)
	for i := range out {
		message := &out[i]
		message.ToolCalls = append([]ToolCall(nil), message.ToolCalls...)
		staged := false
		for j := range message.ToolCalls {
			if message.ToolCalls[j].Function.Name == "ssh_server_stage" {
				message.ToolCalls[j].Function.Arguments = `{"credentials_redacted":true}`
				staged = true
			}
		}
		if staged {
			// Even malformed tool JSON could have accompanied an unrecognized
			// password in the assistant's prose. Never persist that prose.
			message.Content = sshStageHistoryNotice
		} else {
			message.Content = redactKnowledgeText(redactSSHOutput(message.Content, secrets, true))
		}
	}
	return out
}
