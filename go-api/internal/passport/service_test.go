package passport

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/queue"
)

type fakeExecResult struct{}

func (fakeExecResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (fakeExecResult) RowsAffected() (int64, error) {
	return 1, nil
}

type fakeExecer struct {
	query string
	args  []any
	err   error
}

func (f *fakeExecer) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.query = query
	f.args = append([]any(nil), args...)
	if f.err != nil {
		return nil, f.err
	}
	return fakeExecResult{}, nil
}

func TestDBServicePVIncrementsInviteCodeCounter(t *testing.T) {
	execer := &fakeExecer{}
	service := NewDBService(execer)

	if err := service.PV(context.Background(), "ABC12345"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	const expectedQuery = `UPDATE v2_invite_code
SET pv = pv + 1,
    updated_at = EXTRACT(EPOCH FROM NOW())::bigint
WHERE code = $1`

	if execer.query != expectedQuery {
		t.Fatalf("expected query %q, got %q", expectedQuery, execer.query)
	}
	if len(execer.args) != 1 || execer.args[0] != "ABC12345" {
		t.Fatalf("expected invite code arg, got %#v", execer.args)
	}
}

func TestDBServicePVSkipsEmptyInviteCode(t *testing.T) {
	execer := &fakeExecer{}
	service := NewDBService(execer)

	if err := service.PV(context.Background(), "   "); err != nil {
		t.Fatalf("expected nil error on empty invite code, got %v", err)
	}
	if execer.query != "" {
		t.Fatalf("expected no sql execution for empty invite code, got %q", execer.query)
	}
}

type capturePassportQueue struct {
	queueNames []string
	runNow     bool
}

func (c *capturePassportQueue) Enqueue(queueName, jobName string, fn queue.JobFunc) error {
	c.queueNames = append(c.queueNames, queueName)
	if c.runNow {
		return fn(context.Background())
	}
	return nil
}

func (c *capturePassportQueue) Snapshot() queue.Snapshot {
	return queue.Snapshot{}
}

func TestSendEmailBestEffortEnqueuesEmailJob(t *testing.T) {
	execer := &fakeExecer{}
	queueRuntime := &capturePassportQueue{runNow: true}
	service := NewDBServiceWithConfig(config.Config{}, execer).WithQueueRuntime(queueRuntime)

	var sent []string
	service.mailSender = func(to, subject, body string) error {
		sent = append(sent, to)
		return nil
	}

	if err := service.sendEmailBestEffort(context.Background(), "user@example.com", "Subject", "verify", "Body", nil); err != nil {
		t.Fatalf("send email best effort: %v", err)
	}

	if len(queueRuntime.queueNames) != 1 || queueRuntime.queueNames[0] != "send_email" {
		t.Fatalf("unexpected queue names: %#v", queueRuntime.queueNames)
	}
	if len(sent) != 1 || sent[0] != "user@example.com" {
		t.Fatalf("unexpected sent emails: %#v", sent)
	}
	if !strings.Contains(execer.query, "INSERT INTO v2_mail_log") {
		t.Fatalf("expected mail log insert query, got %q", execer.query)
	}
}

func TestRuntimeMailSettingsPreferAdminJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	raw := []byte(`{
  "email_host": "smtp.example.com",
  "email_port": 465,
  "email_username": "mailer",
  "email_password": "secret",
  "email_encryption": "ssl",
  "email_from_address": "noreply@example.com",
  "email_from_name": "Forest Mail"
}`)
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin.json: %v", err)
	}

	oldRoot := passportProjectRoot
	passportProjectRoot = root
	defer func() { passportProjectRoot = oldRoot }()

	service := NewDBServiceWithConfig(config.Config{
		MailHost:        "127.0.0.1",
		MailPort:        25,
		MailUsername:    "env-user",
		MailPassword:    "env-pass",
		MailEncryption:  "",
		MailFromAddress: "env@example.com",
		MailFromName:    "Env Mail",
	}, &fakeExecer{})

	settings := service.runtimeMailSettings()
	if settings.Host != "smtp.example.com" {
		t.Fatalf("expected admin host, got %q", settings.Host)
	}
	if settings.Port != 465 {
		t.Fatalf("expected admin port, got %d", settings.Port)
	}
	if settings.Username != "mailer" || settings.Password != "secret" {
		t.Fatalf("expected admin credentials, got %#v", settings)
	}
	if settings.Encryption != "ssl" {
		t.Fatalf("expected ssl encryption, got %q", settings.Encryption)
	}
	if settings.From != "noreply@example.com" {
		t.Fatalf("expected admin from address, got %q", settings.From)
	}
	if settings.FromName != "Forest Mail" {
		t.Fatalf("expected admin from name, got %q", settings.FromName)
	}
}

