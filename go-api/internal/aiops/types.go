package aiops

import (
	"encoding/json"
	"errors"
)

var (
	ErrForbidden   = errors.New("无权使用 AI 运维或访问此记录")
	ErrValidation  = errors.New("AI 运维参数无效")
	ErrNotFound    = errors.New("AI 运维记录不存在")
	ErrConflict    = errors.New("会话或操作状态已变化，请刷新重试")
	ErrBusy        = errors.New("AI 正在处理请求，请稍后重试")
	ErrUnavailable = errors.New("AI 运维暂不可用")
)
var DefaultModels = []string{"gpt-6.1-sol", "claude-fable-5-1", "deepseek-v4.1-flash", "glm-5.3"}

type Config struct {
	FallbackRelays         []RelayConfig     `json:"fallback_relays"`
	FailoverEnabled        bool              `json:"failover_enabled"`
	FailoverTimeoutSeconds int               `json:"failover_timeout_seconds"`
	ReasoningEfforts       map[string]string `json:"reasoning_efforts"`
	Enabled                bool              `json:"enabled"`
	BaseURL                string            `json:"base_url"`
	Models                 []string          `json:"models"`
	HasAPIKey              bool              `json:"has_api_key"`
}
type ConfigSaveRequest struct {
	FallbackRelays         *[]RelaySaveRequest `json:"fallback_relays,omitempty"`
	FailoverEnabled        *bool               `json:"failover_enabled,omitempty"`
	FailoverTimeoutSeconds *int                `json:"failover_timeout_seconds,omitempty"`
	ReasoningEfforts       *map[string]string  `json:"reasoning_efforts,omitempty"`
	Enabled                bool                `json:"enabled"`
	BaseURL                string              `json:"base_url"`
	Models                 []string            `json:"models"`
	APIKey                 string              `json:"api_key"`
	ClearAPIKey            bool                `json:"clear_api_key"`
}
type RelayConfig struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	BaseURL   string   `json:"base_url"`
	Models    []string `json:"models"`
	Enabled   bool     `json:"enabled"`
	HasAPIKey bool     `json:"has_api_key"`
}
type RelaySaveRequest struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	BaseURL     string   `json:"base_url"`
	Models      []string `json:"models"`
	Enabled     bool     `json:"enabled"`
	APIKey      string   `json:"api_key"`
	ClearAPIKey bool     `json:"clear_api_key"`
}
type CreateSessionRequest struct {
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Title           string `json:"title"`
	Model           string `json:"model"`
}
type AccessModeRequest struct {
	ExpectedVersion *int64 `json:"expected_version,omitempty"`
	ID              int64  `json:"id"`
	Mode            string `json:"mode"`
}
type Session struct {
	ReasoningEffort string `json:"reasoning_effort"`
	AccessVersion   int64  `json:"access_version"`
	AccessMode      string `json:"access_mode"`
	FullAccessBy    int64  `json:"full_access_by"`
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	Model           string `json:"model"`
	CreatedBy       int64  `json:"created_by"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}
type Turn struct {
	ReasoningEffort string `json:"reasoning_effort"`
	AccessMode      string `json:"access_mode"`
	StopRequested   bool   `json:"stop_requested"`
	ID              int64  `json:"id"`
	SessionID       int64  `json:"session_id"`
	RequestID       string `json:"request_id"`
	Status          string `json:"status"`
	Error           string `json:"error"`
	CreatedAt       int64  `json:"created_at"`
	CompletedAt     int64  `json:"completed_at"`
	DraftContent    string `json:"draft_content"`
	DraftRound      int    `json:"draft_round"`
	DraftVersion    int64  `json:"draft_version"`
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
	MessagesTruncated bool      `json:"messages_truncated"`
	Session           Session   `json:"session"`
	Messages          []Message `json:"messages"`
	Turns             []Turn    `json:"turns"`
}
type SendMessageRequest struct {
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	AccessVersion   *int64 `json:"access_version,omitempty"`
	AccessMode      string `json:"access_mode,omitempty"`
	SessionID       int64  `json:"session_id,omitempty"`
	Model           string `json:"model"`
	Content         string `json:"content"`
	RequestID       string `json:"request_id"`
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
	Strict      *bool           `json:"strict,omitempty"`
}
type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}
type ToolOutcome struct {
	ActionID    int64
	Content     string
	Pending     bool
	SSHExitCode *int `json:"-"`
}
