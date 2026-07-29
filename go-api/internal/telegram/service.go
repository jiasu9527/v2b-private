package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/queue"
)

type ticketReplyService interface {
	ReplyTicket(ctx context.Context, req admin.TicketReplyRequest) (bool, error)
}

// EntryMonitorController bridges Telegram actions to the client-entry monitor.
// Implementations should create the run asynchronously and use chatID to deliver
// the completed report through DirectNotifier.NotifyChat.
type EntryMonitorController interface {
	StartClientEntryMonitorRun(ctx context.Context, userID, chatID int64, requestKey string) (runID int64, err error)
	RecentClientEntryMonitorReport(ctx context.Context) (string, error)
}

type EntryMonitorImageController interface {
	RecentClientEntryMonitorReportImage(ctx context.Context) ([]byte, string, error)
}

// EntryMonitorRunOptionsController backs Telegram's rule-group picker. The
// option list is already filtered to runnable, enabled monitor groups.
type EntryMonitorRunOptionsController interface {
	ListClientEntryMonitorRunOptions(ctx context.Context) ([]admin.ClientEntryMonitorRunOption, error)
	StartClientEntryMonitorRunForPoliciesWithMessage(ctx context.Context, policyIDs []int64, userID, chatID, messageID int64, requestKey string) (runID int64, err error)
}

var ErrDirectNotifierUnavailable = errors.New("telegram direct notifier unavailable")

const telegramMessageLimit = 4096

var entryMonitorMenuRetention = 6 * time.Minute

type entryMonitorMenuState struct {
	mu      sync.Mutex
	users   int
	started bool
	timer   *time.Timer
}

// DirectNotifier is the synchronous delivery adapter used by durable workers.
// Unlike Service.NotifyAdmins, it never hands delivery to the in-memory queue.
type DirectNotifier struct {
	service *Service
}

type Service struct {
	cfg                 config.Config
	runtime             *config.RuntimeState
	db                  *sql.DB
	jobs                queue.Enqueuer
	client              *http.Client
	resolveRecipients   func(ctx context.Context, includeStaff bool) ([]int64, error)
	resolveUserID       func(ctx context.Context, token string) (int64, error)
	adminService        ticketReplyService
	entryMonitor        EntryMonitorController
	entryMonitorMenusMu sync.Mutex
	entryMonitorMenus   map[string]*entryMonitorMenuState
	sendMessage         func(ctx context.Context, chatID int64, text string) error
	sendMessageMarkup   func(ctx context.Context, chatID int64, text string, replyMarkup any) error
	editMessageText     func(ctx context.Context, chatID, messageID int64, text string, replyMarkup any) error
	sendPhoto           func(ctx context.Context, chatID int64, photo []byte, caption string, replyMarkup any) error
	answerCallback      func(ctx context.Context, callbackQueryID, text string, showAlert bool) error
	approveJoin         func(ctx context.Context, chatID, userID int64) error
	declineJoin         func(ctx context.Context, chatID, userID int64) error
}

func NewService(cfg config.Config, db *sql.DB) *Service {
	svc := &Service{
		cfg:               cfg,
		db:                db,
		client:            &http.Client{Timeout: 10 * time.Second},
		entryMonitorMenus: make(map[string]*entryMonitorMenuState),
	}
	svc.resolveRecipients = svc.lookupRecipients
	svc.sendMessage = svc.sendMessageNow
	svc.sendMessageMarkup = svc.sendMessageWithMarkupNow
	svc.editMessageText = svc.editMessageTextNow
	svc.sendPhoto = svc.sendPhotoWithMarkupNow
	svc.answerCallback = svc.answerCallbackQueryNow
	svc.approveJoin = svc.approveJoinNow
	svc.declineJoin = svc.declineJoinNow
	return svc
}

func (s *Service) lockEntryMonitorMenu(chatID, messageID int64) (string, *entryMonitorMenuState) {
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(messageID, 10)
	s.entryMonitorMenusMu.Lock()
	if s.entryMonitorMenus == nil {
		s.entryMonitorMenus = make(map[string]*entryMonitorMenuState)
	}
	state := s.entryMonitorMenus[key]
	if state == nil {
		state = &entryMonitorMenuState{}
		s.entryMonitorMenus[key] = state
	}
	state.users++
	s.entryMonitorMenusMu.Unlock()
	state.mu.Lock()
	return key, state
}

