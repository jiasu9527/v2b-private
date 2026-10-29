package aiops

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func intakeTestKey(t *testing.T, phrase string) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if phrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "intake-fixture")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "intake-fixture", []byte(phrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(pem.EncodeToMemory(block)), "\n")
}

func TestSSHIntakeParsesCommonCredentialsWithoutLeaking(t *testing.T) {
	key := intakeTestKey(t, "")
	phrase := " key-phrase-test "
	encKey := intakeTestKey(t, phrase)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	tests := []struct {
		name, content, host, password, key, phrase string
		port                                       int
	}{
		{name: "three lines", content: "帮我检查服务\n192.0.2.10\nroot\nsecret-test", host: "192.0.2.10", password: "secret-test", port: 22},
		{name: "endpoint port", content: "192.0.2.10:2222\nroot\nsecret-test\n帮我检查服务", host: "192.0.2.10", password: "secret-test", port: 2222},
		{name: "four lines", content: "192.0.2.10\n2222\nroot\nsecret-test\n帮我检查服务", host: "192.0.2.10", password: "secret-test", port: 2222},
		{name: "raw whitespace", content: "192.0.2.10\nroot\n  secret-test  \n帮我检查服务", host: "192.0.2.10", password: "  secret-test  ", port: 22},
		{name: "raw quoted password", content: "192.0.2.10\nroot\n\"secret-test\"\n帮我检查服务", host: "192.0.2.10", password: `"secret-test"`, port: 22},
		{name: "labels", content: "host: 192.0.2.10\nusername: root\npassword: secret-test\n帮我检查服务", host: "192.0.2.10", password: "secret-test", port: 22},
		{name: "labeled quoted spacing", content: "host: 192.0.2.10\nusername: root\npassword: \" secret-test \"\n帮我检查服务", host: "192.0.2.10", password: " secret-test ", port: 22},
		{name: "labeled extra spaces", content: "host: 192.0.2.10\nusername: root\npassword:  secret-test  \n帮我检查服务", host: "192.0.2.10", password: " secret-test  ", port: 22},
		{name: "next line password", content: "host: 192.0.2.10\nusername: root\npassword:\n  secret-test  \n帮我检查服务", host: "192.0.2.10", password: "  secret-test  ", port: 22},
		{name: "Chinese fields", content: "主机地址： 192.0.2.10\n端口： 2222\n用户名： root\n密码： 密码测试#123\n帮我检查服务", host: "192.0.2.10", password: "密码测试#123", port: 2222},
		{name: "mixed fields", content: "host: 192.0.2.10\nusername: root\nsecret-test\n帮我检查服务", host: "192.0.2.10", password: "secret-test", port: 22},
		{name: "ssh command labels", content: "ssh -p 2222 root@192.0.2.10\n密码: secret-test\n帮我检查服务", host: "192.0.2.10", password: "secret-test", port: 2222},
		{name: "IPv6", content: "[2001:db8::10]:2222\nroot\nsecret-test\n帮我检查服务", host: "2001:db8::10", password: "secret-test", port: 2222},
		{name: "private key", content: "host: 192.0.2.10\nusername: root\nprivate_key: " + key + "\n帮我检查服务", host: "192.0.2.10", key: key, port: 22},
		{name: "encrypted key", content: "host: 192.0.2.10\nusername: root\n" + encKey + "\npassphrase: \" key-phrase-test \"\n帮我检查服务", host: "192.0.2.10", key: encKey, phrase: phrase, port: 22},
		{name: "provided fingerprint", content: "192.0.2.10\nroot\nsecret-test\nfingerprint: " + fingerprint + "\n帮我检查服务", host: "192.0.2.10", password: "secret-test", port: 22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			safe, records, err := ParseSSHRegistrationInput(tt.content)
			if err != nil || len(records) != 1 {
				t.Fatalf("expected one recognized record: count=%d err=%v", len(records), err)
			}
			got := records[0]
			if got.Host != tt.host || got.Port != tt.port || got.Username != "root" || got.Password != tt.password || got.PrivateKey != tt.key || got.KeyPassphrase != tt.phrase {
				t.Fatal("credential or endpoint bytes changed")
			}
			if !strings.Contains(safe, "帮我检查服务") || !strings.Contains(safe, sshIntakeNotice) {
				t.Fatal("task text or intake notice missing")
			}
			for _, secret := range []string{tt.password, tt.key, tt.phrase} {
				if secret != "" && strings.Contains(safe, secret) {
					t.Fatal("secret survived in sanitized text")
				}
			}
		})
	}
}

