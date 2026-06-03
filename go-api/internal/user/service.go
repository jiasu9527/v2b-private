package user

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/queue"
	"forest/go-api/internal/session"
)

var (
	ErrUnavailable                    = errors.New("user service unavailable")
	ErrNotFound                       = errors.New("user not found")
	ErrPlanNotFound                   = errors.New("plan not found")
	ErrOrderNotFound                  = errors.New("order not found")
	ErrOrderPaidOrMissing             = errors.New("order does not exist or has been paid")
	ErrInvalidParameter               = errors.New("invalid parameter")
	ErrPendingOrderExists             = errors.New("pending order exists")
	ErrDepositAmountInvalid           = errors.New("deposit amount must be greater than 0")
	ErrDepositAmountTooLarge          = errors.New("deposit amount too large")
	ErrPlanSoldOut                    = errors.New("plan sold out")
	ErrPeriodUnavailable              = errors.New("payment period unavailable")
	ErrResetUnavailable               = errors.New("reset package unavailable")
	ErrSubscriptionSoldOut            = errors.New("subscription sold out")
	ErrPlanCannotRenew                = errors.New("plan cannot renew")
	ErrPlanChangeDisabled             = errors.New("plan change disabled")
	ErrPlanExpiredChangeRequired      = errors.New("expired subscription must change plan")
	ErrCouponInvalid                  = errors.New("invalid coupon")
	ErrCouponUnavailable              = errors.New("coupon unavailable")
	ErrCouponNotStarted               = errors.New("coupon not started")
	ErrCouponExpired                  = errors.New("coupon expired")
	ErrCouponPlanRestricted           = errors.New("coupon plan restricted")
	ErrCouponPeriodRestricted         = errors.New("coupon period restricted")
	ErrCouponUserLimit                = errors.New("coupon user limit reached")
	ErrCouponFailed                   = errors.New("coupon failed")
	ErrInsufficientBalance            = errors.New("insufficient balance")
	ErrCommissionRollbackInsufficient = errors.New("insufficient commission rollback balance")
	ErrCreateOrderFailed              = errors.New("failed to create order")
	ErrPaymentMethodUnavailable       = errors.New("payment method unavailable")
	ErrCheckoutFailed                 = errors.New("checkout failed")
	ErrCancelPendingOnly              = errors.New("cancel pending orders only")
	ErrRefundCompletedOnly            = errors.New("refund completed orders only")
	ErrRefundLatestOnly               = errors.New("only latest completed order can be refunded")
	ErrRefundTargetNotSupported       = errors.New("refund target not supported")
	ErrUnsupportedPaymentGateway      = errors.New("payment gateway unsupported")
	ErrNoticeNotFound                 = errors.New("notice not found")
	ErrInviteLimitReached             = errors.New("invite limit reached")
	ErrClientTokenInvalid             = errors.New("client token invalid")
)

type Info struct {
	Email             string `json:"email"`
	TransferEnable    int64  `json:"transfer_enable"`
	DeviceLimit       *int64 `json:"device_limit"`
	LastLoginAt       *int64 `json:"last_login_at"`
	CreatedAt         int64  `json:"created_at"`
	Banned            int64  `json:"banned"`
	AutoRenewal       int64  `json:"auto_renewal"`
	RemindExpire      int64  `json:"remind_expire"`
	RemindTraffic     int64  `json:"remind_traffic"`
	ExpiredAt         *int64 `json:"expired_at"`
	Balance           int64  `json:"balance"`
	CommissionBalance int64  `json:"commission_balance"`
	PlanID            *int64 `json:"plan_id"`
	Discount          *int64 `json:"discount"`
	CommissionRate    *int64 `json:"commission_rate"`
	TelegramID        *int64 `json:"telegram_id"`
	UUID              string `json:"uuid"`
	AvatarURL         string `json:"avatar_url"`
}

type Subscribe struct {
	PlanID         *int64         `json:"plan_id"`
	Token          string         `json:"token"`
	ExpiredAt      *int64         `json:"expired_at"`
	U              int64          `json:"u"`
	D              int64          `json:"d"`
	TransferEnable int64          `json:"transfer_enable"`
	DeviceLimit    *int64         `json:"device_limit"`
	Email          string         `json:"email"`
	UUID           string         `json:"uuid"`
	Plan           map[string]any `json:"plan,omitempty"`
	AliveIP        int64          `json:"alive_ip"`
	SubscribeURL   string         `json:"subscribe_url"`
	ResetDay       *int64         `json:"reset_day"`
	AllowNewPeriod string         `json:"allow_new_period"`
}

