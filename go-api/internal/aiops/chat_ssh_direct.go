package aiops

import "strings"

// The original paste is available only to the active model turn. The visible
// transcript does not become a second, long-lived password store.
const sshDirectHistoryNotice = "[本轮 SSH 登录资料已加密收取；历史对话不重复提供密码或私钥。]"

func sshDirectCredentialPaste(content string) bool {
	if knowledgeScriptSaveIntent(content) {
		return false
	}
	if sshIntakeSensitive.MatchString(content) || sshIntakeBareAfterHost.MatchString(content) {
		return true
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if sshIntakeUnparsedBareCredentials(lines) || sshIntakeUnparsedBareIdentityCredential(lines) {
		return true
	}
	// With an explicit registration request, also protect unusual unlabeled
	// layouts. This is only a storage classification, never a rejection gate.
	return knowledgeChatSSHRegister.MatchString(content) || sshIntakeLooseRegisterIntent.MatchString(content)
}

func (s *Service) protectAIChatInput(content string) (display, cipher string, err error) {
	if sshDirectCredentialPaste(content) {
		cipher, err = s.encryptSecret(content)
		if err != nil {
			return "", "", err
		}
		return sshDirectHistoryNotice, cipher, nil
	}
	return s.protectKnowledgeChatInput(content)
}