func TestRuntimeMailSettingsFallsBackToAdminAppNameForFromName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	raw := []byte(`{
  "app_name": "Forest",
  "email_host": "smtp.example.com",
  "email_port": 25,
  "email_from_address": "noreply@example.com"
}`)
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin.json: %v", err)
	}

	oldRoot := passportProjectRoot
	passportProjectRoot = root
	defer func() { passportProjectRoot = oldRoot }()

	service := NewDBServiceWithConfig(config.Config{
		AppName:         "forest",
		MailHost:        "127.0.0.1",
		MailPort:        25,
		MailFromAddress: "env@example.com",
		MailFromName:    "forest",
	}, &fakeExecer{})

	settings := service.runtimeMailSettings()
	if settings.FromName != "Forest" {
		t.Fatalf("expected from name to fall back to admin app_name, got %q", settings.FromName)
	}
}

func TestSendSMTPUsesSSLEncryption(t *testing.T) {
	server := startTestSMTPServer(t, smtpModeSSL)
	defer server.Close()

	service := NewDBServiceWithConfig(config.Config{
		MailHost:        server.Host(),
		MailPort:        int64(server.Port()),
		MailEncryption:  "ssl",
		MailFromAddress: "noreply@example.com",
		MailFromName:    "Forest Mail",
	}, &fakeExecer{})

	if err := service.sendSMTP("user@example.com", "SSL Subject", "SSL Body"); err != nil {
		t.Fatalf("send smtp ssl: %v", err)
	}
	if !strings.Contains(server.Message(), "Subject: SSL Subject") {
		t.Fatalf("expected ssl message subject, got %q", server.Message())
	}
}

func TestSendSMTPAllowsLegacyPlainAuthWithoutTLS(t *testing.T) {
	server := startTestSMTPServer(t, smtpModePlain)
	defer server.Close()

	service := NewDBServiceWithConfig(config.Config{
		MailHost:        server.Host(),
		MailPort:        int64(server.Port()),
		MailUsername:    "mailer",
		MailPassword:    "secret",
		MailEncryption:  "",
		MailFromAddress: "noreply@example.com",
		MailFromName:    "Forest Mail",
	}, &fakeExecer{})

	if err := service.sendSMTP("user@example.com", "Plain Subject", "Plain Body"); err != nil {
		t.Fatalf("send smtp plain auth: %v", err)
	}
	if !strings.Contains(server.Message(), "Subject: Plain Subject") {
		t.Fatalf("expected plain message subject, got %q", server.Message())
	}
}