func (s *Service) unlockEntryMonitorMenu(key string, state *entryMonitorMenuState, retain bool) {
	if s == nil || state == nil {
		return
	}
	if retain {
		state.started = true
	}
	s.entryMonitorMenusMu.Lock()
	state.users--
	if state.users < 0 {
		state.users = 0
	}
	if state.started {
		if state.timer == nil {
			state.timer = time.AfterFunc(entryMonitorMenuRetention, func() {
				s.expireEntryMonitorMenu(key, state)
			})
		}
	} else if state.users == 0 && s.entryMonitorMenus[key] == state {
		delete(s.entryMonitorMenus, key)
	}
	s.entryMonitorMenusMu.Unlock()
	state.mu.Unlock()
}

func (s *Service) expireEntryMonitorMenu(key string, state *entryMonitorMenuState) {
	if s == nil || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.entryMonitorMenusMu.Lock()
	defer s.entryMonitorMenusMu.Unlock()
	if s.entryMonitorMenus[key] != state {
		return
	}
	if state.users > 0 {
		state.timer = time.AfterFunc(time.Second, func() {
			s.expireEntryMonitorMenu(key, state)
		})
		return
	}
	delete(s.entryMonitorMenus, key)
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

func (s *Service) WithEntryMonitorController(controller EntryMonitorController) *Service {
	s.entryMonitor = controller
	return s
}

func (s *Service) DirectNotifier() *DirectNotifier {
	return &DirectNotifier{service: s}
}

func (n *DirectNotifier) ListAdminChats(ctx context.Context, includeStaff bool) ([]int64, error) {
	if n == nil || n.service == nil {
		return nil, ErrDirectNotifierUnavailable
	}
	ids, err := n.service.resolveRecipients(ctx, includeStaff)
	if err != nil {
		return nil, err
	}
	result := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, chatID := range ids {
		if chatID == 0 {
			continue
		}
		if _, exists := seen[chatID]; exists {
			continue
		}
		seen[chatID] = struct{}{}
		result = append(result, chatID)
	}
	return result, nil
}

func (n *DirectNotifier) NotifyAdmins(ctx context.Context, message string, includeStaff bool) error {
	if n == nil || n.service == nil {
		return ErrDirectNotifierUnavailable
	}
	s := n.service
	cfg := s.currentConfig()
	if !cfg.TelegramBotEnable || strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return ErrDirectNotifierUnavailable
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	ids, err := n.ListAdminChats(ctx, includeStaff)
	if err != nil {
		return err
	}
	sendErrors := make([]error, 0)
	for _, chatID := range ids {
		if err := s.sendMessageChunks(ctx, chatID, message); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("send direct telegram notification to %d: %w", chatID, err))
		}
	}
	return errors.Join(sendErrors...)
}

func (n *DirectNotifier) NotifyChat(ctx context.Context, chatID int64, message string) error {
	if n == nil || n.service == nil {
		return ErrDirectNotifierUnavailable
	}
	s := n.service
	cfg := s.currentConfig()
	if !cfg.TelegramBotEnable || strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return ErrDirectNotifierUnavailable
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	if err := s.sendMessageChunks(ctx, chatID, message); err != nil {
		return fmt.Errorf("send direct telegram notification to %d: %w", chatID, err)
	}
	return nil
}

// EditChatMessage updates the existing Telegram menu message used by an
// interactive monitor run. It intentionally uses text only: final run
// reports are delivered separately as images by the durable worker.
func (n *DirectNotifier) EditChatMessage(ctx context.Context, chatID, messageID int64, message string) error {
	if n == nil || n.service == nil || chatID <= 0 || messageID <= 0 {
		return ErrDirectNotifierUnavailable
	}
	s := n.service
	cfg := s.currentConfig()
	if !cfg.TelegramBotEnable || strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return ErrDirectNotifierUnavailable
	}
	if s.editMessageText == nil {
		return ErrDirectNotifierUnavailable
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	if err := s.editMessageText(ctx, chatID, messageID, message, entryMonitorEmptyInlineKeyboard()); err != nil {
		return fmt.Errorf("edit direct telegram message %d/%d: %w", chatID, messageID, err)
	}
	return nil
}

func (n *DirectNotifier) NotifyChatImage(ctx context.Context, chatID int64, photo []byte, caption string) error {
	if n == nil || n.service == nil {
		return ErrDirectNotifierUnavailable
	}
	s := n.service
	cfg := s.currentConfig()
	if !cfg.TelegramBotEnable || strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return ErrDirectNotifierUnavailable
	}
	if len(photo) == 0 {
		return errors.New("telegram image is empty")
	}
	if s.sendPhoto == nil {
		return ErrDirectNotifierUnavailable
	}
	if err := s.sendPhoto(ctx, chatID, photo, caption, nil); err != nil {
		return fmt.Errorf("send direct telegram image to %d: %w", chatID, err)
	}
	return nil
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
			return s.sendMessageChunks(jobCtx, chatID, message)
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
	AND banned = 0
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
	return s.sendMessageWithMarkupNow(ctx, chatID, text, nil)
}

