package httpapi

import (
	"encoding/base64"
	"strings"
	"testing"

	"forest/go-api/internal/subscribelink"
	usersvc "forest/go-api/internal/user"
)

func TestExtraTrojanRendersOwnCredentialAcrossStructuredFormats(t *testing.T) {
	const (
		raw        = "trojan://8E28E6FF-2F00-14C1-FD08-D6D91AFF8A37@58.247.187.151:20003?allowInsecure=1&peer=mpvideo.qpic.cn#Hong%20Kong%20%7C%2001"
		credential = "8E28E6FF-2F00-14C1-FD08-D6D91AFF8A37"
	)
	node, err := subscribelink.Parse(raw)
	if err != nil {
		t.Fatalf("parse extra Trojan: %v", err)
	}

	clash, ok := buildClashStandardProxy("subscriber-uuid", node)
	if !ok {
		t.Fatal("extra Trojan was omitted from Clash output")
	}
	assertExtraTrojanProxy(t, clash, "name", "server", "port", "password")
	if got := clash["sni"]; got != "mpvideo.qpic.cn" {
		t.Fatalf("Clash SNI = %#v", got)
	}
	if got := clash["skip-cert-verify"]; got != true {
		t.Fatalf("Clash skip-cert-verify = %#v", got)
	}

	singBox, ok := buildSingBoxOutbound("subscriber-uuid", node)
	if !ok {
		t.Fatal("extra Trojan was omitted from sing-box output")
	}
	assertExtraTrojanProxy(t, singBox, "tag", "server", "server_port", "password")
	tlsSettings, ok := singBox["tls"].(map[string]any)
	if !ok || tlsSettings["server_name"] != "mpvideo.qpic.cn" || tlsSettings["insecure"] != true {
		t.Fatalf("unexpected sing-box TLS settings: %#v", singBox["tls"])
	}

	surge, ok := buildSurgeProxyLine("subscriber-uuid", node)
	if !ok {
		t.Fatal("extra Trojan was omitted from Surge output")
	}
	for _, part := range []string{
		"Hong Kong | 01=trojan",
		"58.247.187.151",
		"20003",
		"password=" + credential,
		"sni=mpvideo.qpic.cn",
		"skip-cert-verify=true",
	} {
		if !strings.Contains(surge, part) {
			t.Fatalf("Surge output %q does not contain %q", surge, part)
		}
	}
	if strings.Contains(surge, "subscriber-uuid") {
		t.Fatalf("Surge output replaced standalone credential: %q", surge)
	}
}

func TestNormalTrojanStillUsesSubscriberCredential(t *testing.T) {
	node := map[string]any{
		"type":        "trojan",
		"name":        "Managed",
		"host":        "managed.example.com",
		"port":        int64(443),
		"server_name": "managed.example.com",
	}
	proxy, ok := buildClashStandardProxy("subscriber-uuid", node)
	if !ok || proxy["password"] != "subscriber-uuid" {
		t.Fatalf("managed Trojan credential behavior changed: %#v", proxy)
	}
}

func TestExtraTrojanRawAndShadowrocketKeepConfiguredLink(t *testing.T) {
	const raw = "trojan://fixed-secret@58.247.187.151:20003?allowInsecure=1&peer=mpvideo.qpic.cn#Extra"
	node, err := subscribelink.Parse(raw)
	if err != nil {
		t.Fatalf("parse extra Trojan: %v", err)
	}

	general := buildGeneralSubscribePayload("subscriber-uuid", []map[string]any{node})
	decoded, err := base64.StdEncoding.DecodeString(general)
	if err != nil {
		t.Fatalf("decode general payload: %v", err)
	}
	if got := strings.TrimSpace(string(decoded)); got != raw {
		t.Fatalf("general extra link = %q", got)
	}

	shadowrocket := buildShadowrocketPayload(usersvc.Subscribe{UUID: "subscriber-uuid"}, []map[string]any{node})
	decoded, err = base64.StdEncoding.DecodeString(shadowrocket)
	if err != nil {
		t.Fatalf("decode Shadowrocket payload: %v", err)
	}
	if !strings.Contains(string(decoded), raw+"\r\n") || strings.Contains(string(decoded), "subscriber-uuid@58.247.187.151") {
		t.Fatalf("Shadowrocket extra link changed: %q", string(decoded))
	}
}

func assertExtraTrojanProxy(t *testing.T, proxy map[string]any, nameKey, hostKey, portKey, credentialKey string) {
	t.Helper()
	if got := proxy[nameKey]; got != "Hong Kong | 01" {
		t.Fatalf("%s = %#v", nameKey, got)
	}
	if got := proxy[hostKey]; got != "58.247.187.151" {
		t.Fatalf("%s = %#v", hostKey, got)
	}
	if got := proxy[portKey]; got != int64(20003) {
		t.Fatalf("%s = %#v", portKey, got)
	}
	if got := proxy[credentialKey]; got != "8E28E6FF-2F00-14C1-FD08-D6D91AFF8A37" {
		t.Fatalf("%s = %#v", credentialKey, got)
	}
}
