package aiops

import (
	"errors"
	"strings"
	"testing"
)

func knowledgeFixtureRequest() KnowledgeSaveRequest {
	return KnowledgeSaveRequest{Title: "入口架构", Summary: "探针检测入口", Content: "节点 192.0.2.4:54101，域名 node.example.test，使用 SSH 管理。密码和密钥通过服务器配置录入。", Category: "architecture", Status: "active"}
}

func TestKnowledgeValidationAndCredentialBoundaries(t *testing.T) {
	valid := knowledgeFixtureRequest()
	if _, err := normalizeKnowledgeRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password=example-secret", `{"api_key":"example-secret"}`, "DB_PASSWORD=example-secret", "private_key=example-secret", "client_secret=example-secret", "token: example-token", "密码：实际口令", "-----BEGIN RSA PRIVATE KEY-----\nfixture", "Authorization: Bearer abcdefghijklmnop", "ssh://root:password@192.0.2.4", "https://node.example.test/sub?token=abcdef"} {
		r := valid
		r.Content = secret
		if got, err := normalizeKnowledgeRequest(r); err != nil || got.Content != secret {
			t.Fatal("installation body rejected or modified", err)
		}
		if !knowledgeContentHasCredentials(secret) || redactKnowledgeText(secret) == secret {
			t.Fatal("credential not recognized for encrypted storage", secret)
		}
	}
	for _, change := range []func(*KnowledgeSaveRequest){
		func(r *KnowledgeSaveRequest) { r.Title = strings.Repeat("中", 121) },
		func(r *KnowledgeSaveRequest) { r.Summary = strings.Repeat("中", 501) },
		func(r *KnowledgeSaveRequest) { r.Content = strings.Repeat("中", 12001) },
		func(r *KnowledgeSaveRequest) { r.Content = "" },
		func(r *KnowledgeSaveRequest) { r.Category = "unknown" },
		func(r *KnowledgeSaveRequest) { r.Status = "published" },
		func(r *KnowledgeSaveRequest) { r.ID = 1 },
		func(r *KnowledgeSaveRequest) { r.Content = "abc\x00def" },
	} {
		r := valid
		change(&r)
		if _, err := normalizeKnowledgeRequest(r); !errors.Is(err, ErrValidation) {
			t.Fatal("invalid request accepted", err)
		}
	}
	r := valid
	r.Title = strings.Repeat("中", 120)
	r.Summary = strings.Repeat("中", 500)
	r.Content = strings.Repeat("中", 12000)
	if _, err := normalizeKnowledgeRequest(r); err != nil {
		t.Fatal("Unicode rune limits rejected", err)
	}
	for _, r := range []KnowledgeListRequest{{Current: -1}, {Current: 100001}, {PageSize: 101}, {PageSize: -1}, {Query: strings.Repeat("中", 201)}, {Status: "no"}, {Category: "no"}} {
		if _, err := normalizeKnowledgeList(r); !errors.Is(err, ErrValidation) {
			t.Fatal("bad list accepted", r, err)
		}
	}
}

func TestKnowledgeSecretRedactionPreservesTemplates(t *testing.T) {
	for _, text := range []string{
		`TOKEN=$TOKEN`, `API_KEY="${API_KEY}"`, `key='<KEY>'`, `--api-key "$API_KEY"`,
		`https://example.test/install?key=${KEY}`, `https://example.test/install?key=${KEY}&version=2`, `password=REPLACE_ME`, `token=YOUR_TOKEN`,
		`password=[凭据已保护]`, `架构说明：密钥由环境变量提供，不需要复制`,
	} {
		if got := redactKnowledgeText(text); got != text || knowledgeContentHasCredentials(text) {
			t.Fatalf("template modified: %q -> %q", text, got)
		}
	}
	for _, text := range []string{
		`curl https://example.test/install?key=fixture-secret&version=2`,
		`bash install.sh --api-key "fixture-secret" --port 443`,
		`export NODE_KEY='fixture-secret'`, `TOKEN="fixture-secret with spaces"`,
		"-----BEGIN OPENSSH PRIVATE KEY-----\nfixture-secret\n-----END OPENSSH PRIVATE KEY-----",
		`https://admin:fixture-secret@example.test`, `Authorization: Bearer fixture-secret`,
	} {
		got := redactKnowledgeText(text)
		if strings.Contains(got, "fixture-secret") || !knowledgeContentHasCredentials(text) {
			t.Fatalf("secret not removed: %q", got)
		}
		if redactKnowledgeText(got) != got {
			t.Fatalf("redaction not idempotent: %q", got)
		}
	}
}

func TestKnowledgeMetadataAutomaticallyProtectsCredentials(t *testing.T) {
	req := knowledgeFixtureRequest()
	req.Title, req.Summary, req.Content = "部署 key=fixture-secret", "API_KEY=fixture-secret", "API_KEY=fixture-secret"
	got, err := normalizeKnowledgeRequest(req)
	if err != nil || strings.Contains(got.Title, "fixture-secret") || strings.Contains(got.Summary, "fixture-secret") || got.Content != req.Content {
		t.Fatalf("metadata not protected or body modified: %#v %v", got, err)
	}
}

func TestKnowledgeSecretsForResponseRedaction(t *testing.T) {
	text := `API_KEY="fixture-token"; TOKEN=$TOKEN; curl https://example.test/install?key=fixture-url-key&v=2 --password 'fixture-password'`
	secrets := knowledgeTextSecrets(text)
	for _, want := range []string{"fixture-token", "fixture-url-key", "fixture-password"} {
		found := false
		for _, value := range secrets {
			found = found || value == want
			if strings.Contains(value, "$TOKEN") || strings.Contains(value, "&v=2") {
				t.Fatal("placeholder or unrelated query included", value)
			}
		}
		if !found {
			t.Fatal("missing known secret", want)
		}
	}
}

func TestKnowledgePageSecretsProtectMidValueReads(t *testing.T) {
	body := "部署\nAPI_KEY=fixture-secret-value\n完成"
	start := len([]rune("部署\nAPI_KEY=fixture-"))
	end := start + len([]rune("secret"))
	secrets := knowledgePageSecrets(body, start, end)
	for _, want := range []string{"fixture-secret-value", "secret"} {
		found := false
		for _, value := range secrets {
			found = found || value == want
		}
		if !found {
			t.Fatal("missing full or paginated secret", want)
		}
	}
	if got := redactSSHOutput("值是 secret", secrets, true); strings.Contains(got, "secret") {
		t.Fatal("page fragment exposed", got)
	}
}

func TestKnowledgeRepeatedSecretDoesNotLeakViaIndexOrMetadata(t *testing.T) {
	body := "TOKEN=fixture-secret-repeat\necho fixture-secret-repeat"
	if got := redactKnowledgeText(body); strings.Contains(got, "fixture-secret-repeat") {
		t.Fatal("repeated secret indexed", got)
	}
	req := knowledgeFixtureRequest()
	req.Content, req.Title, req.Summary = body, "部署 fixture-secret-repeat", "使用 fixture-secret-repeat 安装"
	got, err := normalizeKnowledgeRequest(req)
	if err != nil || strings.Contains(got.Title+got.Summary, "fixture-secret-repeat") || got.Content != body {
		t.Fatal("metadata leaked secret known from body", err)
	}
}
