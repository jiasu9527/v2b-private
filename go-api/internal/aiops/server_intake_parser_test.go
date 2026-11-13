package aiops

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSSHIntakeBatchFormats(t *testing.T) {
	const suffix = "\n帮我添加这两台服务器后检查服务"
	for name, body := range map[string]string{
		"bare contiguous": "192.0.2.10\nroot\nfirst-fixture\n192.0.2.11:2222\nadmin\nsecond-fixture",
		"bare numbered":   "1. 192.0.2.10\nroot\nfirst-fixture\n2) 192.0.2.11:2222\nadmin\nsecond-fixture",
		"named blocks":    "名称：香港一\n地址：192.0.2.10\n用户名：root\n密码：first-fixture\n\n名称：美国二\n地址：192.0.2.11\n端口：2222\n账号：admin\n密码：second-fixture",
		"inline":          "192.0.2.10 root first-fixture\n192.0.2.11 2222 admin second-fixture",
		"inline numbered": "1) 192.0.2.10 root first-fixture\n2. 192.0.2.11:2222 admin second-fixture",
		"space labels":    "host 192.0.2.10\nusername root\npassword first-fixture\n地址 192.0.2.11:2222\n用户 admin\n密码 second-fixture",
		"JSON":            `[{"name":"香港一","ip":"192.0.2.10","username":"root","password":"first-fixture"},{"host":"192.0.2.11","port":"2222","user":"admin","password":"second-fixture"}]`,
		"markdown table":  "| 名称 | IP | 端口 | 用户名 | 密码 |\n| --- | --- | --- | --- | --- |\n| 香港一 | 192.0.2.10 | 22 | root | first-fixture |\n| 美国二 | 192.0.2.11 | 2222 | admin | second-fixture |",
		"TSV":             "用户名\t密码\tIP\t端口\nroot\tfirst-fixture\t192.0.2.10\t22\nadmin\tsecond-fixture\t192.0.2.11\t2222",
		"vertical tables": "| 主机 | 192.0.2.10 |\n| 用户 | root |\n| 密码 | first-fixture |\n\n| 主机 | 192.0.2.11:2222 |\n| 用户 | admin |\n| 密码 | second-fixture |",
	} {
		t.Run(name, func(t *testing.T) {
			safe, records, err := ParseSSHRegistrationInput(body + suffix)
			if err != nil || len(records) != 2 {
				t.Fatalf("expected two records: count=%d err=%v", len(records), err)
			}
			if records[0].Host != "192.0.2.10" || records[0].Username != "root" || records[0].Port != 22 || records[0].Password != "first-fixture" || records[1].Host != "192.0.2.11" || records[1].Username != "admin" || records[1].Port != 2222 || records[1].Password != "second-fixture" {
				t.Fatal("record association or credential bytes changed")
			}
			if strings.Contains(safe, "first-fixture") || strings.Contains(safe, "second-fixture") || !strings.Contains(safe, strings.TrimSpace(suffix)) || strings.Count(safe, sshIntakeNotice) != 1 {
				t.Fatal("unsafe or incomplete model-bound task")
			}
		})
	}
}

func TestSSHIntakeBatchPreservesGroupsAcrossFormats(t *testing.T) {
	for name, body := range map[string]string{
		"JSON":           `[{"name":"东京一","group":"日本线路","host":"192.0.2.10","username":"root","password":"fixture-one"},{"name":"香港二","group_name":"香港线路","host":"192.0.2.11","username":"root","password":"fixture-two"}]`,
		"markdown table": "| 名称 | 分组 | IP | 用户名 | 密码 |\n| --- | --- | --- | --- | --- |\n| 东京一 | 日本线路 | 192.0.2.10 | root | fixture-one |\n| 香港二 | 香港线路 | 192.0.2.11 | root | fixture-two |",
		"named blocks":   "名称: 东京一\n分组: 日本线路\n地址: 192.0.2.10\n用户名: root\n密码: fixture-one\n\n名称: 香港二\n分组: 香港线路\n地址: 192.0.2.11\n用户名: root\n密码: fixture-two",
	} {
		t.Run(name, func(t *testing.T) {
			safe, records, err := ParseSSHRegistrationInput(body)
			if err != nil || len(records) != 2 || records[0].Group != "日本线路" || records[1].Group != "香港线路" {
				t.Fatalf("groups lost: %+v err=%v", records, err)
			}
			if strings.Contains(safe, "fixture-one") || strings.Contains(safe, "fixture-two") {
				t.Fatal("model-bound text contains credentials")
			}
		})
	}
}

