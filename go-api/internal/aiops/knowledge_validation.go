package aiops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Retained for compatibility with old integrations; knowledge body writes no
// longer reject credentials. They are encrypted before being persisted.
var ErrKnowledgeCredential = fmt.Errorf("%w：知识库不能保存密码、Token、API Key 或私钥，请删除凭据后再保存", ErrValidation)

const knowledgeProtectedValue = "[凭据已保护]"

var (
	knowledgeSecretAssignment    = regexp.MustCompile(`(?i)((?:\b(?:[a-z][a-z0-9]*[_-])*(?:password|passwd|pwd|passphrase|api[_ -]?key|private[_ -]?key|access[_ -]?token|refresh[_ -]?token|secret[_ -]?key|client[_ -]?secret|token|secret|key)\b|密码|口令|私钥|密钥)["'\x60]*[ \t]*(?:[:=：]|\bis\b|是)[ \t]*)(?:"([^"\r\n]*)"|'([^'\r\n]*)'|\x60([^\x60\r\n]*)\x60|([^\s,;，；&]+))`)
	knowledgeSecretFlag          = regexp.MustCompile(`(?i)(--?(?:api[_-]?key|private[_-]?key|access[_-]?token|token|password|passwd|passphrase|secret|key)[ \t]+)(?:"([^"\r\n]*)"|'([^'\r\n]*)'|([^\s,;]+))`)
	knowledgeSecretAuthorization = regexp.MustCompile(`(?i)(\b(?:Bearer|Basic)[ \t]+)([^\s"'\x60,;]+)`)
	knowledgeSecretURL           = regexp.MustCompile(`(?i)(\b(?:https?|ssh|mysql|postgres(?:ql)?|redis)://[^\s/@:]+:)([^\s/@]+)(@)`)
	knowledgeSecretQuery         = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|token|key|password|secret)=)([^\s&"'\x60]+)`)
	knowledgePrivateKey          = regexp.MustCompile(`(?is)-----BEGIN (?:[A-Z0-9 ]*PRIVATE KEY)-----.*?(?:-----END (?:[A-Z0-9 ]*PRIVATE KEY)-----|\z)`)
	knowledgeVariable            = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*|\{[A-Za-z_][A-Za-z0-9_]*\})$`)
	knowledgePlaceholder         = regexp.MustCompile(`(?i)^(?:<[^<>\r\n]+>|\{\{[^{}\r\n]+\}\}|YOUR[_-][A-Z0-9_-]+|REPLACE[_-]ME|CHANGE[_-]?ME|REDACTED|\*{3,}|\[凭据已保护\])$`)
)

func knowledgeCredentialPlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || knowledgeVariable.MatchString(value) || knowledgePlaceholder.MatchString(value)
}

// redactKnowledgeText builds searchable/displayable metadata. It never mutates
// the original body returned by authorized knowledge reads. Shell placeholders
// are not secrets and must remain usable in installation templates.
func redactKnowledgeText(text string) string {
	out := knowledgePrivateKey.ReplaceAllString(text, knowledgeProtectedValue)
	for _, pattern := range []*regexp.Regexp{knowledgeSecretAssignment, knowledgeSecretFlag, knowledgeSecretAuthorization, knowledgeSecretURL, knowledgeSecretQuery} {
		out = pattern.ReplaceAllStringFunc(out, func(match string) string {
			parts := pattern.FindStringSubmatchIndex(match)
			// Group one is the parameter prefix; the first populated group
			// after it contains the value (including quoted alternatives).
			for i := 4; i+1 < len(parts); i += 2 {
				if parts[i] < 0 {
					continue
				}
				value := match[parts[i]:parts[i+1]]
				if knowledgeCredentialPlaceholder(value) {
					return match
				}
				return match[:parts[i]] + knowledgeProtectedValue + match[parts[i+1]:]
			}
			return match
		})
	}
	// The same literal can be repeated elsewhere in a script (for example an
	// echo or an installation command without a named flag). Protect those
	// repetitions as well, not just the original key assignment.
	return redactKnownKnowledgeSecrets(out, knowledgeTextSecrets(text))
}

func redactKnownKnowledgeSecrets(text string, secrets []string) string {
	values := append([]string(nil), secrets...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if utf8.RuneCountInString(value) >= 4 {
			text = strings.ReplaceAll(text, value, knowledgeProtectedValue)
		}
	}
	return text
}

func knowledgeContentHasCredentials(text string) bool {
	return redactKnowledgeText(text) != text
}

func validateKnowledgeContent(fields ...string) error {
	for _, text := range fields {
		if knowledgeContentHasCredentials(text) {
			return ErrKnowledgeCredential
		}
	}
	return nil
}

// knowledgeTextSecrets exposes only explicitly recognized credential values to
// in-memory response redaction. Never persist this collection as metadata.
func knowledgeTextSecrets(text string) []string {
	var secrets []string
	seen := map[string]bool{}
	add := func(value string) {
		if !knowledgeCredentialPlaceholder(value) && !seen[value] {
			seen[value] = true
			secrets = append(secrets, value)
		}
	}
	for _, value := range knowledgePrivateKey.FindAllString(text, -1) {
		add(value)
	}
	for _, pattern := range []*regexp.Regexp{knowledgeSecretAssignment, knowledgeSecretFlag, knowledgeSecretAuthorization, knowledgeSecretURL, knowledgeSecretQuery} {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			for _, value := range match[2:] {
				if value != "" {
					add(value)
					break
				}
			}
		}
	}
	return secrets
}

// A paginated read may start inside a key or PEM block. Include the intersecting
// fragment for output protection too, without sending full keys outside the
// requested body page to the model. Offsets are validated Unicode boundaries.
func knowledgePageSecrets(content string, start, end int) []string {
	chars := []rune(content)
	if start < 0 || end < start || end > len(chars) {
		return nil
	}
	secrets := knowledgeTextSecrets(content)
	startByte, endByte := len(string(chars[:start])), len(string(chars[:end]))
	seen := map[string]bool{}
	for _, secret := range secrets {
		seen[secret] = true
	}
	var fragments []string
	for _, secret := range secrets {
		for offset := 0; offset < len(content); {
			index := strings.Index(content[offset:], secret)
			if index < 0 {
				break
			}
			index += offset
			left, right := max(index, startByte), min(index+len(secret), endByte)
			if left < right {
				fragment := content[left:right]
				// Tiny intersections cannot identify a credential reliably;
				// do not erase every occurrence of an ordinary letter.
				if utf8.RuneCountInString(fragment) >= 4 && !seen[fragment] {
					seen[fragment] = true
					fragments = append(fragments, fragment)
				}
			}
			offset = index + len(secret)
		}
	}
	return append(secrets, fragments...)
}
