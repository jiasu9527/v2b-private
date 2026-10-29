package aiops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
	payload, err := json.Marshal(completionRequest{Model: model, Messages: messages, Tools: s.toolDefinitions(), ToolChoice: "auto", MaxTokens: 4096})
	if err != nil || len(payload) > 512<<10 {
		return empty, errors.New("对话上下文过长，请新建会话")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return empty, errors.New("无法创建 AI 请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return empty, errors.New("AI 中转请求失败或超时，请检查中转配置")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, errors.New("AI 中转返回错误，请检查密钥、模型权限或额度")
	}
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
	if len(out.Content) > 32000 || len(out.ToolCalls) > 8 {
		return empty, errors.New("AI 响应超出单轮限制")
	}
	seen := map[string]bool{}
	for _, call := range out.ToolCalls {
		if call.ID == "" || len(call.ID) > 200 || seen[call.ID] || call.Type != "function" || call.Function.Name == "" || len(call.Function.Name) > 100 || len(call.Function.Arguments) > 16000 || !json.Valid([]byte(call.Function.Arguments)) || !strings.HasPrefix(strings.TrimSpace(call.Function.Arguments), "{") {
			return empty, errors.New("AI 工具请求格式无效")
		}
		seen[call.ID] = true
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return empty, errors.New("AI 未返回有效内容")
	}
	return out, nil
}
