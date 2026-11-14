package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sshAIResponse(t *testing.T, items ...sshAIExtracted) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Items []sshAIExtracted `json:"items"`
	}{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSSHIntakeNeedsAIFreeFormRegistration(t *testing.T) {
	for _, body := range []string{
		"这几台机器帮我录入：美国 192.0.2.10 root 密码 abc-fixture",
		"192.0.2.10 这台的登录密码是 abc-fixture，用户 root",
		"把这些服务器整理一下：192.0.2.10 root abc-fixture",
		"把这些服务器整理一下：host: first.example.test user: root password: abc-fixture",
		"请登记 edge.example.test root secret-fixture",
		"edge.example.test root 密码：secret-fixture",
		"请登记 地址：2001:db8::10 用户：root 密码：secret-fixture",
		"美国 192.0.2.10 ops secret-fixture",
		"法国 edge.example.test ops secret-fixture",
	} {
		if !sshIntakeNeedsAI(body) {
			t.Fatalf("free-form SSH paste not routed to AI: %q", body)
		}
	}
	for _, body := range []string{
		"ssh root@192.0.2.10 安装服务",
		"curl https://192.0.2.10/status",
		"请帮我整理服务器故障，192.0.2.10 无法连接",
		"保存这个安装脚本到知识库：\n```sh\nPASSWORD=fixture\nHOST=192.0.2.10\n```",
	} {
		if sshIntakeNeedsAI(body) {
			t.Fatalf("ordinary task routed to SSH AI extractor: %q", body)
		}
	}
}

func TestSSHIntakeAIOnlyExtractionRequestSeesRawCredential(t *testing.T) {
	original := "这些机器帮我录入，第一台是 192.0.2.10 的 root，密码：alpha-fixture；第二台是 192.0.2.11 的 admin，密码：beta-fixture"
	first := "第一台是 192.0.2.10 的 root，密码：alpha-fixture"
	second := "第二台是 192.0.2.11 的 admin，密码：beta-fixture"
	var requests int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 2 || req.Messages[1].Content != original || len(req.Tools) != 0 || req.Stream {
			t.Error("one-off extraction request did not contain exact paste without operational tools")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		response := sshAIResponse(t,
			sshAIExtracted{SourceQuote: first, Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22},
			sshAIExtracted{SourceQuote: second, Host: "192.0.2.11", Username: "admin", Password: "beta-fixture", Port: 22},
		)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": response}}}})
	}))
	defer relay.Close()
	s, mock := streamTestService(t, relay.URL)
	safe, inputs, err := s.prepareSSHChatInput(context.Background(), "gpt-6.1-sol", original)
	if err != nil || requests != 1 || len(inputs) != 2 || inputs[0].Password != "alpha-fixture" || inputs[1].Password != "beta-fixture" {
		t.Fatalf("free-form extraction failed: requests=%d count=%d err=%v", requests, len(inputs), err)
	}
	if strings.Contains(safe, "alpha-fixture") || strings.Contains(safe, "beta-fixture") || !strings.Contains(safe, "请登记") {
		t.Fatal("secret escaped into ordinary conversation or intake receipt missing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHIntakeAIRetriesInvalidModelResponseOnce(t *testing.T) {
	original := "帮我录入：192.0.2.10 的 root，密码：alpha-fixture"
	requests := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Tools) != 0 || req.Messages[1].Content != original {
			t.Error("retry changed extraction request or exposed tools")
			w.WriteHeader(400)
			return
		}
		response := `{"items":[]}`
		if requests == 2 {
			response = sshAIResponse(t, sshAIExtracted{Host: "192.0.2.10", Port: 22, Username: "root", Password: "alpha-fixture"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": response}}}})
	}))
	defer relay.Close()
	s, mock := streamTestService(t, relay.URL)
	_, inputs, err := s.prepareSSHChatInput(context.Background(), "gpt-6.1-sol", original)
	if err != nil || requests != 2 || len(inputs) != 1 {
		t.Fatalf("invalid model response not retried: requests=%d count=%d err=%v", requests, len(inputs), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHIntakeAIRecoversOrdinaryModelJSONWithoutExactQuote(t *testing.T) {
	original := "第一个：192.0.2.10 root 密码：alpha-fixture\n第二个：192.0.2.11 admin 密码：beta-fixture"
	response := "```JSON\n" + `{"items":[{"host":"192.0.2.10","port":"22","username":"root","password":"alpha-fixture","source_quote":"近似引用","name":"模型自造名称","note":"extra"},{"host":"192.0.2.11","port":22,"username":"admin","password":"beta-fixture"}]}` + "\n```"
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Password != "alpha-fixture" || inputs[1].Password != "beta-fixture" || inputs[0].Name != "192.0.2.10" {
		t.Fatalf("normal model JSON rejected or inferred metadata saved: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIRecoversSemicolonSeparatedItemsWithoutQuote(t *testing.T) {
	original := "美国 192.0.2.10 root 密码：alpha-fixture；日本 192.0.2.11 admin 密码：beta-fixture"
	response := sshAIResponse(t,
		sshAIExtracted{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22},
		sshAIExtracted{Host: "192.0.2.11", Username: "admin", Password: "beta-fixture", Port: 22},
	)
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Host != "192.0.2.10" || inputs[1].Host != "192.0.2.11" {
		t.Fatalf("semicolon-separated paste rejected: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIRecoversAdjacentOneLineItemsWithoutQuote(t *testing.T) {
	original := "第一台 192.0.2.10 root 密码：alpha-fixture 第二台 192.0.2.11 admin 密码：beta-fixture"
	response := sshAIResponse(t,
		sshAIExtracted{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22},
		sshAIExtracted{Host: "192.0.2.11", Username: "admin", Password: "beta-fixture", Port: 22},
	)
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Password != "alpha-fixture" || inputs[1].Password != "beta-fixture" {
		t.Fatalf("adjacent one-line records rejected: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIRecoversParagraphRecordsWithoutQuote(t *testing.T) {
	original := "第一台服务器\n地址 192.0.2.10\n账号 root\n密码 alpha-fixture\n\n第二台服务器\n地址 192.0.2.11\n账号 admin\n密码 beta-fixture"
	response := sshAIResponse(t,
		sshAIExtracted{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22},
		sshAIExtracted{Host: "192.0.2.11", Username: "admin", Password: "beta-fixture", Port: 22},
	)
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Password != "alpha-fixture" || inputs[1].Password != "beta-fixture" {
		t.Fatalf("paragraph batch rejected: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIRecoversContinuousMultilineRecordsWithoutQuote(t *testing.T) {
	original := "第一台服务器\n地址 192.0.2.10\n账号 root\n密码 alpha-fixture\n第二台服务器\n地址 192.0.2.11\n账号 admin\n密码 beta-fixture"
	response := sshAIResponse(t,
		sshAIExtracted{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22},
		sshAIExtracted{Host: "192.0.2.11", Username: "admin", Password: "beta-fixture", Port: 22},
	)
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Password != "alpha-fixture" || inputs[1].Password != "beta-fixture" {
		t.Fatalf("continuous multiline batch rejected: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIDoesNotTreatContextAddressAsMissingServer(t *testing.T) {
	original := "从管理机 192.0.2.200 发起登记，目标：192.0.2.10 root 密码：alpha-fixture"
	response := sshAIResponse(t, sshAIExtracted{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22})
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 1 || inputs[0].Host != "192.0.2.10" {
		t.Fatalf("context address incorrectly counted as another server: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAINoQuoteRejectsOmittedOrMisassociatedServer(t *testing.T) {
	for name, tt := range map[string]struct {
		original string
		items    []sshAIExtracted
	}{
		"omitted second bare record": {
			original: "192.0.2.10 root alpha-fixture\n192.0.2.11 admin beta-fixture",
			items:    []sshAIExtracted{{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22}},
		},
		"omitted second labeled record": {
			original: "第一台 192.0.2.10 root 密码：alpha-fixture；第二台 192.0.2.11 admin 密码：beta-fixture",
			items:    []sshAIExtracted{{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22}},
		},
		"second password attributed to first": {
			original: "第一台 192.0.2.10 root 密码：alpha-fixture；第二台 192.0.2.11 admin 密码：beta-fixture",
			items: []sshAIExtracted{
				{Host: "192.0.2.10", Username: "root", Password: "beta-fixture", Port: 22},
				{Host: "192.0.2.11", Username: "admin", Password: "alpha-fixture", Port: 22},
			},
		},
		"second username attributed to first": {
			original: "第一台 192.0.2.10 root 密码：alpha-fixture；第二台 192.0.2.11 admin 密码：beta-fixture",
			items: []sshAIExtracted{
				{Host: "192.0.2.10", Username: "admin", Password: "alpha-fixture", Port: 22},
				{Host: "192.0.2.11", Username: "root", Password: "beta-fixture", Port: 22},
			},
		},
		"claimed host is prefix of source address": {
			original: "服务器 192.0.2.10 root 密码：alpha-fixture",
			items:    []sshAIExtracted{{Host: "192.0.2.1", Username: "root", Password: "alpha-fixture", Port: 22}},
		},
		"omitted second bare identity with password on next line": {
			original: "192.0.2.10 root alpha-fixture\n192.0.2.11 admin\nbeta-fixture",
			items:    []sshAIExtracted{{Host: "192.0.2.10", Username: "root", Password: "alpha-fixture", Port: 22}},
		},
		"two labeled credentials for one host": {
			original: "服务器 192.0.2.10 用户 root 密码：alpha-fixture；用户 admin 密码：beta-fixture",
			items:    []sshAIExtracted{{Host: "192.0.2.10", Username: "root", Password: "beta-fixture", Port: 22}},
		},
		"two bare credentials for one host": {
			original: "192.0.2.10 root alpha-fixture admin beta-fixture",
			items:    []sshAIExtracted{{Host: "192.0.2.10", Username: "root", Password: "beta-fixture", Port: 22}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseSSHExtractionJSON(sshAIResponse(t, tt.items...), tt.original)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("unsafe model extraction was accepted: %v", err)
			}
			if strings.Contains(err.Error(), "fixture") {
				t.Fatal("validation error reflected a credential")
			}
		})
	}
}

func TestSSHIntakeAIExtractionValidatesRepeatedHostsAndSecrets(t *testing.T) {
	original := "两台同 IP 不同账号：\nhost: 192.0.2.10\nuser: root\npassword: shared-fixture\nhost: 192.0.2.10\nuser: admin\npassword: shared-fixture"
	response := sshAIResponse(t,
		sshAIExtracted{SourceQuote: "host: 192.0.2.10\nuser: root\npassword: shared-fixture", Host: "192.0.2.10", Port: 22, Username: "root", Password: "shared-fixture"},
		sshAIExtracted{SourceQuote: "host: 192.0.2.10\nuser: admin\npassword: shared-fixture", Host: "192.0.2.10", Port: 22, Username: "admin", Password: "shared-fixture"},
	)
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Username != "root" || inputs[1].Username != "admin" {
		t.Fatalf("valid repeated host/secret rejected: count=%d err=%v", len(inputs), err)
	}

	// A copied address inside a password is data, not a third server.
	original = "host: 192.0.2.10\nusername: root\npassword: 198.51.100.9\nhost: 192.0.2.11\nusername: admin\npassword: second-fixture"
	response = sshAIResponse(t,
		sshAIExtracted{SourceQuote: "host: 192.0.2.10\nusername: root\npassword: 198.51.100.9", Host: "192.0.2.10", Port: 22, Username: "root", Password: "198.51.100.9"},
		sshAIExtracted{SourceQuote: "host: 192.0.2.11\nusername: admin\npassword: second-fixture", Host: "192.0.2.11", Port: 22, Username: "admin", Password: "second-fixture"},
	)
	inputs, err = parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Password != "198.51.100.9" {
		t.Fatalf("IP-looking password mistaken for server: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIExtractionAcceptsDomainAndIPv6WithoutFixedFieldOrder(t *testing.T) {
	original := "第一台 用户 root 密码：first-fixture 地址：edge.example.test\n第二台 用户 admin 密码：second-fixture 地址：2001:db8::11"
	response := sshAIResponse(t,
		sshAIExtracted{SourceQuote: "第一台 用户 root 密码：first-fixture 地址：edge.example.test", Host: "edge.example.test", Port: 22, Username: "root", Password: "first-fixture"},
		sshAIExtracted{SourceQuote: "第二台 用户 admin 密码：second-fixture 地址：2001:db8::11", Host: "2001:db8::11", Port: 22, Username: "admin", Password: "second-fixture"},
	)
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 2 || inputs[0].Host != "edge.example.test" || inputs[1].Host != "2001:db8::11" {
		t.Fatalf("domain/IPv6 free-order extraction failed: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIExtractionAcceptsCompletePrivateKey(t *testing.T) {
	key := intakeTestKey(t, "")
	original := "地址：192.0.2.10\n用户：root\n" + key
	response := sshAIResponse(t, sshAIExtracted{SourceQuote: original, Host: "192.0.2.10", Port: 22, Username: "root", PrivateKey: key})
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 1 || inputs[0].PrivateKey != key {
		t.Fatalf("complete private key extraction failed: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAIExtractionAcceptsPasswordBeforeUsername(t *testing.T) {
	original := "先记密码 first-fixture，再记用户 root，地址 192.0.2.10"
	response := sshAIResponse(t, sshAIExtracted{SourceQuote: original, Host: "192.0.2.10", Port: 22, Username: "root", Password: "first-fixture"})
	inputs, err := parseSSHExtractionJSON(response, original)
	if err != nil || len(inputs) != 1 || inputs[0].Password != "first-fixture" {
		t.Fatalf("password-before-user extraction failed: count=%d err=%v", len(inputs), err)
	}
}

func TestSSHIntakeAICountsAdjacentBareDomains(t *testing.T) {
	original := "first.example.test root first-fixture second.example.test admin second-fixture"
	response := sshAIResponse(t, sshAIExtracted{SourceQuote: "first.example.test root first-fixture", Host: "first.example.test", Port: 22, Username: "root", Password: "first-fixture"})
	if _, err := parseSSHExtractionJSON(response, original); !errors.Is(err, ErrValidation) {
		t.Fatal("adjacent second domain silently omitted")
	}
}

func TestSSHIntakeAIExtractionRejectsOmissionsAndWrongAssociations(t *testing.T) {
	for name, tt := range map[string]struct {
		original string
		items    []sshAIExtracted
	}{
		"omitted IPv4": {
			original: "host: 192.0.2.10\nuser: root\npassword: first-fixture\nhost: 192.0.2.11\nuser: admin\npassword: second-fixture",
			items:    []sshAIExtracted{{SourceQuote: "host: 192.0.2.10\nuser: root\npassword: first-fixture", Host: "192.0.2.10", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"omitted domain": {
			original: "host: first.example.test\nuser: root\npassword: first-fixture\nhost: second.example.test\nuser: admin\npassword: second-fixture",
			items:    []sshAIExtracted{{SourceQuote: "host: first.example.test\nuser: root\npassword: first-fixture", Host: "first.example.test", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"omitted bare domain": {
			original: "first.example.test root first-fixture\nsecond.example.test admin second-fixture",
			items:    []sshAIExtracted{{SourceQuote: "first.example.test root first-fixture", Host: "first.example.test", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"omitted IPv6": {
			original: "host: 2001:db8::10\nuser: root\npassword: first-fixture\nhost: 2001:db8::11\nuser: admin\npassword: second-fixture",
			items:    []sshAIExtracted{{SourceQuote: "host: 2001:db8::10\nuser: root\npassword: first-fixture", Host: "2001:db8::10", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"omitted repeated host": {
			original: "host: first.example.test\nuser: root\npassword: first-fixture\nhost: first.example.test\nuser: admin\npassword: second-fixture",
			items:    []sshAIExtracted{{SourceQuote: "host: first.example.test\nuser: root\npassword: first-fixture", Host: "first.example.test", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"omitted repeated bare IP": {
			original: "192.0.2.10 root first-fixture\n192.0.2.10 admin second-fixture",
			items:    []sshAIExtracted{{SourceQuote: "192.0.2.10 root first-fixture", Host: "192.0.2.10", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"borrowed first password": {
			original: "host: 192.0.2.10\nuser: root\npassword: first-fixture\nhost: 192.0.2.11\nuser: admin\npassword: second-fixture",
			items: []sshAIExtracted{
				{SourceQuote: "host: 192.0.2.10\nuser: root\npassword: first-fixture", Host: "192.0.2.10", Port: 22, Username: "root", Password: "first-fixture"},
				{SourceQuote: "host: 192.0.2.11\nuser: admin\npassword: second-fixture", Host: "192.0.2.11", Port: 22, Username: "admin", Password: "first-fixture"},
			},
		},
		"swapped usernames": {
			original: "host: 192.0.2.10\nuser: root\npassword: first-fixture\nhost: 192.0.2.11\nuser: admin\npassword: second-fixture",
			items: []sshAIExtracted{
				{SourceQuote: "host: 192.0.2.10\nuser: root\npassword: first-fixture", Host: "192.0.2.10", Port: 22, Username: "admin", Password: "first-fixture"},
				{SourceQuote: "host: 192.0.2.11\nuser: admin\npassword: second-fixture", Host: "192.0.2.11", Port: 22, Username: "root", Password: "second-fixture"},
			},
		},
		"guessed unrelated port": {
			original: "部署批次 2222，服务器 192.0.2.10 root 密码：first-fixture",
			items:    []sshAIExtracted{{SourceQuote: "部署批次 2222，服务器 192.0.2.10 root 密码：first-fixture", Host: "192.0.2.10", Port: 2222, Username: "root", Password: "first-fixture"}},
		},
		"truncated username": {
			original: "服务器 192.0.2.10 rootx 密码：first-fixture",
			items:    []sshAIExtracted{{SourceQuote: "服务器 192.0.2.10 rootx 密码：first-fixture", Host: "192.0.2.10", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"truncated password": {
			original: "服务器 192.0.2.10 root 密码：first-fixture-suffix",
			items:    []sshAIExtracted{{SourceQuote: "服务器 192.0.2.10 root 密码：first-fixture-suffix", Host: "192.0.2.10", Port: 22, Username: "root", Password: "first-fixture"}},
		},
		"borrowed username as password": {
			original: "服务器 192.0.2.10 root 密码：first-fixture",
			items:    []sshAIExtracted{{SourceQuote: "服务器 192.0.2.10 root 密码：first-fixture", Host: "192.0.2.10", Port: 22, Username: "root", Password: "root"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseSSHExtractionJSON(sshAIResponse(t, tt.items...), tt.original)
			if !errors.Is(err, ErrValidation) || strings.Contains(err.Error(), "fixture") {
				t.Fatalf("unsafe AI extraction accepted or reflected a secret: %v", err)
			}
		})
	}
}
