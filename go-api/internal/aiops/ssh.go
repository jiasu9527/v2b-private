package aiops

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/cliententry"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/ssh"
)

var sshSlots = make(chan struct{}, 2)
var sshUserRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var errFingerprintObserved = errors.New("SSH host key observed without authentication")

const serverColumns = `id,name,host,port,username,host_key_sha256,enabled,version,password_cipher<>'',private_key_cipher<>'',key_passphrase_cipher<>'',created_at,updated_at`
const diagnosticCommand = `printf '%s\n' '--- system ---'; uname -a; printf '%s\n' '--- uptime ---'; uptime; printf '%s\n' '--- memory ---'; free -m; printf '%s\n' '--- disk ---'; df -h; printf '%s\n' '--- listening sockets ---'; ss -lnt`

type sshSecretServer struct {
	Server
	PasswordCipher, PrivateKeyCipher, PassphraseCipher string
}

func scanServer(row interface{ Scan(...any) error }) (Server, error) {
	var v Server
	err := row.Scan(&v.ID, &v.Name, &v.Host, &v.Port, &v.Username, &v.HostKeySHA256, &v.Enabled, &v.Version, &v.HasPassword, &v.HasPrivateKey, &v.HasKeyPassphrase, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}
func normalizeServer(r ServerSaveRequest) (ServerSaveRequest, error) {
	r.Name = strings.TrimSpace(r.Name)
	r.Username = strings.TrimSpace(r.Username)
	r.HostKeySHA256 = strings.TrimSpace(r.HostKeySHA256)
	host, err := cliententry.NormalizeHost(r.Host)
	if err != nil || len(host) > 255 || r.Port < 1 || r.Port > 65535 || len([]rune(r.Name)) < 1 || len([]rune(r.Name)) > 128 || !sshUserRE.MatchString(r.Username) {
		return r, ErrValidation
	}
	r.Host = host
	if !strings.HasPrefix(r.HostKeySHA256, "SHA256:") {
		return r, ErrValidation
	}
	fp, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(r.HostKeySHA256, "SHA256:"))
	if err != nil || len(fp) != 32 {
		return r, ErrValidation
	}
	if len(r.Password) > 4096 || len(r.PrivateKey) > 32768 || len(r.KeyPassphrase) > 4096 {
		return r, ErrValidation
	}
	if (r.ClearPassword && r.Password != "") || (r.ClearPrivateKey && r.PrivateKey != "") || (r.ClearKeyPassphrase && r.KeyPassphrase != "") {
		return r, ErrValidation
	}
	if r.ID < 0 || (r.ID > 0 && r.Version <= 0) {
		return r, ErrValidation
	}
	return r, nil
}
func (s *Service) ListServers(ctx context.Context, actorID int64) ([]Server, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverColumns+` FROM v2_ai_server ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) SaveServer(ctx context.Context, actorID int64, r ServerSaveRequest) (Server, error) {
	var empty Server
	r, err := normalizeServer(r)
	if err != nil {
		return empty, err
	}
	if err = s.ensureSchema(ctx); err != nil {
		return empty, err
	}
	if err = s.requireAdmin(ctx, actorID); err != nil {
		return empty, err
	}
	if r.ID > 0 {
		if err = s.rejectBusySSHServer(ctx, r.ID); err != nil {
			return empty, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return empty, err
	}
	if err = s.requireAdminTx(ctx, tx, actorID); err != nil {
		return empty, err
	}
	password, key, pass := "", "", ""
	if r.ID == 0 {
		// Serialize creations so two administrators cannot exceed the list cap.
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102904)`); err != nil {
			return empty, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server`).Scan(&count); err != nil {
			return empty, err
		}
		if count >= 200 {
			return empty, ErrValidation
		}
	}
	if r.ID > 0 {
		var version int64
		var oldHost, oldUser, oldPin string
		var oldPort int
		if err = tx.QueryRowContext(ctx, `SELECT password_cipher,private_key_cipher,key_passphrase_cipher,version,host,port,username,host_key_sha256 FROM v2_ai_server WHERE id=$1 FOR UPDATE NOWAIT`, r.ID).Scan(&password, &key, &pass, &version, &oldHost, &oldPort, &oldUser, &oldPin); errors.Is(err, sql.ErrNoRows) {
			return empty, ErrNotFound
		} else if err != nil {
			var pgerr *pgconn.PgError
			if errors.As(err, &pgerr) && pgerr.Code == "55P03" {
				return empty, ErrBusy
			}
			return empty, err
		}
		if version != r.Version {
			return empty, ErrConflict
		}
		if oldHost != r.Host || oldPort != r.Port || oldUser != r.Username || oldPin != r.HostKeySHA256 {
			// Never silently forward stored credentials to a new SSH identity.
			// A changed destination requires fresh credentials from the operator.
			password, key, pass = "", "", ""
			if r.Password == "" && r.PrivateKey == "" {
				return empty, ErrValidation
			}
		}
	}
	for _, v := range []struct {
		incoming string
		clear    bool
		target   *string
	}{{r.Password, r.ClearPassword, &password}, {r.PrivateKey, r.ClearPrivateKey, &key}, {r.KeyPassphrase, r.ClearKeyPassphrase, &pass}} {
		if v.clear {
			*v.target = ""
		} else if v.incoming != "" {
			*v.target, err = s.encryptSecret(v.incoming)
			if err != nil {
				return empty, err
			}
		}
	}
	if password == "" && key == "" {
		return empty, ErrValidation
	}
	if key != "" {
		plain, err := s.decryptSecret(key)
		if err != nil {
			return empty, ErrValidation
		}
		phrase := ""
		if pass != "" {
			phrase, err = s.decryptSecret(pass)
			if err != nil {
				return empty, ErrValidation
			}
		}
		if _, err = parseSSHKey(plain, phrase); err != nil {
			return empty, ErrValidation
		}
	}
	now := time.Now().Unix()
	var result Server
	if r.ID == 0 {
		result, err = scanServer(tx.QueryRowContext(ctx, `INSERT INTO v2_ai_server(actor_id,name,host,port,username,password_cipher,private_key_cipher,key_passphrase_cipher,host_key_sha256,enabled,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11) RETURNING `+serverColumns, actorID, r.Name, r.Host, r.Port, r.Username, password, key, pass, r.HostKeySHA256, r.Enabled, now))
	} else {
		result, err = scanServer(tx.QueryRowContext(ctx, `UPDATE v2_ai_server SET name=$2,host=$3,port=$4,username=$5,password_cipher=$6,private_key_cipher=$7,key_passphrase_cipher=$8,host_key_sha256=$9,enabled=$10,version=version+1,updated_at=$11 WHERE id=$1 RETURNING `+serverColumns, r.ID, r.Name, r.Host, r.Port, r.Username, password, key, pass, r.HostKeySHA256, r.Enabled, now))
	}
	if err != nil {
		return empty, err
	}
	return result, tx.Commit()
}
func (s *Service) DeleteServer(ctx context.Context, actorID, id, version int64) error {
	if id <= 0 || version <= 0 {
		return ErrValidation
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return err
	}
	if err := s.rejectBusySSHServer(ctx, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return err
	}
	if err = s.requireAdminTx(ctx, tx, actorID); err != nil {
		return err
	}
	var lockedVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM v2_ai_server WHERE id=$1 FOR UPDATE NOWAIT`, id).Scan(&lockedVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "55P03" {
			return ErrBusy
		}
		return err
	}
	if lockedVersion != version {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_ai_server WHERE id=$1 AND version=$2`, id, version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *Service) rejectBusySSHServer(ctx context.Context, id int64) error {
	if err := s.reconcileExpiredActions(ctx); err != nil {
		return err
	}
	var running bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_action WHERE kind='ssh_command' AND target_id=$1 AND status='running')`, id).Scan(&running)
	if err != nil {
		return err
	}
	if running {
		return ErrBusy
	}
	return nil
}
func (s *Service) ProbeHostKey(ctx context.Context, actorID int64, r HostKeyRequest) (HostKeyResult, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return HostKeyResult{}, err
	}
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return HostKeyResult{}, err
	}
	host, err := cliententry.NormalizeHost(r.Host)
	if err != nil || r.Port < 1 || r.Port > 65535 {
		return HostKeyResult{}, ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !takeSSHSlot(ctx) {
		return HostKeyResult{}, ErrBusy
	}
	defer func() { <-sshSlots }()
	address := net.JoinHostPort(host, strconv.Itoa(r.Port))
	conn, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return HostKeyResult{}, errors.New("SSH 主机不可达")
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	var out HostKeyResult
	_, _, _, _ = ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "hostkey-observation", Timeout: 8 * time.Second, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		out = HostKeyResult{Host: host, Port: r.Port, Fingerprint: ssh.FingerprintSHA256(key), KeyType: key.Type()}
		return errFingerprintObserved
	}})
	if out.Fingerprint == "" {
		return out, errors.New("未能获取 SSH 主机指纹")
	}
	return out, nil
}
func takeSSHSlot(ctx context.Context) bool {
	select {
	case sshSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	default:
		return false
	}
}
func parseSSHKey(key, pass string) (ssh.Signer, error) {
	if pass != "" {
		return ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(pass))
	}
	return ssh.ParsePrivateKey([]byte(key))
}
func pinnedHostKey(expected string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
			return errors.New("SSH 主机指纹不匹配，连接已拒绝")
		}
		return nil
	}
}
func (s *Service) loadSSHServer(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64, lock bool) (sshSecretServer, error) {
	var v sshSecretServer
	query := `SELECT id,name,host,port,username,host_key_sha256,enabled,version,created_at,updated_at,password_cipher,private_key_cipher,key_passphrase_cipher FROM v2_ai_server WHERE id=$1`
	if lock {
		query += ` FOR SHARE`
	}
	err := q.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Name, &v.Host, &v.Port, &v.Username, &v.HostKeySHA256, &v.Enabled, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.PasswordCipher, &v.PrivateKeyCipher, &v.PassphraseCipher)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if !v.Enabled {
		return v, ErrConflict
	}
	return v, nil
}

