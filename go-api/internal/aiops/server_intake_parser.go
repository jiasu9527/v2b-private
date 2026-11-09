package aiops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The limit applies to a whole paste, before any record is persisted.
const maxSSHIntakeBatchSize = 50

var errSSHIntakeTooMany = fmt.Errorf("%w：一次最多整理 50 台 SSH 服务器，请缩小本批数量；凭据未发送给模型", ErrValidation)
var sshIntakeNumberedLine = regexp.MustCompile(`^\s*(?:[-*]\s+|(?:[0-9]+[)、]\s*|[0-9]+\.[ \t]+))`)
var sshIntakeInline = regexp.MustCompile(`^(\S+)[ \t]+(?:(\d+)[ \t]+)?([a-zA-Z_][a-zA-Z0-9_.-]*\$?)[ \t]+(.+)$`)

// ParseSSHRegistrationInput normalizes common pasted formats locally, then
// validates every record before returning any of them. Model-bound text only
// contains the task and a receipt; credentials stay in encrypted intake rows.
func ParseSSHRegistrationInput(content string) (string, []SSHRegistrationInput, error) {
	fail := func(err error) (string, []SSHRegistrationInput, error) { return "", nil, err }
	if strings.ContainsRune(content, 0) || !utf8.ValidString(content) || strings.Contains(content, sshIntakeKeyMarker) {
		return fail(ErrSSHIntakeInvalid)
	}
	work, err := normalizeSSHIntakePaste(strings.ReplaceAll(content, "\r\n", "\n"))
	if err != nil {
		return fail(err)
	}
	lines := strings.Split(work, "\n")
	var chunks []string
	var pending []string
	inKey := false
	waitingSecret := false
	for index, line := range lines {
		trim := strings.TrimSpace(line)
		if inKey {
			pending = append(pending, line)
			if strings.Contains(trim, "-----END ") && strings.Contains(trim, "PRIVATE KEY-----") {
				inKey = false
			}
			continue
		}
		if sshIntakePEMBegin.MatchString(line) {
			inKey = true
			waitingSecret = false
			pending = append(pending, line)
			continue
		}
		// An explicit empty password label owns the following value even if
		// that value happens to look like a host, name label, or separator.
		if waitingSecret && trim != "" && !strings.HasPrefix(trim, "```") {
			pending = append(pending, line)
			waitingSecret = false
			continue
		}
		field, value, labeled := sshIntakeLabel(line)
		boundary := labeled && field == "host"
		if labeled && field == "name" {
			// A name immediately preceding the next host belongs to that
			// record; a trailing name still belongs to the current record.
			for next := index + 1; next < len(lines); next++ {
				if strings.TrimSpace(lines[next]) == "" {
					continue
				}
				nextField, _, nextLabeled := sshIntakeLabel(lines[next])
				_, _, endpointErr := sshIntakeEndpoint(lines[next], false)
				_, command := sshIntakeSSHCommand(lines[next])
				boundary = nextLabeled && nextField == "host" || endpointErr == nil || command
				break
			}
		}
		if _, ok := sshIntakeSSHCommand(line); ok {
			boundary = true
		}
		if sshIntakeEndpointLike(line) {
			boundary = true
		}
		if boundary && len(pending) > 0 {
			prior := strings.Join(pending, "\n")
			_, records, priorErr := parseSSHRegistrationSingle(prior)
			if priorErr == nil && len(records) == 1 {
				chunks = append(chunks, prior)
				pending = nil
			} else if priorErr == nil && len(records) == 0 && sshIntakeHasIdentity(pending) && sshIntakeHasIdentity(lines[index:]) {
				// Do not consume the next server's IP as the previous server's
				// missing password, then leak its user/password as task text.
				return fail(sshIntakeRecordError(len(chunks)+1, ErrSSHIntakeInvalid))
			}
		}
		pending = append(pending, line)
		waitingSecret = labeled && value == "" && (field == "password" || field == "passphrase" || field == "private_key")
	}
	chunks = append(chunks, strings.Join(pending, "\n"))
	var inputs []SSHRegistrationInput
	var remaining []string
	for index, chunk := range chunks {
		safe, records, parseErr := parseSSHRegistrationSingle(chunk)
		if parseErr != nil {
			return fail(sshIntakeRecordError(index+1, parseErr))
		}
		if len(records) == 0 && len(chunks) > 1 {
			// Never forward a partially recognized tail after a valid record.
			// It may be the password of a malformed second server.
			return fail(sshIntakeRecordError(index+1, ErrSSHIntakeInvalid))
		}
		inputs = append(inputs, records...)
		if len(inputs) > maxSSHIntakeBatchSize {
			return fail(errSSHIntakeTooMany)
		}
		remaining = append(remaining, strings.ReplaceAll(safe, sshIntakeNotice, ""))
	}
	if len(inputs) == 0 {
		if sshIntakeUnparsedBareCredentials(lines) {
			return fail(ErrSSHIntakeInvalid)
		}
		return content, nil, nil
	}
	secrets := make([]string, 0, 3*len(inputs))
	seen := make(map[string]bool, len(inputs))
	for index, input := range inputs {
		identity := fmt.Sprintf("%s\x00%d\x00%s", input.Host, input.Port, input.Username)
		if seen[identity] {
			return fail(sshIntakeRecordError(index+1, ErrSSHIntakeMultiple))
		}
		seen[identity] = true
		secrets = append(secrets, input.Password, input.PrivateKey, input.KeyPassphrase)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for index, input := range inputs {
			for _, public := range []string{input.Host, input.Username, input.Name, input.HostKeySHA256} {
				if strings.Contains(public, secret) {
					return fail(sshIntakeRecordError(index+1, ErrSSHIntakeInvalid))
				}
			}
		}
	}
	safe := redactSSHOutput(strings.Join(remaining, "\n"), secrets, true)
	return strings.TrimSpace(safe + "\n" + sshIntakeNotice), inputs, nil
}

