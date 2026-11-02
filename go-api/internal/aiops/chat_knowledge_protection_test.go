package aiops

import (
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestKnowledgeChatScriptDoesNotBecomeSSHRegistration(t *testing.T) {
	const script = "把下面的安装脚本保存到知识库\n```sh\nHOST=192.0.2.45\nUSER=root\nPASSWORD=fixture-script-secret\nbash install.sh --key fixture-deploy-key\n```"
	content, inputs, err := prepareKnowledgeChatInput(script)
	if err != nil || len(inputs) != 0 || content != script {
		t.Fatal("knowledge script was treated as SSH registration", err, len(inputs))
	}
	const reversed = "```sh\nPASSWORD=fixture-script-secret\nbash install.sh --key fixture-deploy-key\n```\n把上面的安装脚本保存到知识库"
	if content, inputs, err := prepareKnowledgeChatInput(reversed); err != nil || len(inputs) != 0 || content != reversed {
		t.Fatal("request after code block was not recognized", err, len(inputs))
	}
	s := NewService(config.Config{AppKey: "knowledge-chat-fixture"}, nil, nil)
	defer s.Close()
	display, cipher, err := s.protectKnowledgeChatInput(content)
	if err != nil || cipher == "" || strings.Contains(display, "fixture-script-secret") || strings.Contains(display, "fixture-deploy-key") {
		t.Fatal("knowledge script not encrypted/redacted", err)
	}
	plain, err := s.decryptSecret(cipher)
	if err != nil || plain != script {
		t.Fatal("original script was not recoverable", err)
	}
	for _, task := range []string{
		"帮我登记SSH服务器，知识库有安装脚本\nhost: 192.0.2.45\nusername: root\npassword: fixture-ssh-secret",
		"把服务器资料保存到知识库\nhost: 192.0.2.45\nusername: root\npassword: fixture-ssh-secret",
	} {
		content, inputs, err := prepareKnowledgeChatInput(task)
		if err != nil || len(inputs) != 1 || inputs[0].Password != "fixture-ssh-secret" || strings.Contains(content, "fixture-ssh-secret") {
			t.Fatal("ordinary SSH intake boundary changed", err, len(inputs))
		}
	}
	for _, task := range []string{"查询知识库里的安装脚本", "安装脚本在哪里", "保存站点架构到知识库"} {
		if knowledgeScriptSaveIntent(task) {
			t.Fatal("non-script-save task bypasses SSH intake", task)
		}
	}
}

func TestKnowledgeMessagesPersistRedactedCopyWithUsableLiveOriginal(t *testing.T) {
	const body = "#!/bin/sh\nTOKEN=fixture-live-deploy-token\nPASSWORD='fixture-live-password'\n./install --key $TOKEN"
	getRaw, _ := json.Marshal(map[string]any{"id": 12, "version": 3, "contains_credentials": true, "content": body, "truncated": false})
	original := []providerMessage{
		{Role: "assistant", ToolCalls: []ToolCall{knowledgeFlowCall("create", "knowledge_create", map[string]any{"title": "部署", "content": body})}},
		{Role: "tool", ToolCallID: "create", Content: `{"id":12,"version":3,"status":"active"}`},
		{Role: "assistant", ToolCalls: []ToolCall{knowledgeFlowCall("get", "knowledge_get", map[string]any{"id": 12})}},
		{Role: "tool", ToolCallID: "get", Content: string(getRaw)},
		{Role: "assistant", Content: "脚本的参数是 fixture-live-deploy-token", ToolCalls: []ToolCall{knowledgeFlowCall("ssh", "propose_ssh_command", map[string]any{"command": "./install --key fixture-live-deploy-token"})}},
	}
	before, _ := json.Marshal(original)
	persisted := redactKnowledgeMessages(original, nil)
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("persistence redaction mutated live model context")
	}
	raw, _ := json.Marshal(persisted)
	for _, secret := range []string{"fixture-live-deploy-token", "fixture-live-password"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret persisted", secret)
		}
	}
	if !strings.Contains(persisted[0].Content, knowledgeHistoryNotice) || !strings.Contains(persisted[3].Content, "content_redacted") || !strings.Contains(persisted[3].Content, "knowledge_get") {
		t.Fatal("redacted history lacks explicit re-read notice")
	}
	for _, message := range persisted {
		for _, call := range message.ToolCalls {
			if !json.Valid([]byte(call.Function.Arguments)) {
				t.Fatal("redaction damaged tool JSON")
			}
		}
		if message.Role == "tool" && !json.Valid([]byte(message.Content)) {
			t.Fatal("redaction damaged result JSON")
		}
	}
}

func TestKnowledgeGetContinuationPageNeverPersistsCredentialFragment(t *testing.T) {
	const fragment = "naked-key-fragment-without-assignment"
	raw, _ := json.Marshal(map[string]any{"id": 7, "contains_credentials": true, "content": fragment, "content_offset": 1000})
	messages := []providerMessage{
		{Role: "assistant", ToolCalls: []ToolCall{knowledgeFlowCall("page", "knowledge_get", map[string]any{"id": 7, "content_offset": 1000})}},
		{Role: "tool", ToolCallID: "page", Content: string(raw)},
	}
	persisted, _ := json.Marshal(redactKnowledgeMessages(messages, nil))
	if strings.Contains(string(persisted), fragment) || !strings.Contains(string(persisted), "content_redacted") {
		t.Fatal("credential fragment exposed in persisted page")
	}
}
