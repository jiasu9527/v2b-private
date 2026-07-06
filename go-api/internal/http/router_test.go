package httpapi

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/guest"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/queue"
	"forest/go-api/internal/session"
	"forest/go-api/internal/user"
)

func stringPtr(value string) *string {
	return &value
}

type fakeQueueRuntime struct {
	snapshot queue.Snapshot
}

func (f fakeQueueRuntime) Enqueue(string, string, queue.JobFunc) error {
	return nil
}

func (f fakeQueueRuntime) Snapshot() queue.Snapshot {
	return f.snapshot
}

func TestRouterHealthz(t *testing.T) {
	router := NewRouter(config.Config{AppName: "forest-go"})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json response: %v", err)
	}

	if payload["status"] != "ok" {
		t.Fatalf("expected status ok, got %#v", payload["status"])
	}
}

func TestRouterUnknownAPIPathReturnsNotFound(t *testing.T) {
	router := NewRouter(config.Config{AppName: "forest-go"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown api path, got %d", rec.Code)
	}
}

func TestRouterGuestConfigEndpoint(t *testing.T) {
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithGuestService(guest.StaticService{
			ConfigPayload: map[string]any{
				"app_url":         "http://127.0.0.1:8080",
				"app_description": "forest",
			},
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/guest/comm/config", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected guest config json response: %v", err)
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", payload["data"])
	}
	if data["app_url"] != "http://127.0.0.1:8080" {
		t.Fatalf("expected app_url in guest config, got %#v", data["app_url"])
	}
}

func TestRouterGuestPlanFetchEndpoint(t *testing.T) {
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithGuestService(guest.StaticService{
			PlansPayload: []map[string]any{
				{"id": int64(1), "name": "Starter", "show": int64(1)},
			},
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/guest/plan/fetch", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("expected guest plan fetch to disable cache, got %q", rec.Header().Get("Cache-Control"))
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected guest plan json response: %v", err)
	}

	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one plan item, got %#v", payload["data"])
	}
}

func TestRouterGuestInvitePreviewEndpoint(t *testing.T) {
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithGuestService(guest.StaticService{
			InvitePreviewPayload: map[string]any{
				"code":                 "ABC12345",
				"type":                 "campaign",
				"gift_transfer_gb":     10,
				"gift_hours":           24,
				"countdown_expired_at": int64(1234567890),
			},
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/guest/invite/preview?code=ABC12345", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected guest invite preview json response: %v", err)
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", payload["data"])
	}
	if data["type"] != "campaign" {
		t.Fatalf("expected preview type campaign, got %#v", data["type"])
	}
}

func TestRouterGuestTelegramWebhookUsesRuntimeTokenAfterConfigReload(t *testing.T) {
	telegramService := &fakeTelegramService{}
	runtimeState := config.NewRuntimeState(config.Config{TelegramBotToken: "old-token"})
	runtimeState.SetForTest(config.Config{TelegramBotToken: "new-token"})
	router := NewRouter(
		config.Config{AppName: "forest-go", TelegramBotToken: "old-token"},
		WithRuntimeConfig(runtimeState),
		WithTelegramService(telegramService),
	)

	accessToken := fmt.Sprintf("%x", md5.Sum([]byte("new-token")))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/telegram/webhook?access_token="+accessToken, strings.NewReader(`{"message":{"text":"/traffic","chat":{"id":123,"type":"private"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if telegramService.lastPayload == nil {
		t.Fatalf("expected webhook service called")
	}
}

func TestRouterGuestTelegramWebhookUsesAdminJSONTokenWhenRuntimeIsStale(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"telegram_bot_token": "fresh-token"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin config: %v", err)
	}

	workDir := filepath.Join(root, "go-api")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	telegramService := &fakeTelegramService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", TelegramBotToken: "stale-token"},
		WithTelegramService(telegramService),
	)

	accessToken := fmt.Sprintf("%x", md5.Sum([]byte("fresh-token")))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/telegram/webhook?access_token="+accessToken, strings.NewReader(`{"message":{"text":"/traffic","chat":{"id":123,"type":"private"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if telegramService.lastPayload == nil {
		t.Fatalf("expected webhook service called")
	}
}

func TestRouterGuestTelegramWebhookRejectsStaleRuntimeTokenWhenAdminJSONTokenExists(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"telegram_bot_token": "fresh-token"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin config: %v", err)
	}

	workDir := filepath.Join(root, "go-api")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	telegramService := &fakeTelegramService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", TelegramBotToken: "stale-token"},
		WithTelegramService(telegramService),
	)

	accessToken := fmt.Sprintf("%x", md5.Sum([]byte("stale-token")))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/telegram/webhook?access_token="+accessToken, strings.NewReader(`{"message":{"text":"/traffic","chat":{"id":123,"type":"private"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if telegramService.lastPayload != nil {
		t.Fatalf("expected webhook service not called, got %#v", telegramService.lastPayload)
	}
}

func TestRouterGuestTelegramWebhookRejectsInvalidAccessToken(t *testing.T) {
	telegramService := &fakeTelegramService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", TelegramBotToken: "bot-token"},
		WithTelegramService(telegramService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/telegram/webhook?access_token=wrong", strings.NewReader(`{"message":{"text":"/traffic"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if telegramService.lastPayload != nil {
		t.Fatalf("expected webhook service not called, got %#v", telegramService.lastPayload)
	}
}

func TestRouterGuestTelegramWebhookEndpoint(t *testing.T) {
	telegramService := &fakeTelegramService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", TelegramBotToken: "bot-token"},
		WithTelegramService(telegramService),
	)

	accessToken := md5.Sum([]byte("bot-token"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/telegram/webhook?access_token="+strings.ToLower(fmt.Sprintf("%x", accessToken)), strings.NewReader(`{"message":{"text":"/traffic","chat":{"id":123,"type":"private"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	message, ok := telegramService.lastPayload["message"].(map[string]any)
	if !ok || message["text"] != "/traffic" {
		t.Fatalf("unexpected webhook payload: %#v", telegramService.lastPayload)
	}
}

func TestRouterGuestPaymentNotifyEndpoint(t *testing.T) {
	paymentService := &fakePaymentService{notifyResult: "ok"}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithPaymentService(paymentService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/guest/payment/notify/EPay/uuid123", strings.NewReader("out_trade_no=T300&trade_no=P300"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Test", "1")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("expected plain ok body, got %q", rec.Body.String())
	}
	if paymentService.lastNotifyMethod != "EPay" || paymentService.lastNotifyUUID != "uuid123" {
		t.Fatalf("unexpected notify target: method=%q uuid=%q", paymentService.lastNotifyMethod, paymentService.lastNotifyUUID)
	}
	if paymentService.lastNotify.Params["out_trade_no"] != "T300" || paymentService.lastNotify.Params["trade_no"] != "P300" {
		t.Fatalf("unexpected notify params: %#v", paymentService.lastNotify.Params)
	}
}

type fakePassportService struct {
	lastInviteCode string
	lastSendVerify passport.SendEmailVerifyRequest
	lastRegister   passport.RegisterRequest
	lastLogin      passport.LoginRequest
	lastForget     passport.ForgetRequest
	lastTokenLogin passport.TokenLoginRequest
	lastQuickLogin passport.QuickLoginRequest
	lastMailLink   passport.LoginWithMailLinkRequest

	authData       passport.AuthData
	quickLoginURL  string
	mailLinkResult any
	err            error
}

type fakeSessionService struct {
	lastAuthToken    string
	lastRequireAdmin bool
	lastListUserID   int64
	lastRemoveUserID int64
	lastRemoveSID    string
	user             *session.Identity
	sessions         map[string]session.SessionMeta
	authErr          error
	listErr          error
	removeErr        error
	removeOK         bool
}

func (f *fakeSessionService) Authenticate(_ context.Context, authToken string, requireAdmin bool) (*session.Identity, error) {
	f.lastAuthToken = authToken
	f.lastRequireAdmin = requireAdmin
	return f.user, f.authErr
}

func (f *fakeSessionService) ListSessions(_ context.Context, userID int64) (map[string]session.SessionMeta, error) {
	f.lastListUserID = userID
	return f.sessions, f.listErr
}

func (f *fakeSessionService) RemoveSession(_ context.Context, userID int64, sessionID string) (bool, error) {
	f.lastRemoveUserID = userID
	f.lastRemoveSID = sessionID
	return f.removeOK, f.removeErr
}

type fakeUserService struct {
	lastUserID                 int64
	lastClientToken            string
	lastServerUA               string
	lastPlanID                 *int64
	lastOrderStatus            *int64
	lastTradeNo                string
	lastNoticeID               int64
	lastNoticeCur              int64
	lastNoticeSize             int64
	lastInviteCur              int64
	lastInviteSize             int64
	lastInviteCampaignPlanID   int64
	lastInviteCampaignPeriod   string
	lastInviteCampaignID       *int64
	lastInviteCampaignCurrent  int64
	lastInviteCampaignPageSize int64
	lastTicketID               int64
	lastTicketSave             user.TicketCreateRequest
	lastTicketReply            string
	lastOldPassword            string
	lastNewPassword            string
	lastTransferAmount         int64
	lastRedeemGiftcard         string
	lastWithdrawMethod         string
	lastWithdrawAccount        string
	lastPaymentMethodID        int64
	lastCouponCode             string
	lastCouponPlanID           *int64
	lastKnowledgeID            int64
	lastKnowledgeLang          string
	lastKnowledgeKeyword       string
	lastOrderSave              user.OrderSaveRequest
	lastCheckout               user.OrderCheckoutRequest
	lastProfileUpdate          user.ProfileUpdateRequest
	telegramBotInfo            map[string]any
	resetSecurityURL           string
	redeemGiftcardResult       map[string]any
	info                       user.Info
	stat                       []int64
	subscribe                  user.Subscribe
	commConfig                 map[string]any
	coupon                     map[string]any
	stripePublicKey            string
	knowledgeDetail            map[string]any
	knowledgeList              map[string][]map[string]any
	knowledgeCats              []string
	trafficLogs                []map[string]any
	servers                    []map[string]any
	clientEntryGroups          []user.ClientEntryGroup
	plans                      any
	noticeDetail               map[string]any
	notices                    []map[string]any
	noticesTotal               int64
	inviteOverview             map[string]any
	inviteDetails              []map[string]any
	inviteTotal                int64
	inviteCampaign             map[string]any
	inviteCampaignRecords      map[string]any
	tickets                    []map[string]any
	ticketDetail               map[string]any
	orders                     []map[string]any
	orderDetail                map[string]any
	orderStatus                int64
	paymentMethods             []map[string]any
	orderTradeNo               string
	checkoutResult             user.OrderCheckoutResult
	cancelOK                   bool
	inviteSaveOK               bool
	ticketSaveOK               bool
	ticketReplyOK              bool
	ticketCloseOK              bool
	ticketWithdrawOK           bool
	unbindTelegramOK           bool
	transferOK                 bool
	newPeriodOK                bool
	resolvedClientUserID       int64
	resolvedClientErr          error
	err                        error
}

func (f *fakeUserService) Info(_ context.Context, userID int64) (user.Info, error) {
	f.lastUserID = userID
	return f.info, f.err
}

func (f *fakeUserService) Stat(_ context.Context, userID int64) ([]int64, error) {
	f.lastUserID = userID
	return f.stat, f.err
}

func (f *fakeUserService) Subscribe(_ context.Context, userID int64) (user.Subscribe, error) {
	f.lastUserID = userID
	return f.subscribe, f.err
}

func (f *fakeUserService) ResolveClientUserID(_ context.Context, token string) (int64, error) {
	f.lastClientToken = token
	return f.resolvedClientUserID, f.resolvedClientErr
}

func (f *fakeUserService) TelegramBotInfo(_ context.Context) (map[string]any, error) {
	return f.telegramBotInfo, f.err
}

func (f *fakeUserService) UnbindTelegram(_ context.Context, userID int64) (bool, error) {
	f.lastUserID = userID
	return f.unbindTelegramOK, f.err
}

func (f *fakeUserService) ResetSecurity(_ context.Context, userID int64) (string, error) {
	f.lastUserID = userID
	return f.resetSecurityURL, f.err
}

func (f *fakeUserService) UpdateProfile(_ context.Context, userID int64, req user.ProfileUpdateRequest) (bool, error) {
	f.lastUserID = userID
	f.lastProfileUpdate = req
	return true, f.err
}

func (f *fakeUserService) ChangePassword(_ context.Context, userID int64, oldPassword, newPassword string) (bool, error) {
	f.lastUserID = userID
	f.lastOldPassword = oldPassword
	f.lastNewPassword = newPassword
	return true, f.err
}

func (f *fakeUserService) Transfer(_ context.Context, userID, amount int64) (bool, error) {
	f.lastUserID = userID
	f.lastTransferAmount = amount
	return f.transferOK, f.err
}

func (f *fakeUserService) NewPeriod(_ context.Context, userID int64) (bool, error) {
	f.lastUserID = userID
	return f.newPeriodOK, f.err
}

func (f *fakeUserService) RedeemGiftcard(_ context.Context, userID int64, code string) (map[string]any, error) {
	f.lastUserID = userID
	f.lastRedeemGiftcard = code
	return f.redeemGiftcardResult, f.err
}

func (f *fakeUserService) Servers(_ context.Context, userID int64, ua string) ([]map[string]any, error) {
	f.lastUserID = userID
	f.lastServerUA = ua
	return f.servers, f.err
}

func (f *fakeUserService) ClientEntryGroups(_ context.Context, userID int64) ([]user.ClientEntryGroup, error) {
	f.lastUserID = userID
	return f.clientEntryGroups, f.err
}

func (f *fakeUserService) Plans(_ context.Context, userID int64, planID *int64) (any, error) {
	f.lastUserID = userID
	f.lastPlanID = planID
	return f.plans, f.err
}

func (f *fakeUserService) NoticeDetail(_ context.Context, id int64) (map[string]any, error) {
	f.lastNoticeID = id
	return f.noticeDetail, f.err
}

func (f *fakeUserService) Notices(_ context.Context, current, pageSize int64) ([]map[string]any, int64, error) {
	f.lastNoticeCur = current
	f.lastNoticeSize = pageSize
	return f.notices, f.noticesTotal, f.err
}

func (f *fakeUserService) CreateInviteCode(_ context.Context, userID int64) (bool, error) {
	f.lastUserID = userID
	return f.inviteSaveOK, f.err
}

func (f *fakeUserService) InviteOverview(_ context.Context, userID int64) (map[string]any, error) {
	f.lastUserID = userID
	return f.inviteOverview, f.err
}

func (f *fakeUserService) InviteDetails(_ context.Context, userID, current, pageSize int64) ([]map[string]any, int64, error) {
	f.lastUserID = userID
	f.lastInviteCur = current
	f.lastInviteSize = pageSize
	return f.inviteDetails, f.inviteTotal, f.err
}

func (f *fakeUserService) CreateInviteCampaign(_ context.Context, userID, planID int64, period string) (map[string]any, error) {
	f.lastUserID = userID
	f.lastInviteCampaignPlanID = planID
	f.lastInviteCampaignPeriod = period
	return f.inviteCampaign, f.err
}

func (f *fakeUserService) InviteCampaign(_ context.Context, userID int64) (map[string]any, error) {
	f.lastUserID = userID
	return f.inviteCampaign, f.err
}

func (f *fakeUserService) InviteCampaignRecords(_ context.Context, userID int64, campaignID *int64, current, pageSize int64) (map[string]any, error) {
	f.lastUserID = userID
	f.lastInviteCampaignID = campaignID
	f.lastInviteCampaignCurrent = current
	f.lastInviteCampaignPageSize = pageSize
	return f.inviteCampaignRecords, f.err
}

func (f *fakeUserService) AbandonInviteCampaign(_ context.Context, userID int64) (bool, error) {
	f.lastUserID = userID
	return true, f.err
}

func (f *fakeUserService) Tickets(_ context.Context, userID int64) ([]map[string]any, error) {
	f.lastUserID = userID
	return f.tickets, f.err
}

func (f *fakeUserService) TicketDetail(_ context.Context, userID, id int64) (map[string]any, error) {
	f.lastUserID = userID
	f.lastTicketID = id
	return f.ticketDetail, f.err
}

func (f *fakeUserService) CreateTicket(_ context.Context, userID int64, req user.TicketCreateRequest) (bool, error) {
	f.lastUserID = userID
	f.lastTicketSave = req
	return f.ticketSaveOK, f.err
}

func (f *fakeUserService) ReplyTicket(_ context.Context, userID, id int64, message string) (bool, error) {
	f.lastUserID = userID
	f.lastTicketID = id
	f.lastTicketReply = message
	return f.ticketReplyOK, f.err
}

func (f *fakeUserService) CloseTicket(_ context.Context, userID, id int64) (bool, error) {
	f.lastUserID = userID
	f.lastTicketID = id
	return f.ticketCloseOK, f.err
}

func (f *fakeUserService) WithdrawTicket(_ context.Context, userID int64, method, account string) (bool, error) {
	f.lastUserID = userID
	f.lastWithdrawMethod = method
	f.lastWithdrawAccount = account
	return f.ticketWithdrawOK, f.err
}

func (f *fakeUserService) CommConfig(_ context.Context) (map[string]any, error) {
	return f.commConfig, f.err
}

func (f *fakeUserService) StripePublicKey(_ context.Context, paymentID int64) (string, error) {
	f.lastPaymentMethodID = paymentID
	return f.stripePublicKey, f.err
}

func (f *fakeUserService) CheckCoupon(_ context.Context, userID int64, code string, planID *int64) (map[string]any, error) {
	f.lastUserID = userID
	f.lastCouponCode = code
	f.lastCouponPlanID = planID
	return f.coupon, f.err
}

func (f *fakeUserService) KnowledgeDetail(_ context.Context, userID, id int64) (map[string]any, error) {
	f.lastUserID = userID
	f.lastKnowledgeID = id
	return f.knowledgeDetail, f.err
}

func (f *fakeUserService) Knowledges(_ context.Context, language, keyword string) (map[string][]map[string]any, error) {
	f.lastKnowledgeLang = language
	f.lastKnowledgeKeyword = keyword
	return f.knowledgeList, f.err
}

func (f *fakeUserService) KnowledgeCategories(_ context.Context, language string) ([]string, error) {
	f.lastKnowledgeLang = language
	return f.knowledgeCats, f.err
}

func (f *fakeUserService) TrafficLogs(_ context.Context, userID int64) ([]map[string]any, error) {
	f.lastUserID = userID
	return f.trafficLogs, f.err
}

