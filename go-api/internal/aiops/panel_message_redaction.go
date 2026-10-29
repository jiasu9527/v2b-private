package aiops

// Collect only credentials actually declared in an operation's arguments.
// Free-form text cannot establish that arbitrary unknown strings are secrets.
func panelToolSecrets(calls []ToolCall) []string {
	var secrets []string
	for _, call := range calls {
		if oneOf(call.Function.Name, "panel_query", "propose_panel_operation") {
			_, values := previewPanelParameters([]byte(call.Function.Arguments))
			secrets = append(secrets, values...)
		}
	}
	return secrets
}

func redactPanelMessages(messages []providerMessage) []providerMessage {
	var secrets []string
	for _, message := range messages {
		secrets = append(secrets, panelToolSecrets(message.ToolCalls)...)
	}
	out := append([]providerMessage(nil), messages...)
	for index := range out {
		if out[index].Role == "assistant" && len(secrets) > 0 {
			out[index].Content = redactSSHOutput(out[index].Content, secrets, true)
		}
		out[index].ToolCalls = redactPanelOperationToolCalls(out[index].ToolCalls)
	}
	return out
}
