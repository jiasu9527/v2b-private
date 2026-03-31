package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/queue"
)

type ticketReplyService interface {
	ReplyTicket(ctx context.Context, req admin.TicketReplyRequest) (bool, error)
}

type Service struct {
	cfg               config.Config
	runtime           *config.RuntimeState
	db                *sql.DB
	jobs              queue.Enqueuer
	client            *http.Client
	resolveRecipients func(ctx context.Context, includeStaff bool) ([]int64, error)
	resolveUserID     func(ctx context.Context, token string) (int64, error)
	adminService      ticketReplyService
	sendMessage       func(ctx context.Context, chatID int64, text string) error
	approveJoin       func(ctx context.Context, chatID, userID int64) error
	declineJoin       func(ctx context.Context, chatID, userID int64) error
}

func NewService(cfg config.Config, db *sql.DB) *Service {
	svc := &Service{
		cfg:    cfg,
		db:     db,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	svc.resolveRecipients = svc.lookupRecipients
	svc.sendMessage = svc.sendMessageNow
	svc.approveJoin = svc.approveJoinNow
	svc.declineJoin = svc.declineJoinNow
	return svc
}

func (s *Service) WithQueueRuntime(jobs queue.Enqueuer) *Service {
	s.jobs = jobs
	return s
}

func (s *Service) WithRuntimeConfig(runtime *config.RuntimeState) *Service {
	s.runtime = runtime
	return s
}

func (s *Service) WithUserResolver(fn func(context.Context, string) (int64, error)) *Service {
	s.resolveUserID = fn
	return s
}

func (s *Service) WithAdminService(service ticketReplyService) *Service {
	s.adminService = service
	return s
}

func (s *Service) currentConfig() config.Config {
	if s == nil {
		return config.Config{}
	}
	if s.runtime == nil {
		return s.cfg
	}
	return s.runtime.CurrentConfig()
}

func (s *Service) NotifyAdmins(ctx context.Context, message string, includeStaff bool) error {
	cfg := s.currentConfig()
	if s == nil || !cfg.TelegramBotEnable || strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return nil
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}

	ids, err := s.resolveRecipients(ctx, includeStaff)
	if err != nil {
		return err
	}
	for _, chatID := range ids {
		chatID := chatID
		runJob := func(jobCtx context.Context) error {
			return s.sendMessage(jobCtx, chatID, message)
		}
		if s.jobs != nil {
			if err := s.jobs.Enqueue("send_telegram", "telegram:"+strconv.FormatInt(chatID, 10), runJob); err == nil {
				continue
			}
		}
		if err := runJob(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) lookupRecipients(ctx context.Context, includeStaff bool) ([]int64, error) {
	if s.db == nil {
		return nil, nil
	}

	query := `SELECT telegram_id
FROM v2_user
WHERE telegram_id IS NOT NULL
  AND (is_admin = 1`
	if includeStaff {
		query += ` OR is_staff = 1`
	}
	query += `)
ORDER BY id ASC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query telegram recipients: %w", err)
	}
	defer rows.Close()

	result := make([]int64, 0)
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, fmt.Errorf("scan telegram recipient: %w", err)
		}
		result = append(result, chatID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate telegram recipients: %w", err)
	}
	return result, nil
}

func (s *Service) sendMessageNow(ctx context.Context, chatID int64, text string) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("text", text)

	return s.postForm(ctx, "sendMessage", values)
}

func (s *Service) approveJoinNow(ctx context.Context, chatID, userID int64) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("user_id", strconv.FormatInt(userID, 10))

	return s.postForm(ctx, "approveChatJoinRequest", values)
}

func (s *Service) declineJoinNow(ctx context.Context, chatID, userID int64) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("user_id", strconv.FormatInt(userID, 10))

	return s.postForm(ctx, "declineChatJoinRequest", values)
}

func (s *Service) postForm(ctx context.Context, method string, values url.Values) error {
	token := strings.TrimSpace(s.currentConfig().TelegramBotToken)
	endpoint := "https://api.telegram.org/bot" + token + "/sendMessage"
	if strings.TrimSpace(method) != "" {
		endpoint = "https://api.telegram.org/bot" + token + "/" + strings.TrimSpace(method)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram request: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode telegram response: %w", err)
	}
	if !payload.OK {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			message = "telegram request failed"
		}
		return errors.New(message)
	}
	return nil
}