func TestSSHIntakeBatchSecretPreservation(t *testing.T) {
	for name, tt := range map[string]struct{ body, secret string }{
		"bare IP password":             {"192.0.2.10\nroot\n198.51.100.55", "198.51.100.55"},
		"literal list password":        {"192.0.2.10\nroot\n-abc!$", "-abc!$"},
		"explicit next line":           {"host: 192.0.2.10\nusername: root\npassword:\n  [fixture]  ", "  [fixture]  "},
		"inline space and punctuation": {"192.0.2.10 root fixture! $\\with'space  ", "fixture! $\\with'space  "},
		"JSON whitespace":              {`{"host":"192.0.2.10","username":"root","password":"  fixture\tvalue  "}`, "  fixture\tvalue  "},
		"TSV whitespace":               {"IP\t用户名\t密码\n192.0.2.10\troot\t  fixture  ", "  fixture  "},
		"markdown whitespace":          {"| IP | 用户名 | 密码 |\n|192.0.2.10|root|   fixture   |", "  fixture  "},
	} {
		t.Run(name, func(t *testing.T) {
			safe, records, err := ParseSSHRegistrationInput(tt.body + "\n192.0.2.11\nadmin\nsecond-fixture")
			if err != nil || len(records) != 2 {
				t.Fatalf("count=%d err=%v", len(records), err)
			}
			if records[0].Password != tt.secret || records[1].Password != "second-fixture" || strings.Contains(safe, tt.secret) {
				t.Fatal("credential changed or exposed")
			}
		})
	}
}

func TestSSHIntakeBatchRejectsMalformedAndCrossRecordSecrets(t *testing.T) {
	first := "192.0.2.10\nroot\nfirst-fixture\n"
	for name, body := range map[string]string{
		"bare invalid username":            "192.0.2.10\nbad user\nfirst-fixture",
		"bare invalid username port":       "192.0.2.10\n22\nbad user\nfirst-fixture",
		"incomplete second":                first + "192.0.2.11\nroot",
		"missing first bare password":      "192.0.2.10\nroot\n192.0.2.11\nadmin\nsecond-fixture",
		"missing first password":           "host: 192.0.2.10\nusername: root\nhost: 192.0.2.11\nusername: admin\npassword: second-fixture",
		"missing first username":           "host: 192.0.2.10\npassword: first-fixture\nhost: 192.0.2.11\nusername: root\npassword: second-fixture",
		"missing second username":          first + "host: 192.0.2.11\npassword: second-fixture",
		"cross name":                       first + "name: first-fixture server\nhost: 192.0.2.11\nusername: root\npassword: second-fixture",
		"cross group":                      first + "group: first-fixture\nhost: 192.0.2.11\nusername: root\npassword: second-fixture",
		"cross user":                       first + "host: 192.0.2.11\nusername: first-fixture\npassword: second-fixture",
		"duplicate server":                 first + "192.0.2.10\nroot\nsecond-fixture",
		"JSON duplicate":                   `{"host":"192.0.2.10","ip":"192.0.2.11","user":"root","password":"first-fixture"}`,
		"JSON unknown field":               `{"host":"192.0.2.10","user":"root","password":"first-fixture","extra":"second-fixture"}`,
		"embedded escaped JSON":            `请添加 {"host":"192.0.2.10","user":"root","pass\u0077ord":"first-fixture"}`,
		"embedded incomplete escaped JSON": `请添加 {"host":"192.0.2.10","user":"root","pass\u0077ord":"first-fixture"`,
		"empty quoted password":            `{"host":"192.0.2.10","user":"root","password":""}` + "\n192.0.2.11\nroot\nsecond-fixture",
		"malformed escaped JSON":           `{"host":"192.0.2.10","user":"root","pass\u0077ord":"first-fixture"`,
		"malformed JSON":                   first + `{"host":"192.0.2.11","user":"root","password":"second-fixture"`,
		"broken table":                     "|IP|用户名|密码|\n|192.0.2.10|root|first-fixture|\n|192.0.2.11|root|",
		"conflicting inline port":          "192.0.2.10:22 2222 root first-fixture",
	} {
		t.Run(name, func(t *testing.T) {
			safe, inputs, err := ParseSSHRegistrationInput(body)
			if !errors.Is(err, ErrValidation) || safe != "" || len(inputs) != 0 {
				t.Fatal("malformed batch escaped local validation")
			}
			if strings.Contains(err.Error(), "fixture") {
				t.Fatal("error included secret")
			}
		})
	}
}

func TestSSHIntakeBatchKeysAndGlobalRedaction(t *testing.T) {
	keyA, keyB := intakeTestKey(t, ""), intakeTestKey(t, "second-phrase")
	body := "host: 192.0.2.10\nuser: root\n" + keyA + "\nhost: 192.0.2.11\nuser: admin\n" + keyB + "\npassphrase: second-phrase\n请检查 second-phrase 是否可以使用"
	safe, records, err := ParseSSHRegistrationInput(body)
	if err != nil || len(records) != 2 || records[0].PrivateKey != keyA || records[1].PrivateKey != keyB || records[1].KeyPassphrase != "second-phrase" || strings.Contains(safe, "second-phrase") || strings.Contains(safe, "PRIVATE KEY") {
		t.Fatalf("batch private keys failed: count=%d err=%v", len(records), err)
	}
	body = "192.0.2.10\nroot\nfirst-fixture\n请记住 second-fixture\n192.0.2.11\nroot\nsecond-fixture\n请检查 first-fixture"
	safe, records, err = ParseSSHRegistrationInput(body)
	if err != nil || len(records) != 2 || strings.Contains(safe, "fixture") {
		t.Fatalf("cross-record task redaction failed: count=%d err=%v", len(records), err)
	}
}