func (s *Service) sendMessageWithMarkupNow(ctx context.Context, chatID int64, text string, replyMarkup any) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("text", telegramPlainText(text))
	if replyMarkup != nil {
		encoded, err := json.Marshal(replyMarkup)
		if err != nil {
			return fmt.Errorf("encode telegram reply markup: %w", err)
		}
		values.Set("reply_markup", string(encoded))
	}

	return s.postForm(ctx, "sendMessage", values)
}

func (s *Service) editMessageTextNow(ctx context.Context, chatID, messageID int64, text string, replyMarkup any) error {
	if chatID == 0 || messageID <= 0 {
		return errors.New("telegram message reference is invalid")
	}
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("message_id", strconv.FormatInt(messageID, 10))
	values.Set("text", telegramPlainText(text))
	if replyMarkup != nil {
		encoded, err := json.Marshal(replyMarkup)
		if err != nil {
			return fmt.Errorf("encode telegram edit reply markup: %w", err)
		}
		values.Set("reply_markup", string(encoded))
	}
	err := s.postForm(ctx, "editMessageText", values)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	return err
}

func (s *Service) sendPhotoWithMarkupNow(ctx context.Context, chatID int64, photo []byte, caption string, replyMarkup any) error {
	if len(photo) == 0 {
		return errors.New("telegram image is empty")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("encode telegram photo chat id: %w", err)
	}
	if caption = truncateTelegramCaptionText(telegramPlainText(caption)); caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return fmt.Errorf("encode telegram photo caption: %w", err)
		}
	}
	if replyMarkup != nil {
		encoded, err := json.Marshal(replyMarkup)
		if err != nil {
			return fmt.Errorf("encode telegram photo reply markup: %w", err)
		}
		if err := writer.WriteField("reply_markup", string(encoded)); err != nil {
			return fmt.Errorf("encode telegram photo reply markup field: %w", err)
		}
	}
	part, err := writer.CreateFormFile("photo", "entry-monitor.png")
	if err != nil {
		return fmt.Errorf("create telegram photo form: %w", err)
	}
	if _, err := part.Write(photo); err != nil {
		return fmt.Errorf("write telegram photo form: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close telegram photo form: %w", err)
	}

	token := strings.TrimSpace(s.currentConfig().TelegramBotToken)
	endpoint := "https://api.telegram.org/bot" + token + "/sendPhoto"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return fmt.Errorf("build telegram photo request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return s.doTelegramRequest(req)
}

func (s *Service) answerCallbackQueryNow(ctx context.Context, callbackQueryID, text string, showAlert bool) error {
	values := url.Values{}
	values.Set("callback_query_id", strings.TrimSpace(callbackQueryID))
	if text = strings.TrimSpace(text); text != "" {
		values.Set("text", text)
	}
	values.Set("show_alert", strconv.FormatBool(showAlert))

	return s.postForm(ctx, "answerCallbackQuery", values)
}

func (s *Service) sendMessageChunks(ctx context.Context, chatID int64, message string) error {
	message = telegramPlainText(message)
	for _, chunk := range splitTelegramMessage(message) {
		if err := s.sendMessage(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// telegramPlainText keeps bot output readable without relying on Telegram's
// Markdown parser. Some persisted or imported notification text can still
// contain Markdown bold delimiters; with no parse_mode those delimiters would
// be shown literally to operators.
func telegramPlainText(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	message = strings.ReplaceAll(message, "**", "")
	lines := strings.Split(message, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "用户入口一键检测 #"):
			// The database run ID is an implementation detail. Keep it available
			// in the admin panel, but do not expose it in operator chat output.
			lines[index] = "🧭 用户入口检测结果"
		case trimmed == "用户入口检测近期状态":
			lines[index] = "🕘 近期检测状态"
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func splitTelegramMessage(message string) []string {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}

	remaining := []rune(message)
	chunks := make([]string, 0, (len(remaining)+telegramMessageLimit-1)/telegramMessageLimit)
	for len(remaining) > telegramMessageLimit {
		end := telegramMessageLimit
		for index := telegramMessageLimit - 1; index >= telegramMessageLimit/2; index-- {
			if remaining[index] == '\n' {
				end = index + 1
				break
			}
		}
		chunks = append(chunks, string(remaining[:end]))
		remaining = remaining[end:]
	}
	if len(remaining) > 0 {
		chunks = append(chunks, string(remaining))
	}
	return chunks
}

func truncateTelegramCaptionText(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	const limit = 1024
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-20])) + "\n…其余详情请查看后台。"
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
	return s.doTelegramRequest(req)
}

func (s *Service) doTelegramRequest(req *http.Request) error {
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
