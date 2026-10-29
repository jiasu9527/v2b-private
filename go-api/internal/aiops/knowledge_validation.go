package aiops

import (
	"fmt"
	"regexp"
	"strings"
)

var ErrKnowledgeCredential = fmt.Errorf("%w：知识库不能保存密码、Token、API Key 或私钥，请删除凭据后再保存", ErrValidation)

// Match explicit secret values, not architectural discussion of credential
// handling. Network addresses, ports and public host fingerprints are allowed.
var knowledgeCredentialValue = regexp.MustCompile(`(?i)(?:\b(?:[a-z][a-z0-9]*_)*(?:password|passwd|pwd|passphrase|api[_ -]?key|private[_ -]?key|access[_ -]?token|refresh[_ -]?token|secret[_ -]?key|client[_ -]?secret|token)\b|密码|口令|私钥|密钥)["'\x60]*[ \t]*(?:[:=：]|\bis\b|是)[ \t]*["'\x60]*[^\s"'\x60,;，；}]+|\b(?:Bearer|Basic)[ \t]+[A-Za-z0-9._~+/-]{8,}=*|\b(?:https?|ssh|mysql|postgres(?:ql)?|redis)://[^\s/@:]+:[^\s/@]+@|[?&](?:token|key|password)=[^\s&]+`)

func validateKnowledgeContent(fields ...string) error {
	for _, text := range fields {
		if sshIntakePEMBegin.MatchString(text) || strings.Contains(strings.ToUpper(text), "-----BEGIN OPENSSH PRIVATE KEY") || knowledgeCredentialValue.MatchString(text) {
			return ErrKnowledgeCredential
		}
	}
	return nil
}
