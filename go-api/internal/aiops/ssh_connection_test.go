package aiops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"forest/go-api/internal/config"
)

func TestSSHConnectionClassificationUsesTransportEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
		retry      bool
	}{
		{"eof", "network", io.EOF, true},
		{"short read", "network", io.ErrUnexpectedEOF, true},
		{"wrapped reset", "network", fmt.Errorf("ssh: handshake failed: %w", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), true},
		{"refused", "network", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"deadline", "timeout", &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, true},
		{"temporary DNS", "network", &net.DNSError{Err: "PRIVATE_SENTINEL", IsTemporary: true}, true},
		{"permanent DNS", "setup", &net.DNSError{Err: "PRIVATE_SENTINEL", IsNotFound: true}, false},
		{"pin", "host_key", fmt.Errorf("ssh: handshake failed: %w", errSSHHostKeyMismatch), false},
		{"auth", "authentication", errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password] PRIVATE_SENTINEL"), false},
		{"canceled", "canceled", context.Canceled, false},
		{"text is not transport evidence", "setup", errors.New("PRIVATE_SENTINEL EOF timeout connection reset retry me"), false},
		{"unknown protocol error", "setup", errors.New("PRIVATE_SENTINEL unexpected SSH packet"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySSHConnectionFailure(context.Background(), "handshake", tc.err)
			if got.code != tc.code || got.retryable != tc.retry || strings.Contains(got.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("classification=%+v error=%s", got, got.Error())
			}
			got.attempts = sshConnectionAttempts
			if got.retryable && !strings.Contains(got.Error(), "已尝试 3 次连接") {
				t.Fatal("exhaustion lost connection count", got.Error())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := classifySSHConnectionFailure(ctx, "handshake", io.EOF)
	if got.retryable || got.code != "canceled" {
		t.Fatal("cancellation permitted a reconnect", got)
	}
	closedChannel := errors.New("ssh: unexpected packet in response to channel open: <nil>")
	for _, stage := range []string{"dial", "handshake", "session"} {
		got := classifySSHConnectionFailure(context.Background(), stage, closedChannel)
		if got.retryable != (stage == "session") {
			t.Fatal("closed channel exception escaped pre-exec session stage", stage, got)
		}
	}
}

func TestSSHConnectionGuardBeforeDialAndBeforeExec(t *testing.T) {
	for _, allowDial := range []bool{false, true} {
		t.Run(fmt.Sprint(allowDial), func(t *testing.T) {
			host, port, pin, auth, execs := testSSHHost(t, false)
			s := NewService(config.Config{AppKey: "guard-fixture-key"}, nil, nil)
			defer s.Close()
			password, err := s.encryptSecret("secret-test")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			checks := 0
			result := s.runSSHWithGuard(ctx, sshSecretServer{Server: Server{Host: host, Port: port, Username: "root", HostKeySHA256: pin}, PasswordCipher: password}, "MUST_NOT_EXECUTE", nil, func(context.Context) error {
				checks++
				if allowDial && checks == 1 {
					return nil
				}
				return errors.New("PRIVATE_SENTINEL permission revoked")
			})
			var connectionError *sshConnectionFailure
			if !errors.As(result.Err, &connectionError) || connectionError.code != "authorization" || result.Uncertain || result.ExitCode != nil || execs.Load() != 0 || strings.Contains(result.Err.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("guard bypassed or leaked cause: %+v execs=%d", result, execs.Load())
			}
			wantChecks, wantAuth := 1, int64(0)
			if allowDial {
				wantChecks, wantAuth = 2, 1
			}
			if checks != wantChecks || auth.Load() != wantAuth {
				t.Fatalf("checks=%d auth=%d", checks, auth.Load())
			}
		})
	}
}
