package aiops

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTelegramChunksPreserveAllTextWithinUTF16Limit(t *testing.T) {
	text := strings.Repeat("节点😀\n", 3000)
	chunks := telegramTextChunks(text)
	if strings.Join(chunks, "") != text {
		t.Fatal("chunking lost text")
	}
	for _, chunk := range chunks {
		if len(utf16.Encode([]rune(chunk))) > 3500 {
			t.Fatal("Telegram UTF-16 limit exceeded")
		}
	}
}

func TestTelegramCommandAliases(t *testing.T) {
	for input, want := range map[string]string{"/ainew": "/new", "/aisessions": "/sessions", "/aimodel@test fixture": "/model", "/aistop": "/stop", "/aidetails": "/details", "/aiexit": "/exit"} {
		command, _ := telegramCommand(input)
		if command != want {
			t.Fatal(input, command, want)
		}
	}
}

func TestTelegramActionCardUsesPublicPreviewAndBindsExactCommand(t *testing.T) {
	a := Action{ID: 1, SessionID: 2, Kind: "panel_operation", Payload: []byte(`{"operation":"nodes.v2node.save","parameters_cipher":"MUST_NOT_EXPOSE_CIPHER","parameters_preview":{"host":"node.example.test"}}`), Reason: "fixture"}
	card, key, err := telegramActionCard(a)
	if err != nil || strings.Contains(card, "MUST_NOT_EXPOSE") || !strings.Contains(card, "node.example.test") {
		t.Fatal(card, err)
	}
	a.Reason = "different task"
	_, next, _ := telegramActionCard(a)
	if key == next {
		t.Fatal("approval card fingerprint ignored changed operation")
	}
}
