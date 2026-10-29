package aiops

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestSSHExitCodeKnownVersusMissingStatus(t *testing.T) {
	for _, name := range []string{"known exit", "missing status"} {
		t.Run(name, func(t *testing.T) {
			host, port, pin, _, execs := testSSHHostWithExec(t, func(ch ssh.Channel) {
				_, _ = io.WriteString(ch, "partial changes\npassword=secret-test\n")
				if name == "known exit" {
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{23}))
				}
				_ = ch.Close()
			})
			s := NewService(config.Config{AppKey: "exit-fixture"}, nil, nil)
			defer s.Close()
			cipher, err := s.encryptSecret("secret-test")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result := s.runSSH(ctx, sshSecretServer{Server: Server{Host: host, Port: port, Username: "root", HostKeySHA256: pin}, PasswordCipher: cipher}, "fixture-change")
			if result.Err == nil || execs.Load() != 1 || strings.Contains(result.Output, "secret-test") || !strings.Contains(result.Output, "partial changes") {
				t.Fatal("SSH fixture result lost detail or retried", result.Err, execs.Load())
			}
			if name == "known exit" {
				if result.Uncertain || result.ExitCode == nil || *result.ExitCode != 23 || !strings.Contains(result.Err.Error(), "命令已结束") || !strings.Contains(result.Err.Error(), "不要原样重跑") {
					t.Fatal("known SSH failure misclassified", result)
				}
			} else if !result.Uncertain || result.ExitCode != nil {
				t.Fatal("missing exit-status incorrectly recoverable", result)
			}
		})
	}
}
