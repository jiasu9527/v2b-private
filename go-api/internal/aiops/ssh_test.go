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
							if delay {
								time.Sleep(time.Second)
							}
							_, _ = io.WriteString(channel, "password=secret-test\n"+strings.Repeat("output", 10000))
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