type cappedSSHOutput struct {
	mu        sync.Mutex
	bytes     bytes.Buffer
	limit     int
	truncated bool
}

func (w *cappedSSHOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := w.limit - w.bytes.Len()
	if remaining > 0 {
		piece := p
		if len(piece) > remaining {
			piece = piece[:remaining]
		}
		_, _ = w.bytes.Write(piece)
	}
	if n > remaining {
		w.truncated = true
	}
	return n, nil
}
func (w *cappedSSHOutput) String() string {
	return w.safeString(nil, true)
}

// A credential may arrive in separate SSH writes. Do not publish an unfinished
// suffix that is a prefix of a known secret while more output can still arrive.
func (w *cappedSSHOutput) safeString(secrets []string, final bool) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := redactSSHOutput(w.bytes.String(), secrets, final)
	out = strings.ToValidUTF8(out, "�")
	truncated := w.truncated
	if len(out) > 32<<10 {
		out = strings.ToValidUTF8(out[:32<<10], "")
		truncated = true
	}
	if truncated {
		out += "\n[输出已截断为 32 KiB]"
	}
	return out
}

type sshSecretSpan struct {
	start, end int
	complete   bool
}

// Find overlapping credentials against the original bytes, then render the
// union once. Replacing a short secret first would otherwise expose the rest
// of a longer secret containing it, including a private key.
func redactSSHOutput(raw string, secrets []string, final bool) string {
	spans := []sshSecretSpan{}
	for _, secret := range secrets {
		if secret == "" || raw == "" {
			continue
		}
		// KMP bounds work by the captured output plus the secret length. Both
		// are already capped (32 KiB), unlike repeated suffix comparisons.
		prefix := make([]int, len(secret))
		for i, j := 1, 0; i < len(secret); i++ {
			for j > 0 && secret[i] != secret[j] {
				j = prefix[j-1]
			}
			if secret[i] == secret[j] {
				j++
			}
			prefix[i] = j
		}
		matched := 0
		for i := 0; i < len(raw); i++ {
			for matched > 0 && raw[i] != secret[matched] {
				matched = prefix[matched-1]
			}
			if raw[i] == secret[matched] {
				matched++
			}
			if matched == len(secret) {
				spans = append(spans, sshSecretSpan{start: i + 1 - len(secret), end: i + 1, complete: true})
				matched = prefix[matched-1]
			}
		}
		if matched > 0 {
			spans = append(spans, sshSecretSpan{start: len(raw) - matched, end: len(raw)})
		}
	}
	if len(spans) == 0 {
		return raw
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end < spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	merged := spans[:0]
	for _, span := range spans {
		if len(merged) > 0 && span.start <= merged[len(merged)-1].end {
			last := &merged[len(merged)-1]
			last.end = max(last.end, span.end)
			last.complete = last.complete || span.complete
		} else {
			merged = append(merged, span)
		}
	}
	var out strings.Builder
	start := 0
	for _, span := range merged {
		out.WriteString(raw[start:span.start])
		if span.complete {
			out.WriteString("[已隐藏凭据]")
		} else if final {
			out.WriteString("[已隐藏凭据片段]")
		}
		start = span.end
	}
	out.WriteString(raw[start:])
	return out.String()
}

type sshRunResult struct {
	Output    string
	Uncertain bool
	Err       error
}

func (s *Service) runSSH(ctx context.Context, v sshSecretServer, command string) sshRunResult {
	return s.runSSHWithProgress(ctx, v, command, nil)
}

func (s *Service) runSSHWithProgress(ctx context.Context, v sshSecretServer, command string, progress func(string)) sshRunResult {
	if !takeSSHSlot(ctx) {
		return sshRunResult{Err: ErrBusy}
	}
	defer func() { <-sshSlots }()
	auth := []ssh.AuthMethod{}
	secrets := []string{}
	if v.PasswordCipher != "" {
		p, err := s.decryptSecret(v.PasswordCipher)
		if err != nil {
			return sshRunResult{Err: errors.New("SSH 凭据解密失败")}
		}
		auth = append(auth, ssh.Password(p))
		secrets = append(secrets, p)
	}
	if v.PrivateKeyCipher != "" {
		k, err := s.decryptSecret(v.PrivateKeyCipher)
		if err != nil {
			return sshRunResult{Err: errors.New("SSH 凭据解密失败")}
		}
		pass := ""
		if v.PassphraseCipher != "" {
			pass, err = s.decryptSecret(v.PassphraseCipher)
			if err != nil {
				return sshRunResult{Err: errors.New("SSH 凭据解密失败")}
			}
		}
		signer, err := parseSSHKey(k, pass)
		if err != nil {
			return sshRunResult{Err: errors.New("SSH 私钥不可用")}
		}
		auth = append(auth, ssh.PublicKeys(signer))
		secrets = append(secrets, k, pass)
	}
	address := net.JoinHostPort(v.Host, strconv.Itoa(v.Port))
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return sshRunResult{Err: errors.New("SSH 连接失败，命令未发送")}
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(120 * time.Second)
	}
	handshakeDeadline := time.Now().Add(10 * time.Second)
	if deadline.Before(handshakeDeadline) {
		handshakeDeadline = deadline
	}
	_ = conn.SetDeadline(handshakeDeadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	cc, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: v.Username, Auth: auth, HostKeyCallback: pinnedHostKey(v.HostKeySHA256), Timeout: 10 * time.Second})
	if err != nil {
		return sshRunResult{Err: errors.New("SSH 认证或主机指纹验证失败，命令未发送")}
	}
	_ = conn.SetDeadline(deadline)
	client := ssh.NewClient(cc, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return sshRunResult{Err: errors.New("SSH 会话创建失败，命令未发送")}
	}
	defer session.Close()
	out := &cappedSSHOutput{limit: 32 << 10}
	session.Stdout = out
	session.Stderr = out
	session.Stdin = nil
	stopProgress := streamSSHOutput(ctx, out, secrets, progress, time.Second)
	err = session.Run(command)
	stopProgress()
	output := out.safeString(secrets, true)
	if err == nil {
		return sshRunResult{Output: output}
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return sshRunResult{Output: output, Err: fmt.Errorf("远程命令退出码 %d；可能已部分执行，请人工检查，禁止自动重试", exit.ExitStatus())}
	}
	return sshRunResult{Output: output, Uncertain: true, Err: errors.New("SSH 中断或超时，执行结果未知，可能已部分执行；请人工检查，不要自动重试")}
}

var _ io.Writer = (*cappedSSHOutput)(nil)
