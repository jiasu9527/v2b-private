package aiops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"golang.org/x/crypto/ssh"
)

// This fixture accepts real TCP/SSH connections but never invokes a shell.
// Connection failures are selected by attempt number; only an SSH exec request
// is counted as dispatch, independently of authentication or channel creation.
type sshReconnectFixture struct {
	host        string
	port        int
	pin         string
	connections atomic.Int64
	auth        atomic.Int64
	mu          sync.Mutex
	commands    []string
}

func newSSHReconnectFixture(t *testing.T, behavior func(int64) string) *sshReconnectFixture {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	f := &sshReconnectFixture{host: "127.0.0.1", pin: ssh.FingerprintSHA256(signer.PublicKey())}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		f.auth.Add(1)
		if string(password) != "retry-secret-fixture" {
			return nil, errors.New("PRIVATE_AUTH_SENTINEL")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.port = listener.Addr().(*net.TCPAddr).Port
	var mu sync.Mutex
	active := map[net.Conn]bool{}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			attempt := f.connections.Add(1)
			mu.Lock()
			active[conn] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					_ = conn.Close()
					mu.Lock()
					delete(active, conn)
					mu.Unlock()
				}()
				mode := behavior(attempt)
				switch mode {
				case "eof":
					return
				case "reset":
					_ = conn.(*net.TCPConn).SetLinger(0)
					return
				case "timeout":
					// Do not send a banner; remain until the client closes after
					// its handshake deadline. No fixed sleep prolongs cleanup.
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				connectionConfig := *serverConfig
				if mode == "protocol" {
					// A supported legacy algorithm excluded from the production
					// client's defaults gives a real negotiation failure, not EOF.
					connectionConfig.Config.KeyExchanges = []string{"diffie-hellman-group1-sha1"}
				}
				server, channels, requests, err := ssh.NewServerConn(conn, &connectionConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if mode == "session_disconnect" {
						return // client.NewSession has not obtained a channel yet.
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						var payload struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil {
							_ = request.Reply(false, nil)
							continue
						}
						f.mu.Lock()
						f.commands = append(f.commands, payload.Command)
						f.mu.Unlock()
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(channel, "fixture command output\npassword=retry-secret-fixture\n")
						if mode == "exec_disconnect" {
							return // A dispatched command must never be replayed.
						}
						code := uint32(0)
						if mode == "exec_nonzero" {
							code = 7
						}
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
						_ = channel.Close()
						return
					}
					_ = channel.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for conn := range active {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return f
}

func (f *sshReconnectFixture) sentCommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func sshReconnectService(t *testing.T, f *sshReconnectFixture, password string) (*Service, sshSecretServer) {
	t.Helper()
	s := NewService(config.Config{AppKey: "ssh-reconnect-fixture"}, nil, nil)
	t.Cleanup(s.Close)
	cipher, err := s.encryptSecret(password)
	if err != nil {
		t.Fatal(err)
	}
	return s, sshSecretServer{Server: Server{Host: f.host, Port: f.port, Username: "root", HostKeySHA256: f.pin}, PasswordCipher: cipher}
}

func assertSSHReconnectSafeText(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{"retry-secret-fixture", "wrong-secret-fixture", "PRIVATE_AUTH_SENTINEL", "PRIVATE_REMOTE_SENTINEL", "read tcp", "write tcp", "EOF", "connection reset by peer"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("raw SSH error or credential exposed: %q", text)
		}
	}
}

func TestSSHConnectionRetryBeforeExecOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures []string
	}{
		{"handshake EOF", []string{"eof"}},
		{"two transient failures", []string{"eof", "reset"}},
		{"session channel disconnected before exec", []string{"session_disconnect"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSSHReconnectFixture(t, func(attempt int64) string {
				if attempt <= int64(len(tc.failures)) {
					return tc.failures[attempt-1]
				}
				return "success"
			})
			s, server := sshReconnectService(t, f, "retry-secret-fixture")
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			var mu sync.Mutex
			var progress []string
			result := s.runSSHWithProgress(ctx, server, "fixture-one-command", func(text string) {
				mu.Lock()
				progress = append(progress, text)
				mu.Unlock()
			})
			if result.Err != nil || result.Uncertain || result.ExitCode != nil || f.connections.Load() != int64(len(tc.failures)+1) || !reflect.DeepEqual(f.sentCommands(), []string{"fixture-one-command"}) {
				t.Fatalf("retry did not preserve a single dispatch: result=%+v attempts=%d commands=%v", result, f.connections.Load(), f.sentCommands())
			}
			if !strings.Contains(result.Output, "fixture command output") {
				t.Fatalf("command output lost after retry: %q", result.Output)
			}
			mu.Lock()
			allProgress := strings.Join(progress, "\n")
			mu.Unlock()
			if !strings.Contains(allProgress, "重连") && !strings.Contains(allProgress, "重试") {
				t.Fatalf("retry was invisible to progress: %q", allProgress)
			}
			assertSSHReconnectSafeText(t, allProgress+result.Output)
		})
	}
}