func (f *fakeUserService) Orders(_ context.Context, userID int64, status *int64) ([]map[string]any, error) {
	f.lastUserID = userID
	f.lastOrderStatus = status
	return f.orders, f.err
}

func (f *fakeUserService) OrderDetail(_ context.Context, userID int64, tradeNo string) (map[string]any, error) {
	f.lastUserID = userID
	f.lastTradeNo = tradeNo
	return f.orderDetail, f.err
}

func (f *fakeUserService) OrderStatus(_ context.Context, userID int64, tradeNo string) (int64, error) {
	f.lastUserID = userID
	f.lastTradeNo = tradeNo
	return f.orderStatus, f.err
}

func (f *fakeUserService) PaymentMethods(_ context.Context) ([]map[string]any, error) {
	return f.paymentMethods, f.err
}

func (f *fakeUserService) SaveOrder(_ context.Context, userID int64, req user.OrderSaveRequest) (string, error) {
	f.lastUserID = userID
	f.lastOrderSave = req
	return f.orderTradeNo, f.err
}

func (f *fakeUserService) CheckoutOrder(_ context.Context, userID int64, req user.OrderCheckoutRequest) (user.OrderCheckoutResult, error) {
	f.lastUserID = userID
	f.lastCheckout = req
	return f.checkoutResult, f.err
}

func (f *fakeUserService) CancelOrder(_ context.Context, userID int64, tradeNo string) (bool, error) {
	f.lastUserID = userID
	f.lastTradeNo = tradeNo
	return f.cancelOK, f.err
}

type fakeAdminService struct {
	systemStatus                      admin.SystemStatus
	queueStats                        admin.QueueStats
	workload                          []map[string]any
	systemLogs                        []map[string]any
	systemLogsTotal                   int64
	statOverride                      map[string]any
	statOrder                         []map[string]any
	statSummary                       map[string]any
	statRanking                       []map[string]any
	statRecords                       []map[string]any
	serverLastRank                    []map[string]any
	serverTodayRank                   []map[string]any
	inviteLastRank                    []map[string]any
	inviteTodayRank                   []map[string]any
	userLastRank                      []map[string]any
	userTodayRank                     []map[string]any
	statUserRecords                   []map[string]any
	statUserTotal                     int64
	inviteList                        admin.InviteCampaignListResult
	inviteDetail                      map[string]any
	inviteRecords                     admin.InviteCampaignRecordListResult
	serverGroups                      []admin.ServerGroupRecord
	clientEntryGroups                 []admin.ClientEntryGroupRecord
	clientEntryUserPolicies           []admin.ClientEntryUserPolicyRecord
	serverRoutes                      []admin.ServerRouteRecord
	managedServers                    []map[string]any
	hostUpdateResult                  admin.ManagedServerHostUpdateResult
	configData                        map[string]any
	themes                            map[string]any
	themeConfig                       map[string]any
	emailTemplates                    []string
	themeTemplates                    []string
	mailTestLog                       admin.ConfigMailTestLog
	telegramStatus                    admin.TelegramAdminStatus
	telegramTestOK                    bool
	lastTelegramAdminID               int64
	plans                             []admin.PlanRecord
	notices                           []admin.NoticeRecord
	couponList                        admin.CouponListResult
	giftcardList                      admin.GiftcardListResult
	knowledges                        []admin.KnowledgeRecord
	knowledgeDetail                   admin.KnowledgeRecord
	knowledgeCats                     []string
	payments                          []admin.PaymentRecord
	methods                           []string
	form                              map[string]admin.PaymentFormField
	orderList                         admin.OrderListResult
	orderDetail                       map[string]any
	ticketList                        admin.TicketListResult
	ticketDetail                      admin.TicketDetail
	lastPlanSave                      admin.PlanSaveRequest
	lastPlanDropID                    int64
	lastPlanUpdate                    admin.PlanToggleRequest
	lastPlanSortIDs                   []int64
	lastNoticeSave                    admin.NoticeSaveRequest
	lastNoticeDrop                    int64
	lastNoticeShow                    int64
	lastCouponList                    admin.CouponListRequest
	lastCouponSave                    admin.CouponGenerateRequest
	lastCouponDrop                    int64
	lastCouponShow                    int64
	couponCSV                         string
	lastGiftcardList                  admin.GiftcardListRequest
	lastGiftcardSave                  admin.GiftcardGenerateRequest
	lastGiftcardDrop                  int64
	giftcardCSV                       string
	lastKnowledgeID                   int64
	lastKnowledgeSave                 admin.KnowledgeSaveRequest
	lastKnowledgeShow                 int64
	lastKnowledgeDrop                 int64
	lastKnowledgeSort                 []int64
	lastFormID                        *int64
	lastGateway                       string
	lastSave                          admin.PaymentSaveRequest
	lastDropID                        int64
	lastShowID                        int64
	lastSortIDs                       []int64
	lastFetch                         admin.OrderFetchRequest
	lastDetailID                      int64
	lastUpdate                        admin.OrderUpdateRequest
	lastPaid                          string
	lastCancel                        string
	lastRefund                        string
	lastAssign                        admin.OrderAssignRequest
	lastTicketFetch                   admin.TicketListRequest
	lastTicketID                      int64
	lastTicketReply                   admin.TicketReplyRequest
	lastTicketClose                   int64
	lastLogCurrent                    int64
	lastLogPageSize                   int64
	lastLogLevel                      string
	lastStatStartAt                   int64
	lastStatEndAt                     int64
	lastRankingType                   string
	lastRankingLimit                  int64
	lastRecordType                    string
	lastRecordStartAt                 int64
	lastRecordEndAt                   int64
	lastStatUserID                    int64
	lastStatCurrent                   int64
	lastStatPageSize                  int64
	lastInviteList                    admin.InviteCampaignListRequest
	lastInviteID                      int64
	lastInviteRecords                 admin.InviteCampaignRecordListRequest
	lastGroupID                       *int64
	lastGroupSave                     admin.ServerGroupSaveRequest
	lastGroupDrop                     int64
	lastClientEntryID                 *int64
	lastClientEntrySave               admin.ClientEntryGroupSaveRequest
	lastClientEntryDrop               int64
	lastClientEntryUserPolicySave     admin.ClientEntryUserPolicySaveRequest
	lastClientEntryUserPolicyBulkSave admin.ClientEntryUserPolicyBulkSaveRequest
	lastClientEntryUserPolicyDrop     int64
	lastRouteSave                     admin.ServerRouteSaveRequest
	lastRouteDrop                     int64
	lastManagedSort                   map[string]map[int64]int64
	lastOldHost                       string
	lastNewHost                       string
	lastNodeSaveType                  string
	lastNodeSave                      map[string]any
	lastNodeDropType                  string
	lastNodeDropID                    int64
	lastNodeUpdateType                string
	lastNodeUpdateID                  int64
	lastNodeUpdate                    map[string]any
	lastNodeCopyType                  string
	lastNodeCopyID                    int64
	lastConfigKey                     string
	lastConfigSave                    map[string]any
	lastWebhookToken                  string
	lastMailTestEmail                 string
	userList                          admin.UserListResult
	userInfoDetail                    map[string]any
	userGenerateCSV                   string
	userGenerateBatch                 bool
	userDumpCSV                       string
	lastUserFetch                     admin.UserFetchRequest
	lastUserInfoID                    int64
	lastUserUpdate                    admin.UserUpdateRequest
	lastUserGenerate                  admin.UserGenerateRequest
	lastUserMail                      admin.UserMailRequest
	lastUserDump                      []admin.UserFilter
	lastUserBan                       []admin.UserFilter
	lastUserBannedID                  int64
	lastUserBannedValue               int64
	lastUserResetID                   int64
	lastUserDeleteID                  int64
	lastUserDeleteAll                 []admin.UserFilter
	assignTrade                       string
	err                               error
}

type fakePaymentService struct {
	lastCheckoutUserID int64
	lastCheckout       payment.CheckoutRequest
	lastNotifyMethod   string
	lastNotifyUUID     string
	lastNotify         payment.NotifyRequest
	checkoutResult     payment.CheckoutResult
	notifyResult       string
	err                error
}

type fakeTelegramService struct {
	lastPayload map[string]any
	err         error
}

func (f *fakePaymentService) Checkout(_ context.Context, userID int64, req payment.CheckoutRequest) (payment.CheckoutResult, error) {
	f.lastCheckoutUserID = userID
	f.lastCheckout = req
	return f.checkoutResult, f.err
}

func (f *fakePaymentService) Notify(_ context.Context, method, uuid string, req payment.NotifyRequest) (string, error) {
	f.lastNotifyMethod = method
	f.lastNotifyUUID = uuid
	f.lastNotify = req
	return f.notifyResult, f.err
}

func (f *fakeTelegramService) HandleWebhook(_ context.Context, payload map[string]any) error {
	f.lastPayload = payload
	return f.err
}

func (f *fakeAdminService) GetSystemStatus(_ context.Context) (admin.SystemStatus, error) {
	return f.systemStatus, f.err
}

func (f *fakeAdminService) GetQueueStats(_ context.Context) (admin.QueueStats, error) {
	return f.queueStats, f.err
}

func (f *fakeAdminService) GetQueueWorkload(_ context.Context) ([]map[string]any, error) {
	return f.workload, f.err
}

func (f *fakeAdminService) ListSystemLogs(_ context.Context, current, pageSize int64, level string) ([]map[string]any, int64, error) {
	f.lastLogCurrent = current
	f.lastLogPageSize = pageSize
	f.lastLogLevel = level
	return f.systemLogs, f.systemLogsTotal, f.err
}

func (f *fakeAdminService) GetStatOverride(_ context.Context) (map[string]any, error) {
	return f.statOverride, f.err
}

func (f *fakeAdminService) GetStatOrder(_ context.Context) ([]map[string]any, error) {
	return f.statOrder, f.err
}

func (f *fakeAdminService) GetStat(_ context.Context, startAt, endAt int64) (map[string]any, error) {
	f.lastStatStartAt = startAt
	f.lastStatEndAt = endAt
	return f.statSummary, f.err
}

func (f *fakeAdminService) GetRanking(_ context.Context, rankingType string, startAt, endAt, limit int64) ([]map[string]any, error) {
	f.lastRankingType = rankingType
	f.lastRecordStartAt = startAt
	f.lastRecordEndAt = endAt
	f.lastRankingLimit = limit
	return f.statRanking, f.err
}

func (f *fakeAdminService) GetStatRecord(_ context.Context, statType string, startAt, endAt int64) ([]map[string]any, error) {
	f.lastRecordType = statType
	f.lastRecordStartAt = startAt
	f.lastRecordEndAt = endAt
	return f.statRecords, f.err
}

func (f *fakeAdminService) GetServerLastRank(_ context.Context) ([]map[string]any, error) {
	return f.serverLastRank, f.err
}

func (f *fakeAdminService) GetServerTodayRank(_ context.Context) ([]map[string]any, error) {
	return f.serverTodayRank, f.err
}

func (f *fakeAdminService) GetInviteLastRank(_ context.Context) ([]map[string]any, error) {
	return f.inviteLastRank, f.err
}

func (f *fakeAdminService) GetInviteTodayRank(_ context.Context) ([]map[string]any, error) {
	return f.inviteTodayRank, f.err
}

func (f *fakeAdminService) GetUserLastRank(_ context.Context) ([]map[string]any, error) {
	return f.userLastRank, f.err
}

func (f *fakeAdminService) GetUserTodayRank(_ context.Context) ([]map[string]any, error) {
	return f.userTodayRank, f.err
}

func (f *fakeAdminService) GetStatUser(_ context.Context, userID, current, pageSize int64) ([]map[string]any, int64, error) {
	f.lastStatUserID = userID
	f.lastStatCurrent = current
	f.lastStatPageSize = pageSize
	return f.statUserRecords, f.statUserTotal, f.err
}

func (f *fakeAdminService) ListInviteCampaigns(_ context.Context, req admin.InviteCampaignListRequest) (admin.InviteCampaignListResult, error) {
	f.lastInviteList = req
	return f.inviteList, f.err
}

func (f *fakeAdminService) GetInviteCampaign(_ context.Context, id int64) (map[string]any, error) {
	f.lastInviteID = id
	return f.inviteDetail, f.err
}

func (f *fakeAdminService) ListInviteCampaignRecords(_ context.Context, req admin.InviteCampaignRecordListRequest) (admin.InviteCampaignRecordListResult, error) {
	f.lastInviteRecords = req
	return f.inviteRecords, f.err
}

func (f *fakeAdminService) ListServerGroups(_ context.Context, groupID *int64) ([]admin.ServerGroupRecord, error) {
	f.lastGroupID = groupID
	return f.serverGroups, f.err
}

func (f *fakeAdminService) SaveServerGroup(_ context.Context, req admin.ServerGroupSaveRequest) (bool, error) {
	f.lastGroupSave = req
	return true, f.err
}

func (f *fakeAdminService) DeleteServerGroup(_ context.Context, id int64) (bool, error) {
	f.lastGroupDrop = id
	return true, f.err
}

func (f *fakeAdminService) ListClientEntryGroups(_ context.Context, id *int64) ([]admin.ClientEntryGroupRecord, error) {
	f.lastClientEntryID = id
	return f.clientEntryGroups, f.err
}

func (f *fakeAdminService) SaveClientEntryGroup(_ context.Context, req admin.ClientEntryGroupSaveRequest) (bool, error) {
	f.lastClientEntrySave = req
	return true, f.err
}

func (f *fakeAdminService) DeleteClientEntryGroup(_ context.Context, id int64) (bool, error) {
	f.lastClientEntryDrop = id
	return true, f.err
}

func (f *fakeAdminService) ListClientEntryUserPolicies(_ context.Context) ([]admin.ClientEntryUserPolicyRecord, error) {
	return f.clientEntryUserPolicies, f.err
}

func (f *fakeAdminService) SaveClientEntryUserPolicy(_ context.Context, req admin.ClientEntryUserPolicySaveRequest) (bool, error) {
	f.lastClientEntryUserPolicySave = req
	return true, f.err
}

func (f *fakeAdminService) SaveClientEntryUserPolicies(_ context.Context, req admin.ClientEntryUserPolicyBulkSaveRequest) (int64, error) {
	f.lastClientEntryUserPolicyBulkSave = req
	return int64(len(req.Emails)), f.err
}

func (f *fakeAdminService) DeleteClientEntryUserPolicy(_ context.Context, id int64) (bool, error) {
	f.lastClientEntryUserPolicyDrop = id
	return true, f.err
}

func (f *fakeAdminService) ListServerRoutes(_ context.Context) ([]admin.ServerRouteRecord, error) {
	return f.serverRoutes, f.err
}

func (f *fakeAdminService) SaveServerRoute(_ context.Context, req admin.ServerRouteSaveRequest) (bool, error) {
	f.lastRouteSave = req
	return true, f.err
}

func (f *fakeAdminService) DeleteServerRoute(_ context.Context, id int64) (bool, error) {
	f.lastRouteDrop = id
	return true, f.err
}

func (f *fakeAdminService) ListManagedServers(_ context.Context) ([]map[string]any, error) {
	return f.managedServers, f.err
}

func (f *fakeAdminService) SortManagedServers(_ context.Context, values map[string]map[int64]int64) (bool, error) {
	f.lastManagedSort = values
	return true, f.err
}

func (f *fakeAdminService) UpdateManagedServerHost(_ context.Context, oldHost, newHost string) (admin.ManagedServerHostUpdateResult, error) {
	f.lastOldHost = oldHost
	f.lastNewHost = newHost
	return f.hostUpdateResult, f.err
}

func (f *fakeAdminService) ListUsers(_ context.Context, req admin.UserFetchRequest) (admin.UserListResult, error) {
	f.lastUserFetch = req
	return f.userList, f.err
}

func (f *fakeAdminService) GetUserInfoByID(_ context.Context, id int64) (map[string]any, error) {
	f.lastUserInfoID = id
	return f.userInfoDetail, f.err
}

func (f *fakeAdminService) UpdateUser(_ context.Context, req admin.UserUpdateRequest) (bool, error) {
	f.lastUserUpdate = req
	return true, f.err
}

func (f *fakeAdminService) GenerateUsers(_ context.Context, req admin.UserGenerateRequest) (string, bool, error) {
	f.lastUserGenerate = req
	return f.userGenerateCSV, f.userGenerateBatch, f.err
}

func (f *fakeAdminService) DumpUserCSV(_ context.Context, filters []admin.UserFilter) (string, error) {
	f.lastUserDump = filters
	return f.userDumpCSV, f.err
}

func (f *fakeAdminService) SendUserMail(_ context.Context, req admin.UserMailRequest) (bool, error) {
	f.lastUserMail = req
	return true, f.err
}

func (f *fakeAdminService) BanUsers(_ context.Context, filters []admin.UserFilter) (bool, error) {
	f.lastUserBan = filters
	return true, f.err
}

func (f *fakeAdminService) SetUserBanned(_ context.Context, id int64, banned int64) (bool, error) {
	f.lastUserBannedID = id
	f.lastUserBannedValue = banned
	return true, f.err
}

func (f *fakeAdminService) ResetUserSecret(_ context.Context, id int64) (bool, error) {
	f.lastUserResetID = id
	return true, f.err
}

func (f *fakeAdminService) DeleteUser(_ context.Context, id int64) (bool, error) {
	f.lastUserDeleteID = id
	return true, f.err
}

func (f *fakeAdminService) DeleteUsers(_ context.Context, filters []admin.UserFilter) (bool, error) {
	f.lastUserDeleteAll = filters
	return true, f.err
}

func (f *fakeAdminService) SaveManagedServer(_ context.Context, serverType string, payload map[string]any) (bool, error) {
	f.lastNodeSaveType = serverType
	f.lastNodeSave = payload
	return true, f.err
}

func (f *fakeAdminService) DeleteManagedServer(_ context.Context, serverType string, id int64) (bool, error) {
	f.lastNodeDropType = serverType
	f.lastNodeDropID = id
	return true, f.err
}

func (f *fakeAdminService) UpdateManagedServer(_ context.Context, serverType string, id int64, values map[string]any) (bool, error) {
	f.lastNodeUpdateType = serverType
	f.lastNodeUpdateID = id
	f.lastNodeUpdate = values
	return true, f.err
}

func (f *fakeAdminService) CopyManagedServer(_ context.Context, serverType string, id int64) (bool, error) {
	f.lastNodeCopyType = serverType
	f.lastNodeCopyID = id
	return true, f.err
}

func (f *fakeAdminService) FetchConfig(_ context.Context, key string) (map[string]any, error) {
	f.lastConfigKey = key
	return f.configData, f.err
}

