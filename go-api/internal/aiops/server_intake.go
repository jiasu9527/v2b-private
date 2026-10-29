package aiops

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/cliententry"
)

// These errors deliberately contain no part of the input. HTTP handlers may
// expose these fixed messages without exposing a malformed password or key.
var ErrSSHIntakeInvalid = fmt.Errorf("%w：检测到 SSH 凭据，但无法完整识别。请每次发送一台，提供地址、端口、用户名及密码或完整私钥；凭据未发送给模型", ErrValidation)
var ErrSSHIntakeMultiple = fmt.Errorf("%w：每条消息仅支持一台 SSH 服务器，请分开发送；凭据未发送给模型", ErrValidation)

type SSHRegistrationInput struct {
	Name, Host, Username, HostKeySHA256 string
	Password, PrivateKey, KeyPassphrase string
	Port                                int
}

const sshIntakeKeyMarker = "[LOCAL_SSH_PRIVATE_KEY_BLOCK]"
const sshIntakeNotice = "[SSH 连接资料已由后端安全接收；密码、私钥及口令未写入聊天，也未发送给模型。]"

var sshIntakePEMBegin = regexp.MustCompile(`-----BEGIN ([A-Z ]*PRIVATE KEY)-----`)
var sshIntakeSensitive = regexp.MustCompile(`(?i)(?:password|passwd|pwd|passphrase|private[_ -]?key|密码|口令|私钥)["'\x60]*\s*(?:[:=：\t|]|\bis\b|是|[ \t]+\S)|-----[ \t]*(?:BEGIN|END)[^\r\n]*PRIVATE[ \t]+KEY|\bsshpass\s|ssh://[^\s/]*:[^\s/]*@`)

func sshIntakeBarePassword(value string) bool {
	// Bare records are intentionally narrower than named fields, otherwise a
	// normal "ssh ...\nplease inspect ports" task would become a password.
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t") || strings.HasPrefix(value, "```") {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func sshIntakeLabel(line string) (string, string, bool) {
	line = strings.TrimLeft(line, " \t")
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		line = line[2:]
	}
	i := strings.IndexAny(line, ":=：\t")
	if i < 0 {
		return "", "", false
	}
	key := strings.ToLower(strings.Trim(strings.TrimSpace(line[:i]), "*\"'`"))
	_, size := utf8.DecodeRuneInString(line[i:])
	value := line[i+size:]
	// One space is conventional label formatting. Additional spaces remain
	// part of the value; quoted JSON strings can express any exact spacing.
	value = strings.TrimPrefix(value, " ")
	aliases := map[string]string{
		"host": "host", "hostname": "host", "ip": "host", "address": "host", "server": "host", "服务器": "host", "主机": "host", "主机地址": "host", "服务器地址": "host", "地址": "host", "ssh地址": "host", "ssh 地址": "host",
		"port": "port", "ssh port": "port", "ssh_port": "port", "端口": "port", "ssh端口": "port", "ssh 端口": "port",
		"user": "username", "username": "username", "login": "username", "用户名": "username", "用户": "username", "账号": "username", "账户": "username", "登录用户": "username",
		"password": "password", "passwd": "password", "pwd": "password", "密码": "password", "ssh密码": "password", "ssh 密码": "password", "登录密码": "password", "root密码": "password", "root 密码": "password",
		"privatekey": "private_key", "private key": "private_key", "private_key": "private_key", "私钥": "private_key", "ssh私钥": "private_key", "ssh 私钥": "private_key",
		"passphrase": "passphrase", "key passphrase": "passphrase", "key_passphrase": "passphrase", "私钥口令": "passphrase", "私钥密码": "passphrase", "口令": "passphrase",
		"name": "name", "名称": "name", "服务器名称": "name", "备注": "name",
		"fingerprint": "fingerprint", "host key": "fingerprint", "host_key_sha256": "fingerprint", "主机指纹": "fingerprint", "指纹": "fingerprint", "ssh指纹": "fingerprint",
	}
	k, ok := aliases[key]
	return k, value, ok
}