type ClientEntryGroupMember struct {
	ServerType string `json:"server_type"`
	ServerID   int64  `json:"server_id"`
	Sort       *int64 `json:"sort,omitempty"`
}

type ClientEntryGroupIP struct {
	IP   string `json:"ip"`
	Sort *int64 `json:"sort,omitempty"`
}

type ClientEntryGroup struct {
	ID                 int64                    `json:"id"`
	Code               string                   `json:"code"`
	Name               string                   `json:"name"`
	DisplayName        string                   `json:"display_name"`
	Strategy           string                   `json:"strategy"`
	HideMemberNodes    bool                     `json:"hide_member_nodes"`
	Show               int64                    `json:"show"`
	RemoteEnabled      bool                     `json:"remote_enabled"`
	RemoteHost         string                   `json:"remote_host"`
	RemoteSSHPort      int64                    `json:"remote_ssh_port"`
	RemoteSSHUser      string                   `json:"remote_ssh_user"`
	RemoteSSHPassword  string                   `json:"remote_ssh_password"`
	RemoteGroupRef     string                   `json:"remote_group_ref"`
	RemoteExcludeNames []string                 `json:"remote_exclude_names,omitempty"`
	RemoteRefreshSec   int64                    `json:"remote_refresh_sec"`
	Members            []ClientEntryGroupMember `json:"members,omitempty"`
	IPs                []ClientEntryGroupIP     `json:"ips,omitempty"`
	CreatedAt          int64                    `json:"created_at,omitempty"`
	UpdatedAt          int64                    `json:"updated_at,omitempty"`
}

type OrderSaveRequest struct {
	PlanID        int64
	Period        string
	CouponCode    string
	DepositAmount int64
}

type OrderCheckoutRequest struct {
	TradeNo  string
	MethodID int64
	Token    string
}

type TicketCreateRequest struct {
	Subject string
	Level   int64
	Message string
}

type ProfileUpdateRequest struct {
	AutoRenewal   *int64
	RemindExpire  *int64
	RemindTraffic *int64
}

type AdminAssignOrderRequest struct {
	Email       string
	PlanID      int64
	Period      string
	TotalAmount int64
}

type OrderCheckoutResult struct {
	Type int64 `json:"type"`
	Data any   `json:"data"`
}

type Service interface {
	Info(ctx context.Context, userID int64) (Info, error)
	ResolveClientUserID(ctx context.Context, token string) (int64, error)
	Stat(ctx context.Context, userID int64) ([]int64, error)
	Subscribe(ctx context.Context, userID int64) (Subscribe, error)
	TelegramBotInfo(ctx context.Context) (map[string]any, error)
	UnbindTelegram(ctx context.Context, userID int64) (bool, error)
	ResetSecurity(ctx context.Context, userID int64) (string, error)
	UpdateProfile(ctx context.Context, userID int64, req ProfileUpdateRequest) (bool, error)
	ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) (bool, error)
	Transfer(ctx context.Context, userID, amount int64) (bool, error)
	NewPeriod(ctx context.Context, userID int64) (bool, error)
	RedeemGiftcard(ctx context.Context, userID int64, code string) (map[string]any, error)
	Servers(ctx context.Context, userID int64, ua string) ([]map[string]any, error)
	ClientEntryGroups(ctx context.Context, userID int64) ([]ClientEntryGroup, error)
	Plans(ctx context.Context, userID int64, planID *int64) (any, error)
	NoticeDetail(ctx context.Context, id int64) (map[string]any, error)
	Notices(ctx context.Context, current, pageSize int64) ([]map[string]any, int64, error)
	CreateInviteCode(ctx context.Context, userID int64) (bool, error)
	InviteOverview(ctx context.Context, userID int64) (map[string]any, error)
	InviteDetails(ctx context.Context, userID, current, pageSize int64) ([]map[string]any, int64, error)
	CreateInviteCampaign(ctx context.Context, userID, planID int64, period string) (map[string]any, error)
	InviteCampaign(ctx context.Context, userID int64) (map[string]any, error)
	InviteCampaignRecords(ctx context.Context, userID int64, campaignID *int64, current, pageSize int64) (map[string]any, error)
	AbandonInviteCampaign(ctx context.Context, userID int64) (bool, error)
	Tickets(ctx context.Context, userID int64) ([]map[string]any, error)
	TicketDetail(ctx context.Context, userID, id int64) (map[string]any, error)
	CreateTicket(ctx context.Context, userID int64, req TicketCreateRequest) (bool, error)
	ReplyTicket(ctx context.Context, userID, id int64, message string) (bool, error)
	CloseTicket(ctx context.Context, userID, id int64) (bool, error)
	WithdrawTicket(ctx context.Context, userID int64, method, account string) (bool, error)
	CommConfig(ctx context.Context) (map[string]any, error)
	StripePublicKey(ctx context.Context, paymentID int64) (string, error)
	CheckCoupon(ctx context.Context, userID int64, code string, planID *int64) (map[string]any, error)
	KnowledgeDetail(ctx context.Context, userID, id int64) (map[string]any, error)
	Knowledges(ctx context.Context, language, keyword string) (map[string][]map[string]any, error)
	KnowledgeCategories(ctx context.Context, language string) ([]string, error)
	TrafficLogs(ctx context.Context, userID int64) ([]map[string]any, error)
	Orders(ctx context.Context, userID int64, status *int64) ([]map[string]any, error)
	OrderDetail(ctx context.Context, userID int64, tradeNo string) (map[string]any, error)
	OrderStatus(ctx context.Context, userID int64, tradeNo string) (int64, error)
	PaymentMethods(ctx context.Context) ([]map[string]any, error)
	SaveOrder(ctx context.Context, userID int64, req OrderSaveRequest) (string, error)
	CheckoutOrder(ctx context.Context, userID int64, req OrderCheckoutRequest) (OrderCheckoutResult, error)
	CancelOrder(ctx context.Context, userID int64, tradeNo string) (bool, error)
}

