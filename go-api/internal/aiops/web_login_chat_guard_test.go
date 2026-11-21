package aiops

import (
	"context"
	"errors"
	"testing"
)

func TestWebLoginChatBackstop(t *testing.T) {
	blocked := []string{
		"帮我登录 https://panel.example.com，账号 demo，密码 fixture，看看服务状态",
		"网址：https://panel.example.com\n用户名: demo\n口令: fixture",
		"https://panel.example.com\ndemo\nfixture",
		"网页登录\n[控制台](https://panel.example.com/login)\noperator\n\"space preserved\"\n查一下证书",
		"网页登录，密码：fixture",
		"查看 https://operator:fixture@panel.example.com",
	}
	for i, content := range blocked {
		// Empty services prove that the rejection runs before DB access,
		// credential serialization, or any model call.
		request := SendMessageRequest{Content: content, RequestID: "fixture-guard"}
		if _, err := (&Service{}).SendMessage(context.Background(), 1, request); !errors.Is(err, ErrWebLoginChat) {
			t.Fatalf("desktop case %d: expected a web login rejection, got %v", i, err)
		}
		if _, err := (&TelegramBridge{}).MiniSendMessage(context.Background(), "fixture", request); !errors.Is(err, ErrWebLoginChat) {
			t.Fatalf("mini case %d: expected a web login rejection, got %v", i, err)
		}
	}
	for i, content := range []string{
		"看一下 https://docs.example.com 的登录流程",
		"站点 https://panel.example.com\n检查登录后白屏的问题",
		"请用 SSH 登录 192.0.2.10\n用户名: demo\n密码: fixture\n部署地址：https://package.example.com",
		"帮我登记两台服务器\n192.0.2.10\nroot\nfixture",
	} {
		if containsWebLoginChatCredentials(content) {
			t.Fatalf("ordinary chat or SSH case %d must be preserved", i)
		}
	}
}
