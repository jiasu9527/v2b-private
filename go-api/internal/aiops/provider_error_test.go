package aiops

import (
	"strings"
	"testing"
)

func TestProviderErrorsClassifiedWithoutEchoingSecrets(t *testing.T) {
	for _, test := range []struct {
		name, body, contains string
		status               int
	}{
		{"model unavailable", `{"error":{"code":"model_not_found","message":"secret-provider-key upstream account details"}}`, "当前 API Key 分组不可用", 404},
		{"model unavailable 400", `{"error":{"code":"model_not_available"}}`, "当前 API Key 分组不可用", 400},
		{"invalid key", `{"error":{"message":"secret-provider-key"}}`, "鉴权失败（401）", 401},
		{"firewall", `<html>secret-provider-key</html>`, "拒绝访问（403）", 403},
		{"quota", `{"error":{"code":"insufficient_quota","message":"secret-provider-key"}}`, "额度不足", 429},
		{"quota type", `{"error":{"type":"insufficient_quota"}}`, "额度不足", 429},
		{"rate limit", `{"error":{"code":"rate_limit_exceeded"}}`, "请求受限（429）", 429},
		{"wrong endpoint", `<html>secret-provider-key</html>`, "接口或模型不存在（404）", 404},
		{"redirect", `secret-provider-key`, "已拒绝转发 Key", 307},
		{"upstream", `{"error":{"message":"secret-provider-key"}}`, "上游模型暂时异常", 502},
		{"parameter", `{"error":{"code":"unsupported_parameter","message":"secret-provider-key"}}`, "不接受请求参数", 400},
		{"unexpected shape", `{"error":"secret-provider-key"}`, "返回错误", 418},
		{"oversized", strings.Repeat("secret-provider-key", 1000), "接口或模型不存在（404）", 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := providerResponseError(test.status, strings.NewReader(test.body))
			if err == nil || !strings.Contains(err.Error(), test.contains) || strings.Contains(err.Error(), "secret-provider-key") {
				t.Fatalf("unsafe or unhelpful error: %v", err)
			}
		})
	}
}