type ticketAdminNotifier interface {
	NotifyAdmins(ctx context.Context, message string, includeStaff bool) error
}

type TrafficUsage struct {
	U int64
	D int64
}

type TrafficReport struct {
	ServerID   int64
	ServerType string
	ServerRate float64
	Traffic    map[int64]TrafficUsage
}

type DBService struct {
	cfg       config.Config
	runtime   *config.RuntimeState
	db        *sql.DB
	notifier  ticketAdminNotifier
	jobs      queue.Enqueuer
	authCache *session.AuthCache

	clientEntryEnsureOnce sync.Once
	clientEntryEnsureErr  error
}

func NewDBService(cfg config.Config, db *sql.DB) *DBService {
	return &DBService{cfg: cfg, db: db}
}

func (s *DBService) WithAdminNotifier(notifier ticketAdminNotifier) *DBService {
	s.notifier = notifier
	return s
}

func (s *DBService) WithQueueRuntime(jobs queue.Enqueuer) *DBService {
	s.jobs = jobs
	return s
}

func (s *DBService) WithRuntimeConfig(runtime *config.RuntimeState) *DBService {
	s.runtime = runtime
	return s
}

func (s *DBService) WithAuthCache(cache *session.AuthCache) *DBService {
	s.authCache = cache
	return s
}

func (s *DBService) currentConfig() config.Config {
	if s == nil {
		return config.Config{}
	}
	if s.runtime == nil {
		return s.cfg
	}
	return s.runtime.CurrentConfig()
}

func (s *DBService) runtimeValues() config.RuntimeValues {
	if s.runtime == nil {
		cfg := s.currentConfig()
		return config.RuntimeValues{
			AllowNewPeriod:             cfg.AllowNewPeriod,
			ResetTrafficMethod:         cfg.ResetTrafficMethod,
			CommissionAutoCheckEnable:  cfg.CommissionAutoCheckEnable,
			CommissionAutoCheckMinutes: cfg.CommissionAutoCheckMinutes,
			OrderKeepDays:              cfg.OrderKeepDays,
		}
	}
	return s.runtime.Current()
}

