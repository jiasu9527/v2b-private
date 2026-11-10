package newapibridge

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

func fixtureCard() CharacterCard {
	return CharacterCard{Spec: "chara_card_v2", SpecVersion: "2.0", Data: CharacterData{Name: "星野", Description: "温柔的向导", Extensions: map[string]json.RawMessage{"future": json.RawMessage(`{"script":"alert(1)"}`)}}}
}
func testPNGChunk(kind string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(kind)
	b.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	return b.Bytes()
}
func fixtureCardPNG(t *testing.T, kind string) []byte {
	t.Helper()
	raw, _ := json.Marshal(fixtureCard())
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header, 2)
	binary.BigEndian.PutUint32(header[4:], 2)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, testPNGChunk("IHDR", header)...)
	out = append(out, testPNGChunk("tEXt", []byte(kind+"\x00"+base64.StdEncoding.EncodeToString(raw)))...)
	out = append(out, testPNGChunk("IEND", nil)...)
	return out
}
func TestCharacterJSONAndPNGPreserveDataWithoutExecutingExtensions(t *testing.T) {
	raw, _ := json.Marshal(fixtureCard())
	for _, body := range [][]byte{raw, fixtureCardPNG(t, "chara")} {
		c, err := ParseCharacterCard(body)
		if err != nil {
			t.Fatal(err)
		}
		if c.Data.Name != "星野" || !bytes.Contains(c.Data.Extensions["future"], []byte("alert(1)")) {
			t.Fatal(c)
		}
		exported, _ := json.Marshal(c)
		if !bytes.Contains(exported, []byte(`"future"`)) {
			t.Fatal(string(exported))
		}
	}
}
func TestCharacterRejectsUnsupportedAndMalformedImports(t *testing.T) {
	raw, _ := json.Marshal(fixtureCard())
	badCRC := fixtureCardPNG(t, "chara")
	badCRC[len(badCRC)-1] ^= 1
	cases := [][]byte{append(raw, []byte(` {}`)...), []byte(`{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":"x"}}`), fixtureCardPNG(t, "ccv3"), badCRC, bytes.Repeat([]byte("x"), MaxCharacterImportBytes+1)}
	for i, body := range cases {
		if _, err := ParseCharacterCard(body); err == nil {
			t.Fatalf("accepted case %d", i)
		}
	}
	c := fixtureCard()
	c.Data.CharacterBook = &LoreBook{RecursiveScanning: true}
	raw, _ = json.Marshal(c)
	if _, err := ParseCharacterCard(raw); !errors.Is(err, ErrUnsupportedCharacter) {
		t.Fatal(err)
	}
}
func TestLoreSelectionBudgetAndPromptMetadata(t *testing.T) {
	card := fixtureCard()
	card.Data.CreatorNotes = "DO NOT INSERT"
	card.Data.Creator = "NOT PROMPT"
	card.Data.CharacterBook = &LoreBook{TokenBudget: 20, Entries: []LoreEntry{
		{Keys: []string{"FOREST"}, Content: "matched", Enabled: true, Priority: 2},
		{Content: "always", Enabled: true, Constant: true, Priority: 1},
		{Content: strings.Repeat("x", 30), Enabled: true, Constant: true},
		{Content: "disabled", Enabled: false, Constant: true},
	}}
	payload, info, err := buildChatPayload(Conversation{Mode: "character", Model: "test"}, card.Data, nil, 0, "forest", 512)
	if err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]chatPromptMessage)
	text := messages[0].Content
	if !strings.Contains(text, "matched") || !strings.Contains(text, "always") || strings.Contains(text, "disabled") || strings.Contains(text, "DO NOT INSERT") || strings.Contains(text, "NOT PROMPT") {
		t.Fatal(text)
	}
	if info["lore_entries_used"] != 2 || info["lore_entries_omitted"] != 1 {
		t.Fatal(info)
	}
}
func TestPromptContextTrimAndOversizeVisible(t *testing.T) {
	history := []chatPromptMessage{{Role: "user", Content: strings.Repeat("x", 20000)}, {Role: "assistant", Content: strings.Repeat("y", 12000)}, {Role: "user", Content: "recent"}, {Role: "assistant", Content: "answer"}}
	payload, meta, err := buildChatPayload(Conversation{Mode: "chat", Model: "test"}, CharacterData{}, history, 4, "now", 2048)
	if err != nil {
		t.Fatal(err)
	}
	msgs := payload["messages"].([]chatPromptMessage)
	if meta["history_messages_omitted"] != 2 || msgs[1].Content != "recent" {
		t.Fatal(meta, msgs)
	}
	_, _, err = buildChatPayload(Conversation{Mode: "character", Model: "test"}, CharacterData{Description: strings.Repeat("x", 30000)}, nil, 0, "now", 2048)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