func sshIntakeCanonicalField(key string) (string, bool) {
	field, _, ok := sshIntakeLabel(strings.TrimSpace(key) + ": x")
	return field, ok
}

func sshIntakeFormatField(field, value string) string {
	if field == "password" || field == "passphrase" {
		encoded, _ := json.Marshal(value)
		return field + ": " + string(encoded)
	}
	return field + ": " + value
}

// JSON is decoded token by token to reject duplicate aliases rather than
// silently overwriting one server's credentials with a second field.
func sshIntakeJSONObject(dec *json.Decoder) (string, error) {
	fields := make(map[string]string)
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return "", ErrSSHIntakeInvalid
		}
		field, known := sshIntakeCanonicalField(key)
		if !known {
			return "", ErrSSHIntakeInvalid
		}
		if _, exists := fields[field]; exists {
			return "", ErrSSHIntakeMultiple
		}
		var value any
		if err := dec.Decode(&value); err != nil {
			return "", ErrSSHIntakeInvalid
		}
		s, ok := value.(string)
		if !ok && field == "port" {
			if number, isNumber := value.(json.Number); isNumber {
				s, ok = number.String(), true
			}
		}
		if !ok || strings.ContainsAny(s, "\r\n") && field != "private_key" {
			return "", ErrSSHIntakeInvalid
		}
		fields[field] = s
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return "", ErrSSHIntakeInvalid
	}
	var out []string
	for _, field := range []string{"name", "host", "port", "username", "password", "private_key", "passphrase", "fingerprint"} {
		if value, ok := fields[field]; ok {
			out = append(out, sshIntakeFormatField(field, value))
		}
	}
	return strings.Join(out, "\n"), nil
}

func sshIntakeJSON(value string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(value))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil {
		return "", ErrSSHIntakeInvalid
	}
	var records []string
	switch token {
	case json.Delim('{'):
		record, err := sshIntakeJSONObject(dec)
		if err != nil {
			return "", err
		}
		records = append(records, record)
	case json.Delim('['):
		for dec.More() {
			if token, err := dec.Token(); err != nil || token != json.Delim('{') {
				return "", ErrSSHIntakeInvalid
			}
			record, err := sshIntakeJSONObject(dec)
			if err != nil {
				return "", err
			}
			records = append(records, record)
			if len(records) > maxSSHIntakeBatchSize {
				return "", errSSHIntakeTooMany
			}
		}
		if token, err := dec.Token(); err != nil || token != json.Delim(']') {
			return "", ErrSSHIntakeInvalid
		}
	default:
		return "", ErrSSHIntakeInvalid
	}
	if _, err := dec.Token(); err != io.EOF || len(records) == 0 {
		return "", ErrSSHIntakeInvalid
	}
	return strings.Join(records, "\n\n"), nil
}

