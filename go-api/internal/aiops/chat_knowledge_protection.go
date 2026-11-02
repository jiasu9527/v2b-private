package aiops

import (
	"encoding/json"
	"regexp"
	"strings"
)

const knowledgeHistoryNotice = "[历史知识正文含凭据，展示记录已脱敏，不能作为完整安装脚本执行；需要原文时请用 knowledge_get 读取当前已发布版本。]"

var (
	knowledgeChatDestination = regexp.MustCompile(`(?i)知识库|共享知识|运维知识|knowledge\s*(?:base|库)`)
	knowledgeChatSave        = regexp.MustCompile(`(?i)保存|存入|存到|存进|写入|写到|记录|记到|收录|添加|新增|更新|整理|补充|加入|\b(?:save|store|record|add|update)\b`)
	knowledgeChatScript      = regexp.MustCompile(`(?i)脚本|安装命令|部署命令|\b(?:script|installer|installation\s+command|deployment\s+command)\b`)
	knowledgeChatSSHRegister = regexp.MustCompile(`(?i)(?:登记|注册|添加|新增|连接).{0,12}(?:SSH|服务器|主机)|(?:SSH|服务器|主机).{0,12}(?:登记|注册)|\b(?:register|add|connect)\b.{0,16}\b(?:ssh|server)\b`)
)

// A script's PASSWORD/HOST variables are not an SSH registration record. Only
// an explicit request to save a script is routed around the SSH intake parser;
// mentioning knowledge somewhere in an ordinary registration is insufficient.
func knowledgeScriptSaveIntent(content string) bool {
	var outside []string
	fenced := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			outside = append(outside, line)
		}
	}
	intent := strings.Join(outside, "\n")
	if len([]rune(intent)) > 1200 {
		intent = string([]rune(intent)[:1200])
	}
	if knowledgeChatSSHRegister.MatchString(intent) {
		return false
	}
	return knowledgeChatDestination.MatchString(intent) && knowledgeChatSave.MatchString(intent) && knowledgeChatScript.MatchString(intent)
}

func prepareKnowledgeChatInput(content string) (string, []SSHRegistrationInput, error) {
	if knowledgeScriptSaveIntent(content) {
		if strings.ContainsRune(content, 0) {
			return "", nil, ErrValidation
		}
		return content, nil, nil
	}
	return ParseSSHRegistrationInput(content)
}

func (s *Service) protectKnowledgeChatInput(content string) (display, cipher string, err error) {
	if !knowledgeScriptSaveIntent(content) || !knowledgeContentHasCredentials(content) {
		return content, "", nil
	}
	cipher, err = s.encryptSecret(content)
	if err != nil {
		return "", "", err
	}
	return redactKnowledgeText(content), cipher, nil
}

func knowledgeCallSecrets(calls []ToolCall) []string {
	var out []string
	for _, call := range calls {
		if !oneOf(call.Function.Name, "knowledge_create", "knowledge_save") {
			continue
		}
		var args map[string]any
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			continue
		}
		for _, value := range args {
			if text, ok := value.(string); ok {
				out = append(out, knowledgeTextSecrets(text)...)
			}
		}
	}
	return out
}

func knowledgeResultSecrets(content string) []string {
	var result struct {
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(content), &result) != nil {
		return nil
	}
	return knowledgeTextSecrets(result.Content)
}

// Replace values after decoding JSON so quotes/backslashes inside credentials
// cannot break persisted tool-call arguments or tool results.
func redactKnowledgeJSON(value any, secrets []string) any {
	switch item := value.(type) {
	case string:
		return redactKnowledgeText(redactSSHOutput(item, secrets, true))
	case map[string]any:
		for key, child := range item {
			item[key] = redactKnowledgeJSON(child, secrets)
		}
	case []any:
		for index, child := range item {
			item[index] = redactKnowledgeJSON(child, secrets)
		}
	}
	return value
}

// This is a persistence-only copy. The original tool messages must continue to
// the current model request with their usable script contents intact. Later
// turns receive the explicit notice rather than a misleading partial script.
func redactKnowledgeMessages(messages []providerMessage, knownSecrets []string) []providerMessage {
	secrets := append([]string(nil), knownSecrets...)
	getIDs := map[string]bool{}
	for _, message := range messages {
		secrets = append(secrets, knowledgeCallSecrets(message.ToolCalls)...)
		for _, call := range message.ToolCalls {
			if call.Function.Name == "knowledge_get" {
				getIDs[call.ID] = true
			}
		}
	}
	for _, message := range messages {
		if message.Role == "tool" && getIDs[message.ToolCallID] {
			secrets = append(secrets, knowledgeResultSecrets(message.Content)...)
		}
	}
	out := append([]providerMessage(nil), messages...)
	for i := range out {
		message := &out[i]
		message.ToolCalls = append([]ToolCall(nil), message.ToolCalls...)
		for j := range message.ToolCalls {
			call := &message.ToolCalls[j]
			var args map[string]any
			if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
				continue
			}
			originalBody, _ := args["content"].(string)
			redactKnowledgeJSON(args, secrets)
			redactedBody, _ := args["content"].(string)
			if oneOf(call.Function.Name, "knowledge_create", "knowledge_save") && redactedBody != originalBody {
				message.Content += "\n" + knowledgeHistoryNotice
			}
			raw, _ := json.Marshal(args)
			call.Function.Arguments = string(raw)
		}
		if message.Role == "tool" {
			var result map[string]any
			if json.Unmarshal([]byte(message.Content), &result) == nil {
				body, _ := result["content"].(string)
				protected, _ := result["contains_credentials"].(bool)
				if getIDs[message.ToolCallID] && (protected || knowledgeContentHasCredentials(body)) {
					// A page may start in the middle of a credential; redact the
					// whole body rather than relying on a complete assignment.
					result["content"] = knowledgeHistoryNotice
					result["content_redacted"] = true
				}
				redactKnowledgeJSON(result, secrets)
				raw, _ := json.Marshal(result)
				message.Content = string(raw)
				continue
			}
		}
		message.Content = redactKnowledgeText(redactSSHOutput(message.Content, secrets, true))
	}
	return out
}