func TestSSHConnectionRetryExhaustionDoesNotDispatch(t *testing.T) {
	f := newSSHReconnectFixture(t, func(int64) string { return "eof" })
	s, server := sshReconnectService(t, f, "retry-secret-fixture")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := s.runSSH(ctx, server, "must-not-dispatch")
	if result.Err == nil || result.Uncertain || result.ExitCode != nil || f.connections.Load() != 3 || len(f.sentCommands()) != 0 {
		t.Fatalf("unbounded retry or command sent: result=%+v attempts=%d commands=%v", result, f.connections.Load(), f.sentCommands())
	}
	assertSSHReconnectSafeText(t, result.Output+result.Err.Error())
	if !strings.Contains(result.Err.Error(), "命令未发送") {
		t.Fatal("lost pre-dispatch evidence", result.Err)
	}
}

func TestSSHConnectionPermanentFailuresNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		name, mode, password, want string
		wrongPin                   bool
	}{
		{"authentication", "success", "wrong-secret-fixture", "SSH 认证失败，命令未发送", false},
		{"host fingerprint", "success", "retry-secret-fixture", "SSH 主机指纹不匹配，连接已拒绝；命令未发送", true},
		{"protocol negotiation", "protocol", "retry-secret-fixture", "SSH 协议或算法协商失败，命令未发送", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSSHReconnectFixture(t, func(int64) string { return tc.mode })
			s, server := sshReconnectService(t, f, tc.password)
			if tc.wrongPin {
				server.HostKeySHA256 = "SHA256:wrong"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			result := s.runSSH(ctx, server, "must-not-dispatch")
			if result.Err == nil || result.Uncertain || result.ExitCode != nil || f.connections.Load() != 1 || len(f.sentCommands()) != 0 {
				t.Fatalf("permanent failure retried: result=%+v attempts=%d", result, f.connections.Load())
			}
			if tc.want != "" && result.Err.Error() != tc.want {
				t.Fatalf("wrong safe category: %q, want %q", result.Err.Error(), tc.want)
			}
			if tc.wrongPin && f.auth.Load() != 0 {
				t.Fatal("credentials sent before host key validation")
			}
			assertSSHReconnectSafeText(t, result.Output+result.Err.Error())
		})
	}
}

func TestSSHConnectionFailureAfterExecNeverReconnects(t *testing.T) {
	for _, mode := range []string{"exec_disconnect", "exec_nonzero"} {
		t.Run(mode, func(t *testing.T) {
			f := newSSHReconnectFixture(t, func(int64) string { return mode })
			s, server := sshReconnectService(t, f, "retry-secret-fixture")
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			result := s.runSSH(ctx, server, "fixture-possibly-partial-write")
			if result.Err == nil || f.connections.Load() != 1 || !reflect.DeepEqual(f.sentCommands(), []string{"fixture-possibly-partial-write"}) {
				t.Fatalf("dispatched command retried: result=%+v attempts=%d commands=%v", result, f.connections.Load(), f.sentCommands())
			}
			if mode == "exec_disconnect" && (!result.Uncertain || result.ExitCode != nil) {
				t.Fatalf("unknown command outcome was lost: %+v", result)
			}
			if mode == "exec_nonzero" && (result.Uncertain || result.ExitCode == nil || *result.ExitCode != 7) {
				t.Fatalf("known exit status was lost: %+v", result)
			}
			assertSSHReconnectSafeText(t, result.Output+result.Err.Error())
		})
	}
}

func TestSSHConnectionRetryBackoffCancellationStopsReconnect(t *testing.T) {
	f := newSSHReconnectFixture(t, func(int64) string { return "eof" })
	s, server := sshReconnectService(t, f, "retry-secret-fixture")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var notified atomic.Bool
	result := s.runSSHWithProgress(ctx, server, "must-not-dispatch", func(text string) {
		assertSSHReconnectSafeText(t, text)
		if strings.Contains(text, "重试") || strings.Contains(text, "重连") {
			notified.Store(true)
			cancel()
		}
	})
	if !notified.Load() || ctx.Err() == nil || result.Err == nil || result.Uncertain || f.connections.Load() != 1 || len(f.sentCommands()) != 0 {
		t.Fatalf("cancellation did not stop retry: result=%+v attempts=%d notified=%v ctx=%v", result, f.connections.Load(), notified.Load(), ctx.Err())
	}
	assertSSHReconnectSafeText(t, result.Output+result.Err.Error())
}

func TestSSHConnectionHandshakeTimeoutRetriesBeforeExec(t *testing.T) {
	f := newSSHReconnectFixture(t, func(attempt int64) string {
		if attempt == 1 {
			return "timeout"
		}
		return "success"
	})
	s, server := sshReconnectService(t, f, "retry-secret-fixture")
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	result := s.runSSH(ctx, server, "fixture-after-handshake-timeout")
	if result.Err != nil || f.connections.Load() != 2 || !reflect.DeepEqual(f.sentCommands(), []string{"fixture-after-handshake-timeout"}) {
		t.Fatalf("handshake timeout did not retry safely: result=%+v attempts=%d commands=%v", result, f.connections.Load(), f.sentCommands())
	}
	assertSSHReconnectSafeText(t, result.Output)
}