func normalizeSSHIntakePaste(content string) (string, error) {
	lines := strings.Split(content, "\n")
	var out []string
	inKey, waitingSecret := false, false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if inKey {
			out = append(out, line)
			if strings.Contains(trim, "-----END ") && strings.Contains(trim, "PRIVATE KEY-----") {
				inKey = false
			}
			continue
		}
		if waitingSecret && trim != "" && !strings.HasPrefix(trim, "```") {
			out = append(out, line)
			waitingSecret = false
			if sshIntakePEMBegin.MatchString(line) {
				inKey = true
			}
			continue
		}
		if sshIntakePEMBegin.MatchString(line) {
			inKey = true
			out = append(out, line)
			continue
		}
		// Embedded JSON with leading prose is deliberately not guessed, but
		// escaped secret keys must still be caught before reaching a model.
		if !strings.HasPrefix(trim, "{") && !strings.HasPrefix(trim, "[") {
			for offset := 0; offset < len(line); {
				at := strings.IndexAny(line[offset:], "{[")
				if at < 0 {
					break
				}
				offset += at
				candidate := strings.Join(append([]string{line[offset:]}, lines[i+1:]...), "\n")
				dec := json.NewDecoder(strings.NewReader(candidate))
				var raw json.RawMessage
				if decodeErr := dec.Decode(&raw); decodeErr == nil {
					if sshIntakeJSONCredentials(string(raw)) {
						return "", ErrSSHIntakeInvalid
					}
				} else if sshIntakeJSONCredentialTokens(candidate) {
					return "", ErrSSHIntakeInvalid
				}
				offset++
			}
		}
		// A JSON object/array must occupy complete lines; free-form task text
		// may precede or follow it. Noncredential JSON is left untouched.
		if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
			remainder := strings.Join(lines[i:], "\n")
			dec := json.NewDecoder(strings.NewReader(remainder))
			var raw json.RawMessage
			if decodeErr := dec.Decode(&raw); decodeErr == nil {
				candidate := string(raw)
				if sshIntakeSensitive.MatchString(candidate) || sshIntakeJSONCredentials(candidate) {
					consumed := int(dec.InputOffset())
					// A trailing comment belongs on its own line, otherwise a
					// secret suffix could be mistaken for harmless task text.
					tail := remainder[consumed:]
					if newline := strings.IndexByte(tail, '\n'); newline >= 0 {
						tail = tail[:newline]
					}
					if strings.TrimSpace(tail) != "" {
						return "", ErrSSHIntakeInvalid
					}
					normalized, err := sshIntakeJSON(candidate)
					if err != nil {
						return "", err
					}
					out = append(out, normalized)
					i += strings.Count(remainder[:consumed], "\n")
					continue
				}
			} else if sshIntakeJSONCredentialTokens(remainder) {
				return "", ErrSSHIntakeInvalid
			}
		}
		if cells, markdown := sshIntakeCells(line); len(cells) > 1 {
			if columns, header := sshIntakeTableHeader(cells); header {
				rows := 0
				for end := i + 1; end < len(lines); end++ {
					values, isMarkdown := sshIntakeCells(lines[end])
					if len(values) < 2 || markdown != isMarkdown {
						break
					}
					if sshIntakeTableDivider(values) {
						i = end
						continue
					}
					if len(values) != len(columns) {
						return "", ErrSSHIntakeInvalid
					}
					var row []string
					for col, field := range columns {
						if field == "port" && strings.TrimSpace(values[col]) == "" {
							continue
						}
						row = append(row, sshIntakeFormatField(field, values[col]))
					}
					// Keep host before other fields regardless of table order.
					out = append(out, sshIntakeOrderedFields(row))
					rows++
					i = end
				}
				if rows == 0 {
					return "", ErrSSHIntakeInvalid
				}
				continue
			}
			if len(cells) == 2 {
				if field, ok := sshIntakeCanonicalField(cells[0]); ok {
					out = append(out, sshIntakeFormatField(field, cells[1]))
					continue
				}
			}
			if markdown && sshIntakeTableDivider(cells) {
				continue
			}
		}
		// Numbering and bullets are formatting only for explicit endpoints;
		// passwords are never trimmed or stripped as list items.
		candidate := sshIntakeNumberedLine.ReplaceAllString(line, "")
		if _, _, err := sshIntakeEndpoint(candidate, false); err == nil {
			line, trim = candidate, strings.TrimSpace(candidate)
		}
		if field, value, ok := sshIntakeLabel(line); ok {
			out = append(out, line)
			waitingSecret = value == "" && (field == "password" || field == "passphrase" || field == "private_key")
			continue
		}
		if match := sshIntakeInline.FindStringSubmatch(strings.TrimLeft(candidate, " \t")); match != nil {
			if _, _, err := sshIntakeEndpoint(match[1], false); err != nil && sshIntakeEndpointLike(match[1]) && !sshIntakeTaskVerb(match[3]) {
				return "", ErrSSHIntakeInvalid
			}
			if _, _, err := sshIntakeEndpoint(match[1], false); err == nil && !sshIntakeTaskVerb(match[3]) {
				row := "host: " + match[1] + "\nusername: " + match[3] + "\n" + sshIntakeFormatField("password", match[4])
				if match[2] != "" {
					if port, err := strconv.Atoi(match[2]); err != nil || port < 1 || port > 65535 {
						return "", ErrSSHIntakeInvalid
					}
					row += "\nport: " + match[2]
				}
				out = append(out, row)
				continue
			}
		}
		// Space-delimited labels are accepted only when the leading token is
		// a known field; an ordinary sentence remains ordinary task text.
		spaced := strings.TrimLeft(line, " \t")
		if at := strings.IndexAny(spaced, " \t"); at > 0 {
			if field, ok := sshIntakeCanonicalField(spaced[:at]); ok {
				value := spaced[at+1:]
				out = append(out, field+": "+value)
				waitingSecret = value == "" && (field == "password" || field == "passphrase" || field == "private_key")
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), nil
}