func (s *DBService) Info(ctx context.Context, userID int64) (Info, error) {
	if s.db == nil {
		return Info{}, ErrUnavailable
	}

	var (
		info           Info
		deviceLimit    sql.NullInt64
		lastLoginAt    sql.NullInt64
		autoRenewal    sql.NullInt64
		remindExpire   sql.NullInt64
		remindTraffic  sql.NullInt64
		expiredAt      sql.NullInt64
		planID         sql.NullInt64
		discount       sql.NullInt64
		commissionRate sql.NullInt64
		telegramID     sql.NullInt64
	)

	err := s.db.QueryRowContext(ctx, `SELECT
email, transfer_enable, device_limit, last_login_at, created_at, banned,
auto_renewal, remind_expire, remind_traffic, expired_at, balance,
commission_balance, plan_id, discount, commission_rate, telegram_id, uuid
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(
		&info.Email,
		&info.TransferEnable,
		&deviceLimit,
		&lastLoginAt,
		&info.CreatedAt,
		&info.Banned,
		&autoRenewal,
		&remindExpire,
		&remindTraffic,
		&expiredAt,
		&info.Balance,
		&info.CommissionBalance,
		&planID,
		&discount,
		&commissionRate,
		&telegramID,
		&info.UUID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Info{}, ErrNotFound
		}
		return Info{}, fmt.Errorf("query user info: %w", err)
	}

	info.DeviceLimit = nullInt64Ptr(deviceLimit)
	info.LastLoginAt = nullInt64Ptr(lastLoginAt)
	info.AutoRenewal = nullInt64Default(autoRenewal, 0)
	info.RemindExpire = nullInt64Default(remindExpire, 1)
	info.RemindTraffic = nullInt64Default(remindTraffic, 1)
	info.ExpiredAt = nullInt64Ptr(normalizeNullableExpiry(expiredAt))
	info.PlanID = nullInt64Ptr(planID)
	info.Discount = nullInt64Ptr(discount)
	info.CommissionRate = nullInt64Ptr(commissionRate)
	info.TelegramID = nullInt64Ptr(telegramID)
	info.AvatarURL = avatarURL(info.Email)

	return info, nil
}

func (s *DBService) ResolveClientUserID(ctx context.Context, token string) (int64, error) {
	if s.db == nil {
		return 0, ErrUnavailable
	}
	cfg := s.currentConfig()

	token = strings.TrimSpace(token)
	if token == "" {
		return 0, ErrClientTokenInvalid
	}

	resolvedToken := token
	switch cfg.ShowSubscribeMethod {
	case 1:
		value, ok, err := s.kvGet(ctx, "otpn_"+token)
		if err != nil {
			return 0, err
		}
		if !ok || strings.TrimSpace(value) == "" {
			return 0, ErrClientTokenInvalid
		}
		resolvedToken = strings.TrimSpace(value)
		if err := s.kvDelete(ctx, "otpn_"+token); err != nil {
			return 0, err
		}
		if err := s.kvDelete(ctx, "otp_"+resolvedToken); err != nil {
			return 0, err
		}
	case 2:
		cachedToken, ok, err := s.kvGet(ctx, "totp_"+token)
		if err != nil {
			return 0, err
		}
		if ok && strings.TrimSpace(cachedToken) != "" {
			resolvedToken = strings.TrimSpace(cachedToken)
			break
		}

		userID, canonicalToken, err := s.resolveTimedClientToken(ctx, token)
		if err != nil {
			return 0, err
		}
		ttl := cfg.ShowSubscribeExpire
		if ttl <= 0 {
			ttl = 5
		}
		if err := s.kvSet(ctx, "totp_"+token, canonicalToken, ttl*60); err != nil {
			return 0, err
		}
		return userID, nil
	}

	return s.findUserIDByToken(ctx, resolvedToken)
}

func (s *DBService) Stat(ctx context.Context, userID int64) ([]int64, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	openOrders, err := s.count(ctx, `SELECT COUNT(*) FROM v2_order WHERE status = 0 AND user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	openTickets, err := s.count(ctx, `SELECT COUNT(*) FROM v2_ticket WHERE status = 0 AND user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	inviteUsers, err := s.count(ctx, `SELECT COUNT(*) FROM v2_user WHERE invite_user_id = $1`, userID)
	if err != nil {
		return nil, err
	}

	return []int64{openOrders, openTickets, inviteUsers}, nil
}

func (s *DBService) Subscribe(ctx context.Context, userID int64) (Subscribe, error) {
	if s.db == nil {
		return Subscribe{}, ErrUnavailable
	}

	var (
		subscribe   Subscribe
		planID      sql.NullInt64
		expiredAt   sql.NullInt64
		deviceLimit sql.NullInt64
	)

	err := s.db.QueryRowContext(ctx, `SELECT plan_id, token, expired_at, u, d, transfer_enable, device_limit, email, uuid
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(
		&planID,
		&subscribe.Token,
		&expiredAt,
		&subscribe.U,
		&subscribe.D,
		&subscribe.TransferEnable,
		&deviceLimit,
		&subscribe.Email,
		&subscribe.UUID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Subscribe{}, ErrNotFound
		}
		return Subscribe{}, fmt.Errorf("query user subscribe: %w", err)
	}

	subscribe.PlanID = nullInt64Ptr(planID)
	subscribe.ExpiredAt = nullInt64Ptr(normalizeNullableExpiry(expiredAt))
	subscribe.DeviceLimit = nullInt64Ptr(deviceLimit)
	subscribe.AliveIP = 0
	if raw, ok, err := s.kvGet(ctx, "ALIVE_IP_USER_"+strconv.FormatInt(userID, 10)); err != nil {
		return Subscribe{}, err
	} else if ok {
		subscribe.AliveIP = subscribeAliveIPCount(raw)
	}
	runtimeValues := s.runtimeValues()
	subscribe.AllowNewPeriod = strconv.FormatInt(boolToInt64(runtimeValues.AllowNewPeriod), 10)

	if subscribe.PlanID != nil {
		plan, err := s.findPlanMap(ctx, *subscribe.PlanID)
		if err != nil {
			return Subscribe{}, err
		}
		if plan == nil {
			return Subscribe{}, ErrPlanNotFound
		}
		subscribe.Plan = plan

		resetDay := s.calculateResetDay(plan, subscribe.ExpiredAt)
		subscribe.ResetDay = resetDay
	}

	urlValue, err := s.buildSubscribeURL(ctx, userID, subscribe.Token)
	if err != nil {
		return Subscribe{}, err
	}
	subscribe.SubscribeURL = urlValue

	return subscribe, nil
}

func (s *DBService) Plans(ctx context.Context, userID int64, planID *int64) (any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	if planID != nil {
		plan, err := s.findPlanMap(ctx, *planID)
		if err != nil {
			return nil, err
		}
		if plan == nil {
			return nil, ErrPlanNotFound
		}

		userPlanID, err := s.findUserPlanID(ctx, userID)
		if err != nil {
			return nil, err
		}

		show := mapInt64(plan["show"]) != 0
		renew := mapInt64(plan["renew"]) != 0
		id := mapInt64(plan["id"])
		if (!show && !renew) || (!show && (!userPlanID.Valid || userPlanID.Int64 != id)) {
			return nil, ErrPlanNotFound
		}
		return plan, nil
	}

	plans, err := s.queryRowsAsMaps(ctx, `SELECT * FROM v2_plan WHERE "show" = 1 ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query user plans: %w", err)
	}

	counts, err := s.activePlanCounts(ctx)
	if err != nil {
		return nil, err
	}

	for _, plan := range plans {
		if raw, ok := plan["capacity_limit"]; ok && raw != nil {
			planID := mapInt64(plan["id"])
			if count, ok := counts[planID]; ok {
				plan["capacity_limit"] = mapInt64(raw) - count
			}
		}
	}

	return plans, nil
}

func (s *DBService) Orders(ctx context.Context, userID int64, status *int64) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	query := `SELECT * FROM v2_order WHERE user_id = $1`
	args := []any{userID}
	if status != nil {
		query += ` AND status = $2`
		args = append(args, *status)
	}
	query += ` ORDER BY created_at DESC`

	orders, err := s.queryRowsAsMaps(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query user orders: %w", err)
	}

	planMap, err := s.planMapForOrders(ctx, orders)
	if err != nil {
		return nil, err
	}

	for _, order := range orders {
		if plan, ok := planMap[mapInt64(order["plan_id"])]; ok {
			order["plan"] = plan
		}
		delete(order, "id")
		delete(order, "user_id")
	}

	return orders, nil
}

func (s *DBService) OrderDetail(ctx context.Context, userID int64, tradeNo string) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	order, err := s.querySingleMap(ctx, `SELECT * FROM v2_order WHERE user_id = $1 AND trade_no = $2 LIMIT 1`, userID, strings.TrimSpace(tradeNo))
	if err != nil {
		return nil, fmt.Errorf("query order detail: %w", err)
	}
	if order == nil {
		return nil, ErrOrderPaidOrMissing
	}

	if mapInt64(order["plan_id"]) == 0 {
		order["plan"] = map[string]any{
			"id":   int64(0),
			"name": "deposit",
		}
		bonus := int64(0)
		order["bounus"] = bonus
		order["get_amount"] = mapInt64(order["total_amount"]) + bonus
		return order, nil
	}

	planID := mapInt64(order["plan_id"])
	plan, err := s.findPlanMap(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, ErrPlanNotFound
	}
	order["plan"] = plan
	order["try_out_plan_id"] = s.currentConfig().TryOutPlanID

	ids := parseIDList(order["surplus_order_ids"])
	if len(ids) > 0 {
		query, args := buildInt64InQuery(`SELECT * FROM v2_order WHERE id IN (%s)`, ids)
		surplusOrders, err := s.queryRowsAsMaps(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query surplus orders: %w", err)
		}
		order["surplus_orders"] = surplusOrders
	}

	return order, nil
}

func (s *DBService) OrderStatus(ctx context.Context, userID int64, tradeNo string) (int64, error) {
	if s.db == nil {
		return 0, ErrUnavailable
	}
	var status int64
	err := s.db.QueryRowContext(ctx, `SELECT status FROM v2_order WHERE trade_no = $1 AND user_id = $2 LIMIT 1`, strings.TrimSpace(tradeNo), userID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrOrderNotFound
		}
		return 0, fmt.Errorf("query order status: %w", err)
	}
	return status, nil
}

func (s *DBService) PaymentMethods(ctx context.Context) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	methods, err := s.queryRowsAsMaps(ctx, `SELECT id, name, payment, icon, handling_fee_fixed, handling_fee_percent
FROM v2_payment
WHERE enable = 1
ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query payment methods: %w", err)
	}
	return methods, nil
}

func avatarURL(email string) string {
	sum := md5.Sum([]byte(email))
	return "https://cravatar.cn/avatar/" + hex.EncodeToString(sum[:]) + "?s=64&d=identicon"
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func normalizeNullableExpiry(value sql.NullInt64) sql.NullInt64 {
	if !value.Valid || value.Int64 <= 0 {
		return sql.NullInt64{}
	}
	return value
}

func nullInt64Default(value sql.NullInt64, fallback int64) int64 {
	if !value.Valid {
		return fallback
	}
	return value.Int64
}

func (s *DBService) count(ctx context.Context, query string, userID int64) (int64, error) {
	var value int64
	if err := s.db.QueryRowContext(ctx, query, userID).Scan(&value); err != nil {
		return 0, fmt.Errorf("count query failed: %w", err)
	}
	return value, nil
}

func (s *DBService) findPlanMap(ctx context.Context, planID int64) (map[string]any, error) {
	return s.querySingleMap(ctx, `SELECT * FROM v2_plan WHERE id = $1 LIMIT 1`, planID)
}

func (s *DBService) findUserPlanID(ctx context.Context, userID int64) (sql.NullInt64, error) {
	var planID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT plan_id FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt64{}, ErrNotFound
		}
		return sql.NullInt64{}, fmt.Errorf("query user plan id: %w", err)
	}
	return planID, nil
}