func sshIntakeEndpoint(raw string, allowSingleLabel bool) (string, int, error) {
	raw = strings.TrimSpace(raw)
	port := 0
	host := raw
	if strings.HasPrefix(raw, "[") || strings.Count(raw, ":") == 1 {
		var p string
		var err error
		host, p, err = net.SplitHostPort(raw)
		if err != nil {
			return "", 0, ErrSSHIntakeInvalid
		}
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return "", 0, ErrSSHIntakeInvalid
		}
	}
	if !allowSingleLabel && !strings.Contains(host, ".") {
		if _, err := netip.ParseAddr(host); err != nil {
			return "", 0, ErrSSHIntakeInvalid
		}
	}
	host, err := cliententry.NormalizeHost(host)
	if err != nil {
		return "", 0, ErrSSHIntakeInvalid
	}
	return host, port, nil
}

func sshIntakeSSHCommand(line string) (SSHRegistrationInput, bool) {
	var out SSHRegistrationInput
	parts := strings.Fields(line)
	if len(parts) < 2 || parts[0] != "ssh" {
		return out, false
	}
	for i := 1; i < len(parts); i++ {
		if parts[i] == "-p" {
			i++
			if i == len(parts) || out.Port != 0 {
				return SSHRegistrationInput{}, false
			}
			port, err := strconv.Atoi(parts[i])
			if err != nil || port < 1 || port > 65535 {
				return SSHRegistrationInput{}, false
			}
			out.Port = port
			continue
		}
		if out.Host != "" || strings.Count(parts[i], "@") != 1 {
			return SSHRegistrationInput{}, false
		}
		pair := strings.SplitN(parts[i], "@", 2)
		host, port, err := sshIntakeEndpoint(pair[1], true)
		if err != nil || !sshUserRE.MatchString(pair[0]) {
			return SSHRegistrationInput{}, false
		}
		out.Host, out.Username = host, pair[0]
		if port != 0 {
			if out.Port != 0 && out.Port != port {
				return SSHRegistrationInput{}, false
			}
			out.Port = port
		}
	}
	return out, out.Host != "" && out.Username != ""
}

