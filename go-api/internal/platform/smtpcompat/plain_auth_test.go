package smtpcompat

import (
	"net/smtp"
	"strings"
	"testing"
)

func TestPlainAuthAllowsInsecureRemoteWhenExplicitlyEnabled(t *testing.T) {
	auth := PlainAuth("", "mailer", "secret", "mail.sidecloud.cc", true)
	proto, payload, err := auth.Start(&smtp.ServerInfo{
		Name: "mail.sidecloud.cc",
		TLS:  false,
		Auth: []string{"PLAIN"},
	})
	if err != nil {
		t.Fatalf("expected insecure plain auth to be allowed, got %v", err)
	}
	if proto != "PLAIN" {
		t.Fatalf("expected PLAIN auth, got %q", proto)
	}
	if string(payload) != "\x00mailer\x00secret" {
		t.Fatalf("unexpected auth payload: %q", string(payload))
	}
}

func TestPlainAuthRejectsInsecureRemoteByDefault(t *testing.T) {
	auth := PlainAuth("", "mailer", "secret", "mail.sidecloud.cc", false)
	_, _, err := auth.Start(&smtp.ServerInfo{
		Name: "mail.sidecloud.cc",
		TLS:  false,
		Auth: []string{"PLAIN"},
	})
	if err == nil {
		t.Fatal("expected insecure remote auth to be rejected")
	}
	if !strings.Contains(err.Error(), "unencrypted connection") {
		t.Fatalf("expected unencrypted connection error, got %v", err)
	}
}