func TestSendEmailBestEffortRendersHTMLTemplate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "resources", "views", "mail", "forest-v2"), 0o755); err != nil {
		t.Fatalf("mkdir mail template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), []byte(`{"app_name":"Forest","app_url":"https://forest.example.com","email_template":"forest-v2"}`), 0o644); err != nil {
		t.Fatalf("write admin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "resources", "views", "mail", "forest-v2", "verify.blade.php"), []byte(`<html><body><h1>{{$name}}</h1><span>{{$code}}</span></body></html>`), 0o644); err != nil {
		t.Fatalf("write verify template: %v", err)
	}

	oldRoot := passportProjectRoot
	passportProjectRoot = root
	defer func() { passportProjectRoot = oldRoot }()

	service := NewDBServiceWithConfig(config.Config{}, &fakeExecer{})
	var rendered string
	service.mailSender = func(to, subject, body string) error {
		rendered = body
		return nil
	}

	if err := service.sendEmailBestEffort(context.Background(), "user@example.com", "Subject", "verify", "fallback", map[string]string{
		"name": "Forest",
		"code": "654321",
	}); err != nil {
		t.Fatalf("send email best effort: %v", err)
	}
	if !strings.Contains(rendered, "<html>") || !strings.Contains(rendered, "654321") {
		t.Fatalf("expected rendered html template body, got %q", rendered)
	}
}

type smtpTestMode string

const (
	smtpModePlain    smtpTestMode = "plain"
	smtpModeSSL      smtpTestMode = "ssl"
	smtpModeStartTLS smtpTestMode = "starttls"
)

type testSMTPServer struct {
	listener  net.Listener
	mode      smtpTestMode
	tlsConfig *tls.Config
	mu        sync.Mutex
	message   string
}

func startTestSMTPServer(t *testing.T, mode smtpTestMode) *testSMTPServer {
	t.Helper()

	server := &testSMTPServer{mode: mode}
	if mode == smtpModeSSL || mode == smtpModeStartTLS {
		server.tlsConfig = newTestTLSConfig(t)
	}

	var (
		listener net.Listener
		err      error
	)
	if mode == smtpModeSSL {
		listener, err = tls.Listen("tcp", "127.0.0.1:0", server.tlsConfig)
	} else {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatalf("listen smtp server: %v", err)
	}
	server.listener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handleConn(conn)
		}
	}()
	return server
}

func (s *testSMTPServer) Close() {
	_ = s.listener.Close()
}

func (s *testSMTPServer) Host() string {
	host, _, _ := net.SplitHostPort(s.listener.Addr().String())
	return host
}

func (s *testSMTPServer) Port() int {
	_, port, _ := net.SplitHostPort(s.listener.Addr().String())
	value := 0
	_, _ = fmt.Sscanf(port, "%d", &value)
	return value
}

func (s *testSMTPServer) Message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.message
}

func (s *testSMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line); err != nil {
			return false
		}
		if err := writer.Flush(); err != nil {
			return false
		}
		return true
	}
	if !writeLine("220 localhost ESMTP\r\n") {
		return
	}

	tlsActive := s.mode == smtpModeSSL
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		upper := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			if s.mode == smtpModeStartTLS && !tlsActive {
				if !writeLine("250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n") {
					return
				}
				continue
			}
			if !writeLine("250-localhost\r\n250 AUTH PLAIN\r\n") {
				return
			}
		case strings.HasPrefix(upper, "STARTTLS"):
			if !writeLine("220 Ready to start TLS\r\n") {
				return
			}
			tlsConn := tls.Server(conn, s.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
			writer = bufio.NewWriter(conn)
			tlsActive = true
		case strings.HasPrefix(upper, "AUTH "):
			if !writeLine("235 Authentication successful\r\n") {
				return
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if s.mode == smtpModeStartTLS && !tlsActive {
				if !writeLine("530 Must issue STARTTLS first\r\n") {
					return
				}
				continue
			}
			if !writeLine("250 Ok\r\n") {
				return
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			if !writeLine("250 Ok\r\n") {
				return
			}
		case strings.HasPrefix(upper, "DATA"):
			if !writeLine("354 End data with <CR><LF>.<CR><LF>\r\n") {
				return
			}
			data, err := readSMTPData(reader)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.message = data
			s.mu.Unlock()
			if !writeLine("250 Ok\r\n") {
				return
			}
		case strings.HasPrefix(upper, "QUIT"):
			_ = writeLine("221 Bye\r\n")
			return
		default:
			if !writeLine("250 Ok\r\n") {
				return
			}
		}
	}
}

func readSMTPData(reader *bufio.Reader) (string, error) {
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" {
			break
		}
		builder.WriteString(line)
	}
	return builder.String(), nil
}

func newTestTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse key pair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}}
}
