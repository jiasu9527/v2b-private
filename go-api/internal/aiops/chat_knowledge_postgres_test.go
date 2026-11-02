package aiops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgeChatProtectedScriptPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM v2_ai_knowledge WHERE created_by=$1`, f.admin)
	})
	const secret = "fixture-knowledge-chat-private-key-value"
	const script = "#!/bin/sh\nHOST=192.0.2.77\nUSER=root\nPASSWORD=" + secret + "\n./install --key $PASSWORD"
	const task = "把安装脚本保存到知识库，保留完整参数\n```sh\n" + script + "\n```"
	var knowledgeID int64
	detail := knowledgeFlowRun(t, f, f.admin, task, func(round int, request completionRequest) []ToolCall {
		if !knowledgeFlowUserHas(request, secret) {
			t.Error("provider did not receive the original encrypted operator task")
		}
		if knowledgeFlowHas(request, sshIntakeNotice) {
			t.Error("script was routed through SSH registration intake")
		}
		switch round {
		case 1:
			return []ToolCall{knowledgeFlowCall("save-script", "knowledge_create", map[string]any{"title": "SCRIPT_CREDENTIAL_FLOW", "summary": "管理员提供的安装脚本", "content": script, "category": "server", "source_note": "本轮管理员提供", "pinned": true})}
		case 2:
			result := knowledgeFlowResult(t, request, "save-script")
			id, ok := result["id"].(float64)
			if !ok || result["status"] != "active" {
				t.Error("knowledge script was not saved", result)
				return nil
			}
			knowledgeID = int64(id)
			return []ToolCall{knowledgeFlowCall("read-script", "knowledge_get", map[string]any{"id": knowledgeID, "content_limit": 2000})}
		case 3:
			result := knowledgeFlowResult(t, request, "read-script")
			if result["content"] != script || result["contains_credentials"] != true {
				t.Error("live model could not read complete installation script", result["contains_credentials"])
			}
			return nil
		default:
			t.Error("unexpected round", round)
			return nil
		}
	})
	if knowledgeID == 0 {
		t.Fatal("knowledge was not created")
	}
	ui, _ := json.Marshal(detail)
	if strings.Contains(string(ui), secret) || strings.Contains(string(ui), "content_cipher") {
		t.Fatal("message UI leaked protected script")
	}
	var intakeCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_server_intake WHERE session_id=$1`, detail.Session.ID).Scan(&intakeCount); err != nil || intakeCount != 0 {
		t.Fatal("knowledge script created SSH intake", err, intakeCount)
	}
	var storedContent, cipher string
	if err := f.db.QueryRow(`SELECT content,content_cipher FROM v2_ai_message WHERE session_id=$1 AND role='user'`, detail.Session.ID).Scan(&storedContent, &cipher); err != nil {
		t.Fatal(err)
	}
	if cipher == "" || strings.Contains(cipher, secret) || strings.Contains(storedContent, secret) {
		t.Fatal("original input not protected at rest")
	}
	plain, err := f.s.decryptSecret(cipher)
	if err != nil || plain != task {
		t.Fatal("stored input cannot be recovered", err)
	}
	var leaked bool
	if err = f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM v2_ai_message WHERE session_id=$1 AND (strpos(content,$2)>0 OR strpos(tool_calls::TEXT,$2)>0))`, detail.Session.ID, secret).Scan(&leaked); err != nil || leaked {
		t.Fatal("plaintext secret in persisted transcript", err)
	}
	history, err := f.s.history(ctx, detail.Session.ID, detail.Turns[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertContextToolPairs(t, history)
	var originalFound, redactedToolFound bool
	for _, message := range history {
		if message.Role == "user" && message.Content == task {
			originalFound = true
		}
		if message.Role == "tool" && message.ToolCallID == "read-script" {
			redactedToolFound = strings.Contains(message.Content, knowledgeHistoryNotice)
		}
		if message.Role != "user" {
			raw, _ := json.Marshal(message)
			if strings.Contains(string(raw), secret) {
				t.Fatal("historical tool data restored plaintext credentials")
			}
		}
	}
	if !originalFound || !redactedToolFound {
		t.Fatal("history lost original task or re-read requirement", originalFound, redactedToolFound)
	}
	entry, err := f.s.GetKnowledge(ctx, f.admin, knowledgeID)
	if err != nil || entry.Content != script || !entry.ContainsCredentials {
		t.Fatal("authorized knowledge detail lost original script", err)
	}
}
