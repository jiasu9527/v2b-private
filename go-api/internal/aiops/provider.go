package aiops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

type providerHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

var errProviderContentUpdate = errors.New("AI 回复更新已中止")

// Bound a single parallel batch independently of a relay's tool index labels.
// This bounds result amplification; full-access tasks can continue in more batches.
const maxCompletionToolCalls = 64

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
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Model           string            `json:"model"`
	Messages        []providerMessage `json:"messages"`
	Tools           []ToolDefinition  `json:"tools,omitempty"`
	ToolChoice      string            `json:"tool_choice,omitempty"`
	Stream          bool              `json:"stream"`
	MaxTokens       int               `json:"max_tokens"`
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
	return s.completeStreamWithReasoning(ctx, model, "auto", messages, onContent)
}

// reasoningEffort is already resolved and persisted on the turn. Reloading
// connection settings below must never alter it between tool rounds.
func (s *Service) completeStreamWithReasoning(ctx context.Context, model, reasoningEffort string, messages []providerMessage, onContent func(string) error) (providerMessage, error) {
	var empty providerMessage
	if !validReasoningEffort(reasoningEffort) {
		return empty, ErrValidation
	}
	if ctx.Err() != nil {
		return empty, errProviderCanceled
	}
	cfg, endpoints, err := s.loadProviderCandidates(ctx, model)
	if err != nil {
		if ctx.Err() != nil {
			return empty, errProviderCanceled
		}
		return empty, errors.New("AI 中转配置读取失败，请检查后台配置和数据库状态")
	}
	if !cfg.Enabled {
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
	request := completionRequest{Model: model, Messages: []providerMessage{}, Tools: s.toolDefinitions(), ToolChoice: "auto", Stream: true, MaxTokens: 4096}
	if reasoningEffort != "auto" {
		request.ReasoningEffort = reasoningEffort
	}
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
	return s.completeWithRelays(ctx, cfg, endpoints, model, payload, onContent)
}

// Each attempt receives the same encoded request. A failed relay can never
// dispatch a tool: only a completely validated response escapes this function.
func (s *Service) completeAtRelay(ctx context.Context, endpoint RelayEndpoint, payload []byte, onContent func(string) error) (providerMessage, error) {
	var empty providerMessage
	key, err := s.decryptSecret(endpoint.APIKeyCipher)
	if err != nil || key == "" {
		return empty, retryableProviderError("AI 中转密钥不可用，请重新配置")
	}
	base, err := normalizeBaseURL(endpoint.BaseURL)
	if err != nil || base == "" {
		return empty, retryableProviderError("AI 中转地址无效")
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
		return empty, retryableProviderError("AI 中转请求失败或超时，请检查中转配置")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, providerResponseError(resp.StatusCode, resp.Body)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		out, err := readCompletionStream(ctx, resp.Body, onContent)
		if err != nil && !errors.Is(err, errProviderContentUpdate) {
			return empty, retryableProviderError(err.Error())
		}
		return out, err
	}
	// Some compatible relays ignore stream=true. Accept their single JSON
	// response without making a second paid request or changing the model.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (512<<10)+1))
	if err != nil || len(raw) > 512<<10 {
		return empty, retryableProviderError("AI 响应过大或读取失败")
	}
	var result completionResponse
	if json.Unmarshal(raw, &result) != nil || len(result.Choices) != 1 {
		return empty, retryableProviderError("AI 响应格式无效")
	}
	choice := result.Choices[0]
	out := providerMessage{Role: "assistant", ToolCalls: choice.Message.ToolCalls}
	if choice.Message.Content != nil {
		out.Content = *choice.Message.Content
	}
	if choice.FinishReason != "" && choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return empty, retryableProviderError("AI 响应未完整结束，未执行工具；请检查模型输出限制")
	}
	if err = validateCompletion(out); err != nil {
		return empty, retryableProviderError(err.Error())
	}
	if ctx.Err() != nil {
		return empty, retryableProviderError("AI 中转请求失败或超时，请检查中转配置")
	}
	if onContent != nil && out.Content != "" {
		if err = onContent(out.Content); err != nil {
			return empty, errProviderContentUpdate
		}
	}
	return out, nil
}

