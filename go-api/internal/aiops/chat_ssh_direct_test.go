package aiops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestSSHDirectMessageDoesNotRequireInputFormat(t *testing.T) {
	s := NewService(config.Config{AppKey: "direct-ssh-fixture"}, nil, nil)
	defer s.Close()
	for _, original := range []string{
		"帮我添加这些服务器：巴黎 192.0.2.10，登录 root，口令 secret-one",
		"192.0.2.10\nroot\nsecret-two",
		"192.0.2.10 root secret-three",
		"添加 SSH 服务器：srv1，账号 ops，凭据另附",
	} {
		display, cipher, err := s.protectAIChatInput(original)
		if err != nil || display != sshDirectHistoryNotice || cipher == "" {
			t.Fatalf("free-form paste not protected: display=%q err=%v", display, err)
		}
		plain, err := s.decryptSecret(cipher)
		if err != nil || plain != original || strings.Contains(display, "secret-") {
			t.Fatal("original paste was changed or leaked")
		}
	}
	const ordinary = "检查 192.0.2.10 的网络"
	if display, cipher, err := s.protectAIChatInput(ordinary); err != nil || display != ordinary || cipher != "" {
		t.Fatal("ordinary operation was hidden", err)
	}
}

func TestSSHDirectMessageReachesMainAIWithoutPreparse(t *testing.T) {
	f := setupStaffAIFixture(t)
	const secret = "direct-flow-secret-fixture"
	const original = "帮我登记服务器：巴黎 192.0.2.10，root 的口令是 " + secret + "；第二台资料我稍后再给"
	detail := knowledgeFlowRun(t, f, f.admin, original, func(round int, request completionRequest) []ToolCall {
		if round != 1 || !knowledgeFlowUserHas(request, original) {
			t.Error("arbitrary SSH paste did not reach the main AI unchanged", round)
		}
		return nil
	})
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), secret) || strings.Contains(detail.Session.Title, secret) {
		t.Fatal("SSH credential leaked to session UI")
	}
	var display, cipher string
	if err := f.db.QueryRowContext(context.Background(), `SELECT content,content_cipher FROM v2_ai_message WHERE session_id=$1 AND role='user'`, detail.Session.ID).Scan(&display, &cipher); err != nil || display != sshDirectHistoryNotice || cipher == "" {
		t.Fatal("SSH paste not encrypted in transcript", err)
	}
	var staged int
	if err := f.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM v2_ai_server_intake WHERE session_id=$1`, detail.Session.ID).Scan(&staged); err != nil || staged != 0 {
		t.Fatal("preparser staged records before AI", err, staged)
	}
	current, err := f.s.history(context.Background(), detail.Session.ID, detail.Turns[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !messagesContainUserText(current, original) {
		t.Fatal("active turn lost original credentials")
	}
	later, err := f.s.history(context.Background(), detail.Session.ID, detail.Turns[0].ID+1)
	if err != nil {
		t.Fatal(err)
	}
	if messagesContainUserText(later, original) {
		t.Fatal("old SSH turn retransmitted credentials to model")
	}
}

func messagesContainUserText(messages []providerMessage, text string) bool {
	for _, message := range messages {
		if message.Role == "user" && message.Content == text {
			return true
		}
	}
	return false
}

func TestSSHStageToolCallsAreRedactedBeforePersistence(t *testing.T) {
	const secret = "stage-secret-fixture"
	args, _ := json.Marshal(map[string]any{"servers": []any{map[string]any{"host": "192.0.2.10", "username": "root", "password": secret}}})
	original := []providerMessage{{Role: "assistant", Content: "已提交 " + secret, ToolCalls: []ToolCall{{ID: "stage", Function: ToolFunction{Name: "ssh_server_stage", Arguments: string(args)}}}}}
	before, _ := json.Marshal(original)
	redacted := redactSSHStageMessages(original)
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("live model messages were mutated")
	}
	serialized, _ := json.Marshal(redacted)
	if strings.Contains(string(serialized), secret) || !strings.Contains(string(serialized), sshStageHistoryNotice) || !strings.Contains(redacted[0].ToolCalls[0].Function.Arguments, "credentials_redacted") {
		t.Fatal("stage credentials were persisted")
	}
}
