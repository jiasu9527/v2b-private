package newapibridge

import (
	"context"
	"errors"
	"testing"
)

func TestWorkspaceUnavailableModelDoesNotCallRelayOrInvalidateLogin(t *testing.T) {
	s, remote := workspacePostgresFixture(t)
	connectWorkspace(t, s)
	ctx := context.Background()
	c, err := s.CreateConversation(ctx, 1, ConversationCreateRequest{Title: "Retired model", Mode: "chat", Model: "retired-model"})
	if err != nil {
		t.Fatal(err)
	}
	req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "retired-model-request", Content: "hello"}
	var code, status string
	emit := func(name string, data any) error {
		value, _ := data.(map[string]any)
		if name == "error" {
			code, _ = value["code"].(string)
		}
		if name == "done" {
			status, _ = value["status"].(string)
		}
		return nil
	}
	if err = s.SendChat(ctx, 1, "auth", req, emit); !errors.Is(err, ErrWorkspaceModel) {
		t.Fatalf("missing model error = %v", err)
	}
	if code != "model_unavailable" || status != "failed" {
		t.Fatalf("wrong client result: code=%q status=%q", code, status)
	}
	if err = s.SendChat(ctx, 1, "auth", req, emit); err != nil {
		t.Fatalf("replay should return the saved failure: %v", err)
	}
	remote.mu.Lock()
	calls := remote.chats
	remote.mu.Unlock()
	if calls != 0 {
		t.Fatalf("unavailable model invoked paid relay %d times", calls)
	}
	account, err := s.WorkspaceAccount(ctx, 1, "auth")
	if err != nil || account["connected"] != true {
		t.Fatalf("login should remain valid: account=%v err=%v", account, err)
	}
}
