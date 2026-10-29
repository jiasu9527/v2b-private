package aiops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"golang.org/x/crypto/ssh"
)

func testSSHHost(t *testing.T, delay bool) (string, int, string, *atomic.Int64, *atomic.Int64) {
	return testSSHHostWithExec(t, func(channel ssh.Channel) {
		if delay {
			time.Sleep(time.Second)
		}
		_, _ = io.WriteString(channel, "password=secret-test\n"+strings.Repeat("output", 10000))
	})
}

func testSSHHostWithExec(t *testing.T, execute func(ssh.Channel)) (string, int, string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	auth, execs := &atomic.Int64{}, &atomic.Int64{}
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		auth.Add(1)
		if string(password) != "secret-test" {
			return nil, errors.New("bad password")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				cc, chans, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer cc.Close()
				go ssh.DiscardRequests(requests)
				for ch := range chans {
					channel, reqs, err := ch.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for req := range reqs {
							if req.Type != "exec" {
								_ = req.Reply(false, nil)
								continue
							}
							execs.Add(1)
							_ = req.Reply(true, nil)
							execute(channel)
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, ssh.FingerprintSHA256(signer.PublicKey()), auth, execs
}

func TestSSHLiveOutputRedactsAcrossWritesAndStopsBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	host, port, pin, _, execs := testSSHHostWithExec(t, func(channel ssh.Channel) {
		_, _ = io.WriteString(channel, "install stage 1\npassword=secret-")
		<-release
		_, _ = io.WriteString(channel, "test\ninstall complete\n")
	})
	s := NewService(config.Config{AppKey: "fixture-key"}, nil, nil)
	defer s.Close()
	encrypted, _ := s.encryptSecret("secret-test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	progress := make(chan string, 4)
	resultCh := make(chan sshRunResult, 1)
	go func() {
		resultCh <- s.runSSHWithProgress(ctx, sshSecretServer{Server: Server{Host: host, Port: port, Username: "root", HostKeySHA256: pin}, PasswordCipher: encrypted}, "install fixture", func(output string) { progress <- output })
	}()
	select {
	case output := <-progress:
		if !strings.Contains(output, "install stage 1") || strings.Contains(output, "secret-") {
			t.Fatalf("unsafe or missing progress %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no progress before command completes")
	}
	release <- struct{}{}
	select {
	case result := <-resultCh:
		if result.Err != nil || !strings.Contains(result.Output, "install complete") || strings.Contains(result.Output, "secret-test") || execs.Load() != 1 {
			t.Fatalf("bad execution result %+v, executions %d", result, execs.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command did not finish")
	}
}

func TestSSHCappedOutputWithholdsCredentialPrefixes(t *testing.T) {
	for _, secret := range []string{"secret-test", "aaaa", "密钥测试"} {
		for n := 1; n < len(secret); n++ {
			prefix := "log:\n"
			out := &cappedSSHOutput{limit: len(prefix) + n}
			out.Write([]byte(prefix + secret + "\n"))
			for _, final := range []bool{false, true} {
				got := out.safeString([]string{secret}, final)
				want := prefix
				if final {
					want += "[已隐藏凭据片段]"
				}
				want += "\n[输出已截断为 32 KiB]"
				if got != want {
					t.Fatalf("partial secret published final=%v secret=%q n=%d got=%q", final, secret, n, got)
				}
			}
		}
		out := &cappedSSHOutput{limit: 1000}
		out.Write([]byte("log:" + secret))
		if got := out.safeString([]string{secret}, false); got != "log:[已隐藏凭据]" {
			t.Fatalf("full credential redaction failed: %q", got)
		}
	}
}

func TestSSHProgressDoesNotRepeatUnchangedOutput(t *testing.T) {
	out := &cappedSSHOutput{limit: 1024}
	output := make(chan string, 8)
	stop := streamSSHOutput(context.Background(), out, nil, func(s string) { output <- s }, 10*time.Millisecond)
	defer stop()
	out.Write([]byte("first\n"))
	select {
	case got := <-output:
		if got != "first\n" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("progress missing")
	}
	select {
	case got := <-output:
		t.Fatalf("duplicate progress %q", got)
	case <-time.After(40 * time.Millisecond):
	}
	stop()
	out.Write([]byte("after stop"))
	select {
	case got := <-output:
		t.Fatalf("progress after stop %q", got)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSSHRedactionCannotExpandOutputWithoutBound(t *testing.T) {
	out := &cappedSSHOutput{limit: 32 << 10}
	out.Write([]byte(strings.Repeat("x", 32<<10)))
	got := out.safeString([]string{"x"}, true)
	if len(got) > 33000 || strings.Contains(got, "x") {
		t.Fatalf("redaction expanded output beyond bound: bytes=%d", len(got))
	}
}

func TestSSHRedactionMergesOverlappingSecretsBeforeRendering(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		secrets         []string
	}{
		{"contained", "password=abcdef", "password=[已隐藏凭据]", []string{"abcd", "abcdef"}},
		{"crossing", "password=abcdefg", "password=[已隐藏凭据]", []string{"abcde", "defg"}},
		{"private-key-containing-password", "key=-----BEGIN PRIVATE KEY-----\nabc123def456\n-----END PRIVATE KEY-----", "key=[已隐藏凭据]", []string{"abc", "-----BEGIN PRIVATE KEY-----\nabc123def456\n-----END PRIVATE KEY-----"}},
		{"complete-overlap-partial", "password=abcdef", "password=[已隐藏凭据]", []string{"abcd", "defghi"}},
		{"self-overlap", "value=aaaaaa", "value=[已隐藏凭据]", []string{"aaaa"}},
		{"single-char", "value=xxx", "value=[已隐藏凭据]", []string{"x"}},
		{"partial", "value=abc", "value=[已隐藏凭据片段]", []string{"abcdef"}},
		{"non-secret", "stage two complete\n", "stage two complete\n", []string{"abcdef"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, secrets := range [][]string{tc.secrets, reverseSecrets(tc.secrets)} {
				if got := redactSSHOutput(tc.raw, secrets, true); got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
	if got := redactSSHOutput("value=abc", []string{"abcdef"}, false); got != "value=" {
		t.Fatal("partial credential published", got)
	}
	// This input forced quadratic work with suffix-by-suffix comparisons.
	longSecret := "a" + strings.Repeat("b", 32766) + "c"
	if got := redactSSHOutput(strings.Repeat("b", 32768), []string{longSecret}, true); len(got) != 32768 {
		t.Fatal("unmatched bounded output changed")
	}
}

func reverseSecrets(values []string) []string {
	out := append([]string{}, values...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
func TestSSHHostPinRejectsBeforeAuthentication(t *testing.T) {
	host, port, _, auth, execs := testSSHHost(t, false)
	s := NewService(config.Config{AppKey: "fixture-key"}, nil, nil)
	defer s.Close()
	encrypted, _ := s.encryptSecret("secret-test")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := s.runSSH(ctx, sshSecretServer{Server: Server{Host: host, Port: port, Username: "root", HostKeySHA256: "SHA256:wrong"}, PasswordCipher: encrypted}, "echo test")
	if result.Err == nil || auth.Load() != 0 || execs.Load() != 0 {
		t.Fatalf("pin bypass result=%+v auth=%d exec=%d", result, auth.Load(), execs.Load())
	}
}
func TestSSHExecutionBoundedAndRedactsKnownSecret(t *testing.T) {
	host, port, pin, auth, execs := testSSHHost(t, false)
	s := NewService(config.Config{AppKey: "fixture-key"}, nil, nil)
	defer s.Close()
	encrypted, _ := s.encryptSecret("secret-test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := s.runSSH(ctx, sshSecretServer{Server: Server{Host: host, Port: port, Username: "root", HostKeySHA256: pin}, PasswordCipher: encrypted}, "echo test")
	if result.Err != nil || auth.Load() != 1 || execs.Load() != 1 || strings.Contains(result.Output, "secret-test") || !strings.Contains(result.Output, "截断") || len(result.Output) > 33000 {
		t.Fatalf("result err=%v len=%d auth=%d exec=%d", result.Err, len(result.Output), auth.Load(), execs.Load())
	}
}
func TestSSHTimeoutIsUnknownAndNeverRetries(t *testing.T) {
	host, port, pin, _, execs := testSSHHost(t, true)
	s := NewService(config.Config{AppKey: "fixture-key"}, nil, nil)
	defer s.Close()
	encrypted, _ := s.encryptSecret("secret-test")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := s.runSSH(ctx, sshSecretServer{Server: Server{Host: host, Port: port, Username: "root", HostKeySHA256: pin}, PasswordCipher: encrypted}, "irreversible-operation")
	if result.Err == nil || !result.Uncertain || execs.Load() != 1 {
		t.Fatalf("result %+v exec=%d", result, execs.Load())
	}
}
func TestNormalizeSSHServerRejectsUnsafeConnectionFields(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	good := ServerSaveRequest{Name: "test", Host: "192.0.2.1", Port: 22, Username: "root", HostKeySHA256: ssh.FingerprintSHA256(signer.PublicKey()), Password: "x"}
	if _, err := normalizeServer(good); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ServerSaveRequest){func(r *ServerSaveRequest) { r.Host = "x;id" }, func(r *ServerSaveRequest) { r.Username = "root;id" }, func(r *ServerSaveRequest) { r.Port = 0 }, func(r *ServerSaveRequest) { r.HostKeySHA256 = "" }, func(r *ServerSaveRequest) { r.ClearPassword = true }} {
		r := good
		mutate(&r)
		if _, err := normalizeServer(r); err == nil {
			t.Errorf("accepted %#v", r)
		}
	}
}
