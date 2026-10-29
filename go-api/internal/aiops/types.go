package aiops

import (
	"encoding/json"
	"errors"
)

var (
	ErrForbidden   = errors.New("仅管理员可使用 AI 运维")
	ErrValidation  = errors.New("AI 运维参数无效")
	ErrNotFound    = errors.New("AI 运维记录不存在")
	ErrConflict    = errors.New("会话或操作状态已变化，请刷新重试")
	ErrBusy        = errors.New("AI 正在处理请求，请稍后重试")
	ErrUnavailable = errors.New("AI 运维暂不可用")
)
var DefaultModels = []string{"gpt-6.1-sol", "claude-fable-5-1", "deepseek-v4.1-flash", "glm-5.3"}

type Config struct {
	Enabled   bool     `json:"enabled"`
	BaseURL   string   `json:"base_url"`
	Models    []string `json:"models"`
	HasAPIKey bool     `json:"has_api_key"`
}
type ConfigSaveRequest struct {
	Enabled     bool     `json:"enabled"`
	BaseURL     string   `json:"base_url"`
	Models      []string `json:"models"`
	APIKey      string   `json:"api_key"`
	ClearAPIKey bool     `json:"clear_api_key"`
}
type CreateSessionRequest struct {
	Title string `json:"title"`
	Model string `json:"model"`
}
type Session struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	CreatedBy int64  `json:"created_by"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}
type Turn struct {
	ID          int64  `json:"id"`
	SessionID   int64  `json:"session_id"`
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	CreatedAt   int64  `json:"created_at"`
	CompletedAt int64  `json:"completed_at"`
}
type Message struct {
	ID         int64      `json:"id"`
	SessionID  int64      `json:"session_id"`
	TurnID     int64      `json:"turn_id"`
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	CreatedAt  int64      `json:"created_at"`
}
type SessionDetail struct {
	Session  Session   `json:"session"`
	Messages []Message `json:"messages"`
	Turns    []Turn    `json:"turns"`
}
type SendMessageRequest struct {
	SessionID int64  `json:"session_id,omitempty"`
	Model     string `json:"model"`
	Content   string `json:"content"`
	RequestID string `json:"request_id"`
}
type SendMessageResult struct {
	Session Session `json:"session"`
	Turn    Turn    `json:"turn"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}
type ToolFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}
type ToolOutcome struct {
	Content string
	Pending bool
}