func (s *DBService) activePlanCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plan_id, COUNT(*) AS count
FROM v2_user
WHERE plan_id IS NOT NULL AND (expired_at >= $1 OR expired_at IS NULL OR expired_at <= 0)
GROUP BY plan_id`, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("query active plan counts: %w", err)
	}
	defer rows.Close()

	result := make(map[int64]int64)
	for rows.Next() {
		var planID int64
		var count int64
		if err := rows.Scan(&planID, &count); err != nil {
			return nil, fmt.Errorf("scan active plan count: %w", err)
		}
		result[planID] = count
	}
	return result, rows.Err()
}

func (s *DBService) planMapForOrders(ctx context.Context, orders []map[string]any) (map[int64]map[string]any, error) {
	planIDs := make([]int64, 0, len(orders))
	for _, order := range orders {
		planID := mapInt64(order["plan_id"])
		if planID <= 0 || slices.Contains(planIDs, planID) {
			continue
		}
		planIDs = append(planIDs, planID)
	}
	if len(planIDs) == 0 {
		return map[int64]map[string]any{}, nil
	}

	query, args := buildInt64InQuery(`SELECT * FROM v2_plan WHERE id IN (%s)`, planIDs)
	plans, err := s.queryRowsAsMaps(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query plans for orders: %w", err)
	}

	result := make(map[int64]map[string]any, len(plans))
	for _, plan := range plans {
		result[mapInt64(plan["id"])] = plan
	}
	return result, nil
}

func (s *DBService) buildSubscribeURL(ctx context.Context, userID int64, token string) (string, error) {
	cfg := s.currentConfig()
	path := normalizeSubscribePath(cfg.SubscribePath)
	baseURL := selectSubscribeBaseURL(cfg.SubscribeURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.AppURL)
	}
	switch cfg.ShowSubscribeMethod {
	case 1:
		newToken, err := s.oneTimeSubscribeToken(ctx, token)
		if err != nil {
			return "", err
		}
		return appendTokenToURL(baseURL, path, newToken, cfg.SubscribeTokenInPath), nil
	case 2:
		ttl := cfg.ShowSubscribeExpire
		if ttl <= 0 {
			ttl = 5
		}
		counter := time.Now().Unix() / (ttl * 60)
		counterBytes := []byte{0, 0, 0, 0, 0, 0, 0, 0}
		counterBytes[4] = byte(counter >> 24)
		counterBytes[5] = byte(counter >> 16)
		counterBytes[6] = byte(counter >> 8)
		counterBytes[7] = byte(counter)
		mac := hmac.New(sha1.New, []byte(token))
		_, _ = mac.Write(counterBytes)
		hashed := hex.EncodeToString(mac.Sum(nil))
		clientToken := base64URLEncode(fmt.Sprintf("%d:%s", userID, hashed))
		return appendTokenToURL(baseURL, path, clientToken, cfg.SubscribeTokenInPath), nil
	default:
		return appendTokenToURL(baseURL, path, token, cfg.SubscribeTokenInPath), nil
	}
}

func (s *DBService) oneTimeSubscribeToken(ctx context.Context, token string) (string, error) {
	cacheKey := "otp_" + token
	if value, ok, err := s.kvGet(ctx, cacheKey); err != nil {
		return "", err
	} else if ok && strings.TrimSpace(value) != "" {
		return value, nil
	}

	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate one-time subscribe token: %w", err)
	}
	newToken := base64URLEncodeBytes(buf)
	if err := s.kvSet(ctx, cacheKey, newToken, 86400); err != nil {
		return "", err
	}
	if err := s.kvSet(ctx, "otpn_"+newToken, token, 86400); err != nil {
		return "", err
	}
	return newToken, nil
}

func (s *DBService) resolveTimedClientToken(ctx context.Context, token string) (int64, string, error) {
	decoded, err := base64URLDecode(token)
	if err != nil {
		return 0, "", ErrClientTokenInvalid
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return 0, "", ErrClientTokenInvalid
	}

	userID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || userID <= 0 {
		return 0, "", ErrClientTokenInvalid
	}
	clientHash := strings.TrimSpace(parts[1])
	if clientHash == "" {
		return 0, "", ErrClientTokenInvalid
	}

	canonicalToken, err := s.findUserTokenByID(ctx, userID)
	if err != nil {
		return 0, "", err
	}

	ttl := s.currentConfig().ShowSubscribeExpire
	if ttl <= 0 {
		ttl = 5
	}
	counter := time.Now().Unix() / (ttl * 60)
	counterBytes := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	counterBytes[4] = byte(counter >> 24)
	counterBytes[5] = byte(counter >> 16)
	counterBytes[6] = byte(counter >> 8)
	counterBytes[7] = byte(counter)
	mac := hmac.New(sha1.New, []byte(canonicalToken))
	_, _ = mac.Write(counterBytes)
	if hex.EncodeToString(mac.Sum(nil)) != clientHash {
		return 0, "", ErrClientTokenInvalid
	}
	return userID, canonicalToken, nil
}

func (s *DBService) findUserIDByToken(ctx context.Context, token string) (int64, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE token = $1 LIMIT 1`, token).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrClientTokenInvalid
		}
		return 0, fmt.Errorf("query user by token: %w", err)
	}
	return userID, nil
}