// ParseSSHRegistrationInput accepts a single, structurally explicit SSH record.
// It never sends partial recognition onward: ambiguous credential-like input
// returns a fixed error with an empty sanitized result. Bare password lines
// retain all bytes; named values only remove one delimiter formatting space.
func ParseSSHRegistrationInput(content string) (string, []SSHRegistrationInput, error) {
	fail := func(err error) (string, []SSHRegistrationInput, error) { return "", nil, err }
	if strings.ContainsRune(content, 0) || !utf8.ValidString(content) || strings.Contains(content, sshIntakeKeyMarker) {
		return fail(ErrSSHIntakeInvalid)
	}
	work := strings.ReplaceAll(content, "\r\n", "\n")
	var input SSHRegistrationInput
	keyMatches := sshIntakePEMBegin.FindAllStringSubmatchIndex(work, -1)
	if len(keyMatches) > 1 {
		return fail(ErrSSHIntakeMultiple)
	}
	if len(keyMatches) == 1 {
		m := keyMatches[0]
		endMarker := "-----END " + work[m[2]:m[3]] + "-----"
		end := strings.Index(work[m[1]:], endMarker)
		if end < 0 {
			return fail(ErrSSHIntakeInvalid)
		}
		end += m[1] + len(endMarker)
		input.PrivateKey = work[m[0]:end]
		work = work[:m[0]] + sshIntakeKeyMarker + work[end:]
	}
	lines := strings.Split(work, "\n")
	used := make([]bool, len(lines))
	fields := map[string]bool{}
	explicit := false
	sshCommand := false
	set := func(field, value string) error {
		if fields[field] {
			return ErrSSHIntakeMultiple
		}
		fields[field] = true
		switch field {
		case "host":
			host, port, err := sshIntakeEndpoint(value, true)
			if err != nil {
				return err
			}
			input.Host = host
			if port != 0 {
				if input.Port != 0 && input.Port != port {
					return ErrSSHIntakeInvalid
				}
				input.Port = port
			}
		case "port":
			port, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || port < 1 || port > 65535 || (input.Port != 0 && input.Port != port) {
				return ErrSSHIntakeInvalid
			}
			input.Port = port
		case "username":
			input.Username = strings.TrimSpace(value)
		case "password":
			input.Password = value
			explicit = true
		case "private_key":
			explicit = true
			if strings.TrimSpace(value) != sshIntakeKeyMarker || input.PrivateKey == "" {
				return ErrSSHIntakeInvalid
			}
		case "passphrase":
			input.KeyPassphrase = value
			explicit = true
		case "name":
			input.Name = strings.TrimSpace(value)
		case "fingerprint":
			input.HostKeySHA256 = strings.TrimSpace(value)
		}
		return nil
	}
	nextLine := func(index int) int {
		for index++; index < len(lines); index++ {
			if !used[index] && strings.TrimSpace(lines[index]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[index]), "```") {
				return index
			}
		}
		return -1
	}
	// First recognize named fields and simple ssh user@host -p PORT forms.
	for i, line := range lines {
		if used[i] {
			continue
		}
		if strings.TrimSpace(line) == sshIntakeKeyMarker {
			used[i] = true
			explicit = true
			continue
		}
		if field, value, ok := sshIntakeLabel(line); ok {
			used[i] = true
			if (field == "password" || field == "passphrase") && strings.HasPrefix(value, "\"") {
				if err := json.Unmarshal([]byte(value), &value); err != nil {
					return fail(ErrSSHIntakeInvalid)
				}
			}
			if value == "" && (field == "password" || field == "private_key" || field == "passphrase") {
				next := nextLine(i)
				if next < 0 {
					return fail(ErrSSHIntakeInvalid)
				}
				value = lines[next]
				used[next] = true
			}
			if err := set(field, value); err != nil {
				if sshIntakeSensitive.MatchString(content) || input.PrivateKey != "" {
					return fail(err)
				}
				return content, nil, nil
			}
			continue
		}
		if command, ok := sshIntakeSSHCommand(line); ok {
			sshCommand = true
			if fields["host"] || fields["username"] {
				return fail(ErrSSHIntakeMultiple)
			}
			fields["host"], fields["username"] = true, true
			input.Host, input.Username = command.Host, command.Username
			if command.Port != 0 {
				if input.Port != 0 && input.Port != command.Port {
					return fail(ErrSSHIntakeInvalid)
				}
				input.Port = command.Port
			}
			used[i] = true
			// A word after an ssh command could be either a password or a shell
			// command. Require a label instead of guessing and exposing it.
			next := nextLine(i)
			if next >= 0 {
				if _, _, labeled := sshIntakeLabel(lines[next]); !labeled && input.PrivateKey == "" && sshIntakeBarePassword(lines[next]) {
					return fail(ErrSSHIntakeInvalid)
				}
			}
		}
	}
	// Named host/username followed by a bare password is a common mixed paste.
	// Recognize the adjacent value so it can never fall through to the relay.
	if !sshCommand && fields["host"] && fields["username"] && input.Password == "" && input.PrivateKey == "" {
		lastField := -1
		for i := range used {
			if used[i] {
				lastField = i
			}
		}
		next := nextLine(lastField)
		if next >= 0 && sshIntakeBarePassword(lines[next]) {
			if err := set("password", lines[next]); err != nil {
				return fail(err)
			}
			used[next] = true
		}
	}
	// Then recognize host[/port], username and password on separate lines.
	for i, line := range lines {
		if used[i] {
			continue
		}
		host, port, err := sshIntakeEndpoint(line, false)
		if err != nil {
			continue
		}
		j := nextLine(i)
		if j < 0 {
			continue
		}
		portLine := -1
		if n, err := strconv.Atoi(strings.TrimSpace(lines[j])); err == nil && n >= 1 && n <= 65535 {
			if port != 0 && port != n {
				return fail(ErrSSHIntakeInvalid)
			}
			port, portLine = n, j
			j = nextLine(j)
			if j < 0 {
				continue
			}
		}
		username := strings.TrimSpace(lines[j])
		if !sshUserRE.MatchString(username) {
			continue
		}
		k := nextLine(j)
		if input.Password == "" && input.PrivateKey == "" && k < 0 {
			continue
		}
		if fields["host"] || fields["username"] {
			return fail(ErrSSHIntakeMultiple)
		}
		input.Host, input.Username = host, username
		fields["host"], fields["username"] = true, true
		if input.Port != 0 && port != 0 && input.Port != port {
			return fail(ErrSSHIntakeInvalid)
		}
		if port != 0 {
			input.Port = port
		}
		used[i], used[j] = true, true
		if portLine >= 0 {
			used[portLine] = true
		}
		if input.Password == "" && input.PrivateKey == "" {
			if _, _, labeled := sshIntakeLabel(lines[k]); labeled || sshIntakeSensitive.MatchString(lines[k]) || !sshIntakeBarePassword(lines[k]) {
				return fail(ErrSSHIntakeInvalid)
			}
			if err := set("password", lines[k]); err != nil {
				return fail(err)
			}
			used[k] = true
		}
	}
	if input.Password == "" && input.PrivateKey == "" {
		if explicit || sshIntakeSensitive.MatchString(content) {
			return fail(ErrSSHIntakeInvalid)
		}
		return content, nil, nil
	}
	if input.Port == 0 {
		input.Port = 22
	}
	if input.Name == "" {
		input.Name = input.Host
	}
	if input.Host == "" || !sshUserRE.MatchString(input.Username) || input.Port < 1 || input.Port > 65535 || len([]rune(input.Name)) > 128 || len(input.Password) > 4096 || len(input.PrivateKey) > 32768 || len(input.KeyPassphrase) > 4096 {
		return fail(ErrSSHIntakeInvalid)
	}
	if input.HostKeySHA256 != "" {
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(input.HostKeySHA256, "SHA256:"))
		if !strings.HasPrefix(input.HostKeySHA256, "SHA256:") || err != nil || len(decoded) != 32 {
			return fail(ErrSSHIntakeInvalid)
		}
	}
	if input.KeyPassphrase != "" && input.PrivateKey == "" {
		return fail(ErrSSHIntakeInvalid)
	}
	if input.PrivateKey != "" {
		if _, err := parseSSHKey(input.PrivateKey, input.KeyPassphrase); err != nil {
			return fail(ErrSSHIntakeInvalid)
		}
	}
	remaining := []string{}
	for i, line := range lines {
		if !used[i] {
			if sshIntakeSensitive.MatchString(line) || strings.Contains(line, sshIntakeKeyMarker) {
				return fail(ErrSSHIntakeInvalid)
			}
			remaining = append(remaining, line)
		}
	}
	sanitized := strings.Join(remaining, "\n")
	// Remove repeated copies outside the parsed block too; never trust a model
	// to hide a second pasted credential in free-form task text.
	for _, secret := range []string{input.PrivateKey, input.Password, input.KeyPassphrase} {
		if secret == "" {
			continue
		}
		for _, public := range []string{input.Host, input.Username, input.Name, input.HostKeySHA256} {
			if strings.Contains(public, secret) {
				return fail(ErrSSHIntakeInvalid)
			}
		}
	}
	sanitized = redactSSHOutput(sanitized, []string{input.PrivateKey, input.Password, input.KeyPassphrase}, true)
	return strings.TrimSpace(sanitized + "\n" + sshIntakeNotice), []SSHRegistrationInput{input}, nil
}

