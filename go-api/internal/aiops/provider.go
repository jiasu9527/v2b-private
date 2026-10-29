package aiops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

type providerHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func newProviderHTTPClient() *http.Client {
	return &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type providerMessage struct {
	Role        string     `json:"role"`
	Content     string     `json:"content"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	contextKind string
}
type completionRequest struct {
	Model      string            `json:"model"`
	Messages   []providerMessage `json:"messages"`
	Tools      []ToolDefinition  `json:"tools,omitempty"`
	ToolChoice string            `json:"tool_choice,omitempty"`
	Stream     bool              `json:"stream"`
	MaxTokens  int               `json:"max_tokens"`
}
type completionResponse struct {
	Choices []struct {
		Message struct {
			Role      string     `json:"role"`
			Content   *string    `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (s *Service) complete(ctx context.Context, model string, messages []providerMessage) (providerMessage, error) {
	return s.completeStream(ctx, model, messages, nil)
}

// onContent receives the accumulated public answer, never reasoning or tool
// arguments. Tool requests are returned only after a complete valid response.
func (s *Service) completeStream(ctx context.Context, model string, messages []providerMessage, onContent func(string) error) (providerMessage, error) {
	var empty providerMessage
	cfg, cipher, err := s.loadConfig(ctx)
	if err != nil || !cfg.Enabled {
		return empty, errors.New("AI 运维未启用")
	}
	allowed := false
	for _, m := range cfg.Models {
		if m == model {
			allowed = true
		}
	}
	if !allowed {
		return empty, errors.New("所选模型不在允许列表中")
	}
	key, err := s.decryptSecret(cipher)
	if err != nil || key == "" {
		return empty, errors.New("AI 中转密钥不可用，请重新配置")
	}
	base, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil || base == "" {
		return empty, errors.New("AI 中转地址无效")
	}
	request := completionRequest{Model: model, Messages: []providerMessage{}, Tools: s.toolDefinitions(), ToolChoice: "auto", Stream: true, MaxTokens: 4096}
	overhead, err := json.Marshal(request)
	if err != nil {
		return empty, errors.New("无法编码 AI 请求")
	}
	request.Messages, err = compactProviderContext(messages, min(maxHistoryContextBytes, maxProviderRequestBytes-len(overhead)))
	if err != nil {
		return empty, err
	}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > maxProviderRequestBytes {
		return empty, errContextTooLarge
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return empty, errors.New("无法创建 AI 请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return empty, errors.New("AI 中转请求失败或超时，请检查中转配置")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, providerResponseError(resp.StatusCode, resp.Body)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return readCompletionStream(ctx, resp.Body, onContent)
	}
	// Some compatible relays ignore stream=true. Accept their single JSON
	// response without making a second paid request or changing the model.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (512<<10)+1))
	if err != nil || len(raw) > 512<<10 {
		return empty, errors.New("AI 响应过大或读取失败")
	}
	var result completionResponse
	if json.Unmarshal(raw, &result) != nil || len(result.Choices) != 1 {
		return empty, errors.New("AI 响应格式无效")
	}
	choice := result.Choices[0]
	out := providerMessage{Role: "assistant", ToolCalls: choice.Message.ToolCalls}
	if choice.Message.Content != nil {
		out.Content = *choice.Message.Content
	}
	if choice.FinishReason != "" && choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return empty, errors.New("AI 响应未完整结束，未执行工具；请检查模型输出限制")
	}
	if err = validateCompletion(out); err != nil {
		return empty, err
	}
	if onContent != nil && out.Content != "" {
		if err = onContent(out.Content); err != nil {
			return empty, errors.New("AI 回复更新已中止")
		}
	}
	return out, nil
}

func validateCompletion(out providerMessage) error {
	if len(out.Content) > 32000 || len(out.ToolCalls) > 8 {
		return errors.New("AI 响应超出单轮限制")
	}
	seen := map[string]bool{}
	for _, call := range out.ToolCalls {
		if call.ID == "" || len(call.ID) > 200 || seen[call.ID] || call.Type != "function" || call.Function.Name == "" || len(call.Function.Name) > 100 || len(call.Function.Arguments) > 16000 || !json.Valid([]byte(call.Function.Arguments)) || !strings.HasPrefix(strings.TrimSpace(call.Function.Arguments), "{") {
			return errors.New("AI 工具请求格式无效")
		}
		seen[call.ID] = true
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return errors.New("AI 未返回有效内容")
	}
	return nil
}

// Relay error messages can echo credentials, request content or HTML. Inspect
// only bounded machine-readable codes and return fixed local messages, never
// the upstream body. No error is retried automatically or changes the model.
func providerResponseError(status int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 8193))
	var envelope struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if len(raw) <= 8192 {
		_ = json.Unmarshal(raw, &envelope)
	}
	code, kind := envelope.Error.Code, envelope.Error.Type
	switch {
	case status == http.StatusUnauthorized:
		return errors.New("AI 中转鉴权失败（401），请检查 API Key 是否正确、有效")
	case status == http.StatusForbidden:
		return errors.New("AI 中转拒绝访问（403），请检查 Key 权限、IP 白名单或中转防火墙")
	case code == "model_not_found" || code == "model_not_available":
		return errors.New("所选模型在当前 API Key 分组不可用，请在中转开通该模型或手动选择已授权模型")
	case code == "insufficient_quota" || kind == "insufficient_quota" || code == "quota_exceeded":
		return errors.New("AI 中转额度不足，请检查当前 Key 分组的余额或额度")
	case status == http.StatusTooManyRequests:
		return errors.New("AI 中转请求受限（429），请稍后重试或检查分组额度")
	case status == http.StatusNotFound:
		return errors.New("AI 中转接口或模型不存在（404），请核对 Base URL 和模型 ID")
	case status >= 300 && status < 400:
		return errors.New("AI 中转要求重定向，已拒绝转发 Key；请填写最终 API 地址")
	case status >= 500:
		return errors.New("AI 中转或上游模型暂时异常（5xx），请稍后核对服务状态")
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return errors.New("AI 中转不接受请求参数，请确认模型支持 Chat Completions 和工具调用")
	default:
		return errors.New("AI 中转返回错误，请检查密钥、模型权限或额度")
	}
}
