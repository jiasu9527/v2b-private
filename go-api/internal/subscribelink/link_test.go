package subscribelink

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

const exampleTrojanURI = "trojan://8E28E6FF-2F00-14C1-FD08-D6D91AFF8A37@58.247.187.151:20003?allowInsecure=1&peer=mpvideo.qpic.cn#Hong%20Kong%20%7C%2001"

func TestParseTrojanShareLinkPreservesStandaloneNodeValues(t *testing.T) {
	node, err := Parse(exampleTrojanURI)
	if err != nil {
		t.Fatalf("parse example Trojan link: %v", err)
	}

	assertMapValue(t, node, "type", "trojan")
	assertMapValue(t, node, "name", "Hong Kong | 01")
	assertMapValue(t, node, "host", "58.247.187.151")
	assertMapValue(t, node, "port", int64(20003))
	assertMapValue(t, node, "server_name", "mpvideo.qpic.cn")
	assertMapValue(t, node, "allow_insecure", int64(1))
	assertMapValue(t, node, RawURIField, exampleTrojanURI)
	assertMapValue(t, node, CredentialField, "8E28E6FF-2F00-14C1-FD08-D6D91AFF8A37")
	if !IsExtra(node) {
		t.Fatalf("parsed standalone node is missing the extra-node marker: %#v", node)
	}
	if got := Credential("subscriber-uuid", node); got != "8E28E6FF-2F00-14C1-FD08-D6D91AFF8A37" {
		t.Fatalf("standalone credential = %q", got)
	}
	if got := RawURI(node); got != exampleTrojanURI {
		t.Fatalf("raw share link changed: %q", got)
	}

	tlsSettings, ok := node["tls_settings"].(map[string]any)
	if !ok {
		t.Fatalf("missing Trojan TLS settings: %#v", node["tls_settings"])
	}
	assertMapValue(t, tlsSettings, "server_name", "mpvideo.qpic.cn")
	assertMapValue(t, tlsSettings, "allow_insecure", true)

	cacheKey, _ := node["cache_key"].(string)
	sum := sha256.Sum256([]byte(exampleTrojanURI))
	if want := fmt.Sprintf("client-entry-extra-%x", sum[:12]); cacheKey != want {
		t.Fatalf("standalone cache key = %q, want %q", cacheKey, want)
	}
}

func TestNormalizeListKeepsConfiguredOrderIncludingDuplicates(t *testing.T) {
	second := "trojan://second-secret@second.example.com:443#Second"
	values, err := NormalizeList([]string{
		"  " + exampleTrojanURI + "  ",
		"",
		second,
		exampleTrojanURI,
	})
	if err != nil {
		t.Fatalf("normalize list: %v", err)
	}
	if len(values) != 3 || values[0] != exampleTrojanURI || values[1] != second || values[2] != exampleTrojanURI {
		t.Fatalf("standalone node order changed: %#v", values)
	}

	raw, err := EncodeList(values)
	if err != nil {
		t.Fatalf("encode list: %v", err)
	}
	decoded, err := DecodeList(raw)
	if err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(decoded) != 3 || decoded[0] != exampleTrojanURI || decoded[1] != second || decoded[2] != exampleTrojanURI {
		t.Fatalf("encoded list did not round-trip in order: %#v", decoded)
	}
}

func TestNormalizePositionDefaultsAfterAndAllowsBefore(t *testing.T) {
	if got, err := NormalizePosition(""); err != nil || got != PositionAfter {
		t.Fatalf("default position = %q, %v", got, err)
	}
	if got, err := NormalizePosition(" BEFORE "); err != nil || got != PositionBefore {
		t.Fatalf("before position = %q, %v", got, err)
	}
	if _, err := NormalizePosition("middle"); err == nil {
		t.Fatal("invalid position should be rejected")
	}
}

func assertMapValue(t *testing.T, values map[string]any, key string, want any) {
	t.Helper()
	if got := values[key]; got != want {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
}