func sshIntakeOrderedFields(lines []string) string {
	var name, host, rest []string
	for _, line := range lines {
		field, _, _ := sshIntakeLabel(line)
		switch field {
		case "name":
			name = append(name, line)
		case "host":
			host = append(host, line)
		default:
			rest = append(rest, line)
		}
	}
	return strings.Join(append(append(name, host...), rest...), "\n")
}

func sshIntakeCells(line string) ([]string, bool) {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "|") && strings.HasSuffix(trim, "|") {
		cells := strings.Split(trim[1:len(trim)-1], "|")
		for i := range cells {
			cells[i] = strings.TrimSuffix(strings.TrimPrefix(cells[i], " "), " ")
		}
		return cells, true
	}
	if strings.Contains(line, "\t") {
		return strings.Split(line, "\t"), false
	}
	return nil, false
}

func sshIntakeTableHeader(cells []string) ([]string, bool) {
	columns := make([]string, len(cells))
	seen := make(map[string]bool)
	for i, cell := range cells {
		field, ok := sshIntakeCanonicalField(cell)
		if !ok || seen[field] {
			return nil, false
		}
		seen[field] = true
		columns[i] = field
	}
	return columns, seen["host"] && seen["username"] && (seen["password"] || seen["private_key"])
}

func sshIntakeTableDivider(cells []string) bool {
	for _, cell := range cells {
		if strings.Trim(strings.TrimSpace(cell), ":-") != "" || !strings.Contains(cell, "-") {
			return false
		}
	}
	return true
}