func validateCompletion(out providerMessage) error {
	if len(out.Content) > 32000 {
		return errors.New("AI 响应超出单轮限制")
	}
	if len(out.ToolCalls) > maxCompletionToolCalls {
		return errors.New("AI 单次回复的工具调用过多，未执行工具；请分批操作后继续")
	}
	seen := map[string]bool{}
	for _, call := range out.ToolCalls {
		if len(call.Function.Arguments) > providerToolArgumentLimit(call.Function.Name) {
			return errors.New("AI 工具参数过长，未执行工具；请缩小单次参数或分批操作")
		}
		if call.ID == "" || len(call.ID) > 200 || seen[call.ID] || call.Type != "function" || call.Function.Name == "" || len(call.Function.Name) > 100 || !json.Valid([]byte(call.Function.Arguments)) || !strings.HasPrefix(strings.TrimSpace(call.Function.Arguments), "{") {
			return errors.New("AI 工具请求格式无效")
		}
		seen[call.ID] = true
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return errors.New("AI 未返回有效内容")
	}
	return nil
}

func providerToolArgumentLimit(name string) int {
	if name == "panel_query" || name == "propose_panel_operation" || name == "knowledge_save" || name == "knowledge_create" {
		return 32768
	}
	return 16000
}

// Relay error messages can echo credentials, request content or HTML. Inspect
// only bounded machine-readable codes and return fixed local messages, never
// the upstream body. Only classified availability failures may switch relay;
// invalid request parameters and redirects must stop without another request.
func providerResponseError(status int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 8193))
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Param   string `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(raw) <= 8192 {
		_ = json.Unmarshal(raw, &envelope)
	}
	code, kind := envelope.Error.Code, envelope.Error.Type
	switch {
	case status >= 300 && status < 400:
		return errors.New("AI 中转要求重定向，已拒绝转发 Key；请填写最终 API 地址")
	case status == http.StatusUnauthorized:
		return retryableProviderError("AI 中转鉴权失败（401），请检查 API Key 是否正确、有效")
	case status == http.StatusForbidden:
		return retryableProviderError("AI 中转拒绝访问（403），请检查 Key 权限、IP 白名单或中转防火墙")
	case code == "model_not_found" || code == "model_not_available":
		return retryableProviderError("所选模型在当前 API Key 分组不可用，请在中转开通该模型或手动选择已授权模型")
	case code == "insufficient_quota" || kind == "insufficient_quota" || code == "quota_exceeded":
		return retryableProviderError("AI 中转额度不足，请检查当前 Key 分组的余额或额度")
	case status == http.StatusTooManyRequests:
		return retryableProviderError("AI 中转请求受限（429），请稍后重试或检查分组额度")
	case status == http.StatusNotFound:
		return retryableProviderError("AI 中转接口或模型不存在（404），请核对 Base URL 和模型 ID")
	case status == http.StatusRequestTimeout:
		return retryableProviderError("AI 中转请求超时（408），请稍后重试")
	case status >= 500:
		return retryableProviderError(fmt.Sprintf("AI 中转或上游模型暂时异常（HTTP %d），请稍后核对中转服务状态", status))
	case (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) && (envelope.Error.Param == "reasoning_effort" || strings.Contains(strings.ToLower(envelope.Error.Message), "reasoning_effort") || strings.Contains(code, "reasoning_effort")):
		return errors.New("AI 中转或当前模型不支持所选推理强度，请将推理强度切换为中转默认（auto）后重试；本轮未自动降级或重发请求")
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return errors.New("AI 中转不接受请求参数，请确认模型支持 Chat Completions 和工具调用")
	default:
		return errors.New("AI 中转返回错误，请检查密钥、模型权限或额度")
	}
}