func TestSSHIntakeBatchLimit(t *testing.T) {
	var bodies []string
	for i := 1; i <= maxSSHIntakeBatchSize; i++ {
		bodies = append(bodies, fmt.Sprintf("192.0.2.%d root fixture-secret-%d", i, i))
	}
	if _, records, err := ParseSSHRegistrationInput(strings.Join(bodies, "\n")); err != nil || len(records) != maxSSHIntakeBatchSize {
		t.Fatalf("max batch failed: count=%d err=%v", len(records), err)
	}
	bodies = append(bodies, "192.0.2.200 root fixture-extra")
	if safe, records, err := ParseSSHRegistrationInput(strings.Join(bodies, "\n")); !errors.Is(err, ErrValidation) || safe != "" || len(records) != 0 {
		t.Fatal("over-limit batch was not rejected atomically")
	}
	jsonBodies := make([]map[string]string, 51)
	for i := range jsonBodies {
		jsonBodies[i] = map[string]string{"host": fmt.Sprintf("192.0.2.%d", i+1), "user": "root", "password": "fixture-json"}
	}
	raw, _ := json.Marshal(jsonBodies)
	if _, _, err := ParseSSHRegistrationInput(string(raw)); !errors.Is(err, ErrValidation) {
		t.Fatal("JSON limit was not enforced")
	}
}

func TestSSHIntakeNormalizationRegression(t *testing.T) {
	for name, body := range map[string]string{
		"trailing name":      "host: 192.0.2.10\nuser: root\npassword: first-fixture  \nname: 香港",
		"space label suffix": "host 192.0.2.10\nuser root\npassword first-fixture  ",
		"escaped JSON key":   `{"ho\u0073t":"192.0.2.10","user":"root","pass\u0077ord":"first-fixture  "}`,
	} {
		t.Run(name, func(t *testing.T) {
			safe, inputs, err := ParseSSHRegistrationInput(body)
			if err != nil || len(inputs) != 1 || inputs[0].Password != "first-fixture  " || strings.Contains(safe, "first-fixture") {
				t.Fatalf("normalization regression: count=%d err=%v", len(inputs), err)
			}
		})
	}
}

func TestSSHIntakeBatchErrorIndex(t *testing.T) {
	_, _, err := ParseSSHRegistrationInput("192.0.2.10\nroot\nfirst-fixture\nhost: 192.0.2.11\npassword: second-fixture")
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "第 2 条") || strings.Contains(err.Error(), "fixture") {
		t.Fatalf("missing safe batch index: %v", err)
	}
}

func TestSSHIntakeRejectsMalformedBarePortsLocally(t *testing.T) {
	for _, body := range []string{
		"192.0.2.10:70000\nroot\nsecret-fixture",
		"192.0.2.20\nroot\n192.0.2.10:70000\nroot\nsecret-fixture",
		"192.0.2.10:notaport root secret-fixture",
		"192.0.2.10:-1 root secret-fixture",
		"[2001:db8::10]:notaport root secret-fixture",
		"192.0.2.10:70000\n22\nroot\nsecret-fixture",
		"192.0.2.20 root valid-fixture\n192.0.2.10:70000\nroot\nsecret-fixture",
		"192.0.2.20 root valid-fixture\n192.0.2.10:notaport root secret-fixture",
	} {
		safe, inputs, err := ParseSSHRegistrationInput(body)
		if !errors.Is(err, ErrValidation) || safe != "" || len(inputs) != 0 {
			t.Fatal("invalid endpoint credentials escaped local validation")
		}
	}
	for _, task := range []string{
		"192.0.2.10:70000 please inspect ports",
		"192.0.2.10:notaport\n请检查地址格式",
		"curl https://192.0.2.10:70000/status",
	} {
		safe, inputs, err := ParseSSHRegistrationInput(task)
		if err != nil || len(inputs) != 0 || safe != task {
			t.Fatal("ordinary malformed endpoint task changed")
		}
	}
}

func TestSSHIntakeErrorMessageNeverReflectsArbitraryWrapper(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("secret-fixture: %w", ErrSSHIntakeInvalid),
		fmt.Errorf("secret-fixture: %w", errSSHIntakeTooMany),
		fmt.Errorf("secret-fixture: %w", sshIntakeRecordError(2, ErrSSHIntakeInvalid)),
	} {
		message, ok := SSHIntakeErrorMessage(err)
		if !ok || strings.Contains(message, "secret-fixture") {
			t.Fatal("error message reflected input")
		}
	}
	message, ok := SSHIntakeErrorMessage(sshIntakeRecordError(2, ErrSSHIntakeMultiple))
	if !ok || !strings.Contains(message, "第 2 条") {
		t.Fatal("record index missing")
	}
	if _, ok := SSHIntakeErrorMessage(fmt.Errorf("secret-fixture: %w", ErrValidation)); ok {
		t.Fatal("generic validation error trusted")
	}
}
