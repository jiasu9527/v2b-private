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
		if _, err := normalizeKnowledgeRequest(r); !errors.Is(err, ErrKnowledgeCredential) {
			t.Fatal("credential accepted", secret, err)
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