func TestSSHIntakeOrdinaryTasksRemainUnchanged(t *testing.T) {
	for _, content := range []string{
		"帮我检查服务器状态，不要输出密码", "localhost 服务状态如何？", "host: 192.0.2.10\nusername: root\n帮我检查端口",
		"ssh root@192.0.2.10\n帮我检查端口", "ssh root@192.0.2.10\nplease inspect ports",
	} {
		safe, records, err := ParseSSHRegistrationInput(content)
		if err != nil || len(records) != 0 || safe != content {
			t.Fatalf("ordinary task changed: %q err=%v", content, err)
		}
	}
}

func TestSSHIntakeRejectsAmbiguousCredentialMessagesLocally(t *testing.T) {
	for name, content := range map[string]string{
		"password only": "password: secret-test", "missing identity": "host: 192.0.2.10\npassword: secret-test",
		"bad host":                "host: https://bad/path\nusername: root\npassword: secret-test",
		"bad port":                "host: 192.0.2.10\nport: 99999\nusername: root\npassword: secret-test",
		"conflicting port":        "host: 192.0.2.10:2222\nport: 22\nusername: root\npassword: secret-test",
		"bad username":            "host: 192.0.2.10\nusername: root /bin/sh\npassword: secret-test",
		"multiple records":        "192.0.2.10\nroot\nsecret-test\n192.0.2.11\nroot\nsecond-secret",
		"duplicate password":      "host: 192.0.2.10\nusername: root\npassword: secret-test\npassword: second-secret",
		"secret in name":          "host: 192.0.2.10\nusername: root\npassword: secret-test\nname: server-secret-test",
		"secret in user":          "host: 192.0.2.10\nusername: root\npassword: root",
		"invalid fingerprint":     "host: 192.0.2.10\nusername: root\npassword: secret-test\nfingerprint: SHA256:invalid",
		"incomplete key":          "host: 192.0.2.10\nusername: root\n-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-test",
		"invalid key":             "host: 192.0.2.10\nusername: root\n-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-test\n-----END OPENSSH PRIVATE KEY-----",
		"malformed quoted secret": "host: 192.0.2.10\nusername: root\npassword: \"secret-test",
		"markdown table":          "| 主机 | 192.0.2.10 |\n| 用户 | root |\n| 密码 | secret-test |",
		"space label":             "host: 192.0.2.10\nusername: root\npassword secret-test",
		"space Chinese label":     "host: 192.0.2.10\nusername: root\n密码 secret-test",
		"JSON credentials":        `{"host":"192.0.2.10","username":"root","password":"secret-test"}`,
		"ssh URL":                 "ssh://root:secret-test@192.0.2.10:22",
		"sshpass":                 "sshpass -p secret-test ssh root@192.0.2.10",
		"bare ssh password":       "ssh root@192.0.2.10 -p 2222\nsecret-test\n帮我检查服务",
		"ambiguous command":       "ssh root@192.0.2.10\nuptime",
		"ambiguous uname":         "ssh root@192.0.2.10\nuname",
	} {
		t.Run(name, func(t *testing.T) {
			safe, records, err := ParseSSHRegistrationInput(content)
			if !errors.Is(err, ErrValidation) || safe != "" || len(records) != 0 {
				t.Fatal("ambiguous credentials were not rejected locally")
			}
			if strings.Contains(err.Error(), "secret-test") {
				t.Fatal("error exposed malformed credential")
			}
		})
	}
}

func TestSSHIntakeRedactsRepeatedAndOverlappingSecrets(t *testing.T) {
	phrase := "prefix-secret"
	key := intakeTestKey(t, phrase)
	content := "host: 192.0.2.10\nusername: root\npassword: secret\npassphrase: prefix-secret\n" + key + "\n请检查 prefix-secret 和 secret 的连接"
	safe, records, err := ParseSSHRegistrationInput(content)
	if err != nil || len(records) != 1 || strings.Contains(safe, "secret") || strings.Contains(safe, "prefix-") {
		t.Fatalf("repeated or overlapping credential leaked: err=%v", err)
	}
}