func (f *fakeAdminService) SaveConfig(_ context.Context, values map[string]any) (bool, error) {
	f.lastConfigSave = values
	return true, f.err
}

func (f *fakeAdminService) ListThemes(_ context.Context) (map[string]any, error) {
	return f.themes, f.err
}

func (f *fakeAdminService) GetThemeConfig(_ context.Context, _ string) (map[string]any, error) {
	return f.themeConfig, f.err
}

func (f *fakeAdminService) SaveThemeConfig(_ context.Context, _ string, values map[string]any) (map[string]any, error) {
	return values, f.err
}

func (f *fakeAdminService) ListEmailTemplates(_ context.Context) ([]string, error) {
	return f.emailTemplates, f.err
}

func (f *fakeAdminService) ListThemeTemplates(_ context.Context) ([]string, error) {
	return f.themeTemplates, f.err
}

func (f *fakeAdminService) SetTelegramWebhook(_ context.Context, token string) (bool, error) {
	f.lastWebhookToken = token
	return true, f.err
}

func (f *fakeAdminService) GetTelegramAdminStatus(_ context.Context, adminID int64) (admin.TelegramAdminStatus, error) {
	f.lastTelegramAdminID = adminID
	return f.telegramStatus, f.err
}

func (f *fakeAdminService) SendTelegramTestMessage(_ context.Context, adminID int64) (bool, error) {
	f.lastTelegramAdminID = adminID
	return f.telegramTestOK, f.err
}

func (f *fakeAdminService) TestSendMail(_ context.Context, email string) (admin.ConfigMailTestLog, error) {
	f.lastMailTestEmail = email
	return f.mailTestLog, f.err
}

func (f *fakeAdminService) ListPlans(_ context.Context) ([]admin.PlanRecord, error) {
	return f.plans, f.err
}

func (f *fakeAdminService) SavePlan(_ context.Context, req admin.PlanSaveRequest) (bool, error) {
	f.lastPlanSave = req
	return true, f.err
}

func (f *fakeAdminService) DeletePlan(_ context.Context, id int64) (bool, error) {
	f.lastPlanDropID = id
	return true, f.err
}

func (f *fakeAdminService) TogglePlan(_ context.Context, req admin.PlanToggleRequest) (bool, error) {
	f.lastPlanUpdate = req
	return true, f.err
}

func (f *fakeAdminService) SortPlans(_ context.Context, ids []int64) (bool, error) {
	f.lastPlanSortIDs = append([]int64(nil), ids...)
	return true, f.err
}

func (f *fakeAdminService) ListNotices(_ context.Context) ([]admin.NoticeRecord, error) {
	return f.notices, f.err
}

func (f *fakeAdminService) SaveNotice(_ context.Context, req admin.NoticeSaveRequest) (bool, error) {
	f.lastNoticeSave = req
	return true, f.err
}

func (f *fakeAdminService) DeleteNotice(_ context.Context, id int64) (bool, error) {
	f.lastNoticeDrop = id
	return true, f.err
}

func (f *fakeAdminService) ToggleNotice(_ context.Context, id int64) (bool, error) {
	f.lastNoticeShow = id
	return true, f.err
}

func (f *fakeAdminService) ListCoupons(_ context.Context, req admin.CouponListRequest) (admin.CouponListResult, error) {
	f.lastCouponList = req
	return f.couponList, f.err
}

func (f *fakeAdminService) GenerateCoupon(_ context.Context, req admin.CouponGenerateRequest) (string, bool, error) {
	f.lastCouponSave = req
	return f.couponCSV, req.GenerateCount != nil && *req.GenerateCount > 0, f.err
}

func (f *fakeAdminService) DeleteCoupon(_ context.Context, id int64) (bool, error) {
	f.lastCouponDrop = id
	return true, f.err
}

func (f *fakeAdminService) ToggleCoupon(_ context.Context, id int64) (bool, error) {
	f.lastCouponShow = id
	return true, f.err
}

func (f *fakeAdminService) ListGiftcards(_ context.Context, req admin.GiftcardListRequest) (admin.GiftcardListResult, error) {
	f.lastGiftcardList = req
	return f.giftcardList, f.err
}

func (f *fakeAdminService) GenerateGiftcard(_ context.Context, req admin.GiftcardGenerateRequest) (string, bool, error) {
	f.lastGiftcardSave = req
	return f.giftcardCSV, req.GenerateCount != nil && *req.GenerateCount > 0, f.err
}

func (f *fakeAdminService) DeleteGiftcard(_ context.Context, id int64) (bool, error) {
	f.lastGiftcardDrop = id
	return true, f.err
}

func (f *fakeAdminService) ListKnowledges(_ context.Context) ([]admin.KnowledgeRecord, error) {
	return f.knowledges, f.err
}

func (f *fakeAdminService) GetKnowledge(_ context.Context, id int64) (admin.KnowledgeRecord, error) {
	f.lastKnowledgeID = id
	return f.knowledgeDetail, f.err
}

func (f *fakeAdminService) ListKnowledgeCategories(_ context.Context) ([]string, error) {
	return f.knowledgeCats, f.err
}

func (f *fakeAdminService) SaveKnowledge(_ context.Context, req admin.KnowledgeSaveRequest) (bool, error) {
	f.lastKnowledgeSave = req
	return true, f.err
}

func (f *fakeAdminService) ToggleKnowledge(_ context.Context, id int64) (bool, error) {
	f.lastKnowledgeShow = id
	return true, f.err
}

func (f *fakeAdminService) DeleteKnowledge(_ context.Context, id int64) (bool, error) {
	f.lastKnowledgeDrop = id
	return true, f.err
}

func (f *fakeAdminService) SortKnowledges(_ context.Context, ids []int64) (bool, error) {
	f.lastKnowledgeSort = append([]int64(nil), ids...)
	return true, f.err
}

func (f *fakeAdminService) ListPayments(_ context.Context) ([]admin.PaymentRecord, error) {
	return f.payments, f.err
}

func (f *fakeAdminService) ListPaymentMethods(_ context.Context) ([]string, error) {
	return f.methods, f.err
}

func (f *fakeAdminService) GetPaymentForm(_ context.Context, gateway string, id *int64) (map[string]admin.PaymentFormField, error) {
	f.lastGateway = gateway
	f.lastFormID = id
	return f.form, f.err
}

func (f *fakeAdminService) SavePayment(_ context.Context, req admin.PaymentSaveRequest) (bool, error) {
	f.lastSave = req
	return true, f.err
}

func (f *fakeAdminService) DeletePayment(_ context.Context, id int64) (bool, error) {
	f.lastDropID = id
	return true, f.err
}

func (f *fakeAdminService) TogglePayment(_ context.Context, id int64) (bool, error) {
	f.lastShowID = id
	return true, f.err
}

func (f *fakeAdminService) SortPayments(_ context.Context, ids []int64) (bool, error) {
	f.lastSortIDs = append([]int64(nil), ids...)
	return true, f.err
}

func (f *fakeAdminService) FetchOrders(_ context.Context, req admin.OrderFetchRequest) (admin.OrderListResult, error) {
	f.lastFetch = req
	return f.orderList, f.err
}

func (f *fakeAdminService) GetOrderDetail(_ context.Context, id int64) (map[string]any, error) {
	f.lastDetailID = id
	return f.orderDetail, f.err
}

func (f *fakeAdminService) UpdateOrder(_ context.Context, req admin.OrderUpdateRequest) (bool, error) {
	f.lastUpdate = req
	return true, f.err
}

func (f *fakeAdminService) MarkOrderPaid(_ context.Context, tradeNo string) (bool, error) {
	f.lastPaid = tradeNo
	return true, f.err
}

func (f *fakeAdminService) CancelManagedOrder(_ context.Context, tradeNo string) (bool, error) {
	f.lastCancel = tradeNo
	return true, f.err
}

func (f *fakeAdminService) RefundManagedOrder(_ context.Context, tradeNo string) (bool, error) {
	f.lastRefund = tradeNo
	return true, f.err
}

func (f *fakeAdminService) AssignOrder(_ context.Context, req admin.OrderAssignRequest) (string, error) {
	f.lastAssign = req
	return f.assignTrade, f.err
}

func (f *fakeAdminService) ListTickets(_ context.Context, req admin.TicketListRequest) (admin.TicketListResult, error) {
	f.lastTicketFetch = req
	return f.ticketList, f.err
}

func (f *fakeAdminService) GetTicket(_ context.Context, id int64) (admin.TicketDetail, error) {
	f.lastTicketID = id
	return f.ticketDetail, f.err
}

func (f *fakeAdminService) ReplyTicket(_ context.Context, req admin.TicketReplyRequest) (bool, error) {
	f.lastTicketReply = req
	return true, f.err
}

func (f *fakeAdminService) CloseTicket(_ context.Context, id int64) (bool, error) {
	f.lastTicketClose = id
	return true, f.err
}

func (f *fakePassportService) PV(_ context.Context, inviteCode string) error {
	f.lastInviteCode = inviteCode
	return f.err
}

func (f *fakePassportService) SendEmailVerify(_ context.Context, req passport.SendEmailVerifyRequest) error {
	f.lastSendVerify = req
	return f.err
}

func (f *fakePassportService) Register(_ context.Context, req passport.RegisterRequest) (passport.AuthData, error) {
	f.lastRegister = req
	return f.authData, f.err
}

func (f *fakePassportService) Login(_ context.Context, req passport.LoginRequest) (passport.AuthData, error) {
	f.lastLogin = req
	return f.authData, f.err
}

func (f *fakePassportService) Forget(_ context.Context, req passport.ForgetRequest) error {
	f.lastForget = req
	return f.err
}

func (f *fakePassportService) TokenLogin(_ context.Context, req passport.TokenLoginRequest) (passport.TokenLoginResult, error) {
	f.lastTokenLogin = req
	if req.Token != "" {
		return passport.TokenLoginResult{
			RedirectURL: "http://127.0.0.1/#/login?verify=" + req.Token + "&redirect=" + req.Redirect,
		}, nil
	}
	if f.err != nil {
		return passport.TokenLoginResult{}, f.err
	}
	return passport.TokenLoginResult{
		RedirectURL: "",
		AuthData:    &f.authData,
	}, nil
}

func (f *fakePassportService) GetQuickLoginURL(_ context.Context, req passport.QuickLoginRequest) (string, error) {
	f.lastQuickLogin = req
	return f.quickLoginURL, f.err
}

func (f *fakePassportService) LoginWithMailLink(_ context.Context, req passport.LoginWithMailLinkRequest) (any, error) {
	f.lastMailLink = req
	return f.mailLinkResult, f.err
}

func TestRouterPassportPVEndpoint(t *testing.T) {
	service := &fakePassportService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithPassportService(service),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/comm/pv",
		strings.NewReader(`{"invite_code":"ABC12345"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastInviteCode != "ABC12345" {
		t.Fatalf("expected invite code ABC12345, got %q", service.lastInviteCode)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected passport pv json response: %v", err)
	}

	data, ok := payload["data"].(bool)
	if !ok || !data {
		t.Fatalf("expected data=true, got %#v", payload["data"])
	}
}

func TestRouterPassportSendEmailVerifyEndpoint(t *testing.T) {
	service := &fakePassportService{}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/comm/sendEmailVerify",
		strings.NewReader(`{"email":"user@example.com","isforget":1}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go-test")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastSendVerify.Email != "user@example.com" || service.lastSendVerify.IsForget != 1 {
		t.Fatalf("unexpected send verify payload: %#v", service.lastSendVerify)
	}
}

func TestRouterPassportRegisterEndpoint(t *testing.T) {
	service := &fakePassportService{
		authData: passport.AuthData{
			Token:    "token-1",
			IsAdmin:  0,
			AuthData: "jwt-1",
		},
	}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/auth/register",
		strings.NewReader(`{"email":"new@example.com","password":"password123","invite_code":"INV123","email_code":"123456"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go-test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastRegister.Email != "new@example.com" || service.lastRegister.InviteCode != "INV123" {
		t.Fatalf("unexpected register payload: %#v", service.lastRegister)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected register json response: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["auth_data"] != "jwt-1" {
		t.Fatalf("unexpected register response payload: %#v", payload["data"])
	}
}

func TestRouterPassportLoginEndpoint(t *testing.T) {
	service := &fakePassportService{
		authData: passport.AuthData{
			Token:    "token-2",
			IsAdmin:  1,
			AuthData: "jwt-2",
		},
	}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/auth/login",
		strings.NewReader(`{"email":"admin@example.com","password":"password123"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go-test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastLogin.Email != "admin@example.com" {
		t.Fatalf("unexpected login payload: %#v", service.lastLogin)
	}
}

func TestRouterPassportForgetEndpoint(t *testing.T) {
	service := &fakePassportService{}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/auth/forget",
		strings.NewReader(`{"email":"user@example.com","password":"newpassword","email_code":"123456"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastForget.EmailCode != "123456" {
		t.Fatalf("unexpected forget payload: %#v", service.lastForget)
	}
}

func TestRouterPassportToken2LoginEndpoint(t *testing.T) {
	service := &fakePassportService{
		authData: passport.AuthData{
			Token:    "token-3",
			IsAdmin:  0,
			AuthData: "jwt-3",
		},
	}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/passport/auth/token2Login?verify=ABC123&redirect=dashboard",
		nil,
	)
	req.Header.Set("User-Agent", "go-test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastTokenLogin.Verify != "ABC123" || service.lastTokenLogin.Redirect != "dashboard" {
		t.Fatalf("unexpected token2Login payload: %#v", service.lastTokenLogin)
	}
}

func TestRouterPassportToken2LoginRedirectEndpoint(t *testing.T) {
	service := &fakePassportService{}
	service.err = errors.New("unused")
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/passport/auth/token2Login?token=MAILTOKEN&redirect=dashboard",
		nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusFound {
		t.Fatalf("expected redirect status, got %d", rec.Code)
	}
}

func TestRouterPassportGetQuickLoginUrlEndpoint(t *testing.T) {
	service := &fakePassportService{quickLoginURL: "http://127.0.0.1/#/login?verify=xx"}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/auth/getQuickLoginUrl",
		strings.NewReader(`{"auth_data":"jwt-x","redirect":"dashboard"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if service.lastQuickLogin.AuthData != "jwt-x" {
		t.Fatalf("unexpected quick login payload: %#v", service.lastQuickLogin)
	}
}

func TestRouterPassportLoginWithMailLinkEndpointDisabled(t *testing.T) {
	service := &fakePassportService{mailLinkResult: "http://127.0.0.1/#/login?verify=mail"}
	router := NewRouter(config.Config{AppName: "forest-go"}, WithPassportService(service))

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/passport/auth/loginWithMailLink",
		strings.NewReader(`{"email":"user@example.com","redirect":"dashboard"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if service.lastMailLink.Email != "" {
		t.Fatalf("expected mail link service to stay unused, got %#v", service.lastMailLink)
	}
}

func TestRouterUserCheckLoginEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 10, IsAdmin: 1},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/checkLogin?auth_data=jwt-x", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if sessionService.lastAuthToken != "jwt-x" || sessionService.lastRequireAdmin {
		t.Fatalf("unexpected auth call: token=%q requireAdmin=%v", sessionService.lastAuthToken, sessionService.lastRequireAdmin)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json response: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["is_login"] != true || data["is_admin"] != true {
		t.Fatalf("unexpected checkLogin payload: %#v", payload["data"])
	}
}

func TestRouterUserCheckLoginReturnsServiceUnavailableOnSessionError(t *testing.T) {
	sessionService := &fakeSessionService{
		authErr: session.ErrUnavailable,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/checkLogin?auth_data=jwt-x", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json response: %v", err)
	}
	if payload["message"] != "会话服务不可用" {
		t.Fatalf("unexpected session unavailable payload: %#v", payload)
	}
}

func TestRouterUserInfoEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 10},
	}
	userService := &fakeUserService{
		info: user.Info{
			Email:          "user@example.com",
			TransferEnable: 1024,
			AvatarURL:      "https://cravatar.cn/avatar/test?s=64&d=identicon",
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/info", nil)
	req.Header.Set("Authorization", "jwt-y")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected user service to be called with user id 10, got %d", userService.lastUserID)
	}
}

func TestRouterUserGetActiveSessionEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 10},
		sessions: map[string]session.SessionMeta{
			"sess-1": {
				IP:      "127.0.0.1",
				LoginAt: 123,
				UA:      "go-test",
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/getActiveSession?auth_data=jwt-z", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if sessionService.lastListUserID != 10 {
		t.Fatalf("expected list sessions for user id 10, got %d", sessionService.lastListUserID)
	}
}

func TestRouterUserGetStatEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 10},
	}
	userService := &fakeUserService{
		stat: []int64{1, 2, 3},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/getStat", nil)
	req.Header.Set("Authorization", "jwt-stat")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected stat service to be called with user id 10, got %d", userService.lastUserID)
	}
}

func TestRouterUserGetSubscribeEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		subscribe: user.Subscribe{
			Token:          "token-1",
			SubscribeURL:   "/api/v1/client/subscribe?token=token-1",
			AllowNewPeriod: "1",
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/getSubscribe?auth_data=jwt-sub", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected subscribe service to be called with user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `"allow_new_period":"1"`) {
		t.Fatalf("expected allow_new_period string payload, got %s", rec.Body.String())
	}
}

func TestRouterUserServerFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		servers: []map[string]any{
			{"id": int64(1), "type": "vmess", "name": "Node-A", "cache_key": "vmess-1-1710000000-1"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/server/fetch?auth_data=jwt-sub", nil)
	req.Header.Set("User-Agent", "ClashMeta/1.0")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected server service to be called with user id 10, got %d", userService.lastUserID)
	}
	if userService.lastServerUA != "ClashMeta/1.0" {
		t.Fatalf("expected user agent to be forwarded, got %q", userService.lastServerUA)
	}
	if !strings.Contains(rec.Body.String(), `"Node-A"`) {
		t.Fatalf("expected server payload, got %s", rec.Body.String())
	}
}

func TestRouterUserServerFetchReturnsNotModifiedWhenETagMatches(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		servers: []map[string]any{
			{"id": int64(1), "type": "vmess", "name": "Node-A", "cache_key": "vmess-1-1710000000-1"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	firstReq := httptest.NewRequest(http.MethodGet, "/api/v1/user/server/fetch?auth_data=jwt-sub", nil)
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first request 200, got %d body=%s", firstRec.Code, firstRec.Body.String())
	}

	etag := strings.TrimSpace(firstRec.Header().Get("ETag"))
	if etag == "" {
		t.Fatalf("expected ETag header to be set")
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/api/v1/user/server/fetch?auth_data=jwt-sub", nil)
	secondReq.Header.Set("If-None-Match", etag)
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, secondReq)

	if secondRec.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.TrimSpace(secondRec.Body.String()) != "" {
		t.Fatalf("expected empty body for 304, got %q", secondRec.Body.String())
	}
}

func TestRouterUserTelegramGetBotInfoEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		telegramBotInfo: map[string]any{"username": "forest_bot"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/telegram/getBotInfo?auth_data=jwt-tg", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"forest_bot"`) {
		t.Fatalf("expected telegram bot payload, got %s", rec.Body.String())
	}
}

func TestRouterUserUnbindTelegramEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{unbindTelegramOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/unbindTelegram?auth_data=jwt-unbind", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected unbind telegram for user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `true`) {
		t.Fatalf("expected true payload, got %s", rec.Body.String())
	}
}

func TestRouterUserResetSecurityEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{resetSecurityURL: "/api/v1/client/subscribe?token=new-token"}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/resetSecurity?auth_data=jwt-reset", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected reset security for user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `new-token`) {
		t.Fatalf("expected subscribe url payload, got %s", rec.Body.String())
	}
}

func TestRouterUserUpdateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/update", strings.NewReader(`{"auth_data":"jwt-update","auto_renewal":1,"remind_expire":0,"remind_traffic":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected update for user id 10, got %d", userService.lastUserID)
	}
	if userService.lastProfileUpdate.AutoRenewal == nil || *userService.lastProfileUpdate.AutoRenewal != 1 {
		t.Fatalf("unexpected auto renewal payload: %#v", userService.lastProfileUpdate)
	}
	if userService.lastProfileUpdate.RemindExpire == nil || *userService.lastProfileUpdate.RemindExpire != 0 {
		t.Fatalf("unexpected remind expire payload: %#v", userService.lastProfileUpdate)
	}
	if userService.lastProfileUpdate.RemindTraffic == nil || *userService.lastProfileUpdate.RemindTraffic != 1 {
		t.Fatalf("unexpected remind traffic payload: %#v", userService.lastProfileUpdate)
	}
}

