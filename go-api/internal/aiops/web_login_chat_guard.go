package aiops

import (
	"errors"
	"regexp"
	"strings"
)

var ErrWebLoginChat = errors.New("识别到可能包含网页登录资料。请在 HTTPS 网页后台的 AI 运维对话里发送；这条消息未转给模型")

var (
	webChatURL          = regexp.MustCompile(`(?i)https?://[^\s，；。！？、,;<>"'\x60“”‘’「」]+`)
	webChatUserInfo     = regexp.MustCompile(`(?i)https?://[^/?#\s，；。！？、,;<>"']+@`)
	webChatExplicit     = regexp.MustCompile(`(?i)^\s*/?\s*(?:网页登录|登录网页|浏览器登录|browser[ \t]+login|web[ \t]+login)(?:[\s:：,，。]|https?://|$)`)
	webChatHeading      = regexp.MustCompile(`(?i)^[ \t]*/?[ \t]*(?:网页登录|登录网页|浏览器登录|browser[ \t]+login|web[ \t]+login)[ \t:：,，。]*$`)
	webChatLoginIntent  = regexp.MustCompile(`(?i)登录|登陆|登入|登錄|\b(?:log|sign)[ -]+in\b`)
	webChatUserLabel    = regexp.MustCompile(`(?i)(?:^|[\s,，;；])(?:登录用户名|登录账号|登陆账号|用户名|账户名|账号|帐号|账户|username|user name|account)(?:[ \t:=：]|是|$)`)
	webChatPassLabel    = regexp.MustCompile(`(?i)(?:^|[\s,，;；])(?:网页登录密码|登录密码|登陆密码|登录口令|密码|口令|password|passwd|pwd)(?:[ \t:=：]|是|$)`)
	webChatSSH          = regexp.MustCompile(`(?i)^\s*(?:(?:麻烦|请|帮我|给我|用|把|需要|我要|我想|先)[ \t]*)*(?:(?:通过|登录|登陆|连接|连上|登记|导入|添加|录入|注册|配置|部署|检查|查看)[ \t]*)*\b(?:ssh|sftp|scp)\b`)
	webChatPossiblyPair = regexp.MustCompile(`^(?:[^\s]+|"[^"]+"|'[^']+'|“[^”]+”|「[^」]+」)$`)
)

// This is a refusal-only backstop for old clients and direct chat callers.
// It does not extract, store, log, or try to use credentials. The administrator
// UI routes supported messages to the separate read-only endpoint.
func containsWebLoginChatCredentials(content string) bool {
	content = strings.TrimSpace(content)
	if webChatUserInfo.MatchString(content) {
		return true
	}
	explicit := webChatExplicit.MatchString(content)
	if !explicit && (!webChatURL.MatchString(content) || webChatSSH.MatchString(content)) {
		return false
	}
	user, pass := webChatUserLabel.MatchString(content), webChatPassLabel.MatchString(content)
	if (pass && (user || explicit || webChatLoginIntent.MatchString(content))) || (explicit && user) {
		return true
	}

	// Unlabeled pastes: recognize a link after a login instruction or by
	// itself on the first line, followed by two short credential-like lines.
	lines := strings.FieldsFunc(content, func(r rune) bool { return r == '\r' || r == '\n' })
	fields := make([]string, 0, 3)
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" && !(len(fields) == 0 && webChatHeading.MatchString(line)) {
			fields = append(fields, line)
			if len(fields) == 3 {
				break
			}
		}
	}
	if len(fields) != 3 || webChatURL.MatchString(fields[1]) || webChatURL.MatchString(fields[2]) {
		return false
	}
	at := webChatURL.FindStringIndex(fields[0])
	if at == nil || (at[0] != 0 && !explicit && !webChatLoginIntent.MatchString(fields[0][:at[0]]) &&
		!(strings.HasPrefix(fields[0], "[") && strings.HasSuffix(fields[0][:at[0]], "]("))) {
		return false
	}
	return (webChatPossiblyPair.MatchString(fields[1]) || webChatUserLabel.MatchString(fields[1])) &&
		(webChatPossiblyPair.MatchString(fields[2]) || webChatPassLabel.MatchString(fields[2]))
}