// Inspect decoded JSON keys too, so escaped field names cannot evade intake.
func sshIntakeJSONCredentials(content string) bool {
	var root any
	if json.Unmarshal([]byte(content), &root) != nil {
		return false
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch value := value.(type) {
		case map[string]any:
			for key, item := range value {
				field, _ := sshIntakeCanonicalField(key)
				if field == "password" || field == "private_key" || field == "passphrase" || contains(item) {
					return true
				}
			}
		case []any:
			for _, item := range value {
				if contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(root)
}

// HasIdentity only considers the first endpoint and the fields immediately
// following it. It is a boundary ambiguity check, never a credential guess.
func sshIntakeHasIdentity(lines []string) bool {
	hostSeen := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "```") {
			continue
		}
		field, _, labeled := sshIntakeLabel(line)
		if !hostSeen {
			endpointLike := sshIntakeEndpointLike(line)
			if command, ok := sshIntakeSSHCommand(line); ok {
				return command.Username != ""
			}
			if labeled && field == "host" || endpointLike {
				hostSeen = true
			}
			continue
		}
		if labeled {
			if field == "username" {
				return true
			}
			if field == "port" || field == "name" {
				continue
			}
			return false
		}
		if port, err := strconv.Atoi(trim); err == nil && port > 0 && port <= 65535 {
			continue
		}
		if sshIntakeEndpointLike(trim) {
			return false
		}
		return sshUserRE.MatchString(trim)
	}
	return false
}

// Tokenize malformed JSON as far as possible. This still recognizes escaped
// credential field names in an incomplete paste and fails closed locally.
func sshIntakeJSONCredentialTokens(content string) bool {
	dec := json.NewDecoder(strings.NewReader(content))
	for {
		token, err := dec.Token()
		if err != nil {
			return false
		}
		if key, ok := token.(string); ok {
			field, _ := sshIntakeCanonicalField(key)
			if field == "password" || field == "private_key" || field == "passphrase" {
				return true
			}
		}
	}
}

func sshIntakeUnparsedBareCredentials(lines []string) bool {
	for index, line := range lines {
		if !sshIntakeEndpointLike(line) {
			continue
		}
		var next []string
		for _, candidate := range lines[index+1:] {
			if strings.TrimSpace(candidate) == "" || strings.HasPrefix(strings.TrimSpace(candidate), "```") {
				continue
			}
			next = append(next, candidate)
			if len(next) == 3 {
				break
			}
		}
		if len(next) < 2 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(next[0])); err == nil && len(next) > 2 {
			if sshIntakeBarePassword(next[2]) {
				return true
			}
		} else if sshIntakeBarePassword(next[1]) {
			return true
		}
	}
	return false
}

type sshIntakeRecordValidationError struct {
	index int
	cause error
}

func (e *sshIntakeRecordValidationError) Error() string {
	return fmt.Sprintf("第 %d 条 SSH 资料：%s", e.index, sshIntakeFixedError(e.cause).Error())
}

func (e *sshIntakeRecordValidationError) Unwrap() error { return e.cause }

func sshIntakeFixedError(err error) error {
	if errors.Is(err, errSSHIntakeTooMany) {
		return errSSHIntakeTooMany
	}
	if errors.Is(err, ErrSSHIntakeMultiple) {
		return ErrSSHIntakeMultiple
	}
	return ErrSSHIntakeInvalid
}

func sshIntakeRecordError(index int, err error) error {
	return &sshIntakeRecordValidationError{index: index, cause: sshIntakeFixedError(err)}
}

// SSHIntakeErrorMessage allows HTTP and Telegram to preserve safe record
// indices and batch-limit hints without reflecting any arbitrary error text.
func SSHIntakeErrorMessage(err error) (string, bool) {
	var record *sshIntakeRecordValidationError
	if errors.As(err, &record) && record != nil && record.index > 0 && record.index <= maxSSHIntakeBatchSize+1 {
		return record.Error(), true
	}
	for _, fixed := range []error{errSSHIntakeTooMany, ErrSSHIntakeMultiple, ErrSSHIntakeInvalid} {
		if errors.Is(err, fixed) {
			return fixed.Error(), true
		}
	}
	return "", false
}

func sshIntakeTaskVerb(value string) bool {
	switch strings.ToLower(value) {
	case "please", "check", "inspect", "ping", "traceroute", "curl", "telnet":
		return true
	}
	return false
}

// An otherwise valid host with an invalid port remains credential-shaped.
// Validation must fail locally rather than passing its nearby password on as
// ordinary chat. This helper never turns a malformed endpoint into a server.
func sshIntakeEndpointLike(value string) bool {
	value = strings.TrimSpace(sshIntakeNumberedLine.ReplaceAllString(value, ""))
	if _, _, err := sshIntakeEndpoint(value, false); err == nil {
		return true
	}
	if strings.ContainsAny(value, " \t/\\") {
		return false
	}
	var host string
	if strings.HasPrefix(value, "[") {
		end := strings.Index(value, "]:")
		if end <= 1 {
			return false
		}
		host = value[1:end]
	} else if strings.Count(value, ":") == 1 {
		host = strings.SplitN(value, ":", 2)[0]
	} else {
		return false
	}
	_, _, err := sshIntakeEndpoint(host, false)
	return err == nil
}