func TestRouterUserGetQuickLoginURLEndpoint(t *testing.T) {
	passportService := &fakePassportService{quickLoginURL: "https://app.example.com/#/login?verify=abc123&redirect=dashboard"}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithPassportService(passportService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/getQuickLoginUrl", strings.NewReader("auth_data=jwt-quick&redirect=dashboard"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if passportService.lastQuickLogin.AuthData != "jwt-quick" || passportService.lastQuickLogin.Redirect != "dashboard" {
		t.Fatalf("unexpected quick login payload: %#v", passportService.lastQuickLogin)
	}
	if !strings.Contains(rec.Body.String(), "abc123") {
		t.Fatalf("expected quick login url payload, got %s", rec.Body.String())
	}
}

func TestRouterUserChangePasswordEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/changePassword", strings.NewReader(`{"auth_data":"jwt-pass","old_password":"old-secret","new_password":"new-secret-123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 || userService.lastOldPassword != "old-secret" || userService.lastNewPassword != "new-secret-123" {
		t.Fatalf("unexpected change password request: user=%d old=%q new=%q", userService.lastUserID, userService.lastOldPassword, userService.lastNewPassword)
	}
}

func TestRouterUserChangePasswordRejectsShortPassword(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/changePassword", strings.NewReader(`{"auth_data":"jwt-pass","old_password":"old-secret","new_password":"short"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "密码长度不能少于 8 位") {
		t.Fatalf("expected password validation message, got %s", rec.Body.String())
	}
}

func TestRouterUserTransferEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{transferOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/transfer", strings.NewReader(`{"auth_data":"jwt-transfer","transfer_amount":1500}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if sessionService.lastAuthToken != "jwt-transfer" {
		t.Fatalf("expected jwt-transfer auth token, got %q", sessionService.lastAuthToken)
	}
	if userService.lastUserID != 10 || userService.lastTransferAmount != 1500 {
		t.Fatalf("unexpected transfer request: user=%d amount=%d", userService.lastUserID, userService.lastTransferAmount)
	}
	if !strings.Contains(rec.Body.String(), `"data":true`) {
		t.Fatalf("expected successful transfer payload, got %s", rec.Body.String())
	}
}

func TestRouterUserTransferRejectsInvalidAmount(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/transfer", strings.NewReader(`{"auth_data":"jwt-transfer","transfer_amount":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "划转金额参数错误") {
		t.Fatalf("expected transfer validation message, got %s", rec.Body.String())
	}
	if userService.lastTransferAmount != 0 {
		t.Fatalf("expected transfer service not called, got amount=%d", userService.lastTransferAmount)
	}
}

func TestRouterUserNewPeriodEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{newPeriodOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/newPeriod", strings.NewReader(`{"auth_data":"jwt-period"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected newPeriod for user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `"data":true`) {
		t.Fatalf("expected successful newPeriod payload, got %s", rec.Body.String())
	}
}

func TestRouterUserRedeemGiftcardEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		redeemGiftcardResult: map[string]any{"type": int64(5), "value": int64(30)},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/redeemgiftcard", strings.NewReader(`{"auth_data":"jwt-gift","giftcard":"WELCOME2026"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 || userService.lastRedeemGiftcard != "WELCOME2026" {
		t.Fatalf("unexpected redeem request: user=%d giftcard=%q", userService.lastUserID, userService.lastRedeemGiftcard)
	}
	if !strings.Contains(rec.Body.String(), `"type":5`) || !strings.Contains(rec.Body.String(), `"value":30`) {
		t.Fatalf("expected redeem payload with type/value, got %s", rec.Body.String())
	}
}

func TestRouterUserPlanFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	planID := int64(3)
	userService := &fakeUserService{
		plans: map[string]any{"id": int64(3), "name": "Starter"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/plan/fetch?id=3", nil)
	req.Header.Set("Authorization", "jwt-plan")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("expected user plan fetch to disable cache, got %q", rec.Header().Get("Cache-Control"))
	}
	if userService.lastPlanID == nil || *userService.lastPlanID != planID {
		t.Fatalf("expected plan id 3, got %#v", userService.lastPlanID)
	}
}

func TestRouterUserNoticeFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		notices: []map[string]any{
			{"id": int64(9), "title": "Maintenance"},
		},
		noticesTotal: 12,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/notice/fetch?auth_data=jwt-user&current=2&pageSize=7", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastNoticeCur != 2 || userService.lastNoticeSize != 7 {
		t.Fatalf("unexpected notice paging: current=%d pageSize=%d", userService.lastNoticeCur, userService.lastNoticeSize)
	}
}

func TestRouterUserNoticeDetailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		noticeDetail: map[string]any{"id": int64(9), "title": "Maintenance"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/notice/fetch?auth_data=jwt-user&id=9", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastNoticeID != 9 {
		t.Fatalf("expected notice detail id 9, got %d", userService.lastNoticeID)
	}
}

func TestRouterUserInviteSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{inviteSaveOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/invite/save?auth_data=jwt-user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected invite save user id 10, got %d", userService.lastUserID)
	}
}

func TestRouterUserInviteFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		inviteOverview: map[string]any{
			"codes": []map[string]any{{"id": int64(1), "code": "ABCD1234"}},
			"stat":  []int64{3, 500, 200, 20, 800},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/invite/fetch?auth_data=jwt-user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected invite fetch user id 10, got %d", userService.lastUserID)
	}
}

func TestRouterUserInviteDetailsEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		inviteDetails: []map[string]any{{"id": int64(11), "trade_no": "TN-1"}},
		inviteTotal:   18,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/invite/details?auth_data=jwt-user&current=3&page_size=20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastInviteCur != 3 || userService.lastInviteSize != 20 {
		t.Fatalf("unexpected invite details paging: current=%d pageSize=%d", userService.lastInviteCur, userService.lastInviteSize)
	}
}

func TestRouterUserInviteCampaignSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		inviteCampaign: map[string]any{
			"enabled":  1,
			"settings": map[string]any{"reward_amount": int64(1000)},
			"data":     map[string]any{"id": int64(8), "invite_code": "ABCDEFGH"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/invite/campaign/save", strings.NewReader("auth_data=jwt-user&plan_id=3&period=month_price"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastInviteCampaignPlanID != 3 || userService.lastInviteCampaignPeriod != "month_price" {
		t.Fatalf("unexpected invite campaign save request: plan=%d period=%q", userService.lastInviteCampaignPlanID, userService.lastInviteCampaignPeriod)
	}
	if !strings.Contains(rec.Body.String(), `"ABCDEFGH"`) {
		t.Fatalf("expected invite campaign body, got %s", rec.Body.String())
	}
}

func TestRouterUserInviteCampaignFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		inviteCampaign: map[string]any{
			"enabled":  1,
			"settings": map[string]any{"reward_amount": int64(1000)},
			"data":     map[string]any{"id": int64(8), "invite_code": "ABCDEFGH"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/invite/campaign/fetch?auth_data=jwt-user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `"reward_amount":1000`) {
		t.Fatalf("expected invite campaign settings body, got %s", rec.Body.String())
	}
}

func TestRouterUserInviteCampaignRecordsEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		inviteCampaignRecords: map[string]any{
			"data":  []map[string]any{{"id": int64(5), "invitee_email": "invitee@example.com"}},
			"total": int64(1),
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/invite/campaign/records?auth_data=jwt-user&campaign_id=8&current=3&page_size=15", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastInviteCampaignID == nil || *userService.lastInviteCampaignID != 8 || userService.lastInviteCampaignCurrent != 3 || userService.lastInviteCampaignPageSize != 15 {
		t.Fatalf("unexpected invite campaign records request: id=%#v current=%d page_size=%d", userService.lastInviteCampaignID, userService.lastInviteCampaignCurrent, userService.lastInviteCampaignPageSize)
	}
	if !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("expected total in invite campaign records body, got %s", rec.Body.String())
	}
}

func TestRouterUserInviteCampaignAbandonEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/invite/campaign/abandon", strings.NewReader("auth_data=jwt-user"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `"data":true`) {
		t.Fatalf("expected json true response, got %s", rec.Body.String())
	}
}

func TestRouterUserTicketFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		tickets: []map[string]any{{"id": int64(21), "subject": "Need help"}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/ticket/fetch?auth_data=jwt-user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected ticket fetch user id 10, got %d", userService.lastUserID)
	}
}

func TestRouterUserTicketFetchByIDEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		ticketDetail: map[string]any{
			"id":      int64(21),
			"subject": "Need help",
			"message": []map[string]any{{"id": int64(1), "message": "hello", "is_me": true}},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/ticket/fetch?auth_data=jwt-user&id=21", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTicketID != 21 {
		t.Fatalf("expected ticket detail id 21, got %d", userService.lastTicketID)
	}
}

func TestRouterUserTicketSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{ticketSaveOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/ticket/save", strings.NewReader("auth_data=jwt-user&subject=Need+help&level=1&message=hello"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTicketSave.Subject != "Need help" || userService.lastTicketSave.Level != 1 || userService.lastTicketSave.Message != "hello" {
		t.Fatalf("unexpected ticket save request: %#v", userService.lastTicketSave)
	}
}

func TestRouterUserTicketReplyEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{ticketReplyOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/ticket/reply", strings.NewReader("auth_data=jwt-user&id=21&message=handled"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTicketID != 21 || userService.lastTicketReply != "handled" {
		t.Fatalf("unexpected ticket reply request: id=%d message=%q", userService.lastTicketID, userService.lastTicketReply)
	}
}

func TestRouterUserTicketCloseEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{ticketCloseOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/ticket/close", strings.NewReader("auth_data=jwt-user&id=21"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTicketID != 21 {
		t.Fatalf("expected ticket close id 21, got %d", userService.lastTicketID)
	}
}

func TestRouterUserTicketWithdrawEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{ticketWithdrawOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/ticket/withdraw", strings.NewReader("auth_data=jwt-user&withdraw_method=USDT&withdraw_account=TAbc123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 || userService.lastWithdrawMethod != "USDT" || userService.lastWithdrawAccount != "TAbc123" {
		t.Fatalf("unexpected withdraw request: user=%d method=%q account=%q", userService.lastUserID, userService.lastWithdrawMethod, userService.lastWithdrawAccount)
	}
}

func TestRouterUserTicketWithdrawEndpointRequiresMethod(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{ticketWithdrawOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/ticket/withdraw", strings.NewReader("auth_data=jwt-user&withdraw_account=TAbc123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "提现方式不能为空") {
		t.Fatalf("expected withdraw method validation message, got %s", rec.Body.String())
	}
}

func TestRouterUserCommConfigEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		commConfig: map[string]any{
			"currency":         "CNY",
			"withdraw_methods": []string{"USDT", "支付宝"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/comm/config?auth_data=jwt-user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"currency":"CNY"`) {
		t.Fatalf("expected currency in body, got %s", rec.Body.String())
	}
}

func TestRouterUserCommGetStripePublicKeyEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{stripePublicKey: "pk_live_123"}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/comm/getStripePublicKey", strings.NewReader("auth_data=jwt-user&id=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastPaymentMethodID != 9 {
		t.Fatalf("expected payment id 9, got %d", userService.lastPaymentMethodID)
	}
	if !strings.Contains(rec.Body.String(), `"pk_live_123"`) {
		t.Fatalf("expected stripe public key in body, got %s", rec.Body.String())
	}
}

func TestRouterUserCouponCheckEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{coupon: map[string]any{"id": int64(7), "code": "SPRING"}}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/coupon/check", strings.NewReader("auth_data=jwt-user&code=SPRING&plan_id=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastCouponCode != "SPRING" {
		t.Fatalf("expected coupon code SPRING, got %q", userService.lastCouponCode)
	}
	if userService.lastCouponPlanID == nil || *userService.lastCouponPlanID != 3 {
		t.Fatalf("expected coupon plan id 3, got %#v", userService.lastCouponPlanID)
	}
	if !strings.Contains(rec.Body.String(), `"SPRING"`) {
		t.Fatalf("expected coupon payload in body, got %s", rec.Body.String())
	}
}

func TestRouterUserCouponCheckEndpointRequiresCode(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/coupon/check", strings.NewReader("auth_data=jwt-user"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "优惠券不能为空") {
		t.Fatalf("expected coupon validation message, got %s", rec.Body.String())
	}
}

func TestRouterUserKnowledgeFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		knowledgeList: map[string][]map[string]any{
			"guide": {{"id": int64(11), "title": "FAQ", "category": "guide"}},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/knowledge/fetch?auth_data=jwt-user&language=en-US&keyword=faq", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastKnowledgeLang != "en-US" || userService.lastKnowledgeKeyword != "faq" {
		t.Fatalf("unexpected knowledge list params: language=%q keyword=%q", userService.lastKnowledgeLang, userService.lastKnowledgeKeyword)
	}
	if !strings.Contains(rec.Body.String(), `"FAQ"`) {
		t.Fatalf("expected knowledge list payload, got %s", rec.Body.String())
	}
}

func TestRouterUserKnowledgeFetchByIDEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		knowledgeDetail: map[string]any{"id": int64(11), "title": "FAQ", "body": "body"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/knowledge/fetch?auth_data=jwt-user&id=11&language=en-US", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastKnowledgeID != 11 || userService.lastUserID != 10 {
		t.Fatalf("unexpected knowledge detail request: id=%d user=%d", userService.lastKnowledgeID, userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `"FAQ"`) {
		t.Fatalf("expected knowledge detail payload, got %s", rec.Body.String())
	}
}

func TestRouterUserKnowledgeGetCategoryEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{knowledgeCats: []string{"guide", "billing"}}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/knowledge/getCategory?auth_data=jwt-user&language=en-US", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastKnowledgeLang != "en-US" {
		t.Fatalf("expected language en-US, got %q", userService.lastKnowledgeLang)
	}
	if !strings.Contains(rec.Body.String(), `"guide"`) {
		t.Fatalf("expected knowledge category payload, got %s", rec.Body.String())
	}
}

func TestRouterUserStatGetTrafficLogEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		trafficLogs: []map[string]any{{"u": int64(100), "d": int64(200), "record_at": int64(1710000000), "server_rate": 1.5}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/stat/getTrafficLog?auth_data=jwt-user", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected user id 10, got %d", userService.lastUserID)
	}
	if !strings.Contains(rec.Body.String(), `"server_rate":1.5`) {
		t.Fatalf("expected traffic log payload, got %s", rec.Body.String())
	}
}

func TestRouterUserOrderFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	status := int64(0)
	userService := &fakeUserService{
		orders: []map[string]any{{"trade_no": "T100"}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/order/fetch?status=0", nil)
	req.Header.Set("Authorization", "jwt-order")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastOrderStatus == nil || *userService.lastOrderStatus != status {
		t.Fatalf("expected status 0, got %#v", userService.lastOrderStatus)
	}
}

func TestRouterUserOrderDetailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		orderDetail: map[string]any{"trade_no": "T101"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/order/detail?trade_no=T101", nil)
	req.Header.Set("Authorization", "jwt-order")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTradeNo != "T101" {
		t.Fatalf("expected trade no T101, got %q", userService.lastTradeNo)
	}
}

func TestRouterUserOrderCheckEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		orderStatus: 3,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/order/check?trade_no=T102", nil)
	req.Header.Set("Authorization", "jwt-order")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTradeNo != "T102" {
		t.Fatalf("expected trade no T102, got %q", userService.lastTradeNo)
	}
}

func TestRouterUserOrderGetPaymentMethodEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		paymentMethods: []map[string]any{{"id": int64(1), "name": "Stripe"}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/order/getPaymentMethod", nil)
	req.Header.Set("Authorization", "jwt-order")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRouterUserOrderSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		orderTradeNo: "T200",
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/save", strings.NewReader(`{"auth_data":"jwt-order","plan_id":"3","period":"month_price","coupon_code":"SPRING"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastUserID != 10 {
		t.Fatalf("expected user id 10, got %d", userService.lastUserID)
	}
	if userService.lastOrderSave.PlanID != 3 || userService.lastOrderSave.Period != "month_price" || userService.lastOrderSave.CouponCode != "SPRING" {
		t.Fatalf("unexpected save request: %#v", userService.lastOrderSave)
	}
	if !strings.Contains(rec.Body.String(), `"T200"`) {
		t.Fatalf("expected trade no in body, got %s", rec.Body.String())
	}
}

func TestRouterUserOrderSaveUnsupportedPaymentGatewayMessage(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{
		err: user.ErrUnsupportedPaymentGateway,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/save", strings.NewReader(`{"auth_data":"jwt-order","plan_id":"3","period":"month_price"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"不支持当前支付网关"`) {
		t.Fatalf("expected unsupported gateway message, got %s", rec.Body.String())
	}
}

func TestRouterUserOrderCheckoutEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	paymentService := &fakePaymentService{
		checkoutResult: payment.CheckoutResult{
			Type: 1,
			Data: "https://pay.example.com/T201",
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithPaymentService(paymentService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/checkout", strings.NewReader(`{"auth_data":"jwt-order","trade_no":"T201","method":"9","token":"tok_123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if paymentService.lastCheckout.TradeNo != "T201" || paymentService.lastCheckout.MethodID != 9 || paymentService.lastCheckout.Token != "tok_123" {
		t.Fatalf("unexpected checkout request: %#v", paymentService.lastCheckout)
	}
	if !strings.Contains(rec.Body.String(), `"type":1`) {
		t.Fatalf("expected checkout type in body, got %s", rec.Body.String())
	}
}

func TestRouterUserOrderCheckoutUnsupportedPaymentGatewayMessage(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	paymentService := &fakePaymentService{
		err: payment.ErrUnsupportedGateway,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithPaymentService(paymentService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/checkout", strings.NewReader(`{"auth_data":"jwt-order","trade_no":"T201","method":"9"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"不支持当前支付网关"`) {
		t.Fatalf("expected unsupported gateway message, got %s", rec.Body.String())
	}
}

func TestRouterUserOrderCheckoutPassesCurrentAccessDomainToPaymentService(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	paymentService := &fakePaymentService{
		checkoutResult: payment.CheckoutResult{
			Type: 1,
			Data: "https://pay.example.com/T201",
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithPaymentService(paymentService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/checkout", strings.NewReader(`{"auth_data":"jwt-order","trade_no":"T201","method":"9","token":"tok_123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://mirror.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if paymentService.lastCheckout.RequestBaseURL != "https://mirror.example.com" {
		t.Fatalf("expected request base url to be forwarded, got %#v", paymentService.lastCheckout)
	}
}

func TestDetectCheckoutRequestBaseURLUsesRefererWhenOriginMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/checkout", nil)
	req.Header.Set("Referer", "https://mirror.example.com/#/shop")

	got := detectCheckoutRequestBaseURL(req)
	if got != "https://mirror.example.com" {
		t.Fatalf("unexpected checkout request base url: %s", got)
	}
}

func TestDetectCheckoutRequestBaseURLEmptyWithoutBrowserDomainContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/checkout", nil)

	got := detectCheckoutRequestBaseURL(req)
	if got != "" {
		t.Fatalf("expected empty checkout request base url, got %s", got)
	}
}

func TestRouterUserOrderCancelEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	userService := &fakeUserService{cancelOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
		WithUserService(userService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/order/cancel", strings.NewReader(`{"auth_data":"jwt-order","trade_no":"T202"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if userService.lastTradeNo != "T202" {
		t.Fatalf("expected trade no T202, got %q", userService.lastTradeNo)
	}
}

func TestRouterUserRemoveActiveSessionEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user:     &session.Identity{ID: 10},
		removeOK: true,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go"},
		WithSessionService(sessionService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/removeActiveSession", strings.NewReader(`{"auth_data":"jwt-z","session_id":"sess-2"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if sessionService.lastRemoveUserID != 10 || sessionService.lastRemoveSID != "sess-2" {
		t.Fatalf("unexpected remove session call: user=%d session=%q", sessionService.lastRemoveUserID, sessionService.lastRemoveSID)
	}
}

func TestRouterAdminSystemStatusEndpoint(t *testing.T) {
	lastRuntime := int64(123)
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		systemStatus: admin.SystemStatus{
			Schedule:            true,
			Horizon:             false,
			ScheduleLastRuntime: &lastRuntime,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/system/getSystemStatus?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !sessionService.lastRequireAdmin {
		t.Fatalf("expected admin auth requirement")
	}
}

func TestRouterMonitorStatsEndpoint(t *testing.T) {
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithQueueRuntime(fakeQueueRuntime{snapshot: queue.Snapshot{Running: true, Workers: 4, CurrentJobs: 2}}),
	)

	req := httptest.NewRequest(http.MethodGet, "/monitor/api/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"running"`) {
		t.Fatalf("expected running monitor status, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"workers":4`) {
		t.Fatalf("expected workers count in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminSystemLogEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		systemLogs: []map[string]any{
			{"id": int64(9), "level": "error", "title": "Request failed"},
		},
		systemLogsTotal: 1,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/system/getSystemLog?auth_data=jwt-admin&current=2&page_size=50&level=error", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastLogCurrent != 2 || adminService.lastLogPageSize != 50 || adminService.lastLogLevel != "error" {
		t.Fatalf("unexpected system log request: current=%d page_size=%d level=%q", adminService.lastLogCurrent, adminService.lastLogPageSize, adminService.lastLogLevel)
	}
	if !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("expected total in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatOverrideEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		statOverride: map[string]any{
			"online_user": 12,
			"day_income":  3000,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getOverride?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"online_user":12`) {
		t.Fatalf("expected override payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatOrderEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		statOrder: []map[string]any{
			{"type": "注册人数", "date": "03-29", "value": 8},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getOrder?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"注册人数"`) {
		t.Fatalf("expected order stat payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatGetStatEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		statSummary: map[string]any{
			"paid_total":     8800,
			"register_count": 6,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getStat?auth_data=jwt-admin&start_at=1711497600&end_at=1711584000", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastStatStartAt != 1711497600 || adminService.lastStatEndAt != 1711584000 {
		t.Fatalf("unexpected getStat range: start=%d end=%d", adminService.lastStatStartAt, adminService.lastStatEndAt)
	}
	if !strings.Contains(rec.Body.String(), `"paid_total":8800`) {
		t.Fatalf("expected getStat payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatGetRankingEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		statRanking: []map[string]any{
			{"email": "rank@example.com", "count": 4},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getRanking?auth_data=jwt-admin&type=invite_rank&limit=15&start_at=1711497600&end_at=1711584000", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastRankingType != "invite_rank" || adminService.lastRankingLimit != 15 || adminService.lastRecordStartAt != 1711497600 || adminService.lastRecordEndAt != 1711584000 {
		t.Fatalf("unexpected getRanking request: type=%q limit=%d start=%d end=%d", adminService.lastRankingType, adminService.lastRankingLimit, adminService.lastRecordStartAt, adminService.lastRecordEndAt)
	}
	if !strings.Contains(rec.Body.String(), `"rank@example.com"`) {
		t.Fatalf("expected ranking payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatGetStatRecordEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		statRecords: []map[string]any{
			{"record_at": int64(1711497600), "paid_total": 88.8},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getStatRecord?auth_data=jwt-admin&type=paid_total&start_at=1711497600&end_at=1711584000", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastRecordType != "paid_total" || adminService.lastRecordStartAt != 1711497600 || adminService.lastRecordEndAt != 1711584000 {
		t.Fatalf("unexpected getStatRecord request: type=%q start=%d end=%d", adminService.lastRecordType, adminService.lastRecordStartAt, adminService.lastRecordEndAt)
	}
	if !strings.Contains(rec.Body.String(), `"paid_total":88.8`) {
		t.Fatalf("expected stat record payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatServerLastRankEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		serverLastRank: []map[string]any{{"server_name": "HK-1", "total": 123.4}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getServerLastRank?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"HK-1"`) {
		t.Fatalf("expected server last rank payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatServerTodayRankEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		serverTodayRank: []map[string]any{{"server_name": "US-1", "total": 88.1}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getServerTodayRank?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"US-1"`) {
		t.Fatalf("expected server today rank payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatUserLastRankEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		userLastRank: []map[string]any{{"email": "a@example.com", "total": 66.6}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getUserLastRank?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"a@example.com"`) {
		t.Fatalf("expected user last rank payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatUserTodayRankEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		userTodayRank: []map[string]any{{"email": "b@example.com", "total": 77.7}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getUserTodayRank?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"b@example.com"`) {
		t.Fatalf("expected user today rank payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatInviteLastRankEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		inviteLastRank: []map[string]any{{"email": "c@example.com", "count": int64(6)}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getInviteLastRank?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"c@example.com"`) {
		t.Fatalf("expected invite last rank payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatInviteTodayRankEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		inviteTodayRank: []map[string]any{{"email": "d@example.com", "count": int64(8)}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getInviteTodayRank?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"d@example.com"`) {
		t.Fatalf("expected invite today rank payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminStatUserEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		statUserRecords: []map[string]any{{"record_at": int64(1711670400), "u": int64(1), "d": int64(2), "server_rate": "1.00"}},
		statUserTotal:   1,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/stat/getStatUser?auth_data=jwt-admin&user_id=9&current=2&pageSize=20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastStatUserID != 9 || adminService.lastStatCurrent != 2 || adminService.lastStatPageSize != 20 {
		t.Fatalf("unexpected stat user request: user_id=%d current=%d pageSize=%d", adminService.lastStatUserID, adminService.lastStatCurrent, adminService.lastStatPageSize)
	}
	if !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("expected stat user total in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminInviteCampaignFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		inviteList: admin.InviteCampaignListResult{
			Data:  []map[string]any{{"id": int64(8), "user_email": "owner@example.com", "status": int64(0)}},
			Total: 1,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/invite/campaign/fetch?auth_data=jwt-admin&current=2&pageSize=20&filter[0][key]=email&filter[0][condition]==&filter[0][value]=owner@example.com&filter[1][key]=status&filter[1][condition]==&filter[1][value]=0", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastInviteList.Current != 2 || adminService.lastInviteList.PageSize != 20 {
		t.Fatalf("unexpected invite list paging: %#v", adminService.lastInviteList)
	}
	if len(adminService.lastInviteList.Filters) != 2 {
		t.Fatalf("expected 2 invite filters, got %#v", adminService.lastInviteList.Filters)
	}
	if adminService.lastInviteList.Filters[0].Key != "email" || adminService.lastInviteList.Filters[1].Key != "status" {
		t.Fatalf("unexpected invite filters: %#v", adminService.lastInviteList.Filters)
	}
}

func TestRouterAdminInviteCampaignDetailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		inviteDetail: map[string]any{
			"id":          int64(8),
			"invite_code": "ABCDEFGH",
			"user":        map[string]any{"email": "owner@example.com"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/invite/campaign/detail", strings.NewReader(`{"auth_data":"jwt-admin","id":8}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastInviteID != 8 {
		t.Fatalf("expected invite detail id 8, got %d", adminService.lastInviteID)
	}
	if !strings.Contains(rec.Body.String(), `"ABCDEFGH"`) {
		t.Fatalf("expected invite detail body, got %s", rec.Body.String())
	}
}

func TestRouterAdminInviteCampaignRecordsEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		inviteRecords: admin.InviteCampaignRecordListResult{
			Data:  []map[string]any{{"id": int64(5), "invitee_email": "invitee@example.com"}},
			Total: 1,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/invite/campaign/records?auth_data=jwt-admin&campaign_id=8&current=3&page_size=15", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastInviteRecords.CampaignID != 8 || adminService.lastInviteRecords.Current != 3 || adminService.lastInviteRecords.PageSize != 15 {
		t.Fatalf("unexpected invite records request: %#v", adminService.lastInviteRecords)
	}
	if !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("expected invite records total in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminServerGroupFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		serverGroups: []admin.ServerGroupRecord{
			{ID: 3, Name: "VIP", UserCount: 8, ServerCount: 2},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/group/fetch?auth_data=jwt-admin&group_id=3", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGroupID == nil || *adminService.lastGroupID != 3 {
		t.Fatalf("expected group id 3, got %#v", adminService.lastGroupID)
	}
	if !strings.Contains(rec.Body.String(), `"VIP"`) {
		t.Fatalf("expected group payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminServerGroupSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/group/save", strings.NewReader(`{"auth_data":"jwt-admin","id":3,"name":"VIP"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGroupSave.ID == nil || *adminService.lastGroupSave.ID != 3 || adminService.lastGroupSave.Name != "VIP" {
		t.Fatalf("unexpected group save request: %#v", adminService.lastGroupSave)
	}
}

func TestRouterAdminServerGroupDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/group/drop", strings.NewReader("auth_data=jwt-admin&id=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGroupDrop != 3 {
		t.Fatalf("expected drop group id 3, got %d", adminService.lastGroupDrop)
	}
}

func TestRouterAdminServerRouteFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		serverRoutes: []admin.ServerRouteRecord{
			{ID: 5, Remarks: "CN", Match: []string{"geoip:cn"}, Action: "route", ActionValue: stringPtr("proxy")},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/route/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"geoip:cn"`) {
		t.Fatalf("expected route payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminServerRouteSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/route/save", strings.NewReader(`{"auth_data":"jwt-admin","id":5,"remarks":"CN","match":["geoip:cn","domain:example.com"],"action":"route","action_value":"proxy"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastRouteSave.ID == nil || *adminService.lastRouteSave.ID != 5 {
		t.Fatalf("expected route id 5, got %#v", adminService.lastRouteSave)
	}
	if adminService.lastRouteSave.Remarks != "CN" || adminService.lastRouteSave.Action != "route" {
		t.Fatalf("unexpected route save request: %#v", adminService.lastRouteSave)
	}
	if len(adminService.lastRouteSave.Match) != 2 || adminService.lastRouteSave.Match[0] != "geoip:cn" {
		t.Fatalf("unexpected route match values: %#v", adminService.lastRouteSave.Match)
	}
	if adminService.lastRouteSave.ActionValue == nil || *adminService.lastRouteSave.ActionValue != "proxy" {
		t.Fatalf("unexpected route action value: %#v", adminService.lastRouteSave.ActionValue)
	}
}

func TestRouterAdminServerRouteDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/route/drop", strings.NewReader("auth_data=jwt-admin&id=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastRouteDrop != 5 {
		t.Fatalf("expected drop route id 5, got %d", adminService.lastRouteDrop)
	}
}

func TestRouterAdminServerManageGetNodesEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		managedServers: []map[string]any{
			{"id": int64(11), "type": "vmess", "name": "JP-1", "host": "jp.example.com", "port": "443", "available_status": int64(0)},
		},
		clientEntryGroups: []admin.ClientEntryGroupRecord{
			{
				ID:          int64(7),
				Code:        "asia",
				DisplayName: "Asia Entry",
				Members: []admin.ClientEntryGroupMemberRecord{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/manage/getNodes?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"JP-1"`) || !strings.Contains(rec.Body.String(), `"Asia Entry"`) {
		t.Fatalf("expected managed server payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminClientEntryGroupFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		clientEntryGroups: []admin.ClientEntryGroupRecord{
			{
				ID:                int64(7),
				Code:              "asia",
				Name:              "Asia",
				DisplayName:       "Asia Entry",
				Strategy:          "sticky-low-latency",
				HideMemberNodes:   true,
				RemoteEnabled:     true,
				RemoteHost:        "192.0.2.10",
				RemoteSSHPort:     2222,
				RemoteSSHUser:     "root",
				RemoteSSHPassword: "secret",
				RemoteGroupRef:    "专线直出 (#15)",
				RemoteExcludeNames: []string{
					"alice",
					"bob",
				},
				RemoteRefreshSec: 300,
				IPs: []admin.ClientEntryGroupIPRecord{
					{IP: "1.1.1.1"},
					{IP: "8.8.8.8"},
				},
				Members: []admin.ClientEntryGroupMemberRecord{
					{ServerType: "vmess", ServerID: int64(11)},
				},
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/client-entry/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"asia"`) || !strings.Contains(rec.Body.String(), `"ordered-fallback"`) || !strings.Contains(rec.Body.String(), `"1.1.1.1"`) {
		t.Fatalf("expected client entry payload, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"remote_enabled":true`) || !strings.Contains(rec.Body.String(), `"remote_group_ref":"专线直出 (#15)"`) || !strings.Contains(rec.Body.String(), `"remote_exclude_names":["alice","bob"]`) {
		t.Fatalf("expected client entry payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminClientEntryGroupFetchEndpointIncludesRemotePreview(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		clientEntryGroups: []admin.ClientEntryGroupRecord{
			{
				ID:               7,
				Code:             "asia",
				Name:             "Asia",
				DisplayName:      "Asia Entry",
				Strategy:         "ordered-fallback",
				RemoteEnabled:    true,
				RemoteHost:       "https://iso.example.com",
				RemoteGroupRef:   "专线直出 (#15)",
				RemoteRefreshSec: 300,
				IPs: []admin.ClientEntryGroupIPRecord{
					{IP: "1.1.1.1"},
				},
			},
		},
	}
	resolver := &fakeClientEntryRemoteResolver{
		ipsByCode: map[string][]string{
			"asia": {"203.0.113.7", "entry-a.example.com"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
		WithClientEntryRemoteResolver(resolver),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/client-entry/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"remote_resolved_ips":["203.0.113.7","entry-a.example.com"]`) {
		t.Fatalf("expected remote preview in payload, got %s", body)
	}
	if !strings.Contains(body, `"remote_resolved_count":2`) {
		t.Fatalf("expected remote resolved count, got %s", body)
	}
	if !strings.Contains(body, `"effective_entries":["1.1.1.1","203.0.113.7","entry-a.example.com"]`) {
		t.Fatalf("expected effective entries in payload, got %s", body)
	}
	if !strings.Contains(body, `"effective_entry_count":3`) {
		t.Fatalf("expected effective entry count, got %s", body)
	}
	if resolver.lastGroup.Code != "asia" {
		t.Fatalf("expected resolver to receive asia group, got %#v", resolver.lastGroup)
	}
}

func TestRouterAdminClientEntryGroupResolveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		clientEntryGroups: []admin.ClientEntryGroupRecord{
			{
				ID:               7,
				Code:             "asia",
				Name:             "Asia",
				DisplayName:      "Asia Entry",
				Strategy:         "ordered-fallback",
				RemoteEnabled:    true,
				RemoteHost:       "https://iso.example.com",
				RemoteGroupRef:   "专线直出 (#15)",
				RemoteRefreshSec: 300,
				IPs: []admin.ClientEntryGroupIPRecord{
					{IP: "1.1.1.1"},
				},
			},
		},
	}
	resolver := &fakeClientEntryRemoteResolver{
		ipsByCode: map[string][]string{
			"asia": {"203.0.113.9", "entry-b.example.com"},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
		WithClientEntryRemoteResolver(resolver),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/client-entry/resolve", strings.NewReader("auth_data=jwt-admin&id=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":7`) {
		t.Fatalf("expected resolved group id in payload, got %s", body)
	}
	if !strings.Contains(body, `"remote_resolved_ips":["203.0.113.9","entry-b.example.com"]`) {
		t.Fatalf("expected remote resolve payload, got %s", body)
	}
	if !strings.Contains(body, `"effective_entry_count":3`) {
		t.Fatalf("expected effective entry count, got %s", body)
	}
	if adminService.lastClientEntryID == nil || *adminService.lastClientEntryID != 7 {
		t.Fatalf("expected resolve endpoint to query group 7, got %#v", adminService.lastClientEntryID)
	}
}

func TestRouterAdminClientEntryGroupSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/client-entry/save", strings.NewReader(`{"auth_data":"jwt-admin","id":7,"code":"asia","name":"Asia","display_name":"Asia Entry","strategy":"sticky-low-latency","hide_member_nodes":true,"match":["1.1.1.1","8.8.8.8"],"members":[{"server_type":"vmess","server_id":11,"sort":1},{"server_type":"trojan","server_id":12,"sort":2}],"remote_enabled":true,"remote_host":"192.0.2.10","remote_ssh_port":2222,"remote_ssh_user":"root","remote_ssh_password":"secret","remote_group_ref":"专线直出 (#15)","remote_exclude_names":["alice","bob"],"remote_refresh_sec":300}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastClientEntrySave.ID == nil || *adminService.lastClientEntrySave.ID != 7 {
		t.Fatalf("unexpected client entry save id: %#v", adminService.lastClientEntrySave)
	}
	if adminService.lastClientEntrySave.Code != "asia" || adminService.lastClientEntrySave.DisplayName != "Asia Entry" {
		t.Fatalf("unexpected client entry save payload: %#v", adminService.lastClientEntrySave)
	}
	if adminService.lastClientEntrySave.Strategy != "ordered-fallback" {
		t.Fatalf("unexpected normalized client entry strategy: %#v", adminService.lastClientEntrySave)
	}
	if len(adminService.lastClientEntrySave.IPs) != 2 || adminService.lastClientEntrySave.IPs[0].IP != "1.1.1.1" || adminService.lastClientEntrySave.IPs[1].IP != "8.8.8.8" {
		t.Fatalf("unexpected client entry ips: %#v", adminService.lastClientEntrySave.IPs)
	}
	if len(adminService.lastClientEntrySave.Members) != 2 || adminService.lastClientEntrySave.Members[0].ServerType != "vmess" || adminService.lastClientEntrySave.Members[0].ServerID != 11 || adminService.lastClientEntrySave.Members[1].ServerType != "trojan" || adminService.lastClientEntrySave.Members[1].ServerID != 12 {
		t.Fatalf("unexpected client entry members: %#v", adminService.lastClientEntrySave.Members)
	}
	if !adminService.lastClientEntrySave.RemoteEnabled || adminService.lastClientEntrySave.RemoteHost != "192.0.2.10" || adminService.lastClientEntrySave.RemoteSSHPort != 2222 || adminService.lastClientEntrySave.RemoteGroupRef != "专线直出 (#15)" || adminService.lastClientEntrySave.RemoteRefreshSec != 300 {
		t.Fatalf("unexpected client entry remote payload: %#v", adminService.lastClientEntrySave)
	}
	if len(adminService.lastClientEntrySave.RemoteExcludeNames) != 2 || adminService.lastClientEntrySave.RemoteExcludeNames[0] != "alice" || adminService.lastClientEntrySave.RemoteExcludeNames[1] != "bob" {
		t.Fatalf("unexpected client entry remote excludes: %#v", adminService.lastClientEntrySave.RemoteExcludeNames)
	}
}

func TestRouterAdminClientEntryGroupSaveEndpointAcceptsLegacyFormPayload(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	body := strings.NewReader("auth_data=jwt-admin&remarks=Asia+Entry&action=sticky-low-latency&match%5B0%5D=1.1.1.1&match%5B1%5D=8.8.8.8&members%5B0%5D%5Bserver_type%5D=vmess&members%5B0%5D%5Bserver_id%5D=11&members%5B0%5D%5Bsort%5D=1")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/client-entry/save", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if adminService.lastClientEntrySave.Name != "Asia Entry" || adminService.lastClientEntrySave.DisplayName != "Asia Entry" {
		t.Fatalf("unexpected client entry name payload: %#v", adminService.lastClientEntrySave)
	}
	if adminService.lastClientEntrySave.Code != "asia-entry" {
		t.Fatalf("unexpected generated client entry code: %#v", adminService.lastClientEntrySave)
	}
	if adminService.lastClientEntrySave.Strategy != "ordered-fallback" {
		t.Fatalf("unexpected client entry strategy: %#v", adminService.lastClientEntrySave)
	}
	if len(adminService.lastClientEntrySave.IPs) != 2 || adminService.lastClientEntrySave.IPs[0].IP != "1.1.1.1" || adminService.lastClientEntrySave.IPs[1].IP != "8.8.8.8" {
		t.Fatalf("unexpected client entry ips: %#v", adminService.lastClientEntrySave.IPs)
	}
	if len(adminService.lastClientEntrySave.Members) != 1 || adminService.lastClientEntrySave.Members[0].ServerType != "vmess" || adminService.lastClientEntrySave.Members[0].ServerID != 11 {
		t.Fatalf("unexpected client entry members: %#v", adminService.lastClientEntrySave.Members)
	}
}

func TestRouterAdminClientEntryGroupDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/client-entry/drop", strings.NewReader("auth_data=jwt-admin&id=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastClientEntryDrop != 7 {
		t.Fatalf("expected drop client entry id 7, got %d", adminService.lastClientEntryDrop)
	}
}

func TestRouterAdminServerManageSortEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/manage/sort", strings.NewReader(`{"auth_data":"jwt-admin","vmess":{"3":0},"trojan":{"5":1}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastManagedSort["vmess"][3] != 0 || adminService.lastManagedSort["trojan"][5] != 1 {
		t.Fatalf("unexpected managed sort payload: %#v", adminService.lastManagedSort)
	}
}

func TestRouterAdminServerManageUpdateHostEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		hostUpdateResult: admin.ManagedServerHostUpdateResult{
			UpdatedTotal:   2,
			UpdatedByTable: map[string]int64{"v2_server_vmess": 1, "v2_server_vless": 1},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/manage/updateHost", strings.NewReader(`{"auth_data":"jwt-admin","old_host":"old.example.com","new_host":"new.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastOldHost != "old.example.com" || adminService.lastNewHost != "new.example.com" {
		t.Fatalf("unexpected host update payload: old=%q new=%q", adminService.lastOldHost, adminService.lastNewHost)
	}
	if !strings.Contains(rec.Body.String(), `"updated_total":2`) {
		t.Fatalf("expected update host summary, got %s", rec.Body.String())
	}
}

func TestRouterAdminManagedServerSaveEndpoints(t *testing.T) {
	serverTypes := []string{"vmess", "trojan", "shadowsocks", "tuic", "hysteria", "vless", "anytls", "v2node"}

	for _, serverType := range serverTypes {
		t.Run(serverType, func(t *testing.T) {
			sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			adminService := &fakeAdminService{}
			router := NewRouter(
				config.Config{AppName: "forest-go", AdminPath: "localadmin"},
				WithSessionService(sessionService),
				WithAdminService(adminService),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/"+serverType+"/save", strings.NewReader(`{"auth_data":"jwt-admin","id":9,"name":"Node-A","group_id":[1,2],"route_id":[3],"host":"node.example.com","port":443,"server_port":8443,"show":1,"tags":["edge"],"network_settings":{"path":"/ws"}}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
			}
			if adminService.lastNodeSaveType != serverType {
				t.Fatalf("expected save type %q, got %q", serverType, adminService.lastNodeSaveType)
			}
			if _, ok := adminService.lastNodeSave["auth_data"]; ok {
				t.Fatalf("expected auth_data removed from payload: %#v", adminService.lastNodeSave)
			}
			if adminService.lastNodeSave["name"] != "Node-A" || adminService.lastNodeSave["host"] != "node.example.com" {
				t.Fatalf("unexpected save payload: %#v", adminService.lastNodeSave)
			}
			groupID, ok := adminService.lastNodeSave["group_id"].([]any)
			if !ok || len(groupID) != 2 {
				t.Fatalf("expected group_id array, got %#v", adminService.lastNodeSave["group_id"])
			}
			networkSettings, ok := adminService.lastNodeSave["network_settings"].(map[string]any)
			if !ok || networkSettings["path"] != "/ws" {
				t.Fatalf("expected nested network_settings, got %#v", adminService.lastNodeSave["network_settings"])
			}
		})
	}
}

func TestRouterAdminManagedServerSaveEndpointsAcceptFormEncodedNestedPayload(t *testing.T) {
	serverTypes := []string{"vmess", "trojan", "shadowsocks", "tuic", "hysteria", "vless", "anytls", "v2node"}
	body := "auth_data=jwt-admin&id=9&name=Node-A&group_id[0]=1&group_id[1]=2&route_id[0]=3&host=node.example.com&port=443&server_port=8443&show=1&tags[0]=edge&network_settings[path]=%2Fws"

	for _, serverType := range serverTypes {
		t.Run(serverType, func(t *testing.T) {
			sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			adminService := &fakeAdminService{}
			router := NewRouter(
				config.Config{AppName: "forest-go", AdminPath: "localadmin"},
				WithSessionService(sessionService),
				WithAdminService(adminService),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/"+serverType+"/save", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
			}
			groupID, ok := adminService.lastNodeSave["group_id"].([]any)
			if !ok || len(groupID) != 2 || fmt.Sprint(groupID[0]) != "1" || fmt.Sprint(groupID[1]) != "2" {
				t.Fatalf("expected parsed group_id array, got %#v", adminService.lastNodeSave["group_id"])
			}
			routeID, ok := adminService.lastNodeSave["route_id"].([]any)
			if !ok || len(routeID) != 1 || fmt.Sprint(routeID[0]) != "3" {
				t.Fatalf("expected parsed route_id array, got %#v", adminService.lastNodeSave["route_id"])
			}
			tags, ok := adminService.lastNodeSave["tags"].([]any)
			if !ok || len(tags) != 1 || fmt.Sprint(tags[0]) != "edge" {
				t.Fatalf("expected parsed tags array, got %#v", adminService.lastNodeSave["tags"])
			}
			networkSettings, ok := adminService.lastNodeSave["network_settings"].(map[string]any)
			if !ok || networkSettings["path"] != "/ws" {
				t.Fatalf("expected parsed nested object, got %#v", adminService.lastNodeSave["network_settings"])
			}
		})
	}
}

func TestRouterAdminManagedServerUpdateEndpoints(t *testing.T) {
	serverTypes := []string{"vmess", "trojan", "shadowsocks", "tuic", "hysteria", "vless", "anytls", "v2node"}

	for _, serverType := range serverTypes {
		t.Run(serverType, func(t *testing.T) {
			sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			adminService := &fakeAdminService{}
			router := NewRouter(
				config.Config{AppName: "forest-go", AdminPath: "localadmin"},
				WithSessionService(sessionService),
				WithAdminService(adminService),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/"+serverType+"/update", strings.NewReader(`{"auth_data":"jwt-admin","id":9,"show":1}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
			}
			if adminService.lastNodeUpdateType != serverType || adminService.lastNodeUpdateID != 9 {
				t.Fatalf("unexpected update target: type=%q id=%d", adminService.lastNodeUpdateType, adminService.lastNodeUpdateID)
			}
			if _, ok := adminService.lastNodeUpdate["auth_data"]; ok {
				t.Fatalf("expected auth_data removed from update payload: %#v", adminService.lastNodeUpdate)
			}
			if _, ok := adminService.lastNodeUpdate["id"]; ok {
				t.Fatalf("expected id removed from update payload: %#v", adminService.lastNodeUpdate)
			}
			if strings.TrimSpace(adminService.lastNodeUpdate["show"].(json.Number).String()) != "1" {
				t.Fatalf("unexpected update payload: %#v", adminService.lastNodeUpdate)
			}
		})
	}
}

func TestRouterAdminManagedServerUpdateEntryGroupEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/vmess/update", strings.NewReader(`{"auth_data":"jwt-admin","id":9,"entry_group_id":7}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
	}
	if adminService.lastNodeUpdateType != "vmess" || adminService.lastNodeUpdateID != 9 {
		t.Fatalf("unexpected update target: type=%q id=%d", adminService.lastNodeUpdateType, adminService.lastNodeUpdateID)
	}
	if strings.TrimSpace(adminService.lastNodeUpdate["entry_group_id"].(json.Number).String()) != "7" {
		t.Fatalf("unexpected update payload: %#v", adminService.lastNodeUpdate)
	}
}

func TestRouterAdminManagedServerClearEntryGroupEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/vmess/update", strings.NewReader(`{"auth_data":"jwt-admin","id":9,"entry_group_id":null}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
	}
	if adminService.lastNodeUpdateType != "vmess" || adminService.lastNodeUpdateID != 9 {
		t.Fatalf("unexpected update target: type=%q id=%d", adminService.lastNodeUpdateType, adminService.lastNodeUpdateID)
	}
	if value, ok := adminService.lastNodeUpdate["entry_group_id"]; !ok || value != nil {
		t.Fatalf("unexpected clear payload: %#v", adminService.lastNodeUpdate)
	}
}

func TestRouterAdminManagedServerDropEndpoints(t *testing.T) {
	serverTypes := []string{"vmess", "trojan", "shadowsocks", "tuic", "hysteria", "vless", "anytls", "v2node"}

	for _, serverType := range serverTypes {
		t.Run(serverType, func(t *testing.T) {
			sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			adminService := &fakeAdminService{}
			router := NewRouter(
				config.Config{AppName: "forest-go", AdminPath: "localadmin"},
				WithSessionService(sessionService),
				WithAdminService(adminService),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/"+serverType+"/drop", strings.NewReader("auth_data=jwt-admin&id=9"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
			}
			if adminService.lastNodeDropType != serverType || adminService.lastNodeDropID != 9 {
				t.Fatalf("unexpected drop target: type=%q id=%d", adminService.lastNodeDropType, adminService.lastNodeDropID)
			}
		})
	}
}

func TestRouterAdminManagedServerCopyEndpoints(t *testing.T) {
	serverTypes := []string{"vmess", "trojan", "shadowsocks", "tuic", "hysteria", "vless", "anytls", "v2node"}

	for _, serverType := range serverTypes {
		t.Run(serverType, func(t *testing.T) {
			sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
			adminService := &fakeAdminService{}
			router := NewRouter(
				config.Config{AppName: "forest-go", AdminPath: "localadmin"},
				WithSessionService(sessionService),
				WithAdminService(adminService),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/server/"+serverType+"/copy", strings.NewReader("auth_data=jwt-admin&id=9"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d with body %s", rec.Code, rec.Body.String())
			}
			if adminService.lastNodeCopyType != serverType || adminService.lastNodeCopyID != 9 {
				t.Fatalf("unexpected copy target: type=%q id=%d", adminService.lastNodeCopyType, adminService.lastNodeCopyID)
			}
		})
	}
}

func TestRouterAdminUserFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		userList: admin.UserListResult{
			Data:  []map[string]any{{"id": int64(9), "email": "demo@example.com"}},
			Total: 1,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/user/fetch?auth_data=jwt-admin&current=2&pageSize=50&sort=email&sort_type=ASC&filter[0][key]=email&filter[0][condition]=%E6%A8%A1%E7%B3%8A&filter[0][value]=demo%40example.com", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserFetch.Current != 2 || adminService.lastUserFetch.PageSize != 50 || adminService.lastUserFetch.Sort != "email" || adminService.lastUserFetch.SortType != "ASC" {
		t.Fatalf("unexpected fetch request: %#v", adminService.lastUserFetch)
	}
	if len(adminService.lastUserFetch.Filters) != 1 || adminService.lastUserFetch.Filters[0].Key != "email" || adminService.lastUserFetch.Filters[0].Condition != "模糊" || adminService.lastUserFetch.Filters[0].Value != "demo@example.com" {
		t.Fatalf("unexpected fetch filters: %#v", adminService.lastUserFetch.Filters)
	}
	if !strings.Contains(rec.Body.String(), `"demo@example.com"`) {
		t.Fatalf("expected user payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminUserGetInfoEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		userInfoDetail: map[string]any{"id": int64(9), "email": "demo@example.com"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/user/getUserInfoById?auth_data=jwt-admin&id=9", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserInfoID != 9 {
		t.Fatalf("expected user id 9, got %d", adminService.lastUserInfoID)
	}
	if !strings.Contains(rec.Body.String(), `"demo@example.com"`) {
		t.Fatalf("expected user info payload, got %s", rec.Body.String())
	}
}

func TestRouterAdminUserUpdateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	form := "auth_data=jwt-admin&id=9&email=demo%40example.com&transfer_enable=1073741824&device_limit=&expired_at=&banned=1&plan_id=2&commission_rate=10&discount=5&is_admin=0&is_staff=1&u=11&d=22&balance=330&commission_type=1&commission_balance=44&remarks=vip&speed_limit=50&invite_user_email=owner%40example.com"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/update", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if adminService.lastUserUpdate.ID != 9 || adminService.lastUserUpdate.Values["email"] != "demo@example.com" {
		t.Fatalf("unexpected update request: %#v", adminService.lastUserUpdate)
	}
	if _, ok := adminService.lastUserUpdate.Values["auth_data"]; ok {
		t.Fatalf("expected auth_data removed from update payload: %#v", adminService.lastUserUpdate.Values)
	}
	if adminService.lastUserUpdate.Values["invite_user_email"] != "owner@example.com" {
		t.Fatalf("unexpected update invite user email: %#v", adminService.lastUserUpdate.Values)
	}
}

func TestRouterAdminUserSetInviteUserEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/setInviteUser", strings.NewReader("auth_data=jwt-admin&id=9&invite_user_email=owner%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if adminService.lastUserUpdate.ID != 9 {
		t.Fatalf("expected user id 9, got %#v", adminService.lastUserUpdate)
	}
	if adminService.lastUserUpdate.Values["invite_user_email"] != "owner@example.com" {
		t.Fatalf("unexpected invite update payload: %#v", adminService.lastUserUpdate.Values)
	}
	if len(adminService.lastUserUpdate.Values) != 1 {
		t.Fatalf("expected only invite_user_email in alias payload, got %#v", adminService.lastUserUpdate.Values)
	}
}

func TestRouterAdminUserGenerateBatchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		userGenerateCSV:   "账号,密码\r\nu@example.com,pass\r\n",
		userGenerateBatch: true,
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/generate", strings.NewReader("auth_data=jwt-admin&generate_count=2&email_suffix=example.com&password=pass12345"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserGenerate.Values["generate_count"] != "2" {
		t.Fatalf("unexpected generate request: %#v", adminService.lastUserGenerate)
	}
	if !strings.Contains(rec.Body.String(), "账号,密码") {
		t.Fatalf("expected csv response, got %s", rec.Body.String())
	}
}

func TestRouterAdminUserGenerateSingleEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/generate", strings.NewReader("auth_data=jwt-admin&email_prefix=demo&email_suffix=example.com&password=pass12345"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserGenerate.Values["email_prefix"] != "demo" {
		t.Fatalf("unexpected generate request: %#v", adminService.lastUserGenerate)
	}
	if !strings.Contains(rec.Body.String(), `"data":true`) {
		t.Fatalf("expected json true response, got %s", rec.Body.String())
	}
}

func TestRouterAdminUserDumpCSVEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		userDumpCSV: "邮箱\r\ndemo@example.com\r\n",
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/dumpCSV", strings.NewReader("auth_data=jwt-admin&filter[0][key]=email&filter[0][condition]=%E6%A8%A1%E7%B3%8A&filter[0][value]=demo%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastUserDump) != 1 || adminService.lastUserDump[0].Value != "demo@example.com" {
		t.Fatalf("unexpected dump filters: %#v", adminService.lastUserDump)
	}
	if !strings.Contains(rec.Body.String(), "邮箱") {
		t.Fatalf("expected csv response, got %s", rec.Body.String())
	}
}

func TestRouterAdminUserSendMailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	form := "auth_data=jwt-admin&subject=Notice&content=Hello&filter[0][key]=banned&filter[0][condition]==&filter[0][value]=0"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/sendMail", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserMail.Subject != "Notice" || adminService.lastUserMail.Content != "Hello" {
		t.Fatalf("unexpected send mail request: %#v", adminService.lastUserMail)
	}
	if len(adminService.lastUserMail.Filters) != 1 || adminService.lastUserMail.Filters[0].Key != "banned" {
		t.Fatalf("unexpected send mail filters: %#v", adminService.lastUserMail.Filters)
	}
}

func TestRouterAdminUserBanEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/ban", strings.NewReader("auth_data=jwt-admin&filter[0][key]=email&filter[0][condition]=%E6%A8%A1%E7%B3%8A&filter[0][value]=demo%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastUserBan) != 1 || adminService.lastUserBan[0].Key != "email" {
		t.Fatalf("unexpected ban filters: %#v", adminService.lastUserBan)
	}
}

func TestRouterAdminSubscribeGuardSetUserBannedEndpoint(t *testing.T) {
	adminService := &fakeAdminService{}
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(&fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}), WithAdminService(adminService))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/subscribe-guard/set-user-banned", strings.NewReader("auth_data=jwt-admin&id=10&banned=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if adminService.lastUserBannedID != 10 || adminService.lastUserBannedValue != 1 {
		t.Fatalf("unexpected set banned request: id=%d banned=%d", adminService.lastUserBannedID, adminService.lastUserBannedValue)
	}
}

func TestRouterAdminUserResetSecretEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/resetSecret", strings.NewReader("auth_data=jwt-admin&id=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserResetID != 9 {
		t.Fatalf("expected reset user id 9, got %d", adminService.lastUserResetID)
	}
}

func TestRouterAdminUserDelUserEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/delUser", strings.NewReader("auth_data=jwt-admin&id=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserDeleteID != 9 {
		t.Fatalf("expected delete user id 9, got %d", adminService.lastUserDeleteID)
	}
}

func TestRouterAdminUserAllDelEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/user/allDel", strings.NewReader("auth_data=jwt-admin&filter[0][key]=plan_id&filter[0][condition]==&filter[0][value]=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastUserDeleteAll) != 1 || adminService.lastUserDeleteAll[0].Key != "plan_id" {
		t.Fatalf("unexpected allDel filters: %#v", adminService.lastUserDeleteAll)
	}
}

func TestRouterStaffPlanFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{
		plans: []admin.PlanRecord{{ID: 3, Name: "Starter", Count: 12}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/staff/plan/fetch?auth_data=jwt-staff", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("expected staff plan fetch to disable cache, got %q", rec.Header().Get("Cache-Control"))
	}
	if sessionService.lastRequireAdmin {
		t.Fatalf("staff route should not require admin auth")
	}
	if !strings.Contains(rec.Body.String(), `"Starter"`) {
		t.Fatalf("expected plan payload in body, got %s", rec.Body.String())
	}
}

func TestRouterStaffTicketFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{
		ticketList: admin.TicketListResult{
			Data:  []admin.TicketRecord{{ID: 21, Subject: "Need help", Status: 0, ReplyStatus: 1}},
			Total: 18,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/staff/ticket/fetch?auth_data=jwt-staff&current=2&pageSize=50&status=0", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketFetch.Current != 2 || adminService.lastTicketFetch.PageSize != 50 {
		t.Fatalf("unexpected staff ticket fetch request: %#v", adminService.lastTicketFetch)
	}
	if adminService.lastTicketFetch.Status == nil || *adminService.lastTicketFetch.Status != 0 {
		t.Fatalf("unexpected staff ticket status: %#v", adminService.lastTicketFetch.Status)
	}
}

func TestRouterStaffTicketReplyEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/ticket/reply", strings.NewReader("auth_data=jwt-staff&id=21&message=handled"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketReply.ID != 21 || adminService.lastTicketReply.AdminID != 7 || adminService.lastTicketReply.Message != "handled" {
		t.Fatalf("unexpected staff ticket reply request: %#v", adminService.lastTicketReply)
	}
}

func TestRouterStaffTicketCloseEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/ticket/close", strings.NewReader("auth_data=jwt-staff&id=21"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketClose != 21 {
		t.Fatalf("expected staff close ticket id 21, got %d", adminService.lastTicketClose)
	}
}

func TestRouterStaffNoticeFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{
		notices: []admin.NoticeRecord{{ID: 9, Title: "Maintenance", Show: 1}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/staff/notice/fetch?auth_data=jwt-staff", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"Maintenance"`) {
		t.Fatalf("expected staff notice payload in body, got %s", rec.Body.String())
	}
}

func TestRouterStaffNoticeSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/notice/save", strings.NewReader(`{"auth_data":"jwt-staff","id":9,"title":"Maintenance","content":"Tonight","tags":["ops"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastNoticeSave.ID == nil || *adminService.lastNoticeSave.ID != 9 || adminService.lastNoticeSave.Title != "Maintenance" {
		t.Fatalf("unexpected staff notice save request: %#v", adminService.lastNoticeSave)
	}
}

func TestRouterStaffNoticeDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/notice/drop", strings.NewReader("auth_data=jwt-staff&id=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastNoticeDrop != 9 {
		t.Fatalf("expected staff dropped notice id 9, got %d", adminService.lastNoticeDrop)
	}
}

func TestRouterStaffUserGetInfoEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{
		userInfoDetail: map[string]any{"id": int64(9), "email": "demo@example.com", "is_admin": int64(0), "is_staff": int64(0)},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/staff/user/getUserInfoById?auth_data=jwt-staff&id=9", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserInfoID != 9 {
		t.Fatalf("expected staff user info id 9, got %d", adminService.lastUserInfoID)
	}
}

func TestRouterStaffUserUpdateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{
		userInfoDetail: map[string]any{"id": int64(9), "email": "demo@example.com", "is_admin": int64(0), "is_staff": int64(0)},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	form := "auth_data=jwt-staff&id=9&email=demo%40example.com&transfer_enable=1073741824&device_limit=&expired_at=&banned=1&plan_id=2&commission_rate=10&discount=5&u=11&d=22&balance=330&commission_balance=44"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/user/update", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if adminService.lastUserUpdate.ID != 9 || adminService.lastUserUpdate.Values["email"] != "demo@example.com" {
		t.Fatalf("unexpected staff update request: %#v", adminService.lastUserUpdate)
	}
	if adminService.lastUserUpdate.Values["is_admin"] != "0" || adminService.lastUserUpdate.Values["is_staff"] != "0" {
		t.Fatalf("expected staff update to preserve privilege flags, got %#v", adminService.lastUserUpdate.Values)
	}
}

func TestRouterStaffUserSendMailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/user/sendMail", strings.NewReader("auth_data=jwt-staff&subject=Notice&content=Hello"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUserMail.Subject != "Notice" || adminService.lastUserMail.Content != "Hello" {
		t.Fatalf("unexpected staff send mail request: %#v", adminService.lastUserMail)
	}
	if len(adminService.lastUserMail.Filters) != 2 {
		t.Fatalf("expected enforced staff user filters, got %#v", adminService.lastUserMail.Filters)
	}
}

func TestRouterStaffUserBanEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 7, IsStaff: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/staff/user/ban", strings.NewReader("auth_data=jwt-staff&filter[0][key]=email&filter[0][condition]=%E6%A8%A1%E7%B3%8A&filter[0][value]=demo%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastUserBan) != 3 {
		t.Fatalf("expected user filter plus enforced staff filters, got %#v", adminService.lastUserBan)
	}
}

func TestRouterAdminConfigFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"},
	}
	adminService := &fakeAdminService{
		configData: map[string]any{
			"invite": map[string]any{
				"invite_campaign_enable": int64(1),
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/config/fetch?auth_data=jwt-admin&key=invite", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastConfigKey != "invite" {
		t.Fatalf("expected config key invite, got %q", adminService.lastConfigKey)
	}
	if !strings.Contains(rec.Body.String(), `"invite_campaign_enable"`) {
		t.Fatalf("expected invite config in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminConfigSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/config/save", strings.NewReader(`{"auth_data":"jwt-admin","invite_campaign_enable":1,"commission_withdraw_method":["USDT","支付宝"],"deposit_bounus":["100:10"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastConfigSave == nil {
		t.Fatalf("expected config save payload")
	}
	methods, ok := adminService.lastConfigSave["commission_withdraw_method"].([]any)
	if !ok || len(methods) != 2 {
		t.Fatalf("expected withdraw methods array, got %#v", adminService.lastConfigSave["commission_withdraw_method"])
	}
	deposit, ok := adminService.lastConfigSave["deposit_bounus"].([]any)
	if !ok || len(deposit) != 1 || deposit[0] != "100:10" {
		t.Fatalf("expected deposit bonus array, got %#v", adminService.lastConfigSave["deposit_bounus"])
	}
}

func TestRouterAdminConfigEmailTemplateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		emailTemplates: []string{"default", "classic"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/config/getEmailTemplate?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"default"`) {
		t.Fatalf("expected email templates in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminConfigThemeTemplateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(&fakeAdminService{}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/config/getThemeTemplate?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRouterAdminConfigSetTelegramWebhookEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/config/setTelegramWebhook", strings.NewReader("auth_data=jwt-admin&telegram_bot_token=abc123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastWebhookToken != "abc123" {
		t.Fatalf("expected telegram token abc123, got %q", adminService.lastWebhookToken)
	}
}

func TestRouterAdminConfigSetTelegramWebhookPersistsSubmittedWebhookURL(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	body := "auth_data=jwt-admin&telegram_bot_token=abc123&telegram_webhook_url=https%3A%2F%2Fforest666api.com"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/config/setTelegramWebhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if adminService.lastWebhookToken != "abc123" {
		t.Fatalf("expected telegram token abc123, got %q", adminService.lastWebhookToken)
	}
	if adminService.lastConfigSave == nil {
		t.Fatalf("expected webhook config to be saved before setting webhook")
	}
	if adminService.lastConfigSave["telegram_webhook_url"] != "https://forest666api.com" {
		t.Fatalf("expected submitted webhook url saved, got %#v", adminService.lastConfigSave)
	}
	if _, ok := adminService.lastConfigSave["telegram_bot_token"]; ok {
		t.Fatalf("set webhook should not overwrite bot token through config save: %#v", adminService.lastConfigSave)
	}
}

func TestRouterAdminConfigTelegramStatusEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 9, IsAdmin: 1},
	}
	telegramID := int64(12345)
	adminService := &fakeAdminService{
		telegramStatus: admin.TelegramAdminStatus{
			BotEnabled: true, TokenConfigured: true, AdminBound: true, TelegramID: &telegramID, WebhookURL: "https://site.example/api/v1/guest/telegram/webhook?access_token=abc",
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/config/telegramStatus?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if adminService.lastTelegramAdminID != 9 {
		t.Fatalf("expected admin id 9, got %d", adminService.lastTelegramAdminID)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["bot_enabled"] != true || data["admin_bound"] != true {
		t.Fatalf("unexpected telegram status payload: %#v", payload)
	}
}

func TestRouterAdminConfigTestTelegramEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 9, IsAdmin: 1},
	}
	adminService := &fakeAdminService{telegramTestOK: true}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/config/testTelegram", strings.NewReader("auth_data=jwt-admin"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if adminService.lastTelegramAdminID != 9 {
		t.Fatalf("expected admin id 9, got %d", adminService.lastTelegramAdminID)
	}
}

func TestRouterAdminConfigTestSendMailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"},
	}
	adminService := &fakeAdminService{
		mailTestLog: admin.ConfigMailTestLog{
			"email": "admin@example.com",
			"config": map[string]any{
				"host": "127.0.0.1",
				"port": 1025,
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/config/testSendMail", strings.NewReader("auth_data=jwt-admin"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastMailTestEmail != "admin@example.com" {
		t.Fatalf("expected mail test email admin@example.com, got %q", adminService.lastMailTestEmail)
	}
	if !strings.Contains(rec.Body.String(), `"log"`) {
		t.Fatalf("expected mail test log in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminThemeGetThemesEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(&fakeAdminService{}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/theme/getThemes?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRouterAdminThemeGetThemeConfigEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(&fakeAdminService{}),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/theme/getThemeConfig", strings.NewReader(`{"auth_data":"jwt-admin","name":"default"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRouterAdminThemeSaveThemeConfigEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(&fakeAdminService{}),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/theme/saveThemeConfig", strings.NewReader(`{"auth_data":"jwt-admin","name":"default","config":"eyJ0aGVtZV9jb2xvciI6ImdyZWVuIiwiY3VzdG9tX2h0bWwiOiI8Yj5oaTwvYj4ifQ=="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRouterAdminPaymentFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		payments: []admin.PaymentRecord{
			{
				ID:        9,
				UUID:      "uuid123",
				Payment:   "EPay",
				Name:      "Test Pay",
				NotifyURL: "https://api.example.com/api/v1/guest/payment/notify/EPay/uuid123",
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/payment/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one payment item, got %#v", payload["data"])
	}
}

func TestRouterAdminPaymentMethodsEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		methods: []string{"EPay", "StripeCheckout"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/payment/getPaymentMethods?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("expected two payment methods, got %#v", payload["data"])
	}
}

func TestRouterAdminPaymentFormEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		form: map[string]admin.PaymentFormField{
			"url": {
				Label:       "URL",
				Description: "",
				Type:        "input",
				Value:       "https://pay.example.com",
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/getPaymentForm", strings.NewReader(`{"auth_data":"jwt-admin","payment":"EPay","id":8}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGateway != "EPay" {
		t.Fatalf("expected gateway EPay, got %q", adminService.lastGateway)
	}
	if adminService.lastFormID == nil || *adminService.lastFormID != 8 {
		t.Fatalf("expected payment id 8, got %#v", adminService.lastFormID)
	}
}

func TestRouterAdminPaymentSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/save", strings.NewReader(`{"auth_data":"jwt-admin","id":7,"name":"FastPay","icon":"https://cdn.example.com/icon.svg","payment":"EPay","notify_domain":"https://notify.example.com","handling_fee_fixed":150,"handling_fee_percent":2.5,"config":{"url":"https://pay.example.com","pid":"10001","key":"secret"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastSave.ID == nil || *adminService.lastSave.ID != 7 {
		t.Fatalf("expected save id 7, got %#v", adminService.lastSave.ID)
	}
	if adminService.lastSave.Payment != "EPay" || adminService.lastSave.Name != "FastPay" {
		t.Fatalf("unexpected save payload: %#v", adminService.lastSave)
	}
	if adminService.lastSave.Config["pid"] != "10001" || adminService.lastSave.Config["key"] != "secret" {
		t.Fatalf("unexpected payment config: %#v", adminService.lastSave.Config)
	}
}

func TestRouterAdminPaymentSaveEndpointSupportsFormEncoding(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/save", strings.NewReader("auth_data=jwt-admin&id=7&name=FastPay&payment=EPay&config%5Burl%5D=https%3A%2F%2Fpay.example.com&config%5Bpid%5D=10001&config%5Bkey%5D=secret&handling_fee_fixed=150&handling_fee_percent=2.5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastSave.Config["url"] != "https://pay.example.com" || adminService.lastSave.Config["pid"] != "10001" {
		t.Fatalf("unexpected form-encoded config: %#v", adminService.lastSave.Config)
	}
}

func TestRouterAdminPaymentShowEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/show", strings.NewReader(`{"auth_data":"jwt-admin","id":6}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastShowID != 6 {
		t.Fatalf("expected show id 6, got %d", adminService.lastShowID)
	}
}

func TestRouterAdminPaymentDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/drop", strings.NewReader(`{"auth_data":"jwt-admin","id":5}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastDropID != 5 {
		t.Fatalf("expected drop id 5, got %d", adminService.lastDropID)
	}
}

func TestRouterAdminPaymentSortEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/sort", strings.NewReader(`{"auth_data":"jwt-admin","ids":[9,3,5]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastSortIDs) != 3 || adminService.lastSortIDs[0] != 9 || adminService.lastSortIDs[2] != 5 {
		t.Fatalf("unexpected sort ids: %#v", adminService.lastSortIDs)
	}
}

func TestRouterAdminPaymentSortEndpointSupportsFormEncoding(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/payment/sort", strings.NewReader("auth_data=jwt-admin&ids%5B0%5D=9&ids%5B1%5D=3&ids%5B2%5D=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastSortIDs) != 3 || adminService.lastSortIDs[0] != 9 || adminService.lastSortIDs[1] != 3 || adminService.lastSortIDs[2] != 5 {
		t.Fatalf("unexpected form-encoded sort ids: %#v", adminService.lastSortIDs)
	}
}

func TestRouterAdminOrderFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		orderList: admin.OrderListResult{
			Data:  []map[string]any{{"trade_no": "T300", "plan_name": "Starter"}},
			Total: 17,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/order/fetch?auth_data=jwt-admin&current=2&pageSize=50&is_commission=1&filter%5B0%5D%5Bkey%5D=email&filter%5B0%5D%5Bcondition%5D=%E6%A8%A1%E7%B3%8A&filter%5B0%5D%5Bvalue%5D=demo%40example.com", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastFetch.Current != 2 || adminService.lastFetch.PageSize != 50 || !adminService.lastFetch.IsCommission {
		t.Fatalf("unexpected fetch request: %#v", adminService.lastFetch)
	}
	if len(adminService.lastFetch.Filters) != 1 || adminService.lastFetch.Filters[0].Key != "email" || adminService.lastFetch.Filters[0].Condition != "模糊" || adminService.lastFetch.Filters[0].Value != "demo@example.com" {
		t.Fatalf("unexpected fetch filters: %#v", adminService.lastFetch.Filters)
	}
}

func TestRouterAdminOrderDetailEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		orderDetail: map[string]any{
			"id":       int64(8),
			"trade_no": "T301",
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/order/detail", strings.NewReader("auth_data=jwt-admin&id=8"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastDetailID != 8 {
		t.Fatalf("expected detail id 8, got %d", adminService.lastDetailID)
	}
}

func TestRouterAdminOrderUpdateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/order/update", strings.NewReader("auth_data=jwt-admin&trade_no=T302&commission_status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastUpdate.TradeNo != "T302" || adminService.lastUpdate.CommissionStatus == nil || *adminService.lastUpdate.CommissionStatus != 1 {
		t.Fatalf("unexpected update request: %#v", adminService.lastUpdate)
	}
}

func TestRouterAdminOrderPaidEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/order/paid", strings.NewReader("auth_data=jwt-admin&trade_no=T303"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastPaid != "T303" {
		t.Fatalf("expected paid trade no T303, got %q", adminService.lastPaid)
	}
}

func TestRouterAdminOrderCancelEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/order/cancel", strings.NewReader("auth_data=jwt-admin&trade_no=T304"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastCancel != "T304" {
		t.Fatalf("expected cancel trade no T304, got %q", adminService.lastCancel)
	}
}

func TestRouterAdminOrderRefundEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/order/refund", strings.NewReader("auth_data=jwt-admin&trade_no=T304R"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastRefund != "T304R" {
		t.Fatalf("expected refund trade no T304R, got %q", adminService.lastRefund)
	}
}

func TestRouterAdminOrderAssignEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{assignTrade: "T305"}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/order/assign", strings.NewReader("auth_data=jwt-admin&email=demo%40example.com&plan_id=9&period=month_price&total_amount=1299"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastAssign.Email != "demo@example.com" || adminService.lastAssign.PlanID != 9 || adminService.lastAssign.Period != "month_price" || adminService.lastAssign.TotalAmount != 1299 {
		t.Fatalf("unexpected assign request: %#v", adminService.lastAssign)
	}
	if !strings.Contains(rec.Body.String(), `"T305"`) {
		t.Fatalf("expected assigned trade no in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminPlanFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		plans: []admin.PlanRecord{
			{ID: 3, Name: "Starter", Count: 12},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/plan/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("expected admin plan fetch to disable cache, got %q", rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Body.String(), `"Starter"`) {
		t.Fatalf("expected plan payload in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminPlanSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/plan/save", strings.NewReader(`{"auth_data":"jwt-admin","id":7,"name":"Pro","content":"<p>fast</p>","group_id":2,"transfer_enable":100,"device_limit":3,"month_price":1299,"quarter_price":3599,"half_year_price":6999,"year_price":12999,"two_year_price":21999,"three_year_price":30999,"onetime_price":49999,"reset_price":999,"reset_traffic_method":1,"capacity_limit":200,"speed_limit":500,"force_update":true,"show":1,"renew":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastPlanSave.ID == nil || *adminService.lastPlanSave.ID != 7 {
		t.Fatalf("expected plan id 7, got %#v", adminService.lastPlanSave.ID)
	}
	if adminService.lastPlanSave.Name != "Pro" || adminService.lastPlanSave.GroupID != 2 || adminService.lastPlanSave.TransferEnable != 100 {
		t.Fatalf("unexpected save request: %#v", adminService.lastPlanSave)
	}
	if adminService.lastPlanSave.MonthPrice == nil || *adminService.lastPlanSave.MonthPrice != 1299 ||
		adminService.lastPlanSave.YearPrice == nil || *adminService.lastPlanSave.YearPrice != 12999 ||
		adminService.lastPlanSave.TwoYearPrice == nil || *adminService.lastPlanSave.TwoYearPrice != 21999 ||
		!adminService.lastPlanSave.ForceUpdate {
		t.Fatalf("unexpected save pricing request: %#v", adminService.lastPlanSave)
	}
	if adminService.lastPlanSave.Show == nil || *adminService.lastPlanSave.Show != 1 || adminService.lastPlanSave.Renew == nil || *adminService.lastPlanSave.Renew != 0 {
		t.Fatalf("expected save to forward show/renew, got %#v", adminService.lastPlanSave)
	}
}

func TestRouterAdminPlanDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/plan/drop", strings.NewReader("auth_data=jwt-admin&id=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastPlanDropID != 7 {
		t.Fatalf("expected dropped plan id 7, got %d", adminService.lastPlanDropID)
	}
}

func TestRouterAdminPlanUpdateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/plan/update", strings.NewReader("auth_data=jwt-admin&id=7&show=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastPlanUpdate.ID != 7 || adminService.lastPlanUpdate.Show == nil || *adminService.lastPlanUpdate.Show != 1 {
		t.Fatalf("unexpected update request: %#v", adminService.lastPlanUpdate)
	}
}

func TestRouterAdminPlanSortEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/plan/sort", strings.NewReader(`{"auth_data":"jwt-admin","plan_ids":[9,3,5]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastPlanSortIDs) != 3 || adminService.lastPlanSortIDs[0] != 9 || adminService.lastPlanSortIDs[1] != 3 || adminService.lastPlanSortIDs[2] != 5 {
		t.Fatalf("unexpected sort ids: %#v", adminService.lastPlanSortIDs)
	}
}

func TestRouterAdminNoticeFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{
		notices: []admin.NoticeRecord{
			{ID: 9, Title: "Maintenance", Show: 1},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/notice/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"Maintenance"`) {
		t.Fatalf("expected notice payload in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminNoticeSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/notice/save", strings.NewReader(`{"auth_data":"jwt-admin","id":9,"title":"Maintenance","content":"Tonight","img_url":"https://cdn.example.com/banner.png","tags":["ops","urgent"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastNoticeSave.ID == nil || *adminService.lastNoticeSave.ID != 9 {
		t.Fatalf("expected notice id 9, got %#v", adminService.lastNoticeSave.ID)
	}
	if adminService.lastNoticeSave.Title != "Maintenance" || adminService.lastNoticeSave.Content != "Tonight" {
		t.Fatalf("unexpected save request: %#v", adminService.lastNoticeSave)
	}
	if len(adminService.lastNoticeSave.Tags) != 2 || adminService.lastNoticeSave.Tags[0] != "ops" || adminService.lastNoticeSave.Tags[1] != "urgent" {
		t.Fatalf("unexpected save tags: %#v", adminService.lastNoticeSave.Tags)
	}
}

func TestRouterAdminNoticeShowEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/notice/show", strings.NewReader("auth_data=jwt-admin&id=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastNoticeShow != 9 {
		t.Fatalf("expected shown notice id 9, got %d", adminService.lastNoticeShow)
	}
}

func TestRouterAdminNoticeDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{
		user: &session.Identity{ID: 1, IsAdmin: 1},
	}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/notice/drop", strings.NewReader("auth_data=jwt-admin&id=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastNoticeDrop != 9 {
		t.Fatalf("expected dropped notice id 9, got %d", adminService.lastNoticeDrop)
	}
}

func TestRouterAdminCouponFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		couponList: admin.CouponListResult{
			Data:  []admin.CouponRecord{{ID: 7, Name: "SPRING"}},
			Total: 1,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/coupon/fetch?auth_data=jwt-admin&current=2&pageSize=50&sort=id&sort_type=ASC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastCouponList.Current != 2 || adminService.lastCouponList.PageSize != 50 || adminService.lastCouponList.Sort != "id" || adminService.lastCouponList.SortType != "ASC" {
		t.Fatalf("unexpected coupon list request: %#v", adminService.lastCouponList)
	}
}

func TestRouterAdminCouponGenerateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/coupon/generate", strings.NewReader("auth_data=jwt-admin&id=7&name=SPRING&type=1&value=1500&started_at=1710000000&ended_at=1715000000&limit_use=10&limit_use_with_user=2&limit_plan_ids%5B0%5D=3&limit_plan_ids%5B1%5D=5&limit_period%5B0%5D=month_price&limit_period%5B1%5D=year_price"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastCouponSave.ID == nil || *adminService.lastCouponSave.ID != 7 {
		t.Fatalf("expected coupon id 7, got %#v", adminService.lastCouponSave.ID)
	}
	if adminService.lastCouponSave.Name != "SPRING" || adminService.lastCouponSave.Type != 1 || adminService.lastCouponSave.Value != 1500 {
		t.Fatalf("unexpected coupon generate request: %#v", adminService.lastCouponSave)
	}
	if len(adminService.lastCouponSave.LimitPlanIDs) != 2 || adminService.lastCouponSave.LimitPlanIDs[0] != 3 || adminService.lastCouponSave.LimitPlanIDs[1] != 5 {
		t.Fatalf("unexpected coupon limit plans: %#v", adminService.lastCouponSave.LimitPlanIDs)
	}
	if len(adminService.lastCouponSave.LimitPeriod) != 2 || adminService.lastCouponSave.LimitPeriod[0] != "month_price" || adminService.lastCouponSave.LimitPeriod[1] != "year_price" {
		t.Fatalf("unexpected coupon limit periods: %#v", adminService.lastCouponSave.LimitPeriod)
	}
}

func TestRouterAdminCouponGenerateBatchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{couponCSV: "name,code\nSPRING,ABCD1234\n"}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/coupon/generate", strings.NewReader(`{"auth_data":"jwt-admin","name":"SPRING","type":2,"value":20,"started_at":1710000000,"ended_at":1715000000,"generate_count":3}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastCouponSave.GenerateCount == nil || *adminService.lastCouponSave.GenerateCount != 3 {
		t.Fatalf("expected generate_count 3, got %#v", adminService.lastCouponSave.GenerateCount)
	}
	if !strings.Contains(rec.Body.String(), "ABCD1234") {
		t.Fatalf("expected csv body, got %q", rec.Body.String())
	}
}

func TestRouterAdminCouponShowEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/coupon/show", strings.NewReader("auth_data=jwt-admin&id=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastCouponShow != 7 {
		t.Fatalf("expected shown coupon id 7, got %d", adminService.lastCouponShow)
	}
}

func TestRouterAdminCouponDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/coupon/drop", strings.NewReader("auth_data=jwt-admin&id=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastCouponDrop != 7 {
		t.Fatalf("expected dropped coupon id 7, got %d", adminService.lastCouponDrop)
	}
}

func TestRouterAdminGiftcardFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		giftcardList: admin.GiftcardListResult{
			Data:  []admin.GiftcardRecord{{ID: 8, Name: "WELCOME"}},
			Total: 1,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/giftcard/fetch?auth_data=jwt-admin&current=3&pageSize=20&sort=id&sort_type=DESC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGiftcardList.Current != 3 || adminService.lastGiftcardList.PageSize != 20 || adminService.lastGiftcardList.Sort != "id" || adminService.lastGiftcardList.SortType != "DESC" {
		t.Fatalf("unexpected giftcard list request: %#v", adminService.lastGiftcardList)
	}
}

func TestRouterAdminGiftcardGenerateEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/giftcard/generate", strings.NewReader(`{"auth_data":"jwt-admin","id":8,"name":"WELCOME","type":5,"value":30,"plan_id":3,"started_at":1710000000,"ended_at":1715000000,"limit_use":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGiftcardSave.ID == nil || *adminService.lastGiftcardSave.ID != 8 {
		t.Fatalf("expected giftcard id 8, got %#v", adminService.lastGiftcardSave.ID)
	}
	if adminService.lastGiftcardSave.Type != 5 || adminService.lastGiftcardSave.PlanID == nil || *adminService.lastGiftcardSave.PlanID != 3 {
		t.Fatalf("unexpected giftcard request: %#v", adminService.lastGiftcardSave)
	}
}

func TestRouterAdminGiftcardGenerateBatchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{giftcardCSV: "name,code\nWELCOME,ABCDEFGHIJKLMNOP\n"}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/giftcard/generate", strings.NewReader(`{"auth_data":"jwt-admin","name":"WELCOME","type":1,"value":1500,"started_at":1710000000,"ended_at":1715000000,"generate_count":2}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGiftcardSave.GenerateCount == nil || *adminService.lastGiftcardSave.GenerateCount != 2 {
		t.Fatalf("expected generate_count 2, got %#v", adminService.lastGiftcardSave.GenerateCount)
	}
	if !strings.Contains(rec.Body.String(), "ABCDEFGHIJKLMNOP") {
		t.Fatalf("expected csv body, got %q", rec.Body.String())
	}
}

func TestRouterAdminGiftcardDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/giftcard/drop", strings.NewReader("auth_data=jwt-admin&id=8"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastGiftcardDrop != 8 {
		t.Fatalf("expected dropped giftcard id 8, got %d", adminService.lastGiftcardDrop)
	}
}

func TestRouterAdminKnowledgeFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		knowledges: []admin.KnowledgeRecord{{ID: 11, Title: "FAQ", Category: "guide", Show: 1}},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/knowledge/fetch?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"FAQ"`) {
		t.Fatalf("expected knowledge list in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminKnowledgeFetchByIDEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		knowledgeDetail: admin.KnowledgeRecord{ID: 11, Title: "FAQ", Body: "body", Language: "en-US"},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/knowledge/fetch?auth_data=jwt-admin&id=11", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastKnowledgeID != 11 {
		t.Fatalf("expected knowledge id 11, got %d", adminService.lastKnowledgeID)
	}
}

func TestRouterAdminKnowledgeGetCategoryEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{knowledgeCats: []string{"guide", "billing"}}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/knowledge/getCategory?auth_data=jwt-admin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"guide"`) {
		t.Fatalf("expected category list in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminKnowledgeSaveEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/knowledge/save", strings.NewReader(`{"auth_data":"jwt-admin","id":11,"title":"FAQ","category":"guide","language":"en-US","body":"markdown body"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastKnowledgeSave.ID == nil || *adminService.lastKnowledgeSave.ID != 11 {
		t.Fatalf("expected knowledge id 11, got %#v", adminService.lastKnowledgeSave.ID)
	}
	if adminService.lastKnowledgeSave.Title != "FAQ" || adminService.lastKnowledgeSave.Category != "guide" || adminService.lastKnowledgeSave.Language != "en-US" {
		t.Fatalf("unexpected knowledge save request: %#v", adminService.lastKnowledgeSave)
	}
}

func TestRouterAdminKnowledgeShowEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/knowledge/show", strings.NewReader("auth_data=jwt-admin&id=11"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastKnowledgeShow != 11 {
		t.Fatalf("expected shown knowledge id 11, got %d", adminService.lastKnowledgeShow)
	}
}

func TestRouterAdminKnowledgeDropEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/knowledge/drop", strings.NewReader("auth_data=jwt-admin&id=11"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastKnowledgeDrop != 11 {
		t.Fatalf("expected dropped knowledge id 11, got %d", adminService.lastKnowledgeDrop)
	}
}

func TestRouterAdminKnowledgeSortEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/knowledge/sort", strings.NewReader(`{"auth_data":"jwt-admin","knowledge_ids":[9,3,5]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(adminService.lastKnowledgeSort) != 3 || adminService.lastKnowledgeSort[0] != 9 || adminService.lastKnowledgeSort[1] != 3 || adminService.lastKnowledgeSort[2] != 5 {
		t.Fatalf("unexpected knowledge sort ids: %#v", adminService.lastKnowledgeSort)
	}
}

func TestRouterAdminTicketFetchEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		ticketList: admin.TicketListResult{
			Data:  []admin.TicketRecord{{ID: 21, Subject: "Need help", Status: 0, ReplyStatus: 1}},
			Total: 18,
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/ticket/fetch?auth_data=jwt-admin&current=2&pageSize=50&status=0&reply_status%5B0%5D=1&reply_status%5B1%5D=0&email=demo%40example.com", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketFetch.Current != 2 || adminService.lastTicketFetch.PageSize != 50 {
		t.Fatalf("unexpected ticket fetch paging: %#v", adminService.lastTicketFetch)
	}
	if adminService.lastTicketFetch.Status == nil || *adminService.lastTicketFetch.Status != 0 {
		t.Fatalf("unexpected ticket fetch status: %#v", adminService.lastTicketFetch.Status)
	}
	if len(adminService.lastTicketFetch.ReplyStatus) != 2 || adminService.lastTicketFetch.ReplyStatus[0] != 1 || adminService.lastTicketFetch.ReplyStatus[1] != 0 {
		t.Fatalf("unexpected ticket fetch reply_status: %#v", adminService.lastTicketFetch.ReplyStatus)
	}
	if adminService.lastTicketFetch.Email != "demo@example.com" {
		t.Fatalf("unexpected ticket fetch email: %q", adminService.lastTicketFetch.Email)
	}
}

func TestRouterAdminTicketFetchByIDEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{
		ticketDetail: admin.TicketDetail{
			TicketRecord: admin.TicketRecord{ID: 21, Subject: "Need help", UserID: 88, Status: 0},
			Messages: []admin.TicketMessageRecord{
				{ID: 1, TicketID: 21, UserID: 88, Message: "hello", IsMe: false},
				{ID: 2, TicketID: 21, UserID: 1, Message: "handled", IsMe: true},
			},
		},
	}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/ticket/fetch?auth_data=jwt-admin&id=21", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketID != 21 {
		t.Fatalf("expected ticket id 21, got %d", adminService.lastTicketID)
	}
	if !strings.Contains(rec.Body.String(), `"handled"`) {
		t.Fatalf("expected ticket detail in body, got %s", rec.Body.String())
	}
}

func TestRouterAdminTicketReplyEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 9, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/ticket/reply", strings.NewReader("auth_data=jwt-admin&id=21&message=handled"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketReply.ID != 21 || adminService.lastTicketReply.Message != "handled" || adminService.lastTicketReply.AdminID != 9 {
		t.Fatalf("unexpected ticket reply request: %#v", adminService.lastTicketReply)
	}
}

func TestRouterAdminTicketCloseEndpoint(t *testing.T) {
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	adminService := &fakeAdminService{}
	router := NewRouter(
		config.Config{AppName: "forest-go", AdminPath: "localadmin"},
		WithSessionService(sessionService),
		WithAdminService(adminService),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/ticket/close", strings.NewReader("auth_data=jwt-admin&id=21"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if adminService.lastTicketClose != 21 {
		t.Fatalf("expected ticket close id 21, got %d", adminService.lastTicketClose)
	}
}