func (s *DBService) findUserTokenByID(ctx context.Context, userID int64) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `SELECT token FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrClientTokenInvalid
		}
		return "", fmt.Errorf("query user token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", ErrClientTokenInvalid
	}
	return token, nil
}

func (s *DBService) kvGet(ctx context.Context, key string) (string, bool, error) {
	var value string
	var expireAt int64
	err := s.db.QueryRowContext(ctx, `SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`, key).Scan(&value, &expireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query runtime kv: %w", err)
	}
	if expireAt > 0 && expireAt <= time.Now().Unix() {
		return "", false, nil
	}
	return value, true, nil
}

func (s *DBService) kvSet(ctx context.Context, key, value string, ttlSeconds int64) error {
	now := time.Now().Unix()
	expireAt := int64(0)
	if ttlSeconds > 0 {
		expireAt = now + ttlSeconds
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
		key, value, expireAt, now, now)
	if err != nil {
		return fmt.Errorf("set runtime kv: %w", err)
	}
	return nil
}

func (s *DBService) calculateResetDay(plan map[string]any, expiredAt *int64) *int64 {
	if expiredAt == nil || *expiredAt <= time.Now().Unix() {
		return nil
	}
	resetMethod := mapNullableInt64(plan["reset_traffic_method"])
	method := s.runtimeValues().ResetTrafficMethod
	if resetMethod != nil {
		method = *resetMethod
	}
	if method == 2 {
		return nil
	}

	var result int64
	switch method {
	case 0:
		result = calcResetDayByMonthFirstDay()
	case 1:
		result = calcResetDayByExpireDay(*expiredAt)
	case 3:
		result = calcResetDayByYearFirstDay()
	case 4:
		result = calcResetDayByYearExpiredAt(*expiredAt)
	default:
		return nil
	}
	return &result
}

func (s *DBService) queryRowsAsMaps(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("load columns: %w", err)
	}

	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		scanArgs := make([]any, len(columns))
		for i := range values {
			scanArgs[i] = &values[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeDBValue(values[i])
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return result, nil
}

func (s *DBService) querySingleMap(ctx context.Context, query string, args ...any) (map[string]any, error) {
	rows, err := s.queryRowsAsMaps(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func normalizeDBValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return value
	}
}

func mapInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	case []byte:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(string(typed)), 10, 64)
		return parsed
	default:
		return 0
	}
}

func mapNullableInt64(value any) *int64 {
	if value == nil {
		return nil
	}
	v := mapInt64(value)
	return &v
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func appendTokenToURL(baseURL, path, token string, tokenInPath bool) string {
	path = normalizeSubscribePath(path)
	if tokenInPath {
		path = strings.TrimRight(path, "/") + "/" + url.PathEscape(token)
		if baseURL == "" {
			return path
		}
		return strings.TrimRight(baseURL, "/") + path
	}
	if baseURL == "" {
		return path + "?token=" + url.QueryEscape(token)
	}
	return strings.TrimRight(baseURL, "/") + path + "?token=" + url.QueryEscape(token)
}

func normalizeSubscribePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/api/v1/client/subscribe"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func selectSubscribeBaseURL(raw string) string {
	parts := strings.Split(raw, ",")
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		urls = append(urls, strings.TrimRight(part, "/"))
	}
	if len(urls) == 0 {
		return ""
	}
	if len(urls) == 1 {
		return urls[0]
	}
	max := big.NewInt(int64(len(urls)))
	index, err := rand.Int(rand.Reader, max)
	if err != nil {
		return urls[0]
	}
	return urls[index.Int64()]
}

func base64URLEncode(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func base64URLEncodeBytes(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func base64URLDecode(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
}

func parseIDList(value any) []int64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return parseIDString(typed)
	case []byte:
		return parseIDString(string(typed))
	default:
		return nil
	}
}

func parseIDString(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var ids []int64
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &ids); err == nil {
			return ids
		}

		var stringIDs []string
		if err := json.Unmarshal([]byte(raw), &stringIDs); err == nil {
			ids = make([]int64, 0, len(stringIDs))
			for _, item := range stringIDs {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				if id, err := strconv.ParseInt(item, 10, 64); err == nil {
					ids = append(ids, id)
				}
			}
			return ids
		}

		var generic []any
		if err := json.Unmarshal([]byte(raw), &generic); err == nil {
			ids = make([]int64, 0, len(generic))
			for _, item := range generic {
				if id, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(item)), 10, 64); err == nil {
					ids = append(ids, id)
				}
			}
			return ids
		}
		return nil
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		raw = strings.TrimPrefix(strings.TrimSuffix(raw, "}"), "{")
	}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func buildInt64InQuery(format string, values []int64) (string, []any) {
	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
		parts = append(parts, fmt.Sprintf("$%d", len(args)))
	}
	return fmt.Sprintf(format, strings.Join(parts, ",")), args
}

func calcResetDayByMonthFirstDay() int64 {
	today, _ := strconv.ParseInt(time.Now().Format("02"), 10, 64)
	lastDay, _ := strconv.ParseInt(time.Date(time.Now().Year(), time.Now().Month()+1, 0, 0, 0, 0, 0, time.Now().Location()).Format("02"), 10, 64)
	return lastDay - today
}

func calcResetDayByExpireDay(expiredAt int64) int64 {
	day, _ := strconv.ParseInt(time.Unix(expiredAt, 0).Format("02"), 10, 64)
	today, _ := strconv.ParseInt(time.Now().Format("02"), 10, 64)
	lastDay, _ := strconv.ParseInt(time.Date(time.Now().Year(), time.Now().Month()+1, 0, 0, 0, 0, 0, time.Now().Location()).Format("02"), 10, 64)
	if day >= today && day >= lastDay {
		return lastDay - today
	}
	if day >= today {
		return day - today
	}
	return lastDay - today + day
}

func calcResetDayByYearFirstDay() int64 {
	nextYear := time.Date(time.Now().Year()+1, 1, 1, 0, 0, 0, 0, time.Now().Location()).Unix()
	return (nextYear - time.Now().Unix()) / 86400
}

func calcResetDayByYearExpiredAt(expiredAt int64) int64 {
	md := time.Unix(expiredAt, 0).Format("01-02")
	nowYear, _ := time.ParseInLocation("2006-01-02", fmt.Sprintf("%d-%s", time.Now().Year(), md), time.Now().Location())
	nextYear := nowYear.AddDate(1, 0, 0)
	if nowYear.Unix() > time.Now().Unix() {
		return (nowYear.Unix() - time.Now().Unix()) / 86400
	}
	return (nextYear.Unix() - time.Now().Unix()) / 86400
}
