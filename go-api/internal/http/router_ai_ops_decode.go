package httpapi

import (
	"mime"
	"net/http"
)

// SSH bulk paste needs more space than the general 32 KiB support decoder.
// Apply only to chat/send; keep strict field and trailing-value validation.
func decodeAIOpsChatJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeJSON(w, 415, map[string]any{"message": "Content-Type must be application/json"})
		return false
	}
	// 128 KiB decoded content plus JSON escaping (up to six bytes/byte) and
	// envelope. SendMessage independently enforces the decoded UTF-8 limit.
	if err := decodeDNSProbeJSON(w, r, 1<<20, target); err != nil {
		writeJSON(w, 400, map[string]any{"message": "请求格式无效或过大，仅接受声明字段的 JSON 对象"})
		return false
	}
	return true
}
