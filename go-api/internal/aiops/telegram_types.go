package aiops

import (
	"context"
	"sync"
)

type TelegramInput struct {
	BotID                    string
	UpdateID, FromID, ChatID int64
	MessageID                int64
	Text, Callback           string
	IsPrivate                bool
}

type TelegramButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

type TelegramTransport interface {
	Identity() (string, bool)
	Send(context.Context, int64, string, [][]TelegramButton) (int64, error)
	Edit(context.Context, int64, int64, string, [][]TelegramButton) error
}

type TelegramConfig struct {
	Enabled           bool   `json:"enabled"`
	DefaultModel      string `json:"default_model"`
	DefaultAccessMode string `json:"default_access_mode"`
	StreamUpdates     bool   `json:"stream_updates"`
	NotifyCompletion  bool   `json:"notify_completion"`
}

type TelegramBinding struct {
	ActorID    int64  `json:"actor_id"`
	Email      string `json:"email"`
	TelegramID int64  `json:"telegram_id"`
	Active     bool   `json:"active"`
	SessionID  int64  `json:"session_id"`
	BoundAt    int64  `json:"bound_at"`
}

type TelegramStatus struct {
	Config    TelegramConfig    `json:"config"`
	Bindings  []TelegramBinding `json:"bindings"`
	MyBinding *TelegramBinding  `json:"my_binding"`
	Models    []string          `json:"models"`
	BotReady  bool              `json:"bot_ready"`
}

type TelegramBindingCode struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
	Command   string `json:"command"`
}

type TelegramBridge struct {
	ai        *Service
	mu        sync.Mutex
	transport TelegramTransport
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewTelegramBridge(ai *Service) *TelegramBridge { return &TelegramBridge{ai: ai} }