func (s *Service) storeSSHIntakesTx(ctx context.Context, tx *sql.Tx, actorID, sessionID, turnID int64, inputs []SSHRegistrationInput) (string, error) {
	if len(inputs) == 0 {
		return "", nil
	}
	if len(inputs) != 1 || actorID <= 0 || sessionID <= 0 || turnID <= 0 {
		return "", ErrSSHIntakeInvalid
	}
	input := inputs[0]
	secrets := []string{input.Password, input.PrivateKey, input.KeyPassphrase}
	ciphers := make([]string, len(secrets))
	for i, secret := range secrets {
		if secret != "" {
			cipher, err := s.encryptSecret(secret)
			if err != nil {
				return "", err
			}
			ciphers[i] = cipher
		}
	}
	now := time.Now().Unix()
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO v2_ai_server_intake(session_id,turn_id,actor_id,name,host,port,username,host_key_sha256,password_cipher,private_key_cipher,key_passphrase_cipher,created_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, sessionID, turnID, actorID, input.Name, input.Host, input.Port, input.Username, input.HostKeySHA256, ciphers[0], ciphers[1], ciphers[2], now, now+86400).Scan(&id)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"intake_id": id, "name": input.Name, "host": input.Host, "port": input.Port, "username": input.Username, "host_key_sha256": input.HostKeySHA256, "has_password": input.Password != "", "has_private_key": input.PrivateKey != "", "has_key_passphrase": input.KeyPassphrase != "", "expires_at": now + 86400})
	if err != nil {
		return "", errors.New("无法生成 SSH 暂存资料摘要")
	}
	return "\n[后端提供的 SSH 暂存资料；凭据已加密，仅使用 intake_id 登记服务器，不要索取凭据。]\n" + string(raw), nil
}
